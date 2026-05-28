# recommendation-v2

基于 trpc-agent-go StateGraph 的推荐系统。相比原版精简 68%（15,348 → 4,968 行），功能不减反增。

## 流水线

```
intent → memory → recall → rerank → output → side_effect
```

| 节点 | 职责 |
|------|------|
| intent | LLM 意图识别 + 生成多路召回计划 |
| memory | 并行加载记忆 + 结构化画像 + 向量 chunk 召回 |
| recall | 按计划执行多路并行召回 + 加权合并 |
| rerank | LLM 精排（含画像信号 + 用户记忆上下文） |
| output | 输出 + 后处理 hook |
| side_effect | 异步补池 + 曝光记录 |

## 代码结构

```
recommendation_v2/
├── cmd/server/             ← HTTP 入口（main.go / app.go / handlers.go）
├── config/                 ← 配置定义 + yaml
├── migrations/             ← 数据库迁移（golang-migrate）
├── Makefile                ← migrate up/down 快捷命令
│
└── internal/
    ├── agent/
    │   ├── recommend/      ← 推荐 Agent（graph, nodes, intent, rerank, recall, refill, memory, types）
    │   ├── ingest/         ← 入库 Agent
    │   └── search/         ← 搜索 Agent
    │
    ├── util/               ← 共享工具
    │   ├── chunk/          ← 文章解析/切分
    │   └── llm.go          ← Embedder + ChatLLM（依赖注入）
    │
    ├── kafka/              ← Kafka 消费者
    ├── repository/         ← 数据存取层
    └── infrastructure/     ← 基础设施（PG / Milvus / Neo4j / OTel）
```

## 外部依赖

| 组件 | 用途 |
|------|------|
| PostgreSQL | 文章、候选池、用户画像、记忆 |
| Milvus | 向量索引（文章召回、记忆分块、历史检索） |
| Neo4j | GraphRAG 候选扩展（可选，降级不阻塞） |
| Kafka | 文章变更事件消费（可选，降级不阻塞） |
| DashScope (阿里) | text-embedding-v4 / qwen-max / qwen3-rerank |

## API

| 路径 | 方法 | 说明 |
|------|------|------|
| `/health` | GET | 各组件连接状态 |
| `/metrics` | GET | Prometheus 指标（含按 node 的延迟） |
| `/api/v1/docs/ingest` | POST | 文章入库 |
| `/api/v1/search` | POST | 内容搜索 |
| `/api/v1/reco/recommend` | POST | 推荐 |
| `/api/v1/reco/record` | POST | 记录用户行为 |
| `/api/v1/reco/maintain` | POST | 维护用户记忆（从历史行为提炼） |
| `/api/v1/reco/profile` | GET/POST | 用户画像读写 |
| `/api/v1/reco/memory` | POST | 更新用户记忆 |

## 开发

```bash
# 数据库迁移
make migrate-up

# 构建运行
go run ./cmd/server

# 测试
curl http://localhost:8082/health
```
