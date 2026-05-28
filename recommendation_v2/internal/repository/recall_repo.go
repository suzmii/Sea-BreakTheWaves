package repository

import (
	"context"

	"recommendation_v2/config"
	"recommendation_v2/internal/infrastructure"

	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

// RecallCandidate 召回候选（chunk 级别）。
type RecallCandidate struct {
	ArticleID string
	ChunkID   string
	Score     float32
}

// RecallRepo Milvus 向量召回。
type RecallRepo struct{}

func NewRecallRepo() *RecallRepo {
	return &RecallRepo{}
}

// Search 执行单级向量召回。
// 按 article_id 去重，每个文章只保留最高分 chunk。
func (r *RecallRepo) Search(ctx context.Context, vec []float32, limit int) ([]RecallCandidate, error) {
	if limit <= 0 {
		limit = config.Cfg.Recall.RecallK
		if limit <= 0 {
			limit = 80
		}
	}

	cli := infrastructure.Milvus()
	if cli == nil {
		return nil, nil
	}

	opt := milvusclient.NewSearchOption(
		config.Cfg.Milvus.ArticleCollection,
		limit,
		[]entity.Vector{entity.FloatVector(vec)},
	).WithANNSField("vector").WithOutputFields("article_id", "chunk_id")

	rs, err := cli.Search(ctx, opt)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, nil
	}

	set := rs[0]
	articleCol := set.GetColumn("article_id")
	chunkCol := set.GetColumn("chunk_id")

	minScore := config.Cfg.Recall.MinScore
	out := make([]RecallCandidate, 0, set.ResultCount)
	seen := make(map[string]struct{}, set.ResultCount)
	for i := 0; i < set.ResultCount; i++ {
		score := float32(0)
		if i < len(set.Scores) {
			score = set.Scores[i]
		}
		if minScore > 0 && score < minScore {
			continue
		}

		articleID := getColumnString(articleCol, i)
		if articleID == "" {
			id, _ := set.IDs.GetAsString(i)
			articleID = extractArticleID(id)
		}
		if articleID == "" {
			continue
		}
		if _, ok := seen[articleID]; ok {
			continue
		}
		seen[articleID] = struct{}{}

		chunkID := getColumnString(chunkCol, i)
		if chunkID == "" {
			id, _ := set.IDs.GetAsString(i)
			chunkID = id
		}

		out = append(out, RecallCandidate{
			ArticleID: articleID,
			ChunkID:   chunkID,
			Score:     score,
		})
	}
	return out, nil
}

func getColumnString(col any, idx int) string {
	type getter interface {
		Get(int) (any, error)
	}
	g, ok := col.(getter)
	if !ok || g == nil {
		return ""
	}
	v, err := g.Get(idx)
	if err != nil || v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

func extractArticleID(chunkID string) string {
	for i := len(chunkID) - 1; i >= 0; i-- {
		if chunkID[i] == '#' {
			return chunkID[:i]
		}
	}
	return chunkID
}
