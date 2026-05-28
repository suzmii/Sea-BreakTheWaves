package util

import (
	"context"
	"fmt"

	"recommendation_v2/config"

	"github.com/openai/openai-go/option"
	openaiembedder "trpc.group/trpc-go/trpc-agent-go/knowledge/embedder/openai"
	"trpc.group/trpc-go/trpc-agent-go/model"
	openaimodel "trpc.group/trpc-go/trpc-agent-go/model/openai"
)

// Embedder 文本向量化客户端。
type Embedder struct {
	client *openaiembedder.Embedder
}

func NewEmbedder(cfg *config.Config) *Embedder {
	return &Embedder{
		client: openaiembedder.New(
			openaiembedder.WithAPIKey(cfg.Ali.APIKey),
			openaiembedder.WithModel(cfg.Ali.EmbedModel),
			openaiembedder.WithDimensions(cfg.Ali.Dimensions),
			openaiembedder.WithBaseURL(cfg.Ali.BaseURL),
			openaiembedder.WithRequestOptions(option.WithJSONSet("dimensions", cfg.Ali.Dimensions)),
		),
	}
}

func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	vec64, err := e.client.GetEmbedding(ctx, text)
	if err != nil {
		return nil, fmt.Errorf("embed request failed: %w", err)
	}
	vec := make([]float32, len(vec64))
	for i, v := range vec64 {
		vec[i] = float32(v)
	}
	return vec, nil
}

// ChatLLM 对话补全客户端。
type ChatLLM struct {
	client *openaimodel.Model
	config model.GenerationConfig
}

func NewChatLLM(cfg *config.Config) *ChatLLM {
	c := &ChatLLM{
		client: openaimodel.New(cfg.Rerank.ChatModel,
			openaimodel.WithAPIKey(cfg.Ali.APIKey),
			openaimodel.WithBaseURL(cfg.Ali.BaseURL),
			openaimodel.WithVariant(openaimodel.VariantQwen),
		),
		config: model.GenerationConfig{Temperature: &cfg.Rerank.Temperature},
	}
	if c.config.Temperature == nil {
		t := 0.7
		c.config.Temperature = &t
	}
	return c
}

func (c *ChatLLM) Chat(ctx context.Context, messages ...model.Message) (string, error) {
	req := &model.Request{
		Messages:         messages,
		GenerationConfig: c.config,
	}
	respChan, err := c.client.GenerateContent(ctx, req)
	if err != nil {
		return "", fmt.Errorf("llm request failed: %w", err)
	}
	resp := <-respChan
	if resp.Error != nil {
		return "", fmt.Errorf("llm response error: %s", resp.Error.Message)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("llm empty response")
	}
	return resp.Choices[0].Message.Content, nil
}
