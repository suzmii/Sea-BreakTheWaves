# recommendation_v2

基于 trpc-agent-go StateGraph 的推荐系统。

## 架构概览

```
HTTP 请求 (POST /api/v1/reco/recommend)
  │
  ▼
Gin Router → RecommendAgent
               │
               ▼
        StateGraph 流水线
        6 个节点按序执行
```

**流水线节点：**

```
intent → memory → recall → rerank → output → side_effect
```

| 节点 | 职责 |
|------|------|
| intent | LLM 意图识别（general / news / tech / life / entertainment / knowledge） |
| memory | 并行加载用户短期/长期记忆 |
| recall | Milvus 向量召回候选文章（按 article_id 去重） |
| rerank | LLM 精排序 + 可选 DashScope 语义过滤 |
| output | 组装最终输出结果列表 |
| side_effect | 从候选池移除已推荐文章、异步记录曝光历史 |

## 目录结构

```
cmd/
├── server/main.go          # HTTP 服务入口
└── debug/main.go           # 本地调试入口

config/
├── config.go               # 配置结构体定义
└── config.yaml             # 运行时配置

internal/
├── agent/
│   ├── reco_graph.go       # StateGraph 流水线 + LLM 调用
│   └── types.go            # 请求/响应/状态类型定义
├── infrastructure/
│   ├── ai.go               # Embedding + LLM API 封装 (DashScope)
│   ├── milvus.go           # Milvus 初始化 + 3 个 collection 管理
│   ├── postgres.go         # PostgreSQL 连接管理
│   └── telemetry.go        # OpenTelemetry 初始化
└── repository/
    ├── article_repo.go     # 文章元数据 + chunk 内容读取 (PG)
    ├── recall_repo.go      # Milvus 向量召回
    ├── memory_repo.go      # 用户记忆读写 (PG)
    ├── memory_chunk_repo.go # 用户记忆分块 (PG + Milvus 双写)
    ├── pool_repo.go        # 候选池管理 (PG)
    └── user_history_repo.go # 用户推荐历史 (PG + Milvus 双写)
```

## 外部依赖

| 组件 | 用途 |
|------|------|
| PostgreSQL | 文章元数据、候选池、用户记忆、推荐历史的持久化存储 |
| Milvus | 向量索引（文章召回、记忆分块检索、历史相似检索） |
| DashScope (阿里百炼) | Embedding text-embedding-v4、Chat qwen-plus、Rerank qwen3-rerank |

### Milvus Collection

| Collection | 用途 | 说明 |
|-----------|------|------|
| `recall_fine_768` | 文章 chunk 级别召回 | 统一召回 collection |
| `user_memory_chunks` | 记忆分块向量检索 | version_unix 版本过滤 |
| `user_rec_history` | 推荐历史相似检索 | 支持点击/偏好过滤 |

## API

| 路径 | 方法 | 说明 |
|------|------|------|
| `/health` | GET | 健康检查 |
| `/metrics` | GET | Prometheus 指标 |
| `/api/v1/docs/ingest` | POST | 文章入库（支持 JSON 或 Markdown） |
| `/api/v1/search` | POST | 内容搜索 |
| `/api/v1/reco/recommend` | POST | 推荐主接口 |
| `/api/v1/reco/record` | POST | 记录用户行为（曝光/点击/偏好） |
| `/api/v1/reco/memory` | POST | 更新用户记忆 |

## 配置

参考 `config/config.yaml`，主要配置项：

- **milvus**: 地址、collection 名称、向量维度
- **ali**: DashScope API Key、模型选择 (embed/chat/rerank)
- **recall**: 召回数量、最低分数阈值
- **rerank**: LLM 精排参数、DashScope 语义过滤开关
- **pools**: 候选池策略（长期/短期/周期池大小、推荐后是否移除）
