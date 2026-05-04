# 知乎 Tools 真实取数验证报告

## 结论

本次已通过 `zhihu_search` tool 发起真实知乎请求，并成功返回结构化数据。

## 本次运行

- 运行时间：2026-05-04 22:06:17 CST
- query：`AI-Agent`
- count：`3`
- openapi_base_url：`https://developer.zhihu.com`
- zhihu_search_url：``
- access_secret_configured：`true`
- 复跑命令：`go run ./cmd/zhihu_live_check -query AI-Agent -count 3 -out doc/zhihu_tools_live_check.md`

## 明细

| Tool | 状态 | 延迟(ms) | 证据 / 错误 |
|---|---:|---:|---|
| `zhihu_search` | PASS | 4388 | item_count=3; message=success; first_title=锦恢的 AI Agent 小白教程(一)Agent 的基本概念与分类 - 知乎; first_author=锦恢; first_url=https://zhuanlan.zhihu.com/p/1962274523752691074?utm_medium=openapi_platform&utm_source=4818dc34; first_vote_up_count=174 |

## 覆盖范围

- 通过 `trpc-agent-go/tool/function.NewFunctionTool` 生成的 tool wrapper 发起调用，不绕过工具层。
- `zhihu_search` tool 内部调用 `skills/zhihu-search/scripts/zhihu-search.py`，并通过环境变量传递知乎开放平台配置。
- 报告只记录是否配置密钥，不输出 Access Secret。
- 常规 `go test ./...` 使用临时 skill 脚本覆盖参数传递、环境变量注入、tool 声明和 JSON 解码。
