package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Log      LogConfig      `yaml:"log"`
	Otel     OtelConfig     `yaml:"otel"`
	Postgres PostgresConfig `yaml:"postgres"`
	Milvus   MilvusConfig   `yaml:"milvus"`
	Ali      AliConfig      `yaml:"ali"`
	Recall   RecallConfig   `yaml:"recall"`
	Rerank   RerankConfig   `yaml:"rerank"`
	Pools    PoolsConfig    `yaml:"pools"`
	Kafka    KafkaConfig    `yaml:"kafka"`
	Neo4j    Neo4jConfig    `yaml:"neo4j"`
	Chunk    ChunkConfig    `yaml:"chunk"`
	Services ServicesConfig `yaml:"services"`
}

type LogConfig struct {
	Level       string `yaml:"level"`
	ServiceName string `yaml:"service_name"`
}

type OtelConfig struct {
	Enable          bool   `yaml:"enable"`
	ServiceName     string `yaml:"service_name"`
	OtlpGrpcAddress string `yaml:"otlp_grpc_address"`
}

type PostgresConfig struct {
	DSN                    string `yaml:"dsn"`
	MaxOpenConns           int    `yaml:"max_open_conns"`
	MaxIdleConns           int    `yaml:"max_idle_conns"`
	ConnMaxLifetimeSeconds int    `yaml:"conn_max_lifetime_seconds"`
}

type MilvusConfig struct {
	Address           string `yaml:"address"`
	Username          string `yaml:"username"`
	Password          string `yaml:"password"`
	DBName            string `yaml:"dbname"`
	Dim               int    `yaml:"dim"`
	Metric            string `yaml:"metric"`
	ArticleCollection string `yaml:"article_collection"`  // 统一的召回 collection（chunk 级别）
	MemoryCollection  string `yaml:"memory_collection"`   // 用户记忆分块 collection
	HistoryCollection string `yaml:"history_collection"`  // 用户历史记录 collection
}

type AliConfig struct {
	APIKey      string `yaml:"apikey"`
	BaseURL     string `yaml:"baseurl"`
	RerankURL   string `yaml:"rerank_url"`
	RerankModel string `yaml:"rerank_model"`
	ChatModel   string `yaml:"chat_model"`
	EmbedModel  string `yaml:"embed_model"`
	Dimensions  int    `yaml:"dimensions"`
}

type RecallConfig struct {
	RecallK  int         `yaml:"recall_k"`  // 单次召回数量
	MinScore float32     `yaml:"min_score"` // 最低向量分数阈值
	Sources  []SourceConfig `yaml:"sources"` // 召回源配置
	Freshness FreshnessConfig `yaml:"freshness"` // 新鲜度衰减
	FinalTopK int           `yaml:"final_top_k"` // 合并后的最终候选数
}

type SourceConfig struct {
	Name    string  `yaml:"name"`
	TopK    int     `yaml:"top_k"`
	Weight  float64 `yaml:"weight"`
	Enabled bool    `yaml:"enabled"`
}

type FreshnessConfig struct {
	HalfLifeDays float64 `yaml:"half_life_days"`
	MaxDecay     float64 `yaml:"max_decay"`
	Weight       float64 `yaml:"weight"`
}

type RerankConfig struct {
	ChatModel       string  `yaml:"chat_model"`
	Temperature     float64 `yaml:"temperature"`
	EnableDashScope bool    `yaml:"enable_dashscope"` // 是否启用 DashScope 语义过滤
	DashScopeTopK   int     `yaml:"dashscope_topk"`   // DashScope rerank 返回条数
}

type KafkaConfig struct {
	Address    string `yaml:"address"`
	Topic      string `yaml:"topic"`
	Group      string `yaml:"group"`
	RetryTopic string `yaml:"retry_topic"`
	RetryGroup string `yaml:"retry_group"`
}

type Neo4jConfig struct {
	Address  string `yaml:"address"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type ServicesConfig struct {
	HTTPAddr string `yaml:"http_addr"`
	HTTPPort string `yaml:"http_port"`
}

type PoolsConfig struct {
	LongTerm  PoolPolicy `yaml:"long_term"`
	ShortTerm PoolPolicy `yaml:"short_term"`
	Periodic  PoolPolicy `yaml:"periodic"`
	Recommend RecommendPolicy `yaml:"recommend"`
}

type PoolPolicy struct {
	MinSize      int `yaml:"min_size"`
	TargetSize   int `yaml:"target_size"`
	RecallTopK   int `yaml:"recall_topk"`
}

type RecommendPolicy struct {
	TakeSize              int  `yaml:"take_size"`
	RemoveAfterRecommend bool `yaml:"remove_after_recommend"`
}

type ChunkConfig struct {
	MaxTokens   int `yaml:"max_tokens"`
	Overlap     int `yaml:"overlap"`
	KeywordTopK int `yaml:"keyword_topk"`
}

var Cfg Config

func Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, &Cfg)
}

func Init() error {
	return Load("config/config.yaml")
}
