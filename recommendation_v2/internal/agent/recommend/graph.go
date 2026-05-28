package recommend

import (
	"context"
	"fmt"
	"time"

	"recommendation_v2/internal/repository"
	"recommendation_v2/internal/infrastructure"
	"recommendation_v2/internal/util"

	"github.com/google/uuid"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	graph "trpc.group/trpc-go/trpc-agent-go/graph"
	"trpc.group/trpc-go/trpc-agent-go/log"
	"trpc.group/trpc-go/trpc-agent-go/memory"
)

// RecommendAgent 推荐 Agent。
type RecommendAgent struct {
	articleRepo     *repository.ArticleRepo
	recallRepo      *repository.RecallRepo
	memSvc          memory.Service
	poolRepo        *repository.PoolRepo
	historyRepo     *repository.UserHistoryRepo
	memoryChunkRepo *repository.MemoryChunkRepo
	profileRepo     *repository.ProfileRepo
	graphRepo       *repository.GraphRepo
	embedder        *util.Embedder
	chatLLM         *util.ChatLLM
	poolRefill      *poolRefiller
	compiledGraph   *graph.Graph
}

func NewRecommendAgent(
	article *repository.ArticleRepo,
	recall *repository.RecallRepo,
	memSvc memory.Service,
	pool *repository.PoolRepo,
	history *repository.UserHistoryRepo,
	memoryChunk *repository.MemoryChunkRepo,
	profile *repository.ProfileRepo,
	graph *repository.GraphRepo,
	embedder *util.Embedder,
	chatLLM *util.ChatLLM,
	refiller *poolRefiller,
) *RecommendAgent {
	a := &RecommendAgent{
		articleRepo:     article,
		recallRepo:      recall,
		memSvc:          memSvc,
		poolRepo:        pool,
		historyRepo:     history,
		memoryChunkRepo: memoryChunk,
		profileRepo:     profile,
		graphRepo:       graph,
		embedder:        embedder,
		chatLLM:         chatLLM,
		poolRefill:      refiller,
	}
	a.compiledGraph = a.buildGraph()
	return a
}

// Recommend 执行推荐流水线。
func (a *RecommendAgent) Recommend(ctx context.Context, req Request) (Response, error) {
	start := time.Now()

	ps := &pipelineState{
		UserID:       req.UserID,
		Query:        req.Query,
		PeriodBucket: req.PeriodBucket,
	}
	if ps.PeriodBucket == "" {
		ps.PeriodBucket = "d1"
	}

	executor, err := graph.NewExecutor(a.compiledGraph)
	if err != nil {
		return Response{Status: "error"}, fmt.Errorf("graph: new executor: %w", err)
	}

	inv := &agent.Invocation{
		InvocationID: uuid.NewString(),
		AgentName:    "Recommend",
	}
	initialState := graph.State{"state": ps}
	eventChan, err := executor.Execute(ctx, initialState, inv)
	if err != nil {
		return Response{Status: "error"}, fmt.Errorf("graph: execute: %w", err)
	}
	for range eventChan {
	}

	ps.PostProcess()

	log.Infof("[reco] recommend finished user=%s candidates=%d final=%d latency=%dms",
		req.UserID, len(ps.Candidates), len(ps.FinalIDs), time.Since(start).Milliseconds())

	return Response{Status: "ok", IDs: ps.FinalIDs}, nil
}

// buildGraph 构建推荐流水线 StateGraph。
func (a *RecommendAgent) buildGraph() *graph.Graph {
	g := graph.NewStateGraph(nil)

	g.AddNode("intent", measureNode("intent", a.nodeIntent))
	g.AddNode("memory", measureNode("memory", a.nodeMemory))
	g.AddNode("recall", measureNode("recall", a.nodeRecall))
	g.AddNode("rerank", measureNode("rerank", a.nodeRerank))
	g.AddNode("output", measureNode("output", a.nodeOutput))
	g.AddNode("side_effect", measureNode("side_effect", a.nodeSideEffect))

	g.SetEntryPoint("intent")
	g.AddEdge("intent", "memory")
	g.AddEdge("memory", "recall")
	g.AddEdge("recall", "rerank")
	g.AddEdge("rerank", "output")
	g.AddEdge("output", "side_effect")
	g.SetFinishPoint("side_effect")

	return g.MustCompile()
}

func statePtr(s graph.State) *pipelineState {
	return s["state"].(*pipelineState)
}

// measureNode 包装 graph node，记录节点级延迟指标。
func measureNode(name string, fn graph.NodeFunc) graph.NodeFunc {
	return func(ctx context.Context, s graph.State) (any, error) {
		start := time.Now()
		result, err := fn(ctx, s)
		infrastructure.NodeLatency.WithLabelValues(name).Observe(time.Since(start).Seconds())
		return result, err
	}
}
