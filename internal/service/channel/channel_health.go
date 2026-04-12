package channel

import (
	"context"
	"fmt"
	"net/http"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
	pkgredis "tokenhub-server/internal/pkg/redis"
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
func (c *ChannelHealthChecker) StartHealthCheck(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}

	c.logger.Info("starting channel health checker", zap.Duration("interval", interval))

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		// 执行初始检查
		c.checkAll(ctx)

		for {
			select {
			case <-ctx.Done():
				c.logger.Info("channel health checker stopped")
				return
			case <-ticker.C:
				c.checkAll(ctx)
			}
		}
	}()
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
