package repository

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"recommendation_v2/internal/infrastructure"

	"trpc.group/trpc-go/trpc-agent-go/memory"
	"trpc.group/trpc-go/trpc-agent-go/session"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

// MemoryRepo 是 memory.Service 的 PG 后端实现。
// 全量缓存到内存用于搜索，双写到 PG 持久化。
type MemoryRepo struct {
	mu    sync.RWMutex
	cache map[string]map[string]map[string]*memory.Entry // appName -> userID -> memoryID -> Entry
}

var _ memory.Service = (*MemoryRepo)(nil)

func NewMemoryRepo() *MemoryRepo {
	s := &MemoryRepo{
		cache: make(map[string]map[string]map[string]*memory.Entry),
	}
	s.loadFromPG()
	return s
}

func (s *MemoryRepo) loadFromPG() {
	rows, err := infrastructure.Postgres().QueryContext(context.Background(), `
		SELECT id, app_name, user_id, memory_content, topics, kind, created_at, updated_at
		FROM user_memories
	`)
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		entry := scanMemoryEntry(rows)
		if entry == nil {
			continue
		}
		s.putCache(entry)
	}
}

func (s *MemoryRepo) putCache(entry *memory.Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()

	app := s.cache[entry.AppName]
	if app == nil {
		app = make(map[string]map[string]*memory.Entry)
		s.cache[entry.AppName] = app
	}
	user := app[entry.UserID]
	if user == nil {
		user = make(map[string]*memory.Entry)
		app[entry.UserID] = user
	}
	user[entry.ID] = entry
}

func (s *MemoryRepo) delCache(appName, userID, memoryID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if app := s.cache[appName]; app != nil {
		if user := app[userID]; user != nil {
			delete(user, memoryID)
		}
	}
}

func (s *MemoryRepo) listEntries(appName, userID string) []*memory.Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	user := s.cache[appName][userID]
	out := make([]*memory.Entry, 0, len(user))
	for _, e := range user {
		out = append(out, e)
	}
	return out
}

// ListMemoriesSince 返回用户在指定时间之后创建的记忆。
func (s *MemoryRepo) ListMemoriesSince(ctx context.Context, userKey memory.UserKey, since time.Time) ([]*memory.Entry, error) {
	rows, err := infrastructure.Postgres().QueryContext(ctx, `
		SELECT id, app_name, user_id, memory_content, topics, kind, created_at, updated_at
		FROM user_memories
		WHERE app_name=$1 AND user_id=$2 AND created_at > $3
		ORDER BY created_at ASC
	`, userKey.AppName, userKey.UserID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*memory.Entry
	for rows.Next() {
		entry := scanMemoryEntry(rows)
		if entry == nil {
			continue
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}

// AddMemory 添加一条记忆。
func (s *MemoryRepo) AddMemory(ctx context.Context, userKey memory.UserKey, memoryStr string, topics []string, opts ...memory.AddOption) error {
	if err := userKey.CheckUserKey(); err != nil {
		return err
	}

	now := time.Now()
	entry := &memory.Entry{
		AppName:   userKey.AppName,
		UserID:    userKey.UserID,
		CreatedAt: now,
		UpdatedAt: now,
		Memory: &memory.Memory{
			Memory:      memoryStr,
			Topics:      filterTopics(topics),
			LastUpdated: &now,
			Kind:        memory.KindFact,
		},
	}
	if md := memory.ResolveAddOptions(opts); md != nil {
		if md.Kind != "" {
			entry.Memory.Kind = md.Kind
		}
		entry.Memory.EventTime = md.EventTime
		entry.Memory.Participants = md.Participants
		entry.Memory.Location = md.Location
	}
	entry.ID = memorySHA256ID(entry.Memory, userKey.AppName, userKey.UserID)

	topicsJSON, _ := json.Marshal(entry.Memory.Topics)
	_, err := infrastructure.Postgres().ExecContext(ctx, `
		INSERT INTO user_memories(id, app_name, user_id, memory_content, topics, kind, created_at, updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT(id) DO UPDATE SET
			memory_content=EXCLUDED.memory_content, topics=EXCLUDED.topics,
			kind=EXCLUDED.kind, updated_at=EXCLUDED.updated_at
	`, entry.ID, entry.AppName, entry.UserID, entry.Memory.Memory,
		string(topicsJSON), string(entry.Memory.Kind), entry.CreatedAt, entry.UpdatedAt)
	if err != nil {
		return err
	}
	s.putCache(entry)
	return nil
}

// UpdateMemory 更新一条记忆。
func (s *MemoryRepo) UpdateMemory(ctx context.Context, memoryKey memory.Key, memoryStr string, topics []string, opts ...memory.UpdateOption) error {
	if err := memoryKey.CheckMemoryKey(); err != nil {
		return err
	}

	now := time.Now()
	entry := &memory.Entry{
		ID:        memoryKey.MemoryID,
		AppName:   memoryKey.AppName,
		UserID:    memoryKey.UserID,
		UpdatedAt: now,
		Memory: &memory.Memory{
			Memory:      memoryStr,
			Topics:      filterTopics(topics),
			LastUpdated: &now,
			Kind:        memory.KindFact,
		},
	}
	if md := memory.ResolveUpdateOptions(opts); md != nil {
		if md.Kind != "" {
			entry.Memory.Kind = md.Kind
		}
		entry.Memory.EventTime = md.EventTime
		entry.Memory.Participants = md.Participants
		entry.Memory.Location = md.Location
	}

	topicsJSON, _ := json.Marshal(entry.Memory.Topics)
	_, err := infrastructure.Postgres().ExecContext(ctx, `
		UPDATE user_memories SET memory_content=$1, topics=$2, kind=$3, updated_at=$4
		WHERE id=$5 AND app_name=$6 AND user_id=$7
	`, entry.Memory.Memory, string(topicsJSON), string(entry.Memory.Kind),
		entry.UpdatedAt, entry.ID, entry.AppName, entry.UserID)
	if err != nil {
		return err
	}
	s.putCache(entry)
	return nil
}

// DeleteMemory 删除一条记忆。
func (s *MemoryRepo) DeleteMemory(ctx context.Context, memoryKey memory.Key) error {
	if err := memoryKey.CheckMemoryKey(); err != nil {
		return err
	}
	_, err := infrastructure.Postgres().ExecContext(ctx,
		`DELETE FROM user_memories WHERE id=$1 AND app_name=$2 AND user_id=$3`,
		memoryKey.MemoryID, memoryKey.AppName, memoryKey.UserID)
	if err == nil {
		s.delCache(memoryKey.AppName, memoryKey.UserID, memoryKey.MemoryID)
	}
	return err
}

// ClearMemories 清空用户所有记忆。
func (s *MemoryRepo) ClearMemories(ctx context.Context, userKey memory.UserKey) error {
	if err := userKey.CheckUserKey(); err != nil {
		return err
	}
	_, err := infrastructure.Postgres().ExecContext(ctx,
		`DELETE FROM user_memories WHERE app_name=$1 AND user_id=$2`,
		userKey.AppName, userKey.UserID)

	s.mu.Lock()
	if s.cache[userKey.AppName] != nil {
		delete(s.cache[userKey.AppName], userKey.UserID)
	}
	s.mu.Unlock()
	return err
}

// ReadMemories 读取用户最近的记忆。
func (s *MemoryRepo) ReadMemories(ctx context.Context, userKey memory.UserKey, limit int) ([]*memory.Entry, error) {
	entries := s.listEntries(userKey.AppName, userKey.UserID)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].UpdatedAt.After(entries[j].UpdatedAt)
	})
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

// SearchMemories 搜索用户记忆（关键词匹配）。
func (s *MemoryRepo) SearchMemories(ctx context.Context, userKey memory.UserKey, query string, opts ...memory.SearchOption) ([]*memory.Entry, error) {
	entries := s.listEntries(userKey.AppName, userKey.UserID)
	if len(entries) == 0 {
		return nil, nil
	}

	searchOpts := memory.ResolveSearchOptions(query, opts)
	if searchOpts.Query == "" {
		return nil, nil
	}

	minScore := searchOpts.SimilarityThreshold
	if minScore <= 0 {
		minScore = 0.3
	}
	maxResults := searchOpts.MaxResults
	if maxResults <= 0 {
		maxResults = 10
	}

	queryTokens := tokenizeMemQuery(searchOpts.Query)

	type scored struct {
		entry *memory.Entry
		score float64
	}
	var candidates []scored

	for _, e := range entries {
		if e == nil || e.Memory == nil {
			continue
		}
		if searchOpts.Kind != "" && e.Memory.Kind != searchOpts.Kind {
			continue
		}
		if searchOpts.TimeAfter != nil && e.Memory.EventTime != nil &&
			e.Memory.EventTime.Before(*searchOpts.TimeAfter) {
			continue
		}
		if searchOpts.TimeBefore != nil && e.Memory.EventTime != nil &&
			e.Memory.EventTime.After(*searchOpts.TimeBefore) {
			continue
		}

		score := scoreEntry(e, queryTokens)
		if score >= minScore {
			candidates = append(candidates, scored{e, score})
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].entry.UpdatedAt.After(candidates[j].entry.UpdatedAt)
	})
	if len(candidates) > maxResults {
		candidates = candidates[:maxResults]
	}

	out := make([]*memory.Entry, len(candidates))
	for i, c := range candidates {
		c.entry.Score = c.score
		out[i] = c.entry
	}
	return out, nil
}

func (s *MemoryRepo) Tools() []tool.Tool                             { return nil }
func (s *MemoryRepo) EnqueueAutoMemoryJob(_ context.Context, _ *session.Session) error { return nil }
func (s *MemoryRepo) Close() error                                   { return nil }

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func memorySHA256ID(mem *memory.Memory, appName, userID string) string {
	var b strings.Builder
	b.WriteString("memory:")
	b.WriteString(mem.Memory)
	b.WriteString("|app:")
	b.WriteString(appName)
	b.WriteString("|user:")
	b.WriteString(userID)
	if mem.Kind != "" {
		b.WriteString("|kind:")
		b.WriteString(string(mem.Kind))
	}
	hash := sha256.Sum256([]byte(b.String()))
	return fmt.Sprintf("%x", hash)
}

func tokenizeMemQuery(s string) []string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return nil
	}
	seen := make(map[string]bool)
	var tokens []string
	add := func(t string) {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			return
		}
		seen[t] = true
		tokens = append(tokens, t)
	}
	for _, w := range strings.Fields(s) {
		hasCJK := false
		var latBuf strings.Builder
		for _, r := range w {
			if isCJKChar(r) {
				hasCJK = true
				if latBuf.Len() > 0 {
					add(latBuf.String())
					latBuf.Reset()
				}
				add(string(r))
			} else if unicode.IsLetter(r) || unicode.IsDigit(r) {
				latBuf.WriteRune(r)
			}
		}
		if !hasCJK {
			add(w)
		} else if latBuf.Len() > 0 {
			add(latBuf.String())
		}
	}
	return tokens
}

func isCJKChar(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}

func scoreEntry(entry *memory.Entry, queryTokens []string) float64 {
	if len(queryTokens) == 0 {
		return 0
	}
	content := strings.ToLower(entry.Memory.Memory)
	topics := strings.ToLower(strings.Join(entry.Memory.Topics, " "))

	var matched float64
	for _, t := range queryTokens {
		if strings.Contains(content, t) {
			matched++
		} else if strings.Contains(topics, t) {
			matched += 0.65
		}
	}
	return math.Min(matched/float64(len(queryTokens)), 1.0)
}

func filterTopics(topics []string) []string {
	out := make([]string, 0, len(topics))
	for _, t := range topics {
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

func scanMemoryEntry(row interface{ Scan(dest ...any) error }) *memory.Entry {
	var id, appName, userID, memoryContent, topicsJSON, kind string
	var createdAt, updatedAt time.Time
	if err := row.Scan(&id, &appName, &userID, &memoryContent, &topicsJSON, &kind, &createdAt, &updatedAt); err != nil {
		return nil
	}
	var topics []string
	_ = json.Unmarshal([]byte(topicsJSON), &topics)
	return &memory.Entry{
		ID:      id,
		AppName: appName,
		UserID:  userID,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
		Memory: &memory.Memory{
			Memory: memoryContent,
			Topics: topics,
			Kind:   memory.Kind(kind),
		},
	}
}
