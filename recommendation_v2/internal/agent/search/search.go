package search

import (
	"context"
	"fmt"

	"recommendation_v2/internal/repository"
	"recommendation_v2/internal/util"
)

// Request 搜索请求。
type Request struct {
	Query string `json:"query" binding:"required"`
	TopK  int    `json:"topk"`
}

// Item 搜索结果项。
type Item struct {
	ArticleID string  `json:"article_id"`
	Title     string  `json:"title"`
	TypeTags  string  `json:"type_tags"`
	Tags      string  `json:"tags"`
	Score     float32 `json:"score"`
}

// Search 执行内容搜索。
func Search(ctx context.Context, recallRepo *repository.RecallRepo, articleRepo *repository.ArticleRepo, embedder *util.Embedder, query string, topK int) ([]Item, error) {
	if topK <= 0 {
		topK = 20
	}

	vec, err := embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	candidates, err := recallRepo.Search(ctx, vec, topK)
	if err != nil {
		return nil, fmt.Errorf("recall search: %w", err)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	ids := make([]string, 0, len(candidates))
	for _, c := range candidates {
		ids = append(ids, c.ArticleID)
	}

	metas, err := articleRepo.GetMetas(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("get metas: %w", err)
	}

	items := make([]Item, 0, len(candidates))
	for _, c := range candidates {
		m := metas[c.ArticleID]
		items = append(items, Item{
			ArticleID: c.ArticleID,
			Title:     m.Title,
			TypeTags:  m.TypeTags,
			Tags:      m.Tags,
			Score:     c.Score,
		})
	}
	return items, nil
}
