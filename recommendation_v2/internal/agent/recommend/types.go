package recommend

import (
	"recommendation_v2/internal/repository"

	"trpc.group/trpc-go/trpc-agent-go/memory"
)

// UserProfile 用户结构化画像，封装 JSONB 的字段读写。
// 保持底层 map 的灵活性，同时提供类型安全的访问方法。
type UserProfile struct {
	raw map[string]any
}

func NewUserProfileFromMap(m map[string]any) *UserProfile {
	if m == nil {
		m = map[string]any{}
	}
	return &UserProfile{raw: m}
}

func (p *UserProfile) Raw() map[string]any     { return p.raw }
func (p *UserProfile) Set(key string, val any)  { p.raw[key] = val }
func (p *UserProfile) Get(key string) (any, bool) { v, ok := p.raw[key]; return v, ok }

func (p *UserProfile) String(key string) string {
	v, ok := p.raw[key]
	if !ok { return "" }
	s, _ := v.(string)
	return s
}

func (p *UserProfile) Float64(key string) float64 {
	v, ok := p.raw[key]
	if !ok { return 0 }
	f, _ := v.(float64)
	return f
}

func (p *UserProfile) Strings(key string) []string {
	v, ok := p.raw[key]
	if !ok { return nil }
	s, _ := v.([]string)
	return s
}

func (p *UserProfile) Bool(key string) bool {
	v, ok := p.raw[key]
	if !ok { return false }
	b, _ := v.(bool)
	return b
}

// Request 推荐请求。
type Request struct {
	UserID       string `json:"user_id"`
	SessionID    string `json:"session_id"`
	Surface      string `json:"surface"`
	Query        string `json:"query"`
	Explain      bool   `json:"explain,omitempty"`
	PeriodBucket string `json:"period_bucket"`
}

// Response 推荐响应。
type Response struct {
	Status  string   `json:"status"`
	TraceID string   `json:"trace_id"`
	IDs     []string `json:"ids"`
}

// IntentResult 意图识别结果，包含召回计划。
type IntentResult struct {
	Label      string      `json:"label"`
	Confidence float64     `json:"confidence"`
	Signals    []string    `json:"signals"`
	RecallPlan *RecallPlan `json:"-"`
}

// RecallSourceConfig 单个召回源的执行参数。
type RecallSourceConfig struct {
	Name    string  `json:"name"`
	TopK    int     `json:"top_k"`
	Weight  float64 `json:"weight"`
	Enabled bool    `json:"enabled"`
}

// MergeStrategy 多路召回合并策略。
type MergeStrategy string

const (
	MergeStrategyWeightedSum     MergeStrategy = "weighted_sum"     // 加权求和（默认）
	MergeStrategyWeightedProduct MergeStrategy = "weighted_product" // 加权乘积
	MergeStrategyMax             MergeStrategy = "max"              // 取最大值
)

// RecallPlan 多路召回计划，由意图识别阶段生成。
type RecallPlan struct {
	Sources  []RecallSourceConfig
	Strategy MergeStrategy
}


// MemoryContext 用户记忆上下文（按相关度排序的条目列表）。
type MemoryContext struct {
	Entries []*memory.Entry
}

// RerankItem 精排输出中的一条。
type RerankItem struct {
	ArticleID string  `json:"article_id"`
	Score     float64 `json:"score"`
	Reason    string  `json:"reason"`
}

// pipelineState StateGraph 流水线的共享状态。
type pipelineState struct {
	UserID       string
	Query        string
	PeriodBucket string

	Intent     IntentResult
	Memory     MemoryContext
	Profile    *UserProfile
	Candidates []repository.RecallCandidate
	Reranked   []RerankItem
	FinalIDs   []string
}

func (ps *pipelineState) DeepCopy() any { return ps }

// PostProcess 推荐后处理 hook。可在这里做去重、打散、过滤等。
func (ps *pipelineState) PostProcess() {
	// 默认不做额外处理
}
