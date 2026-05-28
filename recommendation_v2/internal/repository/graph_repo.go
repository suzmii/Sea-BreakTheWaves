package repository

import (
	"context"
	"strings"

	"recommendation_v2/internal/infrastructure"
	"recommendation_v2/internal/util/chunk"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// GraphRepo Neo4j 图存储，用于 GraphRAG 候选扩展。
// 节点类型：
//   - Parent: 文章级别（article_id, title, type_tags, keywords）
//   - Child:  chunk 级别（chunk_id, h2）
// 关系类型：
//   - HAS_CHILD: Parent → Child
//   - SIMILAR:   Child → Child（语义相似）
//   - NEXT:      Child → Child（文章内顺序）
type GraphRepo struct{}

func NewGraphRepo() *GraphRepo {
	return &GraphRepo{}
}

func (r *GraphRepo) driver() neo4j.DriverWithContext {
	return infrastructure.Neo4j()
}

func (r *GraphRepo) available() bool {
	return r.driver() != nil
}

// UpsertParent 写入或更新文章节点。
func (r *GraphRepo) UpsertParent(ctx context.Context, a chunk.Article) error {
	if !r.available() {
		return nil
	}
	drv := r.driver()
	sess := drv.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer sess.Close(ctx)

	_, err := sess.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		q := `MERGE (p:Parent {article_id: $article_id})
SET p.title = $title, p.type_tags = $type_tags, p.keywords = $keywords, p.author = $author, p.geo_city = $geo_city`
		_, err := tx.Run(ctx, q, map[string]any{
			"article_id": a.ArticleID,
			"title":      a.Title,
			"type_tags":  strings.Join(a.TypeTags, ","),
			"keywords":   strings.Join(a.Tags, ","),
			"author":     a.Author,
			"geo_city":   a.GeoCity,
		})
		return nil, err
	})
	return err
}

// UpsertChild 写入或更新 chunk 节点。
func (r *GraphRepo) UpsertChild(ctx context.Context, c chunk.Chunk) error {
	if !r.available() {
		return nil
	}
	drv := r.driver()
	sess := drv.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer sess.Close(ctx)

	_, err := sess.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		q := `MERGE (c:Child {chunk_id: $chunk_id})
SET c.article_id = $article_id, c.h2 = $h2`
		_, err := tx.Run(ctx, q, map[string]any{
			"chunk_id":   c.ChunkID,
			"article_id": c.ArticleID,
			"h2":         c.H2,
		})
		return nil, err
	})
	return err
}

// LinkHasChild 关联文章与其 chunk。
func (r *GraphRepo) LinkHasChild(ctx context.Context, articleID, chunkID string) error {
	if !r.available() {
		return nil
	}
	drv := r.driver()
	sess := drv.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer sess.Close(ctx)

	_, err := sess.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx,
			`MATCH (p:Parent {article_id: $aid}) MATCH (c:Child {chunk_id: $cid}) MERGE (p)-[:HAS_CHILD]->(c)`,
			map[string]any{"aid": articleID, "cid": chunkID})
		return nil, err
	})
	return err
}

// BuildSimilarEdges 为 chunk 列表两两计算相似度，建 SIMILAR 边。
func (r *GraphRepo) BuildSimilarEdges(ctx context.Context, chunks []chunk.Chunk, threshold float64, topK int) error {
	if !r.available() || len(chunks) < 2 {
		return nil
	}
	if topK <= 0 {
		topK = 3
	}
	if threshold <= 0 {
		threshold = 0.8
	}

	drv := r.driver()
	sess := drv.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer sess.Close(ctx)

	for i, a := range chunks {
		for j, b := range chunks {
			if j <= i {
				continue
			}
			sim := cosineSimilarity(a.Content, b.Content)
			if sim < threshold {
				continue
			}
			if j-i == 1 && a.ChunkID[:len(a.ArticleID)] == b.ArticleID {
				// 相邻 chunk 建 NEXT 边
				_, err := sess.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
					_, err := tx.Run(ctx,
						`MATCH (a:Child {chunk_id: $a}) MATCH (b:Child {chunk_id: $b}) MERGE (a)-[:NEXT {weight: $w}]->(b)`,
						map[string]any{"a": a.ChunkID, "b": b.ChunkID, "w": sim})
					return nil, err
				})
				if err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// SimExpr 计算纯文本的余弦相似度（基于字面 token 重叠）。
func cosineSimilarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	tokensA := tokenizeString(a)
	tokensB := tokenizeString(b)
	if len(tokensA) == 0 || len(tokensB) == 0 {
		return 0
	}
	intersect := 0
	for t := range tokensA {
		if tokensB[t] {
			intersect++
		}
	}
	// 近似 Jaccard 余弦
	union := len(tokensA) + len(tokensB) - intersect
	if union == 0 {
		return 0
	}
	return float64(intersect) / float64(union)
}

func tokenizeString(s string) map[string]bool {
	out := make(map[string]bool)
	for _, w := range strings.Fields(s) {
			w = strings.TrimSpace(w)
		if len(w) >= 2 {
			out[w] = true
		}
	}
	return out
}

// ExpandCandidates 从候选 articleID 出发，沿 SIMILAR/NEXT 边扩展，返回扩展的 articleID 列表。
func (r *GraphRepo) ExpandCandidates(ctx context.Context, articleIDs []string, limit int) ([]string, error) {
	if !r.available() || len(articleIDs) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}

	drv := r.driver()
	sess := drv.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer sess.Close(ctx)

	rows, err := sess.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		q := `MATCH (p:Parent)-[:HAS_CHILD]->(c:Child)-[r:SIMILAR|NEXT]->(n:Child)<-[:HAS_CHILD]-(exp:Parent)
WHERE p.article_id IN $ids AND NOT exp.article_id IN $ids
RETURN exp.article_id AS id, count(r) AS hits, sum(coalesce(r.weight, 0.5)) AS score
ORDER BY score DESC LIMIT $limit`
		res, err := tx.Run(ctx, q, map[string]any{"ids": articleIDs, "limit": limit})
		if err != nil {
			return nil, err
		}
		var out []string
		for res.Next(ctx) {
			v, _ := res.Record().Get("id")
			if id, ok := v.(string); ok && id != "" {
				out = append(out, id)
			}
		}
		return out, res.Err()
	})
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, nil
	}
	return rows.([]string), nil
}

// DeleteArticle 删除文章及其所有关联节点。
func (r *GraphRepo) DeleteArticle(ctx context.Context, articleID string) error {
	if !r.available() {
		return nil
	}
	drv := r.driver()
	sess := drv.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer sess.Close(ctx)

	_, err := sess.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx,
			`MATCH (p:Parent {article_id: $id}) OPTIONAL MATCH (p)-[:HAS_CHILD]->(c:Child) DETACH DELETE p, c`,
			map[string]any{"id": articleID})
		return nil, err
	})
	return err
}
