package kafka

import (
	"context"
	"encoding/json"
	"fmt"

	"recommendation_v2/config"

	"github.com/IBM/sarama"
	"trpc.group/trpc-go/trpc-agent-go/log"
)

// ArticleSyncEvent Kafka 文章同步事件。
type ArticleSyncEvent struct {
	EventScope    string   `json:"event_scope"`
	EventID       string   `json:"event_id"`
	ArticleID     string   `json:"article_id"`
	Op            string   `json:"op"`
	Title         string   `json:"title,omitempty"`
	Brief         string   `json:"brief,omitempty"`
	CoverURL      string   `json:"cover_url,omitempty"`
	ManualTypeTag string   `json:"manual_type_tag,omitempty"`
	SecondaryTags []string `json:"secondary_tags,omitempty"`
	Markdown      string   `json:"markdown,omitempty"`
}

// MessageHandler 消息处理回调。
type MessageHandler func(ctx context.Context, event ArticleSyncEvent) error

// Consumer 管理 Kafka consumer group 生命周期。
type Consumer struct {
	group   sarama.ConsumerGroup
	handler MessageHandler
	cancel  context.CancelFunc
}

// Start 启动 consumer group。
func Start(ctx context.Context, topic, group string, handler MessageHandler) (*Consumer, error) {
	cfg := sarama.NewConfig()
	cfg.Consumer.Return.Errors = true
	cfg.Consumer.Offsets.Initial = sarama.OffsetNewest
	cfg.Version = sarama.V2_0_0_0

	cg, err := sarama.NewConsumerGroup([]string{config.Cfg.Kafka.Address}, group, cfg)
	if err != nil {
		return nil, fmt.Errorf("new consumer group: %w", err)
	}

	c := &Consumer{group: cg, handler: handler}
	ctx, c.cancel = context.WithCancel(ctx)

	go func() {
		for {
			if err := cg.Consume(ctx, []string{topic}, &articleHandler{handler: handler}); err != nil {
				log.Errorf("[kafka] consume error: %v", err)
			}
			if ctx.Err() != nil {
				return
			}
		}
	}()

	log.Infof("[kafka] consumer started topic=%s group=%s", topic, group)
	return c, nil
}

// Stop 停止 consumer。
func (c *Consumer) Stop() error {
	c.cancel()
	return c.group.Close()
}

// articleHandler 实现 sarama.ConsumerGroupHandler。
type articleHandler struct {
	handler MessageHandler
}

func (h *articleHandler) Setup(_ sarama.ConsumerGroupSession) error   { return nil }
func (h *articleHandler) Cleanup(_ sarama.ConsumerGroupSession) error { return nil }

func (h *articleHandler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	for msg := range claim.Messages() {
		var event ArticleSyncEvent
		if err := json.Unmarshal(msg.Value, &event); err != nil {
			log.Warnf("[kafka] unmarshal event failed: %v", err)
			session.MarkMessage(msg, "")
			continue
		}
		if err := h.handler(session.Context(), event); err != nil {
			log.Warnf("[kafka] handle event failed: %v", err)
		}
		session.MarkMessage(msg, "")
	}
	return nil
}
