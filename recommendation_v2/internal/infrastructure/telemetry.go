package infrastructure

import (
	"context"
	"net/http"

	"recommendation_v2/config"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"trpc.group/trpc-go/trpc-agent-go/log"
	atrace "trpc.group/trpc-go/trpc-agent-go/telemetry/trace"
)

var (
	promRegistry *prometheus.Registry

	// 推荐请求量
	RecoRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "reco_requests_total",
		Help: "Total recommendation requests.",
	}, []string{"status"})

	// 推荐延迟
	RecoLatency = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "reco_latency_seconds",
		Help:    "Recommendation latency.",
		Buckets: []float64{0.01, 0.05, 0.1, 0.3, 0.5, 1, 2, 5},
	}, []string{})

	// 入库请求量
	IngestRequests = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "ingest_requests_total",
		Help: "Total document ingestion requests.",
	})


	// 各 graph node 延迟（按 node 名区分）
	NodeLatency = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "reco_node_latency_seconds",
		Help:    "Recommendation graph node latency.",
		Buckets: []float64{0.01, 0.05, 0.1, 0.3, 0.5, 1, 2, 5},
	}, []string{"node"})
	// 搜索请求量
	SearchRequests = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "search_requests_total",
		Help: "Total search requests.",
	})
)

func init() {
	promRegistry = prometheus.NewRegistry()
	promRegistry.MustRegister(RecoRequests, RecoLatency, IngestRequests, NodeLatency, SearchRequests)
}

// MetricsHandler 返回 /metrics 的 HTTP handler。

// CheckHealth 检查所有基础组件连接状态。
func CheckHealth(ctx context.Context) map[string]string {
	result := make(map[string]string)

	// Postgres
	if pgDB != nil {
		if err := pgDB.PingContext(ctx); err != nil {
			result["postgres"] = "unhealthy: " + err.Error()
		} else {
			result["postgres"] = "ok"
		}
	} else {
		result["postgres"] = "not initialized"
	}

	// Milvus
	if milvusCli != nil {
		result["milvus"] = "ok"
	} else {
		result["milvus"] = "not initialized"
	}

	// Neo4j
	if neo4jDriver != nil {
		if err := neo4jDriver.VerifyConnectivity(ctx); err != nil {
			result["neo4j"] = "unhealthy: " + err.Error()
		} else {
			result["neo4j"] = "ok"
		}
	} else {
		result["neo4j"] = "not initialized"
	}

	return result
}

func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(promRegistry, promhttp.HandlerOpts{})
}

// InitTelemetry 初始化 OpenTelemetry（trace + metric）。
func InitTelemetry(ctx context.Context) (func(), error) {
	serviceName := config.Cfg.Otel.ServiceName
	if serviceName == "" {
		serviceName = "recommendation-v2"
	}

	if config.Cfg.Otel.Enable {
		traceClean, err := atrace.Start(ctx,
			atrace.WithEndpoint(config.Cfg.Otel.OtlpGrpcAddress),
			atrace.WithProtocol("grpc"),
			atrace.WithServiceName(serviceName),
		)
		if err != nil {
			return nil, err
		}
		log.Info("[infra] telemetry tracing started")
		return func() { _ = traceClean() }, nil
	}

	log.Warn("[infra] telemetry tracing disabled")
	return func() {}, nil
}
