package main

import (
	"context"
	"net/http"

	"recommendation_v2/config"
	"recommendation_v2/internal/agent/ingest"
	"recommendation_v2/internal/agent/recommend"
	"recommendation_v2/internal/infrastructure"
	"recommendation_v2/internal/kafka"
	"recommendation_v2/internal/repository"
	"recommendation_v2/internal/util"

	"github.com/gin-gonic/gin"
	"trpc.group/trpc-go/trpc-agent-go/log"
)

// Dependencies 应用所需的所有依赖。
type Dependencies struct {
	ArticleRepo     *repository.ArticleRepo
	RecallRepo      *repository.RecallRepo
	MemoryRepo      *repository.MemoryRepo
	PoolRepo        *repository.PoolRepo
	HistoryRepo     *repository.UserHistoryRepo
	MemoryChunkRepo  *repository.MemoryChunkRepo
	ProfileRepo     *repository.ProfileRepo
	GraphRepo       *repository.GraphRepo
	Embedder        *util.Embedder
	ChatLLM         *util.ChatLLM
	RecoAgent       *recommend.RecommendAgent
	KafkaConsumer   *kafka.Consumer
}

// InitDependencies 初始化所有基础组件和 Agent。
func InitDependencies(ctx context.Context) (*Dependencies, error) {
	if err := infrastructure.InitPostgres(); err != nil {
		return nil, err
	}
	if err := infrastructure.InitNeo4j(); err != nil {
		log.Warnf("[init] neo4j init failed: %v", err)
	}
	if err := infrastructure.InitMilvus(); err != nil {
		return nil, err
	}

	articleRepo := repository.NewArticleRepo()
	recallRepo := repository.NewRecallRepo()
	memoryRepo := repository.NewMemoryRepo()
	poolRepo := repository.NewPoolRepo()
	historyRepo := repository.NewUserHistoryRepo()
	memoryChunkRepo := repository.NewMemoryChunkRepo()
	profileRepo := repository.NewProfileRepo()
	graphRepo := repository.NewGraphRepo()
	embedder := util.NewEmbedder(&config.Cfg)
	chatLLM := util.NewChatLLM(&config.Cfg)

	refiller := recommend.NewPoolRefiller(recallRepo, poolRepo, embedder, 2)
	recoAgent := recommend.NewRecommendAgent(articleRepo, recallRepo, memoryRepo, poolRepo, historyRepo,
		memoryChunkRepo, profileRepo, graphRepo, embedder, chatLLM, refiller)

	kafkaConsumer, _ := kafka.Start(ctx, config.Cfg.Kafka.Topic, config.Cfg.Kafka.Group,
		func(ctx context.Context, event kafka.ArticleSyncEvent) error {
			if event.Markdown == "" {
				log.Warnf("[kafka] article %s has no markdown, skip", event.ArticleID)
				return nil
			}
			return ingest.Ingest(ctx, articleRepo, graphRepo, embedder, ingest.Request{
				ArticleID: event.ArticleID,
				Title:     event.Title,
				Markdown:  event.Markdown,
				Tags:      event.SecondaryTags,
			})
		},
	)
	if kafkaConsumer == nil {
		log.Warn("[init] kafka start failed, running without article sync")
	}

	return &Dependencies{
		ArticleRepo:     articleRepo,
		RecallRepo:      recallRepo,
		MemoryRepo:      memoryRepo,
		PoolRepo:        poolRepo,
		HistoryRepo:     historyRepo,
		MemoryChunkRepo: memoryChunkRepo,
		ProfileRepo:     profileRepo,
		GraphRepo:       graphRepo,
		Embedder:        embedder,
		ChatLLM:         chatLLM,
		RecoAgent:       recoAgent,
		KafkaConsumer:   kafkaConsumer,
	}, nil
}

// SetupRouter 创建 gin engine 并注册路由。
func SetupRouter(d *Dependencies) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, infrastructure.CheckHealth(c.Request.Context()))
	})
	r.GET("/metrics", gin.WrapH(infrastructure.MetricsHandler()))

	r.POST("/api/v1/docs/ingest", func(c *gin.Context) {
		handleIngest(d.ArticleRepo, d.GraphRepo, d.Embedder)(c)
	})
	r.POST("/api/v1/search", func(c *gin.Context) {
		handleSearch(d.RecallRepo, d.ArticleRepo, d.Embedder)(c)
	})
	r.POST("/api/v1/reco/recommend", func(c *gin.Context) {
		handleRecommend(d.RecoAgent)(c)
	})
	r.POST("/api/v1/reco/record", func(c *gin.Context) {
		handleRecord(d.HistoryRepo, d.Embedder)(c)
	})
	r.POST("/api/v1/reco/maintain", func(c *gin.Context) {
		handleMaintain(d.RecoAgent)(c)
	})
	r.GET("/api/v1/reco/profile", func(c *gin.Context) {
		handleGetProfile(d.ProfileRepo)(c)
	})
	r.POST("/api/v1/reco/profile", func(c *gin.Context) {
		handleSetProfile(d.ProfileRepo)(c)
	})
	r.POST("/api/v1/reco/memory", func(c *gin.Context) {
		handleAddMemory(d.MemoryRepo)(c)
	})

	return r
}

// Shutdown 优雅关闭所有资源。
func Shutdown(d *Dependencies) {
	if d.KafkaConsumer != nil {
		_ = d.KafkaConsumer.Stop()
	}
	infrastructure.CloseNeo4j(context.Background())
}
