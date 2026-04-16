package taskqueue

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"tokenhub-server/internal/pkg/logger"
)

// TaskHandler 任务处理函数签名
// ctx 带有 10 分钟超时；progress 用于向 Backend SSE 推送进度
type TaskHandler func(ctx context.Context, payload string, progress ProgressReporter) error

// ProgressReporter 进度上报接口
type ProgressReporter interface {
	Report(status string, pct int, message string) error
	ReportData(status string, pct int, message string, data interface{}) error
}

// Consumer 任务消费者，由 Worker 使用
type Consumer struct {
	redis      *goredis.Client
	signingKey string
	consumerID string // 消费者唯一 ID（Pod 名/UUID）
	handlers   map[string]TaskHandler
}

// NewConsumer 创建任务消费者
func NewConsumer(redis *goredis.Client, signingKey, consumerID string) *Consumer {
	return &Consumer{
		redis:      redis,
		signingKey: signingKey,
		consumerID: consumerID,
		handlers:   make(map[string]TaskHandler),
	}
}

// RegisterHandler 注册任务类型的处理函数
func (c *Consumer) RegisterHandler(taskType string, handler TaskHandler) {
	c.handlers[taskType] = handler
}

// Start 启动消费循环（阻塞，直到 ctx 取消）
func (c *Consumer) Start(ctx context.Context) {
	// 确保消费者组存在
	c.ensureGroup(ctx)

	logger.L.Info("task consumer started",
		zap.String("consumer_id", c.consumerID),
		zap.Int("registered_handlers", len(c.handlers)),
	)

	for {
		select {
		case <-ctx.Done():
			logger.L.Info("task consumer stopping")
			return
		default:
		}

		// XREADGROUP 阻塞读取，每次最多读 1 条，超时 5 秒
		streams, err := c.redis.XReadGroup(ctx, &goredis.XReadGroupArgs{
			Group:    ConsumerGroup,
			Consumer: c.consumerID,
			Streams:  []string{StreamKey, ">"},
			Count:    1,
			Block:    5 * time.Second,
		}).Result()

		if err != nil {
			if err == goredis.Nil || ctx.Err() != nil {
				continue // 超时或 ctx 取消
			}
			logger.L.Warn("xreadgroup error", zap.Error(err))
			time.Sleep(1 * time.Second)
			continue
		}

		for _, stream := range streams {
			for _, msg := range stream.Messages {
				c.processMessage(ctx, msg)
			}
		}
	}
}

// ensureGroup 确保消费者组存在
func (c *Consumer) ensureGroup(ctx context.Context) {
	err := c.redis.XGroupCreateMkStream(ctx, StreamKey, ConsumerGroup, "0").Err()
	if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
		logger.L.Warn("create consumer group", zap.Error(err))
	}
}

// processMessage 处理单条消息
func (c *Consumer) processMessage(ctx context.Context, msg goredis.XMessage) {
	// 解析消息字段
	task := TaskRequest{
		TaskID:       getString(msg.Values, "task_id"),
		TaskType:     getString(msg.Values, "task_type"),
		Payload:      getString(msg.Values, "payload"),
		Signature:    getString(msg.Values, "signature"),
		ReplyChannel: getString(msg.Values, "reply_channel"),
	}
	if ts, ok := msg.Values["timestamp"]; ok {
		if s, ok := ts.(string); ok {
			fmt.Sscanf(s, "%d", &task.Timestamp)
		}
	}

	lg := logger.L.With(
		zap.String("task_id", task.TaskID),
		zap.String("task_type", task.TaskType),
		zap.String("msg_id", msg.ID),
	)

	// 1. 验证签名
	if !c.verifySignature(task) {
		lg.Warn("invalid task signature, rejected")
		c.ack(ctx, msg.ID)
		return
	}

	// 2. 时间窗口校验（防重放）
	if time.Since(time.Unix(task.Timestamp, 0)) > SignatureWindow {
		lg.Warn("task expired, rejected")
		c.ack(ctx, msg.ID)
		return
	}

	// 3. 去重检查
	dedupeKey := "task:processed:" + task.TaskID
	if set, _ := c.redis.SetNX(ctx, dedupeKey, 1, DeduplicationTTL).Result(); !set {
		lg.Debug("duplicate task, skipping")
		c.ack(ctx, msg.ID)
		return
	}

	// 4. 查找 handler
	handler, ok := c.handlers[task.TaskType]
	if !ok {
		lg.Warn("no handler registered for task type")
		c.ack(ctx, msg.ID)
		return
	}

	// 5. 执行任务（带 10 分钟超时）
	taskCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	reporter := &redisProgressReporter{
		redis:   c.redis,
		channel: task.ReplyChannel,
		taskID:  task.TaskID,
	}

	lg.Info("task execution started")
	reporter.Report(StatusRunning, 0, "任务开始执行")

	err := handler(taskCtx, task.Payload, reporter)

	if err != nil {
		lg.Error("task execution failed", zap.Error(err))
		reporter.Report(StatusFailed, 100, err.Error())
	} else {
		lg.Info("task execution completed")
		reporter.Report(StatusCompleted, 100, "任务执行完成")
	}

	// 6. ACK
	c.ack(ctx, msg.ID)
}

// verifySignature 验证 HMAC-SHA256 签名
func (c *Consumer) verifySignature(task TaskRequest) bool {
	mac := hmac.New(sha256.New, []byte(c.signingKey))
	mac.Write([]byte(fmt.Sprintf("%s:%s:%d:", task.TaskID, task.TaskType, task.Timestamp)))
	mac.Write([]byte(task.Payload))
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(task.Signature))
}

func (c *Consumer) ack(ctx context.Context, msgID string) {
	c.redis.XAck(ctx, StreamKey, ConsumerGroup, msgID)
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// redisProgressReporter 通过 Redis Pub/Sub 推送任务进度
type redisProgressReporter struct {
	redis   *goredis.Client
	channel string
	taskID  string
}

func (r *redisProgressReporter) Report(status string, pct int, message string) error {
	return r.ReportData(status, pct, message, nil)
}

func (r *redisProgressReporter) ReportData(status string, pct int, message string, data interface{}) error {
	progress := TaskProgress{
		TaskID:    r.taskID,
		Status:    status,
		Progress:  pct,
		Message:   message,
		Timestamp: time.Now().Unix(),
	}
	if data != nil {
		if b, err := json.Marshal(data); err == nil {
			progress.Data = string(b)
		}
	}

	b, err := json.Marshal(progress)
	if err != nil {
		return err
	}
	return r.redis.Publish(context.Background(), r.channel, string(b)).Err()
}
