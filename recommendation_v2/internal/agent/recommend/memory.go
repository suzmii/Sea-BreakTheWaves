package recommend

import (
	"context"
	"fmt"
	"recommendation_v2/internal/util"
	"sort"
	"strings"
	"time"

	"recommendation_v2/internal/repository"

	"trpc.group/trpc-go/trpc-agent-go/memory"
)

// MaintainWindow 基于用户过去一段时间的行为生成偏好摘要，写入记忆和 Milvus 分块。
// window: 时间窗口（如 24*time.Hour, 7*24*time.Hour）
// topics: 记忆主题标签，如 ["short_term"] 或 ["long_term", "user_profile"]
func (a *RecommendAgent) MaintainWindow(ctx context.Context, userID string, window time.Duration, topics []string) error {
	userKey := memory.UserKey{AppName: "recommendation", UserID: userID}

	// 1. 获取近期历史
	hist, err := a.historyRepo.ListRecent(ctx, userID, 500)
	if err != nil {
		return fmt.Errorf("list history: %w", err)
	}

	// 2. 按时间窗口过滤
	cutoff := time.Now().Add(-window)
	filtered := make([]repository.UserHistoryItem, 0, len(hist))
	articleSet := make(map[string]struct{})
	clickedCnt := 0
	for _, it := range hist {
		if it.TS.Before(cutoff) {
			continue
		}
		filtered = append(filtered, it)
		articleSet[it.ArticleID] = struct{}{}
		if it.Clicked {
			clickedCnt++
		}
	}

	windowLabel := formatWindow(window)

	if len(filtered) == 0 {
		content := fmt.Sprintf("过去 %s 内暂无可用行为记录。", windowLabel)
		return a.memSvc.AddMemory(ctx, userKey, content, append(topics, "empty"))
	}

	// 3. 获取文章元信息
	ids := make([]string, 0, len(articleSet))
	for id := range articleSet {
		ids = append(ids, id)
	}
	metas, err := a.articleRepo.GetMetas(ctx, ids)
	if err != nil {
		return fmt.Errorf("get metas: %w", err)
	}

	// 4. 聚合偏好统计
	typeCnt := map[string]int{}
	tagCnt := map[string]int{}
	articleCnt := map[string]int{}
	recentTitles := make([]string, 0, 6)
	seenTitle := map[string]struct{}{}

	for _, it := range filtered {
		meta, ok := metas[it.ArticleID]
		if !ok {
			continue
		}
		if title := strings.TrimSpace(meta.Title); title != "" {
			articleCnt[title]++
			if _, ok := seenTitle[title]; !ok && len(recentTitles) < 6 {
				seenTitle[title] = struct{}{}
				recentTitles = append(recentTitles, title)
			}
		}
		if !it.Clicked {
			continue
		}
		weight := 1
		if it.Preference > 0 {
			weight += int(it.Preference)
		}
		for _, tt := range splitCSV(meta.TypeTags) {
			typeCnt[tt] += weight
		}
		for _, tg := range splitCSV(meta.Tags) {
			tagCnt[tg] += weight
		}
	}

	recentFocus := topKCountPairs(articleCnt, 5)
	preferTypes := topKCountPairs(typeCnt, 5)
	preferTags := topKCountPairs(tagCnt, 5)
	content := buildWindowSummary(windowLabel, len(filtered), clickedCnt, recentTitles, recentFocus, preferTypes, preferTags)

	// 5. 写入记忆
	if err := a.memSvc.AddMemory(ctx, userKey, content, topics); err != nil {
		return fmt.Errorf("add memory: %w", err)
	}

	// 沉淀记忆到用户画像（非阻塞）
	_ = a.SettleProfile(ctx, userID)

	// 6. 记忆分块 + 向量写入 Milvus
	chunks, vectors, err := splitAndEmbedMemory(ctx, a.embedder, content)
	if err != nil {
		return fmt.Errorf("embed memory chunks: %w", err)
	}
	if len(chunks) > 0 {
		memType := strings.Join(topics, ",")
		if err := a.memoryChunkRepo.ReplaceChunks(ctx, userID, memType, "", time.Now(), chunks, vectors); err != nil {
			return fmt.Errorf("replace memory chunks: %w", err)
		}
	}

	return nil
}

// SettleProfile 将过期的记忆沉淀为结构化画像字段。
// 只处理距离上次沉淀超过 settleThreshold 的记忆（默认 7 天），
// 因为较新的记忆还能被 keyword 召回，不急着提取。
func (a *RecommendAgent) SettleProfile(ctx context.Context, userID string) error {
	raw, err := a.profileRepo.Get(ctx, userID)
	profile := NewUserProfileFromMap(raw)
	_ = err

	settleThreshold := 7 * 24 * time.Hour

	// 读取沉淀游标（unix 秒）
	cursor := profile.Float64("profile_settle_cursor")
	cursorTime := time.Unix(int64(cursor), 0)
	cutoff := time.Now().Add(-settleThreshold)
	if cutoff.Before(cursorTime) || cutoff.Equal(cursorTime) {
		return nil // 记忆还不够老，跳过
	}

	// 读取 cursor 之后到 cutoff 之间的记忆
	memRepo, ok := a.memSvc.(*repository.MemoryRepo)
	if !ok {
		return nil
	}
	entries, err := memRepo.ListMemoriesSince(ctx, memory.UserKey{AppName: "recommendation", UserID: userID}, cursorTime)
	if err != nil {
		return fmt.Errorf("list memories: %w", err)
	}

	// 只处理 cutoff 之前的（不处理最近 7 天内的）
	var expired []*memory.Entry
	for _, e := range entries {
		if e.CreatedAt.Before(cutoff) {
			expired = append(expired, e)
		}
	}
	if len(expired) == 0 {
		// 更新游标到 cutoff，避免下次再扫空集
		_ = a.profileRepo.Set(ctx, userID, "profile_settle_cursor", float64(cutoff.Unix()))
		return nil
	}

	// 从记忆中提取结构化字段
	domainCnt := map[string]int{}
	tagCnt := map[string]int{}
	for _, e := range expired {
		extractDomainAndTags(e.Memory.Memory, domainCnt, tagCnt)
	}

	updates := map[string]any{
		"profile_settle_cursor": float64(cutoff.Unix()),
		"profile_updated_at":    float64(time.Now().Unix()),
	}

	// 置信度递增（最多 0.95）
	confidence := profile.Float64("profile_confidence")
	confidence = confidence + 0.1
	if confidence > 0.95 {
		confidence = 0.95
	}
	updates["profile_confidence"] = confidence

	if len(domainCnt) > 0 {
		updates["interest_domains"] = topKStrings(domainCnt, 10)
	}
	if len(tagCnt) > 0 {
		updates["interest_tags"] = topKStrings(tagCnt, 20)
	}

	return a.profileRepo.SetMulti(ctx, userID, updates)
}

// extractDomainAndTags 从记忆摘要文本中解析出偏好类型和标签。
func extractDomainAndTags(content string, domainCnt, tagCnt map[string]int) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)

		if idx := strings.Index(line, "偏好类型："); idx >= 0 {
			after := line[idx+len("偏好类型："):]
			for _, p := range strings.Split(after, "、") {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				if i := strings.Index(p, "(+"); i > 0 {
					p = p[:i]
				}
				domainCnt[p]++
			}
		}

		if idx := strings.Index(line, "偏好标签："); idx >= 0 {
			after := line[idx+len("偏好标签："):]
			for _, p := range strings.Split(after, "、") {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				if i := strings.Index(p, "(+"); i > 0 {
					p = p[:i]
				}
				tagCnt[p]++
			}
		}
	}
}

// topKStrings 返回 map 中 topK 个 key（按 value 降序）。
func topKStrings(m map[string]int, k int) []string {
	type kv struct {
		K string
		V int
	}
	arr := make([]kv, 0, len(m))
	for k, v := range m {
		arr = append(arr, kv{k, v})
	}
	sort.Slice(arr, func(i, j int) bool {
		if arr[i].V == arr[j].V {
			return arr[i].K < arr[j].K
		}
		return arr[i].V > arr[j].V
	})
	if k > 0 && len(arr) > k {
		arr = arr[:k]
	}
	res := make([]string, 0, len(arr))
	for _, it := range arr {
		res = append(res, it.K)
	}
	return res
}

// splitAndEmbedMemory 将记忆内容切分为段落并向量化。
func splitAndEmbedMemory(ctx context.Context, embedder *util.Embedder, content string) (chunks []string, vectors [][]float32, err error) {
	parts := strings.Split(content, "\n")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		vec, err := embedder.Embed(ctx, p)
		if err != nil {
			return nil, nil, err
		}
		chunks = append(chunks, p)
		vectors = append(vectors, vec)
	}
	return chunks, vectors, nil
}

// formatWindow 将 duration 转为可读的中文描述。
func formatWindow(d time.Duration) string {
	if d == 24*time.Hour {
		return "1 天"
	}
	if d == 7*24*time.Hour {
		return "7 天"
	}
	days := int(d.Hours() / 24)
	if days > 0 {
		return fmt.Sprintf("%d 天", days)
	}
	return d.String()
}

// buildWindowSummary 构建压缩用户画像文本。
func buildWindowSummary(window string, historyCnt, clickedCnt int, recentTitles, recentFocus, types, tags []string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("过去 %s 的压缩用户画像：\n", window))
	b.WriteString(fmt.Sprintf("- 行为历史：共 %d 条记录，点击 %d 条。\n", historyCnt, clickedCnt))
	if len(recentTitles) > 0 {
		b.WriteString("- 最近关注：")
		b.WriteString(strings.Join(recentTitles, "、"))
		b.WriteString("\n")
	}
	if len(recentFocus) > 0 {
		b.WriteString("- 最近高频内容：")
		b.WriteString(strings.Join(recentFocus, "、"))
		b.WriteString("\n")
	}
	if len(types) > 0 {
		b.WriteString("- 长期偏好类型：")
		b.WriteString(strings.Join(types, "、"))
		b.WriteString("\n")
	}
	if len(tags) > 0 {
		b.WriteString("- 长期偏好标签：")
		b.WriteString(strings.Join(tags, "、"))
		b.WriteString("\n")
	}
	b.WriteString("- 检索提示：优先召回与最近关注、长期偏好类型和标签都重合的内容。")
	return strings.TrimSpace(b.String())
}

// splitCSV 按逗号拆分 CSV 字符串并去除空白。
func splitCSV(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	res := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		res = append(res, p)
	}
	return res
}

// topKCountPairs 返回 map 中 topK 个键值对，格式为 "key(+count)"。
func topKCountPairs(m map[string]int, k int) []string {
	if len(m) == 0 {
		return nil
	}
	type kv struct {
		Key string
		Val int
	}
	arr := make([]kv, 0, len(m))
	for k, v := range m {
		arr = append(arr, kv{k, v})
	}
	sort.Slice(arr, func(i, j int) bool {
		if arr[i].Val == arr[j].Val {
			return arr[i].Key < arr[j].Key
		}
		return arr[i].Val > arr[j].Val
	})
	if k > 0 && len(arr) > k {
		arr = arr[:k]
	}
	res := make([]string, 0, len(arr))
	for _, it := range arr {
		res = append(res, fmt.Sprintf("%s(+%d)", it.Key, it.Val))
	}
	return res
}
