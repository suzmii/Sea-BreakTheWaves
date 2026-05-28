package repository

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"recommendation_v2/config"
	"recommendation_v2/internal/infrastructure"

	"github.com/milvus-io/milvus/client/v2/column"
	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

// UserHistoryItem 用户推荐历史记录。
type UserHistoryItem struct {
	HistoryID  string
	UserID     string
	ArticleID  string
	Clicked    bool
	Preference float32
	TS         time.Time
	Embed      []float32 `json:"-"`
	Similarity float32   `json:"-"`
}

// UserHistoryRepo 用户推荐历史存取（仅 Milvus）。
type UserHistoryRepo struct{}

func NewUserHistoryRepo() *UserHistoryRepo {
	return &UserHistoryRepo{}
}

func makeHistoryID(userID, articleID string, ts time.Time) string {
	return fmt.Sprintf("%s|%d|%s", userID, ts.UnixNano(), articleID)
}

// Add 写入一条历史记录到 Milvus。
func (r *UserHistoryRepo) Add(ctx context.Context, it UserHistoryItem) error {
	cli := infrastructure.Milvus()
	if cli == nil {
		return nil
	}
	if it.TS.IsZero() {
		it.TS = time.Now()
	}
	if it.HistoryID == "" {
		it.HistoryID = makeHistoryID(it.UserID, it.ArticleID, it.TS)
	}

	idCol := column.NewColumnVarChar("id", []string{it.HistoryID})
	userCol := column.NewColumnVarChar("user_id", []string{it.UserID})
	articleCol := column.NewColumnVarChar("article_id", []string{it.ArticleID})
	clickedCol := column.NewColumnBool("clicked", []bool{it.Clicked})
	prefCol := column.NewColumnFloat("preference", []float32{it.Preference})
	tsCol := column.NewColumnInt64("ts_unix", []int64{it.TS.Unix()})

	fields := []column.Column{idCol, userCol, articleCol, clickedCol, prefCol, tsCol}
	if len(it.Embed) > 0 {
		vecCol := column.NewColumnFloatVector("vector", config.Cfg.Milvus.Dim, [][]float32{it.Embed})
		fields = append(fields, vecCol)
	}

	_, err := cli.Insert(ctx, milvusclient.NewColumnBasedInsertOption(
		config.Cfg.Milvus.HistoryCollection, fields...,
	))
	return err
}

// ListRecent 返回用户最近 N 条历史记录（从 Milvus 查询后按时间排序）。
func (r *UserHistoryRepo) ListRecent(ctx context.Context, userID string, limit int) ([]UserHistoryItem, error) {
	cli := infrastructure.Milvus()
	if cli == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}

	opt := milvusclient.NewQueryOption(config.Cfg.Milvus.HistoryCollection).
		WithFilter(fmt.Sprintf(`user_id == "%s"`, escFilter(userID))).
		WithOutputFields("id", "user_id", "article_id", "clicked", "preference", "ts_unix").
		WithLimit(limit * 2)

	result, err := cli.Query(ctx, opt)
	if err != nil {
		return nil, err
	}

	return resultSetToHistoryItems(result, limit), nil
}

// SearchSimilar 在 Milvus 中对用户历史做向量相似检索。
func (r *UserHistoryRepo) SearchSimilar(ctx context.Context, userID string, query []float32, topK int) ([]UserHistoryItem, error) {
	cli := infrastructure.Milvus()
	if cli == nil || len(query) == 0 {
		return nil, nil
	}
	if topK <= 0 {
		topK = 10
	}

	opt := milvusclient.NewSearchOption(
		config.Cfg.Milvus.HistoryCollection,
		topK,
		[]entity.Vector{entity.FloatVector(query)},
	).WithANNSField("vector").WithFilter(
		fmt.Sprintf(`user_id == "%s"`, escFilter(userID)),
	).WithOutputFields("id", "user_id", "article_id", "clicked", "preference", "ts_unix")

	rs, err := cli.Search(ctx, opt)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, nil
	}

	set := rs[0]
	items := resultSetToHistoryItems(set, topK)
	for i := range items {
		if i < len(set.Scores) {
			items[i].Similarity = set.Scores[i]
		}
	}
	return items, nil
}

func resultSetToHistoryItems(set milvusclient.ResultSet, limit int) []UserHistoryItem {
	idCol := set.GetColumn("id")
	userCol := set.GetColumn("user_id")
	articleCol := set.GetColumn("article_id")
	clickedCol := set.GetColumn("clicked")
	prefCol := set.GetColumn("preference")
	tsCol := set.GetColumn("ts_unix")

	out := make([]UserHistoryItem, 0, set.ResultCount)
	for i := 0; i < set.ResultCount; i++ {
		var it UserHistoryItem
		if idCol != nil {
			if v, err := idCol.Get(i); err == nil {
				it.HistoryID, _ = v.(string)
			}
		}
		if userCol != nil {
			if v, err := userCol.Get(i); err == nil {
				it.UserID, _ = v.(string)
			}
		}
		if articleCol != nil {
			if v, err := articleCol.Get(i); err == nil {
				it.ArticleID, _ = v.(string)
			}
		}
		if clickedCol != nil {
			if v, err := clickedCol.Get(i); err == nil {
				it.Clicked, _ = v.(bool)
			}
		}
		if prefCol != nil {
			if v, err := prefCol.Get(i); err == nil {
				it.Preference, _ = v.(float32)
			}
		}
		if tsCol != nil {
			if v, err := tsCol.Get(i); err == nil {
				if ts, ok := v.(int64); ok {
					it.TS = time.Unix(ts, 0)
				}
			}
		}
		if it.HistoryID == "" {
			id, _ := set.IDs.GetAsString(i)
			it.HistoryID = id
		}
		out = append(out, it)
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].TS.After(out[j].TS)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func escFilter(s string) string {
	return strings.ReplaceAll(s, `"`, `\\"`)
}
