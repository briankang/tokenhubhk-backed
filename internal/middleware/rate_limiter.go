package middleware

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"

	"tokenhub-server/internal/pkg/errcode"
	pkgredis "tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/pkg/response"
)

// ========== 多层级限流中间件 ==========
// 基于 Redis ZSET 滑动窗口算法，支持 IP/用户/API Key/全局 四个层级

const (
	// rateLimitWindow 滑动窗口大小：1分钟
	rateLimitWindow = 60 * time.Second
	// 默认各级别限流阈值
	defaultIPRPM      = 30   // IP级：未认证请求 30 req/min
	defaultUserRPM    = 120  // 用户级：已认证用户 120 req/min
	defaultAPIKeyRPM  = 60   // API Key级：Open API 60 req/min
	defaultGlobalQPS  = 1000 // 全局 QPS 上限
)

// globalRequestCounter 全局请求计数器（原子操作，每秒重置）
var globalRequestCounter int64
var globalCounterResetTime int64

// RateLimiterConfig 限流配置（可由管理后台动态调整）
type RateLimiterConfig struct {
	IPRPM     int `json:"ipRpm"`
	UserRPM   int `json:"userRpm"`
	APIKeyRPM int `json:"apiKeyRpm"`
	GlobalQPS int `json:"globalQps"`
}

// DefaultRateLimiterConfig 返回默认限流配置
func DefaultRateLimiterConfig() *RateLimiterConfig {
	return &RateLimiterConfig{
		IPRPM:     defaultIPRPM,
		UserRPM:   defaultUserRPM,
		APIKeyRPM: defaultAPIKeyRPM,
		GlobalQPS: defaultGlobalQPS,
	}
}

// LoadRateLimiterConfig 从 Redis 加载动态限流配置，加载失败则使用默认值
func LoadRateLimiterConfig() *RateLimiterConfig {
	cfg := DefaultRateLimiterConfig()
	if pkgredis.Client == nil {
		return cfg
	}
	ctx := context.Background()

	// 尝试从 Redis 读取动态配置
	if v, err := pkgredis.Client.HGetAll(ctx, "config:rate_limits").Result(); err == nil && len(v) > 0 {
		if val, ok := v["ip_rpm"]; ok {
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				cfg.IPRPM = n
			}
		}
		if val, ok := v["user_rpm"]; ok {
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				cfg.UserRPM = n
			}
		}
		if val, ok := v["api_key_rpm"]; ok {
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				cfg.APIKeyRPM = n
			}
		}
		if val, ok := v["global_qps"]; ok {
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				cfg.GlobalQPS = n
			}
		}
	}
	return cfg
}

// SaveRateLimiterConfig 将限流配置保存到 Redis（管理后台调用）
func SaveRateLimiterConfig(cfg *RateLimiterConfig) error {
	if pkgredis.Client == nil {
		return fmt.Errorf("redis not available")
	}
	ctx := context.Background()
	return pkgredis.Client.HSet(ctx, "config:rate_limits", map[string]interface{}{
		"ip_rpm":      cfg.IPRPM,
		"user_rpm":    cfg.UserRPM,
		"api_key_rpm": cfg.APIKeyRPM,
		"global_qps":  cfg.GlobalQPS,
	}).Err()
}

// MultiLevelRateLimiter 多层级限流中间件
// 限流层级优先级：全局QPS → IP级 → 用户级/API Key级
// 响应头：X-RateLimit-Limit, X-RateLimit-Remaining, X-RateLimit-Reset
func MultiLevelRateLimiter() gin.HandlerFunc {
	return func(c *gin.Context) {
		redis := pkgredis.Client
		if redis == nil {
			// Redis 不可用时放行 (fail-open)
			c.Next()
			return
		}

		cfg := LoadRateLimiterConfig()

		// 层级1：全局 QPS 上限保护
		if !checkGlobalQPS(cfg.GlobalQPS) {
			setRateLimitHeaders(c, cfg.GlobalQPS, 0, time.Now().Add(time.Second).Unix())
			response.Error(c, http.StatusTooManyRequests, errcode.ErrRateLimit)
			c.Abort()
			return
		}

		ctx := context.Background()

		// 根据认证状态选择限流层级
		// 如果有 apiKeyId（Open API 认证），使用 API Key 限流
		if apiKeyID, exists := c.Get("apiKeyId"); exists {
			key := fmt.Sprintf("rl:apikey:%v", apiKeyID)
			limit := cfg.APIKeyRPM
			if !slidingWindowCheck(ctx, redis, key, limit, c) {
				return
			}
			c.Next()
			return
		}

		// 如果有 userId（JWT 认证），使用用户级限流
		if userID, exists := c.Get("userId"); exists {
			key := fmt.Sprintf("rl:user:%v", userID)
			limit := cfg.UserRPM
			if !slidingWindowCheck(ctx, redis, key, limit, c) {
				return
			}
			c.Next()
			return
		}

		// 未认证请求：使用 IP 级限流
		ip := c.ClientIP()
		key := fmt.Sprintf("rl:ip:%s", ip)
		limit := cfg.IPRPM
		if !slidingWindowCheck(ctx, redis, key, limit, c) {
			return
		}

		c.Next()
	}
}

// checkGlobalQPS 全局 QPS 检查（基于原子计数器，每秒重置）
func checkGlobalQPS(maxQPS int) bool {
	if maxQPS <= 0 {
		return true
	}

	nowSec := time.Now().Unix()
	lastReset := atomic.LoadInt64(&globalCounterResetTime)

	// 如果是新的一秒，重置计数器
	if nowSec > lastReset {
		if atomic.CompareAndSwapInt64(&globalCounterResetTime, lastReset, nowSec) {
			atomic.StoreInt64(&globalRequestCounter, 0)
		}
	}

	// 原子递增并检查是否超限
	current := atomic.AddInt64(&globalRequestCounter, 1)
	return current <= int64(maxQPS)
}

// slidingWindowCheck 基于 Redis ZSET 的滑动窗口限流检查
// 步骤：ZADD + ZREMRANGEBYSCORE（清除窗口外） + ZCARD（计数）
// 返回：true=放行，false=已拒绝并设置响应
func slidingWindowCheck(ctx context.Context, redis *goredis.Client, key string, limit int, c *gin.Context) bool {
	now := time.Now().UnixMilli()
	windowStart := now - rateLimitWindow.Milliseconds()

	pipe := redis.Pipeline()
	// 移除滑动窗口外的过期条目
	pipe.ZRemRangeByScore(ctx, key, "0", fmt.Sprintf("%d", windowStart))
	// 统计当前窗口内的请求数
	countCmd := pipe.ZCard(ctx, key)
	// 添加当前请求到窗口（member使用纳秒避免重复）
	member := fmt.Sprintf("%d:%d", now, time.Now().UnixNano()%1000000)
	pipe.ZAdd(ctx, key, goredis.Z{Score: float64(now), Member: member})
	// 设置 key 过期时间（窗口大小+1秒缓冲）
	pipe.Expire(ctx, key, rateLimitWindow+time.Second)

	if _, err := pipe.Exec(ctx); err != nil {
		// Redis 错误时放行 (fail-open)
		return true
	}

	count := countCmd.Val()
	remaining := int64(limit) - count
	if remaining < 0 {
		remaining = 0
	}
	resetTime := time.Now().Add(rateLimitWindow).Unix()

	// 设置限流响应头
	setRateLimitHeaders(c, limit, remaining, resetTime)

	// 超限返回 429
	if count >= int64(limit) {
		response.Error(c, http.StatusTooManyRequests, errcode.ErrRateLimit)
		c.Abort()
		return false
	}

	return true
}

// setRateLimitHeaders 设置标准限流响应头
func setRateLimitHeaders(c *gin.Context, limit int, remaining int64, resetTime int64) {
	c.Header("X-RateLimit-Limit", strconv.Itoa(limit))
	c.Header("X-RateLimit-Remaining", strconv.FormatInt(remaining, 10))
	c.Header("X-RateLimit-Reset", strconv.FormatInt(resetTime, 10))
}
