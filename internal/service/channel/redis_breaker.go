package channel

import (
	"context"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"tokenhub-server/internal/pkg/logger"
)

// BreakerState 熔断器状态常量
const (
	BreakerClosed   = "closed"
	BreakerOpen     = "open"
	BreakerHalfOpen = "half_open"
)

// RedisCircuitBreaker 基于 Redis 的分布式熔断器。
// 所有 Pod 共享同一熔断状态，确保水平扩容时行为一致。
// Redis 不可用时降级为 fail-open（不熔断），保证服务可用性。
type RedisCircuitBreaker struct {
	redis       *goredis.Client
	channelID   uint
	maxFailures int64   // 连续失败阈值触发熔断（默认 5）
	failRate    float64 // 失败率阈值（默认 0.5，最少 5 请求）
	minRequests int64   // 计算失败率的最小请求数（默认 5）
	openTimeout int64   // Open 状态持续秒数（默认 60）
	windowSec   int64   // 统计窗口秒数（默认 30）
}

// RedisCircuitBreakerConfig 熔断器配置
type RedisCircuitBreakerConfig struct {
	MaxFailures int64   // 连续失败阈值（默认 5）
	FailRate    float64 // 失败率阈值（默认 0.5）
	MinRequests int64   // 最小请求数（默认 5）
	OpenTimeout int64   // Open 状态秒数（默认 60）
	WindowSec   int64   // 统计窗口秒数（默认 30）
}

// DefaultBreakerConfig 返回默认熔断器配置（与原 gobreaker 参数一致）
func DefaultBreakerConfig() RedisCircuitBreakerConfig {
	return RedisCircuitBreakerConfig{
		MaxFailures: 5,
		FailRate:    0.5,
		MinRequests: 5,
		OpenTimeout: 60,
		WindowSec:   30,
	}
}

// NewRedisCircuitBreaker 创建分布式熔断器实例
func NewRedisCircuitBreaker(redis *goredis.Client, channelID uint, cfg RedisCircuitBreakerConfig) *RedisCircuitBreaker {
	if cfg.MaxFailures <= 0 {
		cfg.MaxFailures = 5
	}
	if cfg.FailRate <= 0 {
		cfg.FailRate = 0.5
	}
	if cfg.MinRequests <= 0 {
		cfg.MinRequests = 5
	}
	if cfg.OpenTimeout <= 0 {
		cfg.OpenTimeout = 60
	}
	if cfg.WindowSec <= 0 {
		cfg.WindowSec = 30
	}
	return &RedisCircuitBreaker{
		redis:       redis,
		channelID:   channelID,
		maxFailures: cfg.MaxFailures,
		failRate:    cfg.FailRate,
		minRequests: cfg.MinRequests,
		openTimeout: cfg.OpenTimeout,
		windowSec:   cfg.WindowSec,
	}
}

// --- Redis Key 设计 ---
// breaker:state:{channelID}   → "closed" | "open" | "half_open"
// breaker:counts:{channelID}  → Hash { requests, failures, consecutive_failures }
// breaker:open_at:{channelID} → Unix timestamp

func (b *RedisCircuitBreaker) stateKey() string {
	return fmt.Sprintf("breaker:state:%d", b.channelID)
}
func (b *RedisCircuitBreaker) countsKey() string {
	return fmt.Sprintf("breaker:counts:%d", b.channelID)
}
func (b *RedisCircuitBreaker) openAtKey() string {
	return fmt.Sprintf("breaker:open_at:%d", b.channelID)
}

// State 读取当前熔断状态。Redis 不可用时返回 "closed"（fail-open）。
func (b *RedisCircuitBreaker) State(ctx context.Context) string {
	state, err := b.redis.Get(ctx, b.stateKey()).Result()
	if err != nil {
		return BreakerClosed // Redis 不可用，fail-open
	}

	// Open 状态超时检查 → 自动转 HalfOpen
	if state == BreakerOpen {
		openAtStr, err := b.redis.Get(ctx, b.openAtKey()).Result()
		if err == nil {
			openAt, _ := strconv.ParseInt(openAtStr, 10, 64)
			if time.Now().Unix()-openAt > b.openTimeout {
				// 使用 Lua 原子 CAS：仅当仍为 open 时才切换
				script := `
					if redis.call("GET", KEYS[1]) == "open" then
						redis.call("SET", KEYS[1], "half_open")
						return 1
					end
					return 0
				`
				b.redis.Eval(ctx, script, []string{b.stateKey()}).Val()
				return BreakerHalfOpen
			}
		}
	}

	return state
}

// IsOpen 判断熔断器是否处于 Open 状态（应跳过此渠道）
func (b *RedisCircuitBreaker) IsOpen(ctx context.Context) bool {
	return b.State(ctx) == BreakerOpen
}

// RecordSuccess 记录成功请求
func (b *RedisCircuitBreaker) RecordSuccess(ctx context.Context) {
	key := b.countsKey()
	windowTTL := time.Duration(b.windowSec) * time.Second

	pipe := b.redis.Pipeline()
	pipe.HIncrBy(ctx, key, "requests", 1)
	pipe.HSet(ctx, key, "consecutive_failures", 0) // 重置连续失败
	pipe.Expire(ctx, key, windowTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return // Redis 不可用，静默跳过
	}

	// HalfOpen 状态下成功 → 关闭熔断器
	state := b.State(ctx)
	if state == BreakerHalfOpen {
		b.redis.Set(ctx, b.stateKey(), BreakerClosed, 0)
		b.redis.Del(ctx, b.openAtKey())
		// 清除计数器，重新开始统计
		b.redis.Del(ctx, b.countsKey())
		logger.L.Info("熔断器关闭（恢复）",
			zap.Uint("channel_id", b.channelID),
		)
	}
}

// RecordFailure 记录失败请求，判断是否需要触发熔断
func (b *RedisCircuitBreaker) RecordFailure(ctx context.Context) {
	key := b.countsKey()
	windowTTL := time.Duration(b.windowSec) * time.Second

	pipe := b.redis.Pipeline()
	pipe.HIncrBy(ctx, key, "requests", 1)
	pipe.HIncrBy(ctx, key, "failures", 1)
	consFailCmd := pipe.HIncrBy(ctx, key, "consecutive_failures", 1)
	pipe.Expire(ctx, key, windowTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return // Redis 不可用，静默跳过
	}

	consFail := consFailCmd.Val()

	// 判断是否触发熔断
	shouldTrip := consFail >= b.maxFailures
	if !shouldTrip {
		vals, err := b.redis.HMGet(ctx, key, "requests", "failures").Result()
		if err == nil && len(vals) >= 2 && vals[0] != nil && vals[1] != nil {
			reqs, _ := strconv.ParseInt(fmt.Sprint(vals[0]), 10, 64)
			fails, _ := strconv.ParseInt(fmt.Sprint(vals[1]), 10, 64)
			if reqs >= b.minRequests && float64(fails)/float64(reqs) > b.failRate {
				shouldTrip = true
			}
		}
	}

	if shouldTrip {
		openTTL := time.Duration(b.openTimeout+10) * time.Second
		b.redis.Set(ctx, b.stateKey(), BreakerOpen, openTTL)
		b.redis.Set(ctx, b.openAtKey(), time.Now().Unix(), openTTL)
		logger.L.Warn("熔断器开启",
			zap.Uint("channel_id", b.channelID),
			zap.Int64("consecutive_failures", consFail),
		)
	}
}

// Reset 重置熔断器状态（管理员手动恢复时使用）
func (b *RedisCircuitBreaker) Reset(ctx context.Context) {
	b.redis.Del(ctx, b.stateKey(), b.countsKey(), b.openAtKey())
}
