package infrastructure

import (
	"context"
	"time"

	"recommendation_v2/config"

	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/index"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"trpc.group/trpc-go/trpc-agent-go/log"
)

var milvusCli *milvusclient.Client

func Milvus() *milvusclient.Client {
	return milvusCli
}

func InitMilvus() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cli, err := milvusclient.New(ctx, &milvusclient.ClientConfig{
		Address:  config.Cfg.Milvus.Address,
		Username: config.Cfg.Milvus.Username,
		Password: config.Cfg.Milvus.Password,
	})
	if err != nil {
		return err
	}

	_ = cli.CreateDatabase(ctx, milvusclient.NewCreateDatabaseOption(config.Cfg.Milvus.DBName))
	_ = cli.UseDatabase(ctx, milvusclient.NewUseDatabaseOption(config.Cfg.Milvus.DBName))

	milvusCli = cli

	dim := config.Cfg.Milvus.Dim

	if err := ensureArticleCollection(ctx, dim); err != nil {
		return err
	}
	if err := ensureMemoryCollection(ctx, dim); err != nil {
		return err
	}
	if err := ensureHistoryCollection(ctx, dim); err != nil {
		return err
	}

	log.Infof("[infra] milvus initialized, db=%s", config.Cfg.Milvus.DBName)
	return nil
}

func ensureCollection(ctx context.Context, name string, schema *entity.Schema) error {
	exist, err := milvusCli.HasCollection(ctx, milvusclient.NewHasCollectionOption(name))
	if err != nil {
		return err
	}
	if exist {
		_, err = milvusCli.LoadCollection(ctx, milvusclient.NewLoadCollectionOption(name))
		return err
	}

	if err := milvusCli.CreateCollection(ctx, milvusclient.NewCreateCollectionOption(name, schema)); err != nil {
		return err
	}

	idx := index.NewAutoIndex(entity.COSINE)
	if _, err := milvusCli.CreateIndex(ctx, milvusclient.NewCreateIndexOption(name, "vector", idx)); err != nil {
		return err
	}

	_, err = milvusCli.LoadCollection(ctx, milvusclient.NewLoadCollectionOption(name))
	return err
}

// ensureArticleCollection 文章召回向量集合（chunk 级别）。
func ensureArticleCollection(ctx context.Context, dim int) error {
	name := config.Cfg.Milvus.ArticleCollection
	schema := entity.NewSchema().WithName(name).WithAutoID(false).WithDynamicFieldEnabled(true).
		WithField(entity.NewField().WithName("id").WithDataType(entity.FieldTypeVarChar).
			WithTypeParams(entity.TypeParamMaxLength, "128").WithIsPrimaryKey(true).WithIsAutoID(false)).
		WithField(entity.NewField().WithName("vector").WithDataType(entity.FieldTypeFloatVector).WithDim(int64(dim))).
		WithField(entity.NewField().WithName("article_id").WithDataType(entity.FieldTypeVarChar).
			WithTypeParams(entity.TypeParamMaxLength, "128")).
		WithField(entity.NewField().WithName("chunk_id").WithDataType(entity.FieldTypeVarChar).
			WithTypeParams(entity.TypeParamMaxLength, "128")).
		WithField(entity.NewField().WithName("h2").WithDataType(entity.FieldTypeVarChar).
			WithTypeParams(entity.TypeParamMaxLength, "256")).
		WithField(entity.NewField().WithName("document").WithDataType(entity.FieldTypeVarChar).
			WithTypeParams(entity.TypeParamMaxLength, "8192")).
		WithField(entity.NewField().WithName("tags").WithDataType(entity.FieldTypeVarChar).
			WithTypeParams(entity.TypeParamMaxLength, "256")).
		WithField(entity.NewField().WithName("score").WithDataType(entity.FieldTypeFloat)).
		WithField(entity.NewField().WithName("created_at_unix").WithDataType(entity.FieldTypeInt64)).
		WithField(entity.NewField().WithName("user_id").WithDataType(entity.FieldTypeVarChar).
			WithTypeParams(entity.TypeParamMaxLength, "128"))
	return ensureCollection(ctx, name, schema)
}

// ensureMemoryCollection 用户记忆分块向量集合。
func ensureMemoryCollection(ctx context.Context, dim int) error {
	name := config.Cfg.Milvus.MemoryCollection
	schema := entity.NewSchema().WithName(name).WithAutoID(false).WithDynamicFieldEnabled(true).
		WithField(entity.NewField().WithName("id").WithDataType(entity.FieldTypeVarChar).
			WithTypeParams(entity.TypeParamMaxLength, "256").WithIsPrimaryKey(true).WithIsAutoID(false)).
		WithField(entity.NewField().WithName("vector").WithDataType(entity.FieldTypeFloatVector).WithDim(int64(dim))).
		WithField(entity.NewField().WithName("user_id").WithDataType(entity.FieldTypeVarChar).
			WithTypeParams(entity.TypeParamMaxLength, "128")).
		WithField(entity.NewField().WithName("memory_type").WithDataType(entity.FieldTypeVarChar).
			WithTypeParams(entity.TypeParamMaxLength, "32")).
		WithField(entity.NewField().WithName("period_bucket").WithDataType(entity.FieldTypeVarChar).
			WithTypeParams(entity.TypeParamMaxLength, "32")).
		WithField(entity.NewField().WithName("chunk_index").WithDataType(entity.FieldTypeInt64)).
		WithField(entity.NewField().WithName("version_unix").WithDataType(entity.FieldTypeInt64)).
		WithField(entity.NewField().WithName("content").WithDataType(entity.FieldTypeVarChar).
			WithTypeParams(entity.TypeParamMaxLength, "8192"))
	return ensureCollection(ctx, name, schema)
}

// ensureHistoryCollection 用户历史记录向量集合。
func ensureHistoryCollection(ctx context.Context, dim int) error {
	name := config.Cfg.Milvus.HistoryCollection
	schema := entity.NewSchema().WithName(name).WithAutoID(false).WithDynamicFieldEnabled(true).
		WithField(entity.NewField().WithName("id").WithDataType(entity.FieldTypeVarChar).
			WithTypeParams(entity.TypeParamMaxLength, "256").WithIsPrimaryKey(true).WithIsAutoID(false)).
		WithField(entity.NewField().WithName("vector").WithDataType(entity.FieldTypeFloatVector).WithDim(int64(dim))).
		WithField(entity.NewField().WithName("user_id").WithDataType(entity.FieldTypeVarChar).
			WithTypeParams(entity.TypeParamMaxLength, "128")).
		WithField(entity.NewField().WithName("article_id").WithDataType(entity.FieldTypeVarChar).
			WithTypeParams(entity.TypeParamMaxLength, "128")).
		WithField(entity.NewField().WithName("clicked").WithDataType(entity.FieldTypeBool)).
		WithField(entity.NewField().WithName("preference").WithDataType(entity.FieldTypeFloat)).
		WithField(entity.NewField().WithName("ts_unix").WithDataType(entity.FieldTypeInt64))
	return ensureCollection(ctx, name, schema)
}
