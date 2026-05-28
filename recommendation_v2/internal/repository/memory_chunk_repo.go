package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"recommendation_v2/config"
	"recommendation_v2/internal/infrastructure"

	"github.com/milvus-io/milvus/client/v2/column"
	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

// MemoryChunkRepo 管理 user_memory_chunks（记忆分块）。
// PG 落原文/审计字段，Milvus 负责向量索引。
type MemoryChunkRepo struct {
	maxContentLen int
}

func NewMemoryChunkRepo() *MemoryChunkRepo {
	return &MemoryChunkRepo{maxContentLen: 8192}
}

func chunkID(userID string, memType string, periodBucket string, chunkIndex int, versionUnix int64) string {
	return fmt.Sprintf("%s|%s|%s|%d|%d", userID, memType, periodBucket, chunkIndex, versionUnix)
}

// ReplaceChunks 用新的 chunks 覆盖某个记忆（先删旧再写新）。
// updatedAt 作为 Milvus version 过滤条件。
func (r *MemoryChunkRepo) ReplaceChunks(ctx context.Context, userID, memType, periodBucket string, updatedAt time.Time, chunks []string, vectors [][]float32) error {
	// PG：删旧写新
	if _, err := infrastructure.Postgres().ExecContext(ctx, `
		DELETE FROM user_memory_chunks
		WHERE user_id=$1 AND memory_type=$2 AND period_bucket=$3
	`, userID, memType, periodBucket); err != nil {
		return err
	}

	for i, c := range chunks {
		if _, err := infrastructure.Postgres().ExecContext(ctx, `
			INSERT INTO user_memory_chunks(user_id, memory_type, period_bucket, chunk_index, content, updated_at)
			VALUES($1,$2,$3,$4,$5,$6)
		`, userID, memType, periodBucket, i, c, updatedAt); err != nil {
			return err
		}
	}

	// Milvus 写入
	cli := infrastructure.Milvus()
	if cli == nil {
		return nil
	}

	versionUnix := updatedAt.Unix()
	ids := make([]string, 0, len(chunks))
	vecs := make([][]float32, 0, len(chunks))
	idxs := make([]int64, 0, len(chunks))
	contents := make([]string, 0, len(chunks))
	memTypes := make([]string, 0, len(chunks))
	periods := make([]string, 0, len(chunks))

	for i, c := range chunks {
		if i >= len(vectors) || len(vectors[i]) == 0 {
			continue
		}
		ids = append(ids, chunkID(userID, memType, periodBucket, i, versionUnix))
		vecs = append(vecs, vectors[i])
		idxs = append(idxs, int64(i))
		if len(c) > r.maxContentLen {
			c = c[:r.maxContentLen]
		}
		contents = append(contents, c)
		memTypes = append(memTypes, memType)
		periods = append(periods, periodBucket)
	}

	if len(ids) == 0 {
		return nil
	}

	userIDs := repeatStr(userID, len(ids))
	idCol := column.NewColumnVarChar("id", ids)
	vecCol := column.NewColumnFloatVector("vector", config.Cfg.Milvus.Dim, vecs)
	userCol := column.NewColumnVarChar("user_id", userIDs)
	memTypeCol := column.NewColumnVarChar("memory_type", memTypes)
	periodCol := column.NewColumnVarChar("period_bucket", periods)
	chunkIdxCol := column.NewColumnInt64("chunk_index", idxs)
	versionCol := column.NewColumnInt64("version_unix", repeatInt64(versionUnix, len(ids)))
	contentCol := column.NewColumnVarChar("content", contents)

	opt := milvusclient.NewColumnBasedInsertOption(
		config.Cfg.Milvus.MemoryCollection,
		idCol, vecCol, userCol, memTypeCol, periodCol, chunkIdxCol, versionCol, contentCol,
	)
	_, err := cli.Insert(ctx, opt)
	return err
}

// SearchChunksByUser 搜索用户的所有记忆分块（不限制 version/memType）。
func (r *MemoryChunkRepo) SearchChunksByUser(ctx context.Context, userID string, query []float32, topK int) ([]string, error) {
	cli := infrastructure.Milvus()
	if cli == nil || len(query) == 0 {
		return nil, nil
	}
	if topK <= 0 {
		topK = 5
	}

	filter := fmt.Sprintf(`user_id == "%s"`, escMilvus(userID))
	opt := milvusclient.NewSearchOption(
		config.Cfg.Milvus.MemoryCollection,
		topK,
		[]entity.Vector{entity.FloatVector(query)},
	).WithANNSField("vector").WithFilter(filter).WithOutputFields("content")

	rs, err := cli.Search(ctx, opt)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, nil
	}

	set := rs[0]
	contentCol := set.GetColumn("content")
	out := make([]string, 0, set.ResultCount)
	for i := 0; i < set.ResultCount; i++ {
		if contentCol == nil {
			continue
		}
		if v, err := contentCol.Get(i); err == nil {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
	}
	return out, nil
}

// SearchMemoryChunks 在 Milvus 中做相似检索，返回最相关的记忆片段。
func (r *MemoryChunkRepo) SearchMemoryChunks(ctx context.Context, userID, memType, periodBucket string, updatedAt time.Time, query []float32, topK int) ([]string, error) {
	cli := infrastructure.Milvus()
	if cli == nil || len(query) == 0 {
		return nil, nil
	}
	if topK <= 0 {
		topK = 5
	}

	versionUnix := updatedAt.Unix()
	filter := fmt.Sprintf(`user_id == "%s" && memory_type == "%s" && period_bucket == "%s" && version_unix == %d`,
		escMilvus(userID), escMilvus(memType), escMilvus(periodBucket), versionUnix,
	)

	opt := milvusclient.NewSearchOption(
		config.Cfg.Milvus.MemoryCollection,
		topK,
		[]entity.Vector{entity.FloatVector(query)},
	).WithANNSField("vector").WithFilter(filter).WithOutputFields("content", "chunk_index")

	rs, err := cli.Search(ctx, opt)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, nil
	}

	set := rs[0]
	contentCol := set.GetColumn("content")
	out := make([]string, 0, set.ResultCount)
	for i := 0; i < set.ResultCount; i++ {
		if contentCol == nil {
			continue
		}
		if v, err := contentCol.Get(i); err == nil {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
	}
	return out, nil
}

func escMilvus(s string) string {
	return strings.ReplaceAll(s, `"`, `\\"`)
}

func repeatStr(v string, n int) []string {
	res := make([]string, n)
	for i := range res {
		res[i] = v
	}
	return res
}

func repeatInt64(v int64, n int) []int64 {
	res := make([]int64, n)
	for i := range res {
		res[i] = v
	}
	return res
}
