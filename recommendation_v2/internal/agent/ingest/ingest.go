package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"recommendation_v2/config"
	"recommendation_v2/internal/repository"
	"recommendation_v2/internal/util/chunk"
	"recommendation_v2/internal/util"

	"trpc.group/trpc-go/trpc-agent-go/log"
)

// Request 文章入库请求。
type Request struct {
	ArticleID   string   `json:"article_id"`
	Title       string   `json:"title"`
	Cover       string   `json:"cover"`
	TypeTags    []string `json:"type_tags"`
	Tags        []string `json:"tags"`
	Score       float64  `json:"score"`
	Author      string   `json:"author"`
	GeoCity     string   `json:"geo_city"`
	ArticleJSON string   `json:"article_json"`
	Markdown    string   `json:"markdown"`
}

// Ingest 执行完整入库流程：parse → chunk → PG → embed → Milvus。
func Ingest(ctx context.Context, articleRepo *repository.ArticleRepo, graphRepo *repository.GraphRepo, embedder *util.Embedder, req Request) error {
	start := time.Now()

	a, err := parseInput(req)
	if err != nil {
		return fmt.Errorf("parse article: %w", err)
	}
	log.Infof("[ingest] parsed article=%s title=%q", a.ArticleID, a.Title)

	splitRes, err := chunk.SplitArticle(a,
		config.Cfg.Chunk.MaxTokens,
		config.Cfg.Chunk.Overlap,
		config.Cfg.Chunk.KeywordTopK)
	if err != nil {
		return fmt.Errorf("split article: %w", err)
	}
	log.Infof("[ingest] split article=%s fine_chunks=%d keywords=%v",
		a.ArticleID, len(splitRes.FineChunks), splitRes.Keywords)

	if err := articleRepo.UpsertArticle(ctx, a); err != nil {
		return fmt.Errorf("upsert article: %w", err)
	}
	if len(splitRes.FineChunks) > 0 {
		if err := articleRepo.UpsertChunks(ctx, splitRes.FineChunks); err != nil {
			return fmt.Errorf("upsert chunks: %w", err)
		}
	}
	log.Infof("[ingest] pg write done article=%s", a.ArticleID)

	if graphRepo != nil {
		if err := graphRepo.UpsertParent(ctx, a); err != nil {
			log.Warnf("[ingest] graph upsert parent failed: %v", err)
		} else {
			for _, c := range splitRes.FineChunks {
				_ = graphRepo.UpsertChild(ctx, c)
				_ = graphRepo.LinkHasChild(ctx, a.ArticleID, c.ChunkID)
			}
			_ = graphRepo.BuildSimilarEdges(ctx, splitRes.FineChunks, 0.8, 3)
		}
	}

	if err := embedAndWrite(ctx, articleRepo, a.ArticleID, embedder, splitRes.FineChunks); err != nil {
		return fmt.Errorf("embed and write milvus: %w", err)
	}

	log.Infof("[ingest] complete article=%s chunks=%d latency=%dms",
		a.ArticleID, len(splitRes.FineChunks), time.Since(start).Milliseconds())
	return nil
}

func parseInput(req Request) (chunk.Article, error) {
	if req.ArticleJSON != "" {
		var a chunk.Article
		if err := json.Unmarshal([]byte(req.ArticleJSON), &a); err != nil {
			return chunk.Article{}, fmt.Errorf("unmarshal article_json: %w", err)
		}
		a.ArticleID = chunk.NormalizeArticleID(req.ArticleID, a)
		if req.Title != "" {
			a.Title = req.Title
		}
		if req.Cover != "" {
			a.Cover = req.Cover
		}
		if len(req.TypeTags) > 0 {
			a.TypeTags = req.TypeTags
		}
		if len(req.Tags) > 0 {
			a.Tags = req.Tags
		}
		if req.Score > 0 {
			a.Score = req.Score
		}
		if req.Author != "" {
			a.Author = req.Author
		}
		if req.GeoCity != "" {
			a.GeoCity = req.GeoCity
		}
		return a, nil
	}

	if req.Markdown != "" {
		a, err := chunk.ParseMarkdownArticle(req.ArticleID, req.Markdown, req.Title)
		if err != nil {
			return chunk.Article{}, err
		}
		if req.Author != "" {
			a.Author = req.Author
		}
		if req.GeoCity != "" {
			a.GeoCity = req.GeoCity
		}
		return a, nil
	}

	return chunk.Article{}, fmt.Errorf("neither article_json nor markdown provided")
}

func embedAndWrite(ctx context.Context, articleRepo *repository.ArticleRepo, articleID string, embedder *util.Embedder, chunks []chunk.Chunk) error {
	if len(chunks) == 0 {
		return nil
	}

	var chunkIDs, h2s, contents []string
	var vectors [][]float32

	for _, c := range chunks {
		if c.ContentType != "text" || strings.TrimSpace(c.Content) == "" {
			continue
		}
		vec, err := embedder.Embed(ctx, c.Content)
		if err != nil {
			log.Warnf("[ingest] embed chunk=%s failed: %v, skip", c.ChunkID, err)
			continue
		}
		vectors = append(vectors, vec)
		chunkIDs = append(chunkIDs, c.ChunkID)
		h2s = append(h2s, struncate(c.H2, 256))
		contents = append(contents, struncate(c.Content, 8192))
	}

	if len(chunkIDs) == 0 {
		return nil
	}
	return articleRepo.InsertChunkVectors(ctx, articleID, chunkIDs, h2s, contents, vectors)
}

func struncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
