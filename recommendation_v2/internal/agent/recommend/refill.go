package recommend

import (
	"context"
	"sync"
	"time"

	"recommendation_v2/internal/repository"
	"recommendation_v2/internal/util"

	"trpc.group/trpc-go/trpc-agent-go/log"
	"trpc.group/trpc-go/trpc-agent-go/telemetry/trace"
)

// poolRefiller 异步候选池补充。
type poolRefiller struct {
	recallRepo *repository.RecallRepo
	poolRepo   *repository.PoolRepo
	embedder   *util.Embedder
	sem        chan struct{}
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
}

func NewPoolRefiller(recallRepo *repository.RecallRepo, poolRepo *repository.PoolRepo, embedder *util.Embedder, maxConcurrent int) *poolRefiller {
	if maxConcurrent <= 0 {
		maxConcurrent = 2
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &poolRefiller{
		recallRepo: recallRepo,
		poolRepo:   poolRepo,
		embedder:   embedder,
		sem:        make(chan struct{}, maxConcurrent),
		ctx:        ctx,
		cancel:     cancel,
	}
}

// EnsurePool 非阻塞检查池子大小，不足时 goroutine 补充。
func (r *poolRefiller) EnsurePool(ctx context.Context, userID string, poolType repository.PoolType, bucket, query string, targetSize, recallTopK int) {
	if targetSize <= 0 || query == "" {
		return
	}
	size, err := r.poolRepo.GetPoolSize(ctx, userID, poolType, bucket)
	if err != nil {
		log.Warnf("[refill] get size failed: %v", err)
		return
	}
	if size >= targetSize {
		return
	}

	select {
	case r.sem <- struct{}{}:
	case <-r.ctx.Done():
		return
	}

	r.wg.Add(1)
	go func() {
		defer func() {
			<-r.sem
			r.wg.Done()
		}()
		r.refill(r.ctx, userID, poolType, bucket, query, targetSize-size, recallTopK)
	}()
}

func (r *poolRefiller) Stop() {
	r.cancel()
	r.wg.Wait()
}

func (r *poolRefiller) refill(ctx context.Context, userID string, poolType repository.PoolType, bucket, query string, need, recallTopK int) {
	start := time.Now()
	ctx, span := trace.Tracer.Start(ctx, "refill "+string(poolType))
	defer span.End()
	log.Infof("[refill] start user=%s pool=%s query=%q need=%d", userID, poolType, query, need)

	vec, err := r.embedder.Embed(ctx, query)
	if err != nil {
		log.Warnf("[refill] embed failed: %v", err)
		return
	}

	pull := need * 3
	if pull < 20 {
		pull = 20
	}
	if recallTopK > 0 && pull > recallTopK {
		pull = recallTopK
	}

	candidates, err := r.recallRepo.Search(ctx, vec, pull)
	if err != nil {
		log.Warnf("[refill] search failed: %v", err)
		return
	}
	if len(candidates) == 0 {
		return
	}

	items := make([]repository.PoolItem, 0, len(candidates))
	for _, c := range candidates {
		items = append(items, repository.PoolItem{
			UserID:       userID,
			PoolType:     poolType,
			PeriodBucket: bucket,
			ArticleID:    c.ArticleID,
			Score:        c.Score,
			Similarity:   c.Score,
			RemarkScore:  c.Score,
		})
	}

	if err := r.poolRepo.AddItems(ctx, items); err != nil {
		log.Warnf("[refill] add items failed: %v", err)
		return
	}

	log.Infof("[refill] done user=%s pool=%s added=%d latency=%dms",
		userID, poolType, len(items), time.Since(start).Milliseconds())
}
