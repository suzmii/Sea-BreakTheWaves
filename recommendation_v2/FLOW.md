# 推荐系统 v2 — 数据流文档

## Pipeline 总览

```
              ┌──────────────────────────────┐
              │  文章入库 (Ingest)            │
              │  POST /api/v1/docs/ingest    │
              └──────────┬───────────────────┘
                         │
                         ▼
              ┌──────────────────────────────┐
              │  推荐请求 (Recommend)         │
              │  POST /api/v1/reco/recommend │
              │                              │
              │  1. nodeIntent               │
              │     意图识别 + 召回计划       │
              │                              │
              │  2. nodeMemory               │
              │     记忆加载 + 画像 + chunk   │
              │                              │
              │  3. nodeRecall               │
              │     多路并行召回 + 加权合并    │
              │                              │
              │  4. nodeRerank               │
              │     LLM 个性化精排            │
              │                              │
              │  5. nodeOutput               │
              │     输出 + 后处理 hook        │
              │                              │
              │  6. nodeSideEffect            │
              │     补池 + 曝光记录           │
              └──────────────────────────────┘
                         │
                         ▼
              ┌──────────────────────────────┐
              │  记忆沉淀 (Maintain)          │
              │  POST /api/v1/reco/maintain  │
              │  └─ SettleProfile (回调)      │
              └──────────────────────────────┘
```

---

## 1. 文章入库

### 入口

```
POST /api/v1/docs/ingest
```

### 请求结构

```
ingestRequest
├── article_id    string      "art_001"
├── title         string      "周末露营攻略"
├── cover         string      "https://..."
├── type_tags     []string    ["旅行", "户外"]
├── tags          []string    ["露营", "帐篷"]
├── score         float64     0.85
├── author        string      "张三"
├── geo_city      string      "杭州"
├── article_json  string      (JSON of chunk.Article, 可选)
└── markdown      string      (原始 markdown, 可选)
```

### 流程

```
ingestRequest
│
├─ parseInput()
│   └─ chunk.Article
│       ├── ArticleID  "art_001"
│       ├── Title      "周末露营攻略"
│       ├── TypeTags   ["旅行","户外"]
│       ├── Tags       ["露营","帐篷"]
│       ├── Score      0.85
│       ├── Author     "张三"
│       ├── GeoCity    "杭州"
│       └── Sections   [{H2, Blocks}]
│
├─ chunk.SplitArticle(article, maxTokens, overlap, keywordTopK)
│   └─ SplitResult
│       ├── CoarseText   string (标题+类型+关键词摘要)
│       ├── CoarseIntro  string (intro)
│       ├── FineChunks   []Chunk (按段落/heading 切分的文本片段)
│       ├── Keywords     []string
│       └── KeywordScore float32
│
├─ PG 写入
│   ├─ articleRepo.UpsertArticle()    → articles 表
│   └─ articleRepo.UpsertChunks()     → article_chunks 表
│
└─ embedAndWrite()
    └─ 每个 chunk:
        ├─ agent.TextVector(chunk.Content) → []float32 (768d)
        └─ articleRepo.InsertChunkVectors()
            └─ Milvus article_collection
                └─ {id, vector(768d), article_id, chunk_id, h2, document}
```

### 结束状态

| 存储 | 变更 |
|---|---|
| PG `articles` | +1 行 (含 geo_city, author) |
| PG `article_chunks` | +N 行 |
| Milvus `article_collection` | +N 条向量 |

---

## 2. 推荐请求

### 入口

```
POST /api/v1/reco/recommend
```

### 请求 → 初始状态

```
Request                    pipelineState
├── UserID       "user123"  ├── UserID       "user123"
├── Query        "..."      ├── Query        "..."
├── PeriodBucket "d1"       ├── PeriodBucket "d1"
└── Surface      "home_feed" ├── Intent       零值
                            ├── Memory       零值
                            ├── Profile      零值
                            ├── Candidates   nil
                            ├── Reranked     nil
                            └── FinalIDs     nil
```

---

## 3. nodeIntent — 意图识别 + 召回计划

### 输入状态

`pipelineState` 中：`UserID`、`Query` 已赋值，其余为零值。

### 流程

```
nodeIntent(ctx, s)
│
├─ Query == "" ?
│   ├─ Yes → buildFallbackIntent() → {Label:"general", Confidence:1.0, RecallPlan}
│   └─ No  → doIntent(ctx, query)
│       │
│       ├─ callLLM(sysPrompt, query)
│       │   └─ {"label":"explore","confidence":0.9,"signals":["露营"]}
│       │
│       ├─ buildRecallPlan(label, signals)
│       │   ├─ 从 config.yaml 读取启用的源
│       │   ├─ 按 label 动态调整 weight
│       │   │   news      → trending×1.5, geo×0.5
│       │   │   life/ent  → geo×1.5
│       │   │   knowledge → geo×0.3
│       │   └─ RecallPlan
│       │       ├── Strategy: weighted_sum
│       │       └── Sources:
│       │           └─ [{Name:"text_vector", TopK:80, Weight:1.0, Enabled:true},
│       │              {Name:"geo_match",   TopK:30, Weight:0.6, Enabled:true}]
│       │
│       └─ IntentResult
│           └── {Label:"explore", Confidence:0.9, Signals:[], RecallPlan}
│
└─ ps.Intent = result
```

### 输出状态变化

| 字段 | 之前 | 之后 |
|---|---|---|
| `ps.Intent` | 零值 | `{Label:"explore", Confidence:0.9, RecallPlan:{...}}` |

---

## 4. nodeMemory — 记忆 + 画像 + chunk 向量召回

### 输入状态

`ps.Intent` 已赋值，`ps.Memory` / `ps.Profile` 为空。

### 流程

```
nodeMemory(ctx, s)
│
├─ searchQuery = ps.Query
│   └─ query 为空时后备: ps.Intent.Label
│
├─ 三路并行 (goroutine)
│   │
│   ├─ ① ReadMemories(userKey, 20)
│   │   └─ 最近 20 条记忆 (时间倒序)
│   │      └─ 例如 MaintainWindow 写入的摘要
│   │
│   ├─ ② SearchMemories(userKey, searchQuery, topK=10, threshold=0.2)
│   │   └─ BM25 keyword 搜索 → 10 条相关记忆
│   │
│   └─ ③ TextVector(searchQuery) → SearchChunksByUser(userID, vec, 5)
│       └─ 包装为 memory.Entry (topic:"memory_chunk")
│           └─ Milvus 中记忆分块的向量命中
│
├─ 合并 & 去重 (按 Entry.ID)
│   └─ ps.Memory = MemoryContext{combined}
│
└─ profileRepo.GetOrInit(userID)
    └─ map → UserProfile (封装)
        └─ ps.Profile = *UserProfile{interest_domains, home_city, ...}
```

### 输出状态变化

| 字段 | 之前 | 之后 |
|---|---|---|
| `ps.Memory` | 零值 | `{Entries: [30 条记忆]} ` |
| `ps.Profile` | 零值 | `*UserProfile{interest_domains:["旅行"], home_city:"杭州"}` |

---

## 5. nodeRecall — 多路召回

### 输入状态

`ps.Intent` 含 `RecallPlan`，`ps.Profile` 含画像信号，`ps.Candidates` 为 nil。

### 流程

```
nodeRecall(ctx, s)
│
└─ executeRecallPlan(plan, intent, profile)
    │
    ├─ 读取 plan.Sources → 匹配 recallSources() 注册的实现
    │
    ├─ 按 plan 并行执行各源
    │   │
    │   ├─ text_vector (weight:1.0)
    │   │   ├─ TextVector(intent.Label) → []float32
    │   │   ├─ recallRepo.Search(vec, topK=80)
    │   │   │   └─ Milvus ANN → 80 条 RecallCandidate
    │   │   └─ 输出 {Name, Candidates, Weight}
    │   │
    │   └─ geo_match (weight:0.6)
    │       ├─ profile.String("home_city") → "杭州"
    │       ├─ PG: SELECT article_id FROM articles WHERE geo_city = '杭州'
    │       └─ 输出 {Name, Candidates[{Score:0.9}], Weight}
    │
    ├─ applyFreshness (score modifier)
    │   └─ 按 score 估算天数 → 新鲜度衰减
    │
    └─ mergeCandidates(runs, strategy, topK=80)
        ├─ normalizeScores (每路独立 Min-Max → [0,1])
        ├─ combineScore (策略: weighted_sum / weighted_product / max)
        └─ sort + topK → 80 条 RecallCandidate
```

### 输出状态变化

| 字段 | 之前 | 之后 |
|---|---|---|
| `ps.Candidates` | nil | `[{ArticleID, ChunkID, Score}, ...]` (80 条) |

---

## 6. nodeRerank — 精排序

### 输入状态

`ps.Candidates` 80 条，`ps.Memory` 含记忆，`ps.Profile` 含画像。

### 流程

```
nodeRerank(ctx, s)
│
├─ (可选) semanticFilter()
│   ├─ DashScope rerank API
│   ├─ 语义相关度过滤
│   └─ 候选数可能减少
│
└─ doRerank(candidates, memory, intent, profile, articleRepo)
    │
    ├─ articleRepo.GetMetas(articleIDs)
    │   └─ map[articleID]ArticleMeta{Title, TypeTags, Tags, GeoCity, Author}
    │
    ├─ 构建 prompt 模板:
    │
    │   用户意图：{intent.Label}
    │   用户记忆：{memory 条目内容}
    │   用户画像：{profile 兴趣领域、标签、城市}
    │   候选列表：
    │     1. art_001 | 标题 | type:... | tags:... | score:0.95
    │     ...
    │   → 输出 top 20 JSON
    │
    ├─ callLLM(temperature, prompt)
    │   └─ JSON → [{article_id, score, reason}]
    │
    └─ 过滤: 去除重复/无效 ID → top 20
```

### 输出状态变化

| 字段 | 之前 | 之后 |
|---|---|---|
| `ps.Reranked` | nil | `[{ArticleID, Score, Reason}, ...]` (20 条) |

---

## 7. nodeOutput — 输出 + 后处理

### 流程

```
nodeOutput(ctx, s)
│
├─ 从 Reranked 提取 ArticleID
│   └─ ps.FinalIDs = ["art_001", "art_005", ...]
│
└─ ps.PostProcess()      ← 可扩展 hook
    └─ 默认: 空操作 (可在此做去重/打散/多样性/已推荐过滤)
```

### 输出状态变化

| 字段 | 之前 | 之后 |
|---|---|---|
| `ps.FinalIDs` | nil | `["art_001", "art_005", ...]` (20 个) |

---

## 8. nodeSideEffect — 推荐后副作用

### 输入状态

`ps.FinalIDs` 20 个推荐结果。

### 流程

```
nodeSideEffect(ctx, s)
│
├─ EnsurePool(3 个池, 异步)
│   ├─ long_term  → 异步 goroutine (信号量限流 2)
│   ├─ short_term → 异步 goroutine
│   └─ periodic   → 异步 goroutine
│       └─ 每个 goroutine:
│           ├─ TextVector(intentLabel) → vec
│           ├─ recallRepo.Search(vec, topK)
│           └─ poolRepo.AddItems(候选)
│
├─ (可选) RemoveItems(3 个池)
│   └─ 从池中移除已推荐的文章
│
└─ 异步记录曝光
    └─ goroutine (独立 trace span):
        ├─ TextVector(intentLabel) → vec
        └─ historyRepo.Add × N (写入 user_rec_history + Milvus)
```

### 最终返回

```json
Response{Status:"ok", IDs:["art_001", "art_005", ..., "art_020"]}
```

---

## 9. 记忆沉淀 (独立流程)

### 入口

```
POST /api/v1/reco/maintain
```

### 流程

```
MaintainWindow(userID, window, topics)
│
├─ historyRepo.ListRecent(userID, 500)
│   └─ 按 window 过滤 → 该时间窗口内行为
│
├─ articleRepo.GetMetas(articleIDs)
│   └─ 聚合 type_tags / tags (点击加权)
│
├─ 构建摘要文本
│   过去 1 天的压缩用户画像：
│   - 行为历史：共 10 条记录，点击 5 条
│   - 最近关注：周末露营攻略
│   - 长期偏好类型：旅行(+5)、户外(+3)
│   - 长期偏好标签：露营(+4)
│
├─ memSvc.AddMemory(userKey, content, topics)
│   └─ user_memories 表新增
│
├─ SettleProfile() (回调)
│   │
│   └─ SettleProfile(userID)
│       ├─ profileRepo.Get → cursor (profile_settle_cursor)
│       ├─ 只处理 cursor ~ now-7d 之间的记忆
│       ├─ memSvc.ListMemoriesSince(cursor)
│       ├─ 解析每条记忆中的 "偏好类型"/"偏好标签"
│       ├─ 聚合频次 → interest_domains / interest_tags
│       ├─ 递增 profile_confidence (+0.1, 上限 0.95)
│       └─ profileRepo.SetMulti(...)
│
└─ splitAndEmbedMemory(content)
    ├─ 按段落切分
    ├─ TextVector(段落) × N
    └─ memoryChunkRepo.ReplaceChunks(Milvus)
```

---

## 存储全景

### PG 表

| 表 | 用途 | 核心字段 |
|---|---|---|
| `articles` | 文章元信息 | article_id, title, cover, type_tags, tags, score, author, geo_city, publish_time |
| `article_chunks` | 文章 chunk 原文 | chunk_id, article_id, h2, content |
| `user_pool_items` | 推荐候选池 | user_id, pool_type, period_bucket, article_id, score, similarity |
| `user_rec_history` | 用户行为记录 | history_id, user_id, article_id, clicked, preference, ts |
| `user_memories` | 用户文字记忆 | id, app_name, user_id, memory_content, topics, kind |
| `user_memory` | 旧记忆表（兼容） | user_id, memory_type, period_bucket, content |
| `user_profiles` | 结构化画像 (JSONB) | user_id, data (JSONB), updated_at |
| `user_memory_chunks` | 记忆分块原文 | user_id, memory_type, period_bucket, chunk_index, content |

### Milvus Collections

| Collection | 用途 | 字段 |
|---|---|---|
| `article_collection` (fine) | 文章 chunk 向量 | id, vector(768d), article_id, chunk_id, h2, document |
| `history_collection` | 用户历史向量 | id, vector, user_id, article_id, clicked, preference, ts_unix |
| `memory_collection` | 记忆分块向量 | id, vector, user_id, memory_type, period_bucket, chunk_index, version_unix, content |
