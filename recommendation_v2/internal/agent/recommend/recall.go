package recommend

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"recommendation_v2/config"
	"recommendation_v2/internal/infrastructure"
	"recommendation_v2/internal/repository"
	"recommendation_v2/internal/util"

	"trpc.group/trpc-go/trpc-agent-go/log"
)

// =============================================================================
// 召回源名称常量 — 在此注册新召回源的名字
// =============================================================================

const (
	RecallSourceTextVector = "text_vector" // 文本语义向量召回
	RecallSourceTrending   = "trending"    // 实时热点召回（待接入）
	RecallSourceGeoMatch   = "geo_match"   // 地理位置召回（待接入）
	RecallSourceSocial     = "social"      // 社交圈召回（待接入）
)

// Recaller 单个召回源的执行接口。
// 新召回源实现此接口后，在 recallSources() 中注册即可。
type Recaller interface {
	Name() string
	Fetch(ctx context.Context, plan *RecallPlan, intent IntentResult, profile *UserProfile, topK int) ([]repository.RecallCandidate, error)
}

// recallSources 所有已注册的召回源。
// 新增召回源时：1) 在常量区加 name  2) 实现 Recaller 接口  3) 在这里注册
func (a *RecommendAgent) recallSources() map[string]Recaller {
	return map[string]Recaller{
		RecallSourceTextVector: &textVectorRecaller{repo: a.recallRepo, embedder: a.embedder},
		// RecallSourceTrending: &trendingRecaller{...},
		RecallSourceGeoMatch: &geoMatchRecaller{articleRepo: a.articleRepo},
		// RecallSourceSocial:   &socialRecaller{...},
	}
}

// =============================================================================
// 多路召回执行框架
// =============================================================================

// recallRun 一路召回的执行结果。
type recallRun struct {
	Name       string
	Candidates []repository.RecallCandidate
	Weight     float64
}

// executeRecallPlan 按计划并行执行多路召回，合并加权排序后返回。
func (a *RecommendAgent) executeRecallPlan(ctx context.Context, plan *RecallPlan, intent IntentResult, profile *UserProfile) ([]repository.RecallCandidate, error) {
	if plan == nil || len(plan.Sources) == 0 {
		return nil, nil
	}

	sources := a.recallSources()

	type runResult struct {
		run recallRun
		err error
	}
	ch := make(chan runResult, len(plan.Sources))
	active := 0

	for _, sc := range plan.Sources {
		if !sc.Enabled || sc.TopK <= 0 {
			continue
		}
		s, ok := sources[sc.Name]
		if !ok {
			continue
		}
		active++
		sc, s := sc, s
		go func() {
			candidates, err := s.Fetch(ctx, plan, intent, profile, sc.TopK)
			if err != nil {
				log.Warnf("[recall] source %s failed: %v", sc.Name, err)
				ch <- runResult{recallRun{Name: sc.Name, Weight: sc.Weight}, err}
				return
			}
			ch <- runResult{recallRun{Name: sc.Name, Candidates: candidates, Weight: sc.Weight}, nil}
		}()
	}

	var runs []recallRun
	for range active {
		r := <-ch
		if r.err == nil && len(r.run.Candidates) > 0 {
			runs = append(runs, r.run)
		}
	}

	applyFreshness(runs, config.Cfg.Recall.Freshness)
	merged := mergeCandidates(runs, plan.Strategy, config.Cfg.Recall.FinalTopK)
	// GraphRAG 扩展候选
	if len(merged) > 0 {
		merged = a.graphExpand(ctx, merged)
	}
	return merged, nil
}

// mergeCandidates 合并多路召回结果：按 article_id 去重，按策略合并得分，排序返回。
func mergeCandidates(runs []recallRun, strategy MergeStrategy, topK int) []repository.RecallCandidate {
	if len(runs) == 0 {
		return nil
	}

	type mergedCandidate struct {
		candidate repository.RecallCandidate
		score     float64
	}
	merged := make(map[string]*mergedCandidate)

	for _, run := range runs {
		if len(run.Candidates) == 0 {
			continue
		}
		normalized := normalizeScores(run.Candidates)
		for i, c := range run.Candidates {
			m, ok := merged[c.ArticleID]
			if !ok {
				score := combineScore(strategy, normalized[i], run.Weight, 0)
				merged[c.ArticleID] = &mergedCandidate{
					candidate: c,
					score:     score,
				}
			} else {
				m.score = combineScore(strategy, normalized[i], run.Weight, m.score)
			}
		}
	}

	out := make([]repository.RecallCandidate, 0, len(merged))
	for _, m := range merged {
		m.candidate.Score = float32(m.score)
		out = append(out, m.candidate)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	if topK > 0 && len(out) > topK {
		out = out[:topK]
	}
	return out
}

// combineScore 按策略合并归一化分和权重。
func combineScore(strategy MergeStrategy, normalized, weight, existing float64) float64 {
	weighted := normalized * weight
	switch strategy {
	case MergeStrategyWeightedProduct:
		if existing == 0 {
			return weighted
		}
		return existing * weighted
	case MergeStrategyMax:
		if weighted > existing {
			return weighted
		}
		return existing
	case MergeStrategyWeightedSum:
		fallthrough
	default:
		return existing + weighted
	}
}

// normalizeScores Min-Max 归一化到 [0, 1]。
func normalizeScores(candidates []repository.RecallCandidate) []float64 {
	if len(candidates) == 0 {
		return nil
	}
	min := float64(candidates[0].Score)
	max := float64(candidates[0].Score)
	for _, c := range candidates {
		s := float64(c.Score)
		if s < min {
			min = s
		}
		if s > max {
			max = s
		}
	}
	rng := max - min
	if rng == 0 {
		out := make([]float64, len(candidates))
		for i := range out {
			out[i] = 0.5
		}
		return out
	}
	out := make([]float64, len(candidates))
	for i, c := range candidates {
		out[i] = (float64(c.Score) - min) / rng
	}
	return out
}

// graphExpand 通过图关系扩展候选（GraphRAG）。
func (a *RecommendAgent) graphExpand(ctx context.Context, candidates []repository.RecallCandidate) []repository.RecallCandidate {
	if a.graphRepo == nil {
		return candidates
	}
	ids := make([]string, 0, len(candidates))
	for _, c := range candidates {
		ids = append(ids, c.ArticleID)
	}
	expanded, err := a.graphRepo.ExpandCandidates(ctx, ids, 20)
	if err != nil {
		return candidates
	}
	if len(expanded) == 0 {
		return candidates
	}
	// 去重
	seen := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		seen[c.ArticleID] = true
	}
	for _, id := range expanded {
		if seen[id] {
			continue
		}
		seen[id] = true
		candidates = append(candidates, repository.RecallCandidate{
			ArticleID: id,
			Score:     0.5, // 图扩展的候选给中等分
		})
	}
	return candidates
}

// =============================================================================
// 召回源：text_vector — 文本语义向量召回
// =============================================================================

type textVectorRecaller struct {
	repo     *repository.RecallRepo
	embedder *util.Embedder
}

func (r *textVectorRecaller) Name() string { return RecallSourceTextVector }

func (r *textVectorRecaller) Fetch(ctx context.Context, _ *RecallPlan, intent IntentResult, _ *UserProfile, topK int) ([]repository.RecallCandidate, error) {
	vec, err := r.embedder.Embed(ctx, intent.Label)
	if err != nil {
		return nil, fmt.Errorf("text_vector embed: %w", err)
	}
	return r.repo.Search(ctx, vec, topK)
}

// =============================================================================
// 召回源：geo_match — 按城市匹配召回
// =============================================================================

type geoMatchRecaller struct {
	articleRepo *repository.ArticleRepo
}

func (r *geoMatchRecaller) Name() string { return RecallSourceGeoMatch }

func (r *geoMatchRecaller) Fetch(ctx context.Context, _ *RecallPlan, _ IntentResult, profile *UserProfile, topK int) ([]repository.RecallCandidate, error) {
	homeCity := profile.String("home_city")
	if homeCity == "" {
		return nil, nil
	}

	rows, err := infrastructure.Postgres().QueryContext(ctx, `
		SELECT article_id FROM articles WHERE geo_city = $1 ORDER BY score DESC LIMIT $2
	`, homeCity, topK)
	if err != nil {
		return nil, fmt.Errorf("geo query: %w", err)
	}
	defer rows.Close()

	var out []repository.RecallCandidate
	for rows.Next() {
		var articleID string
		if err := rows.Scan(&articleID); err != nil {
			continue
		}
		out = append(out, repository.RecallCandidate{
			ArticleID: articleID,
			Score:     0.9,
		})
	}
	return out, nil
}

// =============================================================================
// 新鲜度衰减 — 作为 score modifier 作用于所有候选
// =============================================================================

func applyFreshness(runs []recallRun, cfg config.FreshnessConfig) {
	if cfg.Weight == 0 || len(runs) == 0 {
		return
	}
	halfLife := cfg.HalfLifeDays * 24 * float64(time.Hour)
	if halfLife <= 0 {
		return
	}
	maxDecay := cfg.MaxDecay
	if maxDecay <= 0 {
		maxDecay = 0.1
	}
	if maxDecay > 1 {
		maxDecay = 1
	}

	for i := range runs {
		for j, c := range runs[i].Candidates {
			days := 30 - math.Min(float64(c.Score)*30, 29)
			if days < 1 {
				days = 1
			}
			age := time.Duration(days * 24 * float64(time.Hour))
			freshness := 1.0 - float64(age)/halfLife
			if freshness < maxDecay {
				freshness = maxDecay
			}
			runs[i].Candidates[j].Score = float32(freshness)
		}
	}
}
