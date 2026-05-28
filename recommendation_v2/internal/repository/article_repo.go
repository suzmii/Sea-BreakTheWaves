package repository

import (
	"context"
	"fmt"
	"strings"

	"time"
	"recommendation_v2/internal/util/chunk"
	"recommendation_v2/config"
	"recommendation_v2/internal/infrastructure"

	"github.com/milvus-io/milvus/client/v2/column"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

// ArticleMeta 文章元信息。
type ArticleMeta struct {
	ArticleID string
	Title     string
	TypeTags  string
	Tags      string
	GeoCity   string
	Author    string
}

// ChunkContent chunk 内容。
type ChunkContent struct {
	ChunkID string
	Content string
}

// ArticleRepo PG 文章数据存取。
type ArticleRepo struct{}

func NewArticleRepo() *ArticleRepo {
	return &ArticleRepo{}
}

// GetMetas 批量获取文章元信息。
func (r *ArticleRepo) GetMetas(ctx context.Context, articleIDs []string) (map[string]ArticleMeta, error) {
	if len(articleIDs) == 0 {
		return nil, nil
	}

	params := make([]string, 0, len(articleIDs))
	args := make([]any, 0, len(articleIDs))
	for i, id := range articleIDs {
		params = append(params, fmt.Sprintf("$%d", i+1))
		args = append(args, id)
	}

	rows, err := infrastructure.Postgres().QueryContext(ctx,
		fmt.Sprintf(`SELECT article_id, title, type_tags, tags, COALESCE(geo_city,''), COALESCE(author,'') FROM articles WHERE article_id IN (%s)`, strings.Join(params, ",")),
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]ArticleMeta, len(articleIDs))
	for rows.Next() {
		var m ArticleMeta
		if err := rows.Scan(&m.ArticleID, &m.Title, &m.TypeTags, &m.Tags, &m.GeoCity, &m.Author); err != nil {
			continue
		}
		out[m.ArticleID] = m
	}
	return out, nil
}

// GetChunks 批量获取 chunk 内容。
// UpsertArticle 写入或更新文章元信息。
func (r *ArticleRepo) UpsertArticle(ctx context.Context, a chunk.Article) error {
	_, err := infrastructure.Postgres().ExecContext(ctx, `
		INSERT INTO articles(article_id, title, cover, type_tags, tags, score, geo_city, author, created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8, now())
		ON CONFLICT(article_id) DO UPDATE SET
			title=EXCLUDED.title, cover=EXCLUDED.cover,
			type_tags=EXCLUDED.type_tags, tags=EXCLUDED.tags,
			score=EXCLUDED.score, geo_city=EXCLUDED.geo_city,
				author=EXCLUDED.author
	`, a.ArticleID, a.Title, a.Cover,
		strings.Join(a.TypeTags, ","), strings.Join(a.Tags, ","),
		a.Score, a.GeoCity, a.Author)
	return err
}

// UpsertChunks 批量写入或更新 chunk。
func (r *ArticleRepo) UpsertChunks(ctx context.Context, chunks []chunk.Chunk) error {
	for _, c := range chunks {
		if _, err := infrastructure.Postgres().ExecContext(ctx, `
			INSERT INTO article_chunks(chunk_id, article_id, h2, content, created_at)
			VALUES($1,$2,$3,$4, now())
			ON CONFLICT(chunk_id) DO UPDATE SET
				article_id=EXCLUDED.article_id, h2=EXCLUDED.h2,
				content=EXCLUDED.content
		`, c.ChunkID, c.ArticleID, c.H2, c.Content); err != nil {
			return err
		}
	}
	return nil
}

// InsertChunkVectors 将已向量化的 chunk 写入 Milvus。
func (r *ArticleRepo) InsertChunkVectors(ctx context.Context, articleID string, chunkIDs, h2s, contents []string, vectors [][]float32) error {
	cli := infrastructure.Milvus()
	if cli == nil {
		return fmt.Errorf("milvus not initialized")
	}
	if len(chunkIDs) == 0 {
		return nil
	}

	dim := config.Cfg.Milvus.Dim
	articleIDs := repeatStr(articleID, len(chunkIDs))

	opt := milvusclient.NewColumnBasedInsertOption(
		config.Cfg.Milvus.ArticleCollection,
		column.NewColumnVarChar("id", chunkIDs),
		column.NewColumnFloatVector("vector", dim, vectors),
		column.NewColumnVarChar("user_id", repeatStr("", len(chunkIDs))),
		column.NewColumnVarChar("article_id", articleIDs),
		column.NewColumnVarChar("chunk_id", chunkIDs),
		column.NewColumnVarChar("tags", repeatStr("", len(chunkIDs))),
		column.NewColumnFloat("score", repeatFloat32(0, len(chunkIDs))),
		column.NewColumnInt64("created_at_unix", repeatInt64(time.Now().Unix(), len(chunkIDs))),
		column.NewColumnVarChar("h2", h2s),
		column.NewColumnVarChar("document", contents),
	)
	_, err := cli.Insert(ctx, opt)
	return err
}

// DeleteChunkVectors 从 Milvus 删除指定文章的 chunk 向量。
func (r *ArticleRepo) DeleteChunkVectors(ctx context.Context, articleID string) error {
	cli := infrastructure.Milvus()
	if cli == nil {
		return nil
	}
	expr := fmt.Sprintf(`article_id == "%s"`, escQuote(articleID))
	_, err := cli.Delete(ctx, milvusclient.NewDeleteOption(
		config.Cfg.Milvus.ArticleCollection,
	).WithExpr(expr))
	return err
}

func escQuote(s string) string {
	return strings.ReplaceAll(s, `"`, `\\"`)
}

// DeleteArticle 删除文章（PG + Milvus）。Neo4j 删除由调用方通过 GraphRepo 处理。
func (r *ArticleRepo) DeleteArticle(ctx context.Context, articleID string) error {
	if _, err := infrastructure.Postgres().ExecContext(ctx,
		`DELETE FROM articles WHERE article_id=$1`, articleID); err != nil {
		return err
	}
	return r.DeleteChunkVectors(ctx, articleID)
}

func (r *ArticleRepo) GetChunks(ctx context.Context, chunkIDs []string) (map[string]string, error) {
	if len(chunkIDs) == 0 {
		return nil, nil
	}

	params := make([]string, 0, len(chunkIDs))
	args := make([]any, 0, len(chunkIDs))
	for i, id := range chunkIDs {
		params = append(params, fmt.Sprintf("$%d", i+1))
		args = append(args, id)
	}

	rows, err := infrastructure.Postgres().QueryContext(ctx,
		fmt.Sprintf(`SELECT chunk_id, content FROM article_chunks WHERE chunk_id IN (%s)`, strings.Join(params, ",")),
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]string, len(chunkIDs))
	for rows.Next() {
		var chunkID, content string
		if err := rows.Scan(&chunkID, &content); err != nil {
			continue
		}
		out[chunkID] = content
	}
	return out, nil
}

func repeatFloat32(v float32, n int) []float32 {
	res := make([]float32, n)
	for i := range res {
		res[i] = v
	}
	return res
}
