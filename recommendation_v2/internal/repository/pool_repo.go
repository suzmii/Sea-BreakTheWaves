package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"recommendation_v2/internal/infrastructure"
)

// PoolType 候选池类型。
type PoolType string

const (
	PoolLongTerm  PoolType = "long_term"
	PoolShortTerm PoolType = "short_term"
	PoolPeriodic  PoolType = "periodic"
)

// PoolItem 候选池元素。
type PoolItem struct {
	UserID       string
	PoolType     PoolType
	PeriodBucket string
	ArticleID    string
	Score        float32
	Similarity   float32
	RemarkScore  float32
	InsertedAt   time.Time
}

// PoolRepo 候选池 PG 存取。
type PoolRepo struct{}

func NewPoolRepo() *PoolRepo {
	return &PoolRepo{}
}

// GetPoolSize 返回池中候选数量。
func (r *PoolRepo) GetPoolSize(ctx context.Context, userID string, poolType PoolType, periodBucket string) (int, error) {
	var n int
	err := infrastructure.Postgres().QueryRowContext(ctx, `
		SELECT COUNT(1) FROM user_pool_items
		WHERE user_id=$1 AND pool_type=$2 AND period_bucket=$3
	`, userID, string(poolType), periodBucket).Scan(&n)
	return n, err
}

// AddItems 批量把候选放入池子（重复 article_id 自动忽略）。
func (r *PoolRepo) AddItems(ctx context.Context, items []PoolItem) error {
	if len(items) == 0 {
		return nil
	}

	// 批量 multi-value INSERT，避免逐条 N 次 round trip
	const cols = 7
	args := make([]any, 0, len(items)*cols)
	buf := strings.Builder{}
	buf.WriteString("INSERT INTO user_pool_items(user_id, pool_type, period_bucket, article_id, score, similarity, remark_score, inserted_at) VALUES ")
	for i, it := range items {
		offset := i * cols
		if i > 0 {
			buf.WriteString(", ")
		}
		fmt.Fprintf(&buf, "($%d,$%d,$%d,$%d,$%d,$%d,$%d,now())",
			offset+1, offset+2, offset+3, offset+4, offset+5, offset+6, offset+7)
		args = append(args, it.UserID, string(it.PoolType), it.PeriodBucket, it.ArticleID, it.Score, it.Similarity, it.RemarkScore)
	}
	buf.WriteString(" ON CONFLICT(user_id, pool_type, period_bucket, article_id) DO UPDATE SET")
	buf.WriteString(" score=EXCLUDED.score, similarity=EXCLUDED.similarity, remark_score=EXCLUDED.remark_score, inserted_at=now()")

	_, err := infrastructure.Postgres().ExecContext(ctx, buf.String(), args...)
	return err
}

// PopTopK 从池子取出 topK（按 remark_score 降序），可选删除。
func (r *PoolRepo) PopTopK(ctx context.Context, userID string, poolType PoolType, periodBucket string, topK int, remove bool) ([]PoolItem, error) {
	rows, err := infrastructure.Postgres().QueryContext(ctx, `
		SELECT user_id, pool_type, period_bucket, article_id, score, similarity, remark_score, inserted_at
		FROM user_pool_items
		WHERE user_id=$1 AND pool_type=$2 AND period_bucket=$3
		ORDER BY remark_score DESC, inserted_at ASC
		LIMIT $4
	`, userID, string(poolType), periodBucket, topK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []PoolItem
	for rows.Next() {
		var it PoolItem
		var pt string
		if err := rows.Scan(&it.UserID, &pt, &it.PeriodBucket, &it.ArticleID, &it.Score, &it.Similarity, &it.RemarkScore, &it.InsertedAt); err != nil {
			return nil, err
		}
		it.PoolType = PoolType(pt)
		res = append(res, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if remove && len(res) > 0 {
		for _, it := range res {
			if _, err := infrastructure.Postgres().ExecContext(ctx, `
				DELETE FROM user_pool_items
				WHERE user_id=$1 AND pool_type=$2 AND period_bucket=$3 AND article_id=$4
			`, it.UserID, string(it.PoolType), it.PeriodBucket, it.ArticleID); err != nil {
				return nil, err
			}
		}
	}
	return res, nil
}

// RemoveItems 从池子中移除指定 article_id 列表（推荐后出池）。
func (r *PoolRepo) RemoveItems(ctx context.Context, userID string, poolType PoolType, periodBucket string, articleIDs []string) error {
	for _, id := range articleIDs {
		if _, err := infrastructure.Postgres().ExecContext(ctx, `
			DELETE FROM user_pool_items
			WHERE user_id=$1 AND pool_type=$2 AND period_bucket=$3 AND article_id=$4
		`, userID, string(poolType), periodBucket, id); err != nil {
			return err
		}
	}
	return nil
}
