package main

import (
	"net/http"

	"recommendation_v2/internal/agent/search"
	"recommendation_v2/internal/infrastructure"
	"recommendation_v2/internal/repository"
	"recommendation_v2/internal/util"
	atrace "trpc.group/trpc-go/trpc-agent-go/telemetry/trace"

	"github.com/gin-gonic/gin"
	"trpc.group/trpc-go/trpc-agent-go/log"
)

func handleSearch(recallRepo *repository.RecallRepo, articleRepo *repository.ArticleRepo, embedder *util.Embedder) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req search.Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": -1, "msg": err.Error()})
			return
		}

		ctx, span := atrace.Tracer.Start(c.Request.Context(), "search")
		defer span.End()

		items, err := search.Search(ctx, recallRepo, articleRepo, embedder, req.Query, req.TopK)
		if err != nil {
			log.Errorf("[search] failed: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"code": -2, "msg": err.Error()})
			return
		}
		infrastructure.SearchRequests.Inc()
		c.JSON(http.StatusOK, gin.H{"code": 0, "msg": "ok", "data": items})
	}
}
