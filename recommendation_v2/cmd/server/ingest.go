package main

import (
	"net/http"

	"recommendation_v2/internal/agent/ingest"
	"recommendation_v2/internal/infrastructure"
	"recommendation_v2/internal/repository"
	"recommendation_v2/internal/util"
	atrace "trpc.group/trpc-go/trpc-agent-go/telemetry/trace"

	"github.com/gin-gonic/gin"
	"trpc.group/trpc-go/trpc-agent-go/log"
)

func handleIngest(articleRepo *repository.ArticleRepo, graphRepo *repository.GraphRepo, embedder *util.Embedder) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req ingest.Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": -1, "msg": err.Error()})
			return
		}
		ctx, span := atrace.Tracer.Start(c.Request.Context(), "ingest")
		defer span.End()

		if err := ingest.Ingest(ctx, articleRepo, graphRepo, embedder, req); err != nil {
			log.Errorf("[ingest] failed: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"code": -2, "msg": err.Error()})
			return
		}
		infrastructure.IngestRequests.Inc()
		c.JSON(http.StatusOK, gin.H{"code": 0, "msg": "ok"})
	}
}
