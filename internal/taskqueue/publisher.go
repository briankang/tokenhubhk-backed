package taskqueue

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"tokenhub-server/internal/pkg/logger"
)

// Publisher 任务发布者，由 Backend 使用将重操作委派给 Worker
type Publisher struct {
	redis      *goredis.Client
	signingKey string // HMAC-SHA256 签名密钥
}

// NewPublisher 创建任务发布者
func NewPublisher(redis *goredis.Client, signingKey string) *Publisher {
	return &Publisher{redis: redis, signingKey: signingKey}
}

// Publish 发布一个异步任务到 Worker，返回 taskID 供后续追踪
func (p *Publisher) Publish(ctx context.Context, taskType string, payload interface{}) (string, error) {
	taskID := uuid.New().String()
	timestamp := time.Now().Unix()

	// 序列化 payload
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}
	payloadStr := string(payloadBytes)

	// 生成 HMAC-SHA256 签名
	signature := p.sign(taskID, taskType, timestamp, payloadBytes)

	replyChannel := ProgressPrefix + taskID

	// 写入 Redis Stream
	err = p.redis.XAdd(ctx, &goredis.XAddArgs{
		Stream: StreamKey,
		Values: map[string]interface{}{
			"task_id":       taskID,
			"task_type":     taskType,
			"payload":       payloadStr,
			"timestamp":     timestamp,
			"signature":     signature,
			"reply_channel": replyChannel,
		},
	}).Err()
	if err != nil {
		return "", fmt.Errorf("xadd to stream: %w", err)
	}

	logger.L.Info("任务已发布",
		zap.String("task_id", taskID),
		zap.String("task_type", taskType),
	)

	return taskID, nil
}

// sign 生成 HMAC-SHA256 签名
func (p *Publisher) sign(taskID, taskType string, timestamp int64, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(p.signingKey))
	mac.Write([]byte(fmt.Sprintf("%s:%s:%d:", taskID, taskType, timestamp)))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// SubscribeProgress 订阅任务进度（供 Backend SSE handler 使用）
// 返回一个 channel，当 Worker 发布进度时推送 TaskProgress
func (p *Publisher) SubscribeProgress(ctx context.Context, taskID string) (<-chan TaskProgress, error) {
	channel := ProgressPrefix + taskID
	sub := p.redis.Subscribe(ctx, channel)

	// 验证订阅成功
	if _, err := sub.Receive(ctx); err != nil {
		sub.Close()
		return nil, fmt.Errorf("subscribe progress: %w", err)
	}

	ch := make(chan TaskProgress, 10)

	go func() {
		defer close(ch)
		defer sub.Close()

		msgCh := sub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-msgCh:
				if !ok {
					return
				}
				var progress TaskProgress
				if err := json.Unmarshal([]byte(msg.Payload), &progress); err != nil {
					continue
				}
				select {
				case ch <- progress:
				case <-ctx.Done():
					return
				}
				// 任务完成或失败时退出
				if progress.Status == StatusCompleted || progress.Status == StatusFailed {
					return
				}
			}
		}
	}()

	return ch, nil
}
