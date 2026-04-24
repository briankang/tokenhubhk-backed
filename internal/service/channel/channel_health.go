package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
	pkgredis "tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/pkg/safego"
)

// HealthResult 单个渠道健康检查结果
type HealthResult struct {
	ChannelID  uint      `json:"channel_id"`
	Healthy    bool      `json:"healthy"`
	LatencyMs  int64     `json:"latency_ms"`
	StatusCode int       `json:"status_code,omitempty"`
	Error      string    `json:"error,omitempty"`
	CheckedAt  time.Time `json:"checked_at"`
}

// ChannelHealthChecker 渠道健康检查器，定期检测渠道可用性
type ChannelHealthChecker struct {
	db     *gorm.DB
	redis  *goredis.Client
	logger *zap.Logger
}

// NewChannelHealthChecker 创建渠道健康检查器实例
func NewChannelHealthChecker(db *gorm.DB, redis *goredis.Client) *ChannelHealthChecker {
	return &ChannelHealthChecker{
		db:     db,
		redis:  redis,
		logger: logger.L,
	}
}

// StartHealthCheck 启动后台协程定期检查所有活跃渠道的健康状态
// 同时启动自动恢复协程，每30分钟尝试恢复被禁用的渠道
func (c *ChannelHealthChecker) StartHealthCheck(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}

	c.logger.Info("starting channel health checker", zap.Duration("interval", interval))

	safego.Go("channel-health-ticker", func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		// 执行初始检查（单次执行也用 Run 隔离 panic）
		safego.Run("channel-health-initial-check", func() { c.checkAll(ctx) })

		for {
			select {
			case <-ctx.Done():
				c.logger.Info("channel health checker stopped")
				return
			case <-ticker.C:
				safego.Run("channel-health-tick", func() { c.checkAll(ctx) })
			}
		}
	})

	// 启动自动恢复协程（每30分钟检查被禁用的渠道）
	safego.Go("channel-recovery-ticker", func() {
		recoveryTicker := time.NewTicker(30 * time.Minute)
		defer recoveryTicker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-recoveryTicker.C:
				safego.Run("channel-recovery-tick", func() { c.tryRecoverDisabledChannels(ctx) })
			}
		}
	})
}

// checkAll 对所有活跃渠道执行健康检查
func (c *ChannelHealthChecker) checkAll(ctx context.Context) {
	var channels []model.Channel
	if err := c.db.WithContext(ctx).Where("status IN ?", []string{"active", "testing"}).Find(&channels).Error; err != nil {
		c.logger.Error("failed to fetch channels for health check", zap.Error(err))
		return
	}

	c.logger.Info("running health check", zap.Int("channel_count", len(channels)))

	for i := range channels {
		// 每次检查使用独立的超时上下文
		checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		result := c.CheckChannel(checkCtx, &channels[i])
		cancel()

		if result != nil {
			c.UpdateStatus(ctx, channels[i].ID, result)
		}
	}
}

// CheckChannel 对单个渠道执行健康检查，发送轻量级 GET /v1/models 请求
func (c *ChannelHealthChecker) CheckChannel(ctx context.Context, channel *model.Channel) *HealthResult {
	if channel == nil {
		return nil
	}

	result := &HealthResult{
		ChannelID: channel.ID,
		CheckedAt: time.Now(),
	}

	start := time.Now()

	// 尝试访问渠道端点（只读、轻量级）
	url := channel.Endpoint + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		result.Error = fmt.Sprintf("failed to create request: %v", err)
		return result
	}
	req.Header.Set("Authorization", "Bearer "+channel.APIKey)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	result.LatencyMs = time.Since(start).Milliseconds()

	if err != nil {
		result.Error = fmt.Sprintf("request failed: %v", err)
		return result
	}
	defer resp.Body.Close()

	result.StatusCode = resp.StatusCode
	if resp.StatusCode >= 200 && resp.StatusCode < 500 {
		// 2xx-4xx 表示端点可达（4xx 可能是认证问题但服务存活）
		result.Healthy = true
	} else {
		result.Error = fmt.Sprintf("upstream returned %d", resp.StatusCode)
	}

	return result
}

// UpdateStatus 根据健康检查结果更新渠道状态，并存储到 Redis
func (c *ChannelHealthChecker) UpdateStatus(ctx context.Context, channelID uint, result *HealthResult) {
	if result == nil {
		return
	}

	// 将健康检查结果存入 Redis
	if c.redis != nil {
		key := fmt.Sprintf("channel:health:%d", channelID)
		_ = pkgredis.SetJSON(ctx, key, result, 10*time.Minute)
	}

	// 渠道不健康时记录警告，但不自动禁用（熔断器处理临时故障）
	if !result.Healthy {
		c.logger.Warn("channel health check failed",
			zap.Uint("channel_id", channelID),
			zap.String("error", result.Error),
			zap.Int64("latency_ms", result.LatencyMs),
		)

		// 在 Redis 中记录连续失败次数
		failKey := fmt.Sprintf("channel:health:fail_count:%d", channelID)
		count, err := c.redis.Incr(ctx, failKey).Result()
		if err == nil {
			c.redis.Expire(ctx, failKey, 30*time.Minute)
			// 连续 10 次健康检查失败后自动禁用渠道
			if count >= 10 {
				c.logger.Error("channel disabled due to repeated health check failures",
					zap.Uint("channel_id", channelID),
					zap.Int64("consecutive_failures", count),
				)
				c.db.WithContext(ctx).Model(&model.Channel{}).
					Where("id = ?", channelID).
					Update("status", "disabled")
				c.redis.Del(ctx, failKey)
			}
		}
	} else {
		// 成功时重置失败计数器
		failKey := fmt.Sprintf("channel:health:fail_count:%d", channelID)
		c.redis.Del(ctx, failKey)
	}
}

// CheckChannelWithChat 使用轻量 Chat Completion 请求探测渠道可用性
// 发送最短的聊天请求（max_tokens=1）来验证渠道的完整调用链路是否可用
func (c *ChannelHealthChecker) CheckChannelWithChat(ctx context.Context, channel *model.Channel) *HealthResult {
	if channel == nil {
		return nil
	}

	result := &HealthResult{
		ChannelID: channel.ID,
		CheckedAt: time.Now(),
	}

	start := time.Now()

	// 构造最小化的 Chat Completion 请求
	chatReqBody := map[string]interface{}{
		"model": "gpt-3.5-turbo", // 使用最便宜的模型做探针
		"messages": []map[string]string{
			{"role": "user", "content": "hi"},
		},
		"max_tokens": 1,
	}
	bodyBytes, _ := json.Marshal(chatReqBody)

	url := buildChatURL(channel.Endpoint)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		result.Error = fmt.Sprintf("failed to create request: %v", err)
		return result
	}
	req.Header.Set("Authorization", "Bearer "+channel.APIKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	result.LatencyMs = time.Since(start).Milliseconds()

	if err != nil {
		result.Error = fmt.Sprintf("request failed: %v", err)
		return result
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	result.StatusCode = resp.StatusCode
	if resp.StatusCode >= 200 && resp.StatusCode < 500 {
		result.Healthy = true
	} else {
		result.Error = fmt.Sprintf("upstream returned %d", resp.StatusCode)
	}

	return result
}

// tryRecoverDisabledChannels 尝试恢复被自动禁用的渠道
// 对 status=disabled 的渠道执行 Chat Completion 探针
// 连续3次成功后恢复为 active 状态
func (c *ChannelHealthChecker) tryRecoverDisabledChannels(ctx context.Context) {
	var channels []model.Channel
	if err := c.db.WithContext(ctx).Where("status = ?", "disabled").Find(&channels).Error; err != nil {
		c.logger.Error("查询禁用渠道失败", zap.Error(err))
		return
	}

	if len(channels) == 0 {
		return
	}

	c.logger.Info("尝试恢复禁用渠道", zap.Int("count", len(channels)))

	for i := range channels {
		ch := &channels[i]
		checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		result := c.CheckChannelWithChat(checkCtx, ch)
		cancel()

		if result == nil {
			continue
		}

		recoveryKey := fmt.Sprintf("channel:health:recovery_count:%d", ch.ID)

		if result.Healthy {
			// 探针成功，增加恢复计数
			count, err := c.redis.Incr(ctx, recoveryKey).Result()
			if err != nil {
				continue
			}
			c.redis.Expire(ctx, recoveryKey, 2*time.Hour)

			if count >= 3 {
				// 连续3次成功，恢复渠道
				c.logger.Info("渠道自动恢复",
					zap.Uint("channel_id", ch.ID),
					zap.String("channel_name", ch.Name))
				c.db.WithContext(ctx).Model(&model.Channel{}).
					Where("id = ?", ch.ID).
					Update("status", "active")
				c.redis.Del(ctx, recoveryKey)
			} else {
				c.logger.Info("渠道恢复探测成功",
					zap.Uint("channel_id", ch.ID),
					zap.Int64("recovery_count", count),
					zap.String("need", "3"))
			}
		} else {
			// 探针失败，重置恢复计数
			c.redis.Del(ctx, recoveryKey)
		}
	}
}
