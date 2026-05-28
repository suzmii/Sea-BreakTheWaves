package recommend

import (
	"context"
	"fmt"
	"time"

	"recommendation_v2/config"
	"recommendation_v2/internal/repository"

	graph "trpc.group/trpc-go/trpc-agent-go/graph"
	"trpc.group/trpc-go/trpc-agent-go/telemetry/trace"
	"trpc.group/trpc-go/trpc-agent-go/log"
	"trpc.group/trpc-go/trpc-agent-go/memory"
)

// nodeIntent 意图识别。
func (a *RecommendAgent) nodeIntent(ctx context.Context, s graph.State) (any, error) {
	ps := statePtr(s)

	if ps.Query == "" {
		ps.Intent = buildFallbackIntent()
		ps.Intent.Confidence = 1.0
		return s, nil
	}

	parsed, err := doIntent(ctx, a.chatLLM, ps.Query)
	if err != nil {
		log.Warnf("[reco] intent parse failed: %v", err)
	}
	ps.Intent = parsed
	return s, nil
}

// nodeMemory 加载用户记忆（按相关度排序）。
func (a *RecommendAgent) nodeMemory(ctx context.Context, s graph.State) (any, error) {
	ps := statePtr(s)
	userKey := memory.UserKey{AppName: "recommendation", UserID: ps.UserID}

	searchQuery := ps.Query
	if searchQuery == "" {
		searchQuery = ps.Intent.Label
	}

	// 并行加载：最近记忆 + keyword 搜索 + 向量 chunk 召回
	type result struct {
		entries []*memory.Entry
		err     error
	}
	ch := make(chan result, 3)

	go func() {
		entries, err := a.memSvc.ReadMemories(ctx, userKey, 20)
		ch <- result{entries, err}
	}()
	go func() {
		entries, err := a.memSvc.SearchMemories(ctx, userKey, searchQuery,
			memory.WithSearchOptions(memory.SearchOptions{MaxResults: 10, SimilarityThreshold: 0.2}))
		ch <- result{entries, err}
	}()
	go func() {
		vec, err := a.embedder.Embed(ctx, searchQuery)
		if err != nil {
			ch <- result{nil, err}
			return
		}
		chunks, err := a.memoryChunkRepo.SearchChunksByUser(ctx, ps.UserID, vec, 5)
		if err != nil || len(chunks) == 0 {
			ch <- result{nil, err}
			return
		}
		var entries []*memory.Entry
		now := time.Now()
		for i, c := range chunks {
			if c == "" {
				continue
			}
			entries = append(entries, &memory.Entry{
				ID:   fmt.Sprintf("chunk_%s_%d", ps.UserID, i),
				AppName: userKey.AppName,
				UserID:  ps.UserID,
				CreatedAt: now,
				UpdatedAt: now,
				Memory: &memory.Memory{
					Memory:      c,
					Topics:      []string{"memory_chunk"},
					LastUpdated: &now,
					Kind:        memory.KindFact,
				},
			})
		}
		ch <- result{entries, nil}
	}()

	// 去重合并
	seen := make(map[string]bool)
	var combined []*memory.Entry
	for range 3 {
		r := <-ch
		if r.err != nil {
			log.Warnf("[reco] memory load failed: %v", r.err)
			continue
		}
		for _, e := range r.entries {
			if e != nil && !seen[e.ID] {
				seen[e.ID] = true
				combined = append(combined, e)
			}
		}
	}
	if combined == nil {
		combined = []*memory.Entry{}
	}
	ps.Memory = MemoryContext{Entries: combined}

	// 加载用户画像
	raw := a.profileRepo.GetOrInit(ctx, ps.UserID)
	ps.Profile = NewUserProfileFromMap(raw)

	return s, nil
}

// nodeRecall 按意图中的召回计划执行多路并行召回，合并后给 rerank。
func (a *RecommendAgent) nodeRecall(ctx context.Context, s graph.State) (any, error) {
	ps := statePtr(s)

	candidates, err := a.executeRecallPlan(ctx, ps.Intent.RecallPlan, ps.Intent, ps.Profile)
	if err != nil {
		return nil, err
	}
	ps.Candidates = candidates
	return s, nil
}

// nodeRerank 个性化精排。
func (a *RecommendAgent) nodeRerank(ctx context.Context, s graph.State) (any, error) {
	ps := statePtr(s)
	if len(ps.Candidates) == 0 {
		return s, nil
	}

	if config.Cfg.Rerank.EnableDashScope && ps.Query != "" {
		filtered, err := semanticFilter(ctx, ps.Query, ps.Candidates)
		if err != nil {
			log.Warnf("[reco] semantic filter failed, skip: %v", err)
		} else if len(filtered) > 0 {
			ps.Candidates = filtered
		}
	}

	reranked, err := doRerank(ctx, ps.Candidates, ps.Memory, ps.Intent, ps.Profile, a.chatLLM, a.articleRepo)
	if err != nil {
		return nil, err
	}
	ps.Reranked = reranked
	return s, nil
}

// nodeOutput 生成最终输出。
func (a *RecommendAgent) nodeOutput(ctx context.Context, s graph.State) (any, error) {
	ps := statePtr(s)

	ids := make([]string, 0, len(ps.Reranked))
	for _, r := range ps.Reranked {
		ids = append(ids, r.ArticleID)
	}
	ps.FinalIDs = ids
	return s, nil
}

// nodeSideEffect 推荐后副作用。
func (a *RecommendAgent) nodeSideEffect(ctx context.Context, s graph.State) (any, error) {
	ps := statePtr(s)
	if len(ps.FinalIDs) == 0 {
		return s, nil
	}

	a.poolRefill.EnsurePool(ctx, ps.UserID, repository.PoolLongTerm, "", ps.Intent.Label,
		config.Cfg.Pools.LongTerm.TargetSize, config.Cfg.Pools.LongTerm.RecallTopK)
	a.poolRefill.EnsurePool(ctx, ps.UserID, repository.PoolShortTerm, "", ps.Intent.Label,
		config.Cfg.Pools.ShortTerm.TargetSize, config.Cfg.Pools.ShortTerm.RecallTopK)
	a.poolRefill.EnsurePool(ctx, ps.UserID, repository.PoolPeriodic, ps.PeriodBucket, ps.Intent.Label,
		config.Cfg.Pools.Periodic.TargetSize, config.Cfg.Pools.Periodic.RecallTopK)

	if config.Cfg.Pools.Recommend.RemoveAfterRecommend {
		_ = a.poolRepo.RemoveItems(ctx, ps.UserID, repository.PoolLongTerm, "", ps.FinalIDs)
		_ = a.poolRepo.RemoveItems(ctx, ps.UserID, repository.PoolShortTerm, "", ps.FinalIDs)
		_ = a.poolRepo.RemoveItems(ctx, ps.UserID, repository.PoolPeriodic, ps.PeriodBucket, ps.FinalIDs)
	}

	// 异步记录曝光（独立 trace span，不阻塞请求）
	go func() {
		sctx, span := trace.Tracer.Start(context.Background(), "record_exposure")
		defer span.End()
		cctx, cancel := context.WithTimeout(sctx, 10*time.Second)
		defer cancel()

		vec, err := a.embedder.Embed(cctx, ps.Intent.Label)
		if err != nil {
			log.Warnf("[reco] exposure embed failed: %v", err)
			return
		}

		for _, id := range ps.FinalIDs {
			_ = a.historyRepo.Add(cctx, repository.UserHistoryItem{
				UserID:    ps.UserID,
				ArticleID: id,
				Clicked:   false,
				Embed:     vec,
			})
		}
		log.Infof("[reco] exposure recorded user=%s count=%d", ps.UserID, len(ps.FinalIDs))
	}()

	return s, nil
}
