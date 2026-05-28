package recommend

import (
	"recommendation_v2/internal/util"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"recommendation_v2/config"
	"recommendation_v2/internal/repository"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

func doRerank(ctx context.Context, candidates []repository.RecallCandidate, memory MemoryContext, intent IntentResult, profile *UserProfile, chatLLM *util.ChatLLM, articleRepo *repository.ArticleRepo) ([]RerankItem, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	topK := 20

	articleIDs := make([]string, 0, len(candidates))
	for _, c := range candidates {
		articleIDs = append(articleIDs, c.ArticleID)
	}
	metas, err := articleRepo.GetMetas(ctx, articleIDs)
	if err != nil {
		return nil, err
	}

	var candLines strings.Builder
	for i, c := range candidates {
		m := metas[c.ArticleID]
		fmt.Fprintf(&candLines, "%d. %s | %s | type:%s | tags:%s | score:%.3f\n",
			i+1, c.ArticleID, m.Title, m.TypeTags, m.Tags, c.Score)
	}

	var memParts []string
	for _, e := range memory.Entries {
		if e != nil && e.Memory != nil {
			t := e.Memory.Memory
			if len(e.Memory.Topics) > 0 {
				t += " [" + strings.Join(e.Memory.Topics, ",") + "]"
			}
			memParts = append(memParts, t)
		}
	}
	memText := strings.Join(memParts, "\n")

	sysPrompt := "你是一个推荐系统的精排序器。根据用户意图、记忆和候选列表，输出排序后的 JSON 结果。"
	profileText := ""
	if profile != nil {
		var parts []string
		if v := profile.Strings("interest_domains"); len(v) > 0 {
			parts = append(parts, "兴趣领域: "+strings.Join(v, ","))
		}
		if v := profile.Strings("interest_tags"); len(v) > 0 {
			parts = append(parts, "兴趣标签: "+strings.Join(v, ","))
		}
		if v := profile.String("home_city"); v != "" {
			parts = append(parts, "所在城市: "+v)
		}
		if len(parts) > 0 {
			profileText = "\n用户画像：\n" + strings.Join(parts, "\n")
		}
	}

	userPrompt := fmt.Sprintf("用户意图：%s\n用戶记忆：\n%s%s\n候选列表：\n%s\n请只输出一个合法 JSON 对象，格式：{\"ranked\":[{\"article_id\":\"...\",\"score\":0.xx,\"reason\":\"...\"}]}\n只保留 top %d，reason 要简短。",
		intent.Label, memText, profileText, candLines.String(), topK)

	content, err := chatLLM.Chat(ctx,
		model.Message{Role: model.RoleSystem, Content: sysPrompt},
		model.Message{Role: model.RoleUser, Content: userPrompt},
	)
	if err != nil {
		return nil, err
	}

	var result struct {
		Ranked []RerankItem `json:"ranked"`
	}
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, fmt.Errorf("rerank llm parse failed: %w", err)
	}

	allowed := make(map[string]struct{}, len(candidates))
	for _, c := range candidates {
		allowed[c.ArticleID] = struct{}{}
	}
	filtered := make([]RerankItem, 0, len(result.Ranked))
	seen := make(map[string]struct{}, len(result.Ranked))
	for _, item := range result.Ranked {
		if _, ok := allowed[item.ArticleID]; !ok || item.ArticleID == "" {
			continue
		}
		if _, ok := seen[item.ArticleID]; ok {
			continue
		}
		seen[item.ArticleID] = struct{}{}
		filtered = append(filtered, item)
		if len(filtered) >= topK {
			break
		}
	}
	return filtered, nil
}

func semanticFilter(ctx context.Context, query string, candidates []repository.RecallCandidate) ([]repository.RecallCandidate, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	chunkIDs := make([]string, 0, len(candidates))
	for _, c := range candidates {
		chunkIDs = append(chunkIDs, c.ChunkID)
	}
	chunkContent, err := (&repository.ArticleRepo{}).GetChunks(ctx, chunkIDs)
	if err != nil {
		return nil, err
	}

	topK := config.Cfg.Rerank.DashScopeTopK
	if topK <= 0 {
		topK = len(candidates)
	}

	docs := make([]string, 0, len(chunkContent))
	chunkByContent := make(map[string]repository.RecallCandidate, len(candidates))
	for _, c := range candidates {
		content := chunkContent[c.ChunkID]
		if content == "" {
			continue
		}
		docs = append(docs, content)
		chunkByContent[content] = c
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("no chunk content found")
	}

	body := map[string]any{
		"model":     config.Cfg.Ali.RerankModel,
		"query":     query,
		"documents": docs,
		"top_k":     topK,
	}
	b, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, config.Cfg.Ali.RerankURL, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+config.Cfg.Ali.APIKey)

	cli := &http.Client{Timeout: 30 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dashscope rerank: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	var result struct {
		Output struct {
			Results []struct {
				Index          int     `json:"index"`
				RelevanceScore float32 `json:"relevance_score"`
			} `json:"results"`
		} `json:"output"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("dashscope parse: %w", err)
	}

	out := make([]repository.RecallCandidate, 0, len(result.Output.Results))
	for _, r := range result.Output.Results {
		if r.Index < 0 || r.Index >= len(docs) {
			continue
		}
		c, ok := chunkByContent[docs[r.Index]]
		if !ok {
			continue
		}
		c.Score = r.RelevanceScore
		out = append(out, c)
	}
	return out, nil
}
