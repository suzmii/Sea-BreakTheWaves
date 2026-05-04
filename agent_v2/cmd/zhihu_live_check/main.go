package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent_v2/config"
	zhihutools "agent_v2/tools"

	agenttool "trpc.group/trpc-go/trpc-agent-go/tool"
)

type checkResult struct {
	Tool      string
	Status    string
	Evidence  string
	LatencyMs int64
	Error     string
}

func main() {
	configPath := flag.String("config", "config.yaml", "配置文件路径")
	outPath := flag.String("out", "doc/zhihu_tools_live_check.md", "Markdown 报告输出路径")
	query := flag.String("query", "AI-Agent", "知乎搜索关键词")
	count := flag.Int("count", 3, "返回数量，范围 1-10")
	timeout := flag.Duration("timeout", 45*time.Second, "整体验证超时时间")
	flag.Parse()

	if err := run(*configPath, *outPath, *query, *count, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "zhihu live check failed: %v\n", err)
		os.Exit(1)
	}
}

func run(configPath, outPath, query string, count int, timeout time.Duration) error {
	_ = config.Load(configPath)
	cfg := config.Cfg.Zhihu

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if strings.TrimSpace(cfg.AccessSecret) == "" && strings.TrimSpace(os.Getenv("ZHIHU_ACCESS_SECRET")) == "" {
		return writeReport(outPath, cfg, query, count, false, []checkResult{{
			Tool:   "zhihu_search",
			Status: "SKIP",
			Error:  "missing zhihu.access_secret or ZHIHU_ACCESS_SECRET",
		}})
	}

	toolMap := mapTools(zhihutools.NewZhihuTools(cfg))
	result := callZhihuSearch(ctx, toolMap, query, count)
	return writeReport(outPath, cfg, query, count, true, []checkResult{result})
}

func mapTools(items []agenttool.Tool) map[string]agenttool.Tool {
	out := make(map[string]agenttool.Tool, len(items))
	for _, item := range items {
		out[item.Declaration().Name] = item
	}
	return out
}

func callZhihuSearch(ctx context.Context, toolMap map[string]agenttool.Tool, query string, count int) checkResult {
	result := checkResult{Tool: "zhihu_search"}
	start := time.Now()
	out, err := callTool(ctx, toolMap[result.Tool], zhihutools.ZhihuSearchInput{
		Query: query,
		Count: count,
	})
	result.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		result.Status = "FAIL"
		result.Error = err.Error()
		return result
	}
	resp, ok := out.(zhihutools.ZhihuSearchResult)
	if !ok {
		result.Status = "FAIL"
		result.Error = fmt.Sprintf("unexpected result type %T", out)
		return result
	}
	if resp.Code != 0 {
		result.Status = "FAIL"
		result.Error = fmt.Sprintf("code=%d message=%s", resp.Code, resp.Message)
		return result
	}
	result.Status = "PASS"
	result.Evidence = zhihuEvidence(resp)
	return result
}

func callTool(ctx context.Context, item agenttool.Tool, args any) (any, error) {
	if item == nil {
		return nil, fmt.Errorf("tool not found")
	}
	callable, ok := item.(agenttool.CallableTool)
	if !ok {
		return nil, fmt.Errorf("tool %s is not callable", item.Declaration().Name)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	return callable.Call(ctx, raw)
}

func zhihuEvidence(resp zhihutools.ZhihuSearchResult) string {
	parts := []string{
		fmt.Sprintf("item_count=%d", resp.ItemCount),
		"message=" + resp.Message,
	}
	if len(resp.Items) > 0 {
		first := resp.Items[0]
		parts = append(parts,
			"first_title="+first.Title,
			"first_author="+first.AuthorName,
			"first_url="+first.URL,
			fmt.Sprintf("first_vote_up_count=%d", first.VoteUpCount),
		)
	}
	return joinEvidence(parts...)
}

func writeReport(outPath string, cfg config.ZhihuConfig, query string, count int, attempted bool, results []checkResult) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	now := time.Now().Format("2006-01-02 15:04:05 MST")
	pass, fail, skip := 0, 0, 0
	for _, result := range results {
		switch result.Status {
		case "PASS":
			pass++
		case "SKIP":
			skip++
		default:
			fail++
		}
	}

	var b strings.Builder
	b.WriteString("# 知乎 Tools 真实取数验证报告\n\n")
	b.WriteString("## 结论\n\n")
	if attempted && fail == 0 && pass == len(results) {
		b.WriteString("本次已通过 `zhihu_search` tool 发起真实知乎请求，并成功返回结构化数据。\n\n")
	} else if attempted {
		b.WriteString(fmt.Sprintf("本次已发起真实请求：PASS %d，FAIL %d，SKIP %d。失败项见明细。\n\n", pass, fail, skip))
	} else {
		b.WriteString("本次未能发起真实知乎请求：运行环境缺少知乎 Access Secret。代码级 tool wrapper 已有单元测试覆盖，真实取数需设置密钥后复跑。\n\n")
	}

	b.WriteString("## 本次运行\n\n")
	b.WriteString(fmt.Sprintf("- 运行时间：%s\n", now))
	b.WriteString(fmt.Sprintf("- query：`%s`\n", query))
	b.WriteString(fmt.Sprintf("- count：`%d`\n", count))
	b.WriteString(fmt.Sprintf("- openapi_base_url：`%s`\n", cfg.OpenAPIBaseURL))
	b.WriteString(fmt.Sprintf("- zhihu_search_url：`%s`\n", cfg.ZhihuSearchURL))
	b.WriteString(fmt.Sprintf("- access_secret_configured：`%t`\n", strings.TrimSpace(cfg.AccessSecret) != "" || strings.TrimSpace(os.Getenv("ZHIHU_ACCESS_SECRET")) != ""))
	b.WriteString("- 复跑命令：`go run ./cmd/zhihu_live_check -query AI-Agent -count 3 -out doc/zhihu_tools_live_check.md`\n\n")

	b.WriteString("## 明细\n\n")
	b.WriteString("| Tool | 状态 | 延迟(ms) | 证据 / 错误 |\n")
	b.WriteString("|---|---:|---:|---|\n")
	for _, result := range results {
		detail := result.Evidence
		if detail == "" {
			detail = result.Error
		}
		b.WriteString(fmt.Sprintf(
			"| `%s` | %s | %d | %s |\n",
			escapeCell(result.Tool),
			escapeCell(result.Status),
			result.LatencyMs,
			escapeCell(detail),
		))
	}

	b.WriteString("\n## 覆盖范围\n\n")
	b.WriteString("- 通过 `trpc-agent-go/tool/function.NewFunctionTool` 生成的 tool wrapper 发起调用，不绕过工具层。\n")
	b.WriteString("- `zhihu_search` tool 内部调用 `skills/zhihu-search/scripts/zhihu-search.py`，并通过环境变量传递知乎开放平台配置。\n")
	b.WriteString("- 报告只记录是否配置密钥，不输出 Access Secret。\n")
	b.WriteString("- 常规 `go test ./...` 使用临时 skill 脚本覆盖参数传递、环境变量注入、tool 声明和 JSON 解码。\n")

	return os.WriteFile(outPath, []byte(b.String()), 0o644)
}

func joinEvidence(parts ...string) string {
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			clean = append(clean, part)
		}
	}
	return strings.Join(clean, "; ")
}

func escapeCell(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}
