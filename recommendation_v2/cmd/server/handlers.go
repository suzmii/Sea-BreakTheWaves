package main

import (
	"net/http"
	"strings"
	"time"

	"recommendation_v2/internal/agent/recommend"
	"recommendation_v2/internal/infrastructure"
	"recommendation_v2/internal/repository"
	"recommendation_v2/internal/util"

	"github.com/gin-gonic/gin"
	"trpc.group/trpc-go/trpc-agent-go/log"
	"trpc.group/trpc-go/trpc-agent-go/memory"
	atrace "trpc.group/trpc-go/trpc-agent-go/telemetry/trace"
)

func handleRecommend(recoAgent *recommend.RecommendAgent) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		var req recommend.Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": -1, "msg": err.Error()})
			return
		}
		ctx, span := atrace.Tracer.Start(c.Request.Context(), "recommend")
		defer span.End()
		resp, err := recoAgent.Recommend(ctx, req)
		status := "ok"
		if err != nil {
			status = "error"
		}
		infrastructure.RecoRequests.WithLabelValues(status).Inc()
		infrastructure.RecoLatency.WithLabelValues().Observe(time.Since(start).Seconds())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": -2, "msg": err.Error(), "data": resp})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 0, "msg": "ok", "data": resp})
	}
}

func handleRecord(historyRepo *repository.UserHistoryRepo, embedder *util.Embedder) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			UserID    string  `json:"user_id" binding:"required"`
			ArticleID string  `json:"article_id" binding:"required"`
			Clicked   bool    `json:"clicked"`
			Preference float32 `json:"preference"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": -1, "msg": err.Error()})
			return
		}
		var embed []float32
		vec, err := embedder.Embed(c.Request.Context(), req.ArticleID)
		if err == nil {
			embed = vec
		}
		if err := historyRepo.Add(c.Request.Context(), repository.UserHistoryItem{
			UserID:    req.UserID,
			ArticleID: req.ArticleID,
			Clicked:   req.Clicked,
			Preference: req.Preference,
			Embed:     embed,
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": -2, "msg": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 0, "msg": "ok"})
	}
}

func handleMaintain(recoAgent *recommend.RecommendAgent) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			UserID string `json:"user_id" binding:"required"`
			Window string `json:"window" binding:"required"`
			Topics string `json:"topics"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": -1, "msg": err.Error()})
			return
		}
		var window time.Duration
		switch req.Window {
		case "1d":
			window = 24 * time.Hour
		case "7d":
			window = 7 * 24 * time.Hour
		default:
			c.JSON(http.StatusBadRequest, gin.H{"code": -1, "msg": "window must be 1d or 7d"})
			return
		}
		topics := strings.Split(req.Topics, ",")
		if len(topics) == 0 || (len(topics) == 1 && topics[0] == "") {
			topics = []string{"user_profile"}
		}
		if err := recoAgent.MaintainWindow(c.Request.Context(), req.UserID, window, topics); err != nil {
			log.Errorf("[maintain] failed: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"code": -2, "msg": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 0, "msg": "ok"})
	}
}

func handleGetProfile(profileRepo *repository.ProfileRepo) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.Query("user_id")
		if userID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"code": -1, "msg": "user_id required"})
			return
		}
		profile, err := profileRepo.Get(c.Request.Context(), userID)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"code": -2, "msg": "profile not found"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 0, "msg": "ok", "data": profile})
	}
}

func handleSetProfile(profileRepo *repository.ProfileRepo) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			UserID string         `json:"user_id" binding:"required"`
			Data   map[string]any `json:"data" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": -1, "msg": err.Error()})
			return
		}
		if err := profileRepo.SetMulti(c.Request.Context(), req.UserID, req.Data); err != nil {
			log.Errorf("[profile] set failed: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"code": -2, "msg": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 0, "msg": "ok"})
	}
}

func handleAddMemory(memoryRepo *repository.MemoryRepo) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			UserID     string   `json:"user_id" binding:"required"`
			MemoryType string   `json:"memory_type" binding:"required"`
			Content    string   `json:"content" binding:"required"`
			Topics     []string `json:"topics"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": -1, "msg": err.Error()})
			return
		}
		topics := req.Topics
		if topics == nil {
			topics = []string{req.MemoryType}
		}
		if err := memoryRepo.AddMemory(c.Request.Context(),
			memory.UserKey{AppName: "recommendation", UserID: req.UserID},
			req.Content, topics); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": -2, "msg": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"code": 0, "msg": "ok"})
	}
}
