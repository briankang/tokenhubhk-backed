package middleware

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
	pkgredis "tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/pkg/response"
)

// API Key 异常熔断默认参数（可由 config:apikey_anomaly 覆盖）
const (
	defaultAnomalyThreshold     = 20             // 60 秒内累计 ≥N 次错误触发封禁
	defaultAnomalyWindowSeconds = 60             // 错误计数窗口（秒）
	defaultBlockDurationSeconds = 300            // 封禁时长（秒，默认 5 分钟）
	apiKeyAnomalyCounterPrefix  = "apikey:anomaly:"
	apiKeyAnomalyBlockedPrefix  = "apikey:blocked:"
	apiKeyAnomalyConfigKey      = "config:apikey_anomaly"
)

// APIKeyAnomalyConfig 异常熔断动态配置
type APIKeyAnomalyConfig struct {
	Enabled             bool `json:"enabled"`
	Threshold           int  `json:"threshold"`
	WindowSeconds       int  `json:"windowSeconds"`
	BlockDurationSeconds int `json:"blockDurationSeconds"`
}

// DefaultAPIKeyAnomalyConfig 返回默认异常熔断配置
func DefaultAPIKeyAnomalyConfig() *APIKeyAnomalyConfig {
	return &APIKeyAnomalyConfig{
		Enabled:              true,
		Threshold:            defaultAnomalyThreshold,
		WindowSeconds:        defaultAnomalyWindowSeconds,
		BlockDurationSeconds: defaultBlockDurationSeconds,
	}
}

// LoadAPIKeyAnomalyConfig 从 Redis 加载动态配置
func LoadAPIKeyAnomalyConfig() *APIKeyAnomalyConfig {
	cfg := DefaultAPIKeyAnomalyConfig()
	if pkgredis.Client == nil {
		return cfg
	}
	ctx := context.Background()
	v, err := pkgredis.Client.HGetAll(ctx, apiKeyAnomalyConfigKey).Result()
	if err != nil || len(v) == 0 {
		return cfg
	}
	if s, ok := v["enabled"]; ok {
		if b, err := strconv.ParseBool(s); err == nil {
			cfg.Enabled = b
		}
	}
	if s, ok := v["threshold"]; ok {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			cfg.Threshold = n
		}
	}
	if s, ok := v["window_seconds"]; ok {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			cfg.WindowSeconds = n
		}
	}
	if s, ok := v["block_duration_seconds"]; ok {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			cfg.BlockDurationSeconds = n
		}
	}
	return cfg
}

// SaveAPIKeyAnomalyConfig 持久化异常熔断配置
func SaveAPIKeyAnomalyConfig(cfg *APIKeyAnomalyConfig) error {
	if pkgredis.Client == nil {
		return fmt.Errorf("redis not available")
	}
	ctx := context.Background()
	return pkgredis.Client.HSet(ctx, apiKeyAnomalyConfigKey, map[string]interface{}{
		"enabled":                strconv.FormatBool(cfg.Enabled),
		"threshold":              cfg.Threshold,
		"window_seconds":         cfg.WindowSeconds,
		"block_duration_seconds": cfg.BlockDurationSeconds,
	}).Err()
}

// APIKeyAnomalyGuard API Key 异常快速熔断中间件。
// 挂载在 /v1/* OpenAI 兼容路由的 APIKey Auth 之后：
//  1. 请求进入 → 检查 blocked:{keyId}，若存在直接 429
//  2. 请求完成 → 若响应状态 >=400，INCR anomaly:{keyId}，window 60s
//  3. 计数 >= threshold → 写入 blocked:{keyId} TTL block_duration
//
// 仅在有 apiKeyId 上下文时生效；Redis 不可用时 fail-open。
func APIKeyAnomalyGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		redis := pkgredis.Client
		if redis == nil {
			c.Next()
			return
		}

		apiKeyIDVal, exists := c.Get("apiKeyId")
		if !exists {
			c.Next()
			return
		}

		cfg := LoadAPIKeyAnomalyConfig()
		if !cfg.Enabled {
			c.Next()
			return
		}

		ctx := context.Background()
		blockedKey := fmt.Sprintf("%s%v", apiKeyAnomalyBlockedPrefix, apiKeyIDVal)

		// 前置检查：已封禁直接拒绝
		if ttl, err := redis.TTL(ctx, blockedKey).Result(); err == nil && ttl > 0 {
			secs := int64(ttl.Seconds())
			if secs < 1 {
				secs = 1
			}
			c.Header("Retry-After", strconv.FormatInt(secs, 10))
			c.Header("X-RateLimit-Reason", "apikey_anomaly_blocked")
			recordRateLimitEvent(c, "apikey", fmt.Sprintf("%v", apiKeyIDVal), "apikey_anomaly", cfg.Threshold, cfg.WindowSeconds)
			response.ErrorMsg(c, http.StatusTooManyRequests, errcode.ErrRateLimit.Code,
				fmt.Sprintf("API Key 检测到异常调用已临时限制，请 %d 秒后重试", secs))
			c.Abort()
			return
		}

		c.Next()

		// 后置处理：只统计 4xx/5xx 错误
		status := c.Writer.Status()
		if status < 400 {
			return
		}

		counterKey := fmt.Sprintf("%s%v", apiKeyAnomalyCounterPrefix, apiKeyIDVal)
		// 原子递增 + 首次设置 TTL
		count, err := redis.Incr(ctx, counterKey).Result()
		if err != nil {
			return
		}
		if count == 1 {
			redis.Expire(ctx, counterKey, time.Duration(cfg.WindowSeconds)*time.Second)
		}

		if int(count) >= cfg.Threshold {
			redis.Set(ctx, blockedKey, "1", time.Duration(cfg.BlockDurationSeconds)*time.Second)
			// 重置 counter 避免封禁解除后立即重新触发
			redis.Del(ctx, counterKey)
		}
	}
}
