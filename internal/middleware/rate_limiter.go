package middleware

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"tokenhub-server/internal/pkg/errcode"
	pkgredis "tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/pkg/response"
	membersvc "tokenhub-server/internal/service/member"
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
// 特殊头 X-Test-Skip-RateLimit + RATE_LIMIT_BYPASS_TOKEN 环境变量可在测试时绕过
func MultiLevelRateLimiter() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 测试绕过：RATE_LIMIT_BYPASS_TOKEN 环境变量非空且 Header 匹配时放行
		if bypassToken := os.Getenv("RATE_LIMIT_BYPASS_TOKEN"); bypassToken != "" {
			if c.GetHeader("X-Test-Skip-RateLimit") == bypassToken {
				c.Next()
				return
			}
		}

		// 管理员路由跳过 IP 级限流：已由 JWT + ADMIN 角色双重保护，无需额外限流
		if strings.HasPrefix(c.Request.URL.Path, "/api/v1/admin/") {
			c.Next()
			return
		}

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
		// 回环地址和 Docker 内网豁免：本地开发时浏览器经过 nginx 后 IP 为 172.18.0.1（Docker 网桥），
		// 外网用户永远不会有这些 IP，豁免不会影响生产安全性
		if ip == "::1" || ip == "127.0.0.1" || strings.HasPrefix(ip, "172.17.") || strings.HasPrefix(ip, "172.18.") {
			c.Next()
			return
		}
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

	// 超限返回 429，消息中包含重置时间
	if count >= int64(limit) {
		secondsUntilReset := resetTime - time.Now().Unix()
		if secondsUntilReset < 1 {
			secondsUntilReset = 1
		}
		c.Header("Retry-After", strconv.FormatInt(secondsUntilReset, 10))
		response.ErrorMsg(c, http.StatusTooManyRequests, errcode.ErrRateLimit.Code,
			fmt.Sprintf("Too many requests, please retry after %d seconds", secondsUntilReset))
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

// ========== 会员级别限流中间件 ==========
// 在 Auth 中间件之后执行，根据用户会员等级的 DefaultRPM 进行精确限流

// MemberRateLimiter 会员级别限流中间件
// 根据用户会员等级配置的 DefaultRPM 进行 RPM 限流
// 需要在 Auth() 之后挂载，保证 userId 已设置到 gin.Context
func MemberRateLimiter(db *gorm.DB, redisClient *goredis.Client) gin.HandlerFunc {
	memberSvc := membersvc.NewMemberLevelService(db, redisClient)

	return func(c *gin.Context) {
		// 测试绕过：RATE_LIMIT_BYPASS_TOKEN 环境变量非空且 Header 匹配时放行
		if bypassToken := os.Getenv("RATE_LIMIT_BYPASS_TOKEN"); bypassToken != "" {
			if c.GetHeader("X-Test-Skip-RateLimit") == bypassToken {
				c.Next()
				return
			}
		}

		if redisClient == nil {
			// Redis 不可用时放行 (fail-open)
			c.Next()
			return
		}

		// 获取 userId（由 Auth 中间件设置）
		userIDVal, exists := c.Get("userId")
		if !exists {
			c.Next()
			return
		}
		userID, ok := userIDVal.(uint)
		if !ok {
			c.Next()
			return
		}

		// 查询用户会员等级的 RPM 限制（带 Redis 缓存）
		limits, err := memberSvc.GetUserRateLimits(c.Request.Context(), userID)
		if err != nil || limits == nil {
			// 查询失败时放行 (fail-open)
			c.Next()
			return
		}

		// RPM 限流检查（使用独立的 key 前缀，与全局限流器区分）
		if limits.RPM > 0 {
			key := fmt.Sprintf("rl:member_rpm:%d", userID)
			ctx := context.Background()
			if !slidingWindowCheck(ctx, redisClient, key, limits.RPM, c) {
				return
			}
		}

		c.Next()
	}
}

// ========== TPM (Tokens Per Minute) 限流工具 ==========
// 基于 Redis INCRBY + 分钟级 key 实现，供 completions handler 调用

// TPMLimiter TPM 限流器，跟踪每个用户每分钟的 Token 消耗
type TPMLimiter struct {
	redis     *goredis.Client
	memberSvc *membersvc.MemberLevelService
}

// NewTPMLimiter 创建 TPM 限流器实例
func NewTPMLimiter(db *gorm.DB, redisClient *goredis.Client) *TPMLimiter {
	return &TPMLimiter{
		redis:     redisClient,
		memberSvc: membersvc.NewMemberLevelService(db, redisClient),
	}
}

// CheckTPM 预检 TPM 限制：在请求执行前，用 estimatedTokens 估算是否会超限
// 返回 true=放行，false=超限
func (t *TPMLimiter) CheckTPM(ctx context.Context, userID uint, estimatedTokens int) (bool, int) {
	if t.redis == nil || estimatedTokens <= 0 {
		return true, 0
	}

	// 获取用户 TPM 限制
	limits, err := t.memberSvc.GetUserRateLimits(ctx, userID)
	if err != nil || limits == nil || limits.TPM <= 0 {
		return true, 0 // 无限制或查询失败，放行
	}

	// 读取当前分钟的 Token 消耗
	minuteKey := time.Now().Unix() / 60
	key := fmt.Sprintf("tpm:user:%d:%d", userID, minuteKey)
	current, _ := t.redis.Get(ctx, key).Int()

	if current+estimatedTokens > limits.TPM {
		return false, limits.TPM // 超限
	}
	return true, limits.TPM
}

// RecordTPM 记录实际 Token 消耗（请求完成后调用）
func (t *TPMLimiter) RecordTPM(ctx context.Context, userID uint, actualTokens int) {
	if t.redis == nil || actualTokens <= 0 {
		return
	}

	minuteKey := time.Now().Unix() / 60
	key := fmt.Sprintf("tpm:user:%d:%d", userID, minuteKey)
	t.redis.IncrBy(ctx, key, int64(actualTokens))
	// 设置 2 分钟 TTL（覆盖当前分钟 + 下一分钟查询窗口）
	t.redis.Expire(ctx, key, 120*time.Second)
}

// GetTPMUsage 获取用户当前分钟的 TPM 使用量（用于响应头或调试）
func (t *TPMLimiter) GetTPMUsage(ctx context.Context, userID uint) (current int, limit int) {
	if t.redis == nil {
		return 0, 0
	}

	minuteKey := time.Now().Unix() / 60
	key := fmt.Sprintf("tpm:user:%d:%d", userID, minuteKey)
	current, _ = t.redis.Get(ctx, key).Int()

	limits, err := t.memberSvc.GetUserRateLimits(ctx, userID)
	if err == nil && limits != nil {
		limit = limits.TPM
	}

	return current, limit
}
