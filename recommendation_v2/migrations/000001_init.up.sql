CREATE TABLE IF NOT EXISTS articles (
    article_id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    cover TEXT,
    type_tags TEXT,
    tags TEXT,
    score REAL NOT NULL DEFAULT 0,
    author TEXT NOT NULL DEFAULT '',
    geo_city TEXT NOT NULL DEFAULT '',
    publish_time TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS article_chunks (
    chunk_id TEXT PRIMARY KEY,
    article_id TEXT NOT NULL REFERENCES articles(article_id) ON DELETE CASCADE,
    h2 TEXT,
    content TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS user_pool_items (
    user_id TEXT NOT NULL,
    pool_type TEXT NOT NULL,
    period_bucket TEXT NOT NULL DEFAULT '',
    article_id TEXT NOT NULL REFERENCES articles(article_id) ON DELETE CASCADE,
    score REAL NOT NULL DEFAULT 0,
    similarity REAL NOT NULL DEFAULT 0,
    remark_score REAL NOT NULL DEFAULT 0,
    inserted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, pool_type, period_bucket, article_id)
);

CREATE INDEX IF NOT EXISTS idx_user_pool_items_user_type
 ON user_pool_items(user_id, pool_type, period_bucket);

CREATE TABLE IF NOT EXISTS user_rec_history (
    history_id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    article_id TEXT NOT NULL REFERENCES articles(article_id) ON DELETE CASCADE,
    clicked BOOLEAN NOT NULL DEFAULT false,
    preference REAL NOT NULL DEFAULT 0,
    ts TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_user_rec_history_user_ts
 ON user_rec_history(user_id, ts DESC);

CREATE TABLE IF NOT EXISTS user_memories (
    id TEXT PRIMARY KEY,
    app_name TEXT NOT NULL DEFAULT 'recommendation',
    user_id TEXT NOT NULL,
    memory_content TEXT NOT NULL,
    topics TEXT NOT NULL DEFAULT '[]',
    kind TEXT NOT NULL DEFAULT 'fact',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_user_memories_user
 ON user_memories(app_name, user_id);

CREATE TABLE IF NOT EXISTS user_memory (
    user_id TEXT NOT NULL,
    memory_type TEXT NOT NULL,
    period_bucket TEXT NOT NULL DEFAULT '',
    content TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, memory_type, period_bucket)
);

CREATE TABLE IF NOT EXISTS user_profiles (
    user_id TEXT PRIMARY KEY,
    data JSONB NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS user_memory_chunks (
    user_id TEXT NOT NULL,
    memory_type TEXT NOT NULL,
    period_bucket TEXT NOT NULL DEFAULT '',
    chunk_index INT NOT NULL,
    content TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, memory_type, period_bucket, chunk_index)
);
