package recommend

import (
	"context"
	"encoding/json"
	"recommendation_v2/internal/util"

	"recommendation_v2/config"
	"trpc.group/trpc-go/trpc-agent-go/model"

	"trpc.group/trpc-go/trpc-agent-go/log"
)

func doIntent(ctx context.Context, chatLLM *util.ChatLLM, query string) (IntentResult, error) {
	sysPrompt := `你是一个推荐系统的意图识别器。请判断用户的输入属于以下哪种意图，只输出 JSON。
可能的意图标签：general（无特定意图/随便看看）、news（新闻资讯）、tech（科技）、life（生活）、entertainment（娱乐）、knowledge（知识干货）
示例输出：{"label":"tech","confidence":0.85,"signals":["ai","编程"]}`

	content, err := chatLLM.Chat(ctx,
		model.Message{Role: model.RoleSystem, Content: sysPrompt},
		model.Message{Role: model.RoleUser, Content: query},
	)
	if err != nil {
		return buildFallbackIntent(), err
	}

	var result IntentResult
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		log.Warnf("[reco] intent parse llm output failed: %v, raw=%s", err, content)
		return buildFallbackIntent(), nil
	}
	if result.Label == "" {
		result.Label = "general"
	}
	result.RecallPlan = buildRecallPlan(result.Label, result.Signals)
	return result, nil
}

func buildRecallPlan(label string, signals []string) *RecallPlan {
	plan := &RecallPlan{Strategy: MergeStrategyWeightedSum}
	for _, sc := range config.Cfg.Recall.Sources {
		if !sc.Enabled {
			continue
		}
		cfg := RecallSourceConfig{
			Name: sc.Name, TopK: sc.TopK, Weight: sc.Weight, Enabled: true,
		}
		// 按意图标签动态调整
		switch label {
		case "news":
			if cfg.Name == RecallSourceGeoMatch {
				cfg.Weight *= 0.5
			}
			if cfg.Name == RecallSourceTrending {
				cfg.Weight *= 1.5
			}
		case "knowledge", "tech":
			if cfg.Name == RecallSourceGeoMatch {
				cfg.Weight *= 0.3
			}
		case "life", "entertainment":
			if cfg.Name == RecallSourceGeoMatch {
				cfg.Weight *= 1.5
			}
		}
		plan.Sources = append(plan.Sources, cfg)
	}
	if len(plan.Sources) == 0 {
		plan.Sources = []RecallSourceConfig{{
			Name: RecallSourceTextVector, TopK: 80, Weight: 1.0, Enabled: true,
		}}
		plan.Strategy = MergeStrategyWeightedSum
	}
	return plan
}

func buildFallbackIntent() IntentResult {
	return IntentResult{
		Label: "general", Confidence: 0.5,
		RecallPlan: buildRecallPlan("general", nil),
	}
}
