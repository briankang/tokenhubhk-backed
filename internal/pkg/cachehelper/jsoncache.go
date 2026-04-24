// Package cachehelper 提供基于 Redis 的泛型 JSON 缓存读写工具。
//
// 核心用例：handler 层的"先读 Redis → miss 回源 → 回写 Redis"样板代码封装。
// 设计原则：
//  1. Redis 不可用时 fail-open（直接回调 loader，不阻塞业务）
//  2. loader 失败时错误回传，不写缓存
//  3. 泛型 + 单个 goroutine 调用，内部无锁无 singleflight（如需防穿透可外层叠加）
package cachehelper

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"tokenhub-server/internal/pkg/logger"
	pkgredis "tokenhub-server/internal/pkg/redis"
)

// Loader 回源函数签名。
type Loader[T any] func(ctx context.Context) (T, error)

// GetOrLoad 尝试从 Redis 读取 key，未命中则调用 loader 回源并回写。
//
// 行为：
//   - Redis HIT: 反序列化后直接返回，不触发 loader
//   - Redis MISS: 调用 loader，成功则写入（JSON 序列化 + TTL），失败返回错误
//   - Redis 异常（连接/超时/反序列化）: 降级为 MISS 走 loader
//   - loader 返回的值（含 nil 指针 / 空切片）一律序列化并缓存，调用方自行判断
func GetOrLoad[T any](ctx context.Context, key string, ttl time.Duration, loader Loader[T]) (T, error) {
	var zero T

	// 尝试从 Redis 读取
	if pkgredis.Client != nil {
		raw, err := pkgredis.Client.Get(ctx, key).Result()
		if err == nil {
			var cached T
			if unmarshalErr := json.Unmarshal([]byte(raw), &cached); unmarshalErr == nil {
				return cached, nil
			} else {
				// 反序列化失败：删除脏数据，继续走 loader
				if logger.L != nil {
					logger.L.Warn("cachehelper: unmarshal failed, evicting",
						zap.String("key", key), zap.Error(unmarshalErr))
				}
				_ = pkgredis.Client.Del(ctx, key).Err()
			}
		} else if !errors.Is(err, goredis.Nil) {
			// Redis 异常（非 key miss）：fail-open
			if logger.L != nil {
				logger.L.Debug("cachehelper: redis error, fallback to loader",
					zap.String("key", key), zap.Error(err))
			}
		}
	}

	// 回源
	fresh, err := loader(ctx)
	if err != nil {
		return zero, err
	}

	// 回写（尽力而为）
	if pkgredis.Client != nil {
		if data, merr := json.Marshal(fresh); merr == nil {
			if serr := pkgredis.Client.Set(ctx, key, string(data), ttl).Err(); serr != nil {
				if logger.L != nil {
					logger.L.Debug("cachehelper: set failed",
						zap.String("key", key), zap.Error(serr))
				}
			}
		} else if logger.L != nil {
			logger.L.Debug("cachehelper: marshal failed",
				zap.String("key", key), zap.Error(merr))
		}
	}

	return fresh, nil
}

// Invalidate 删除一个或多个 key（Redis 不可用时静默忽略）。
func Invalidate(ctx context.Context, keys ...string) {
	if pkgredis.Client == nil || len(keys) == 0 {
		return
	}
	if err := pkgredis.Client.Del(ctx, keys...).Err(); err != nil && logger.L != nil {
		logger.L.Debug("cachehelper: invalidate failed", zap.Strings("keys", keys), zap.Error(err))
	}
}

// InvalidatePattern 扫描并批量删除匹配 pattern 的 key（用 SCAN 非 KEYS）。
//
// countPerScan 每批扫描数量，0 时默认 500。
// 返回实际删除的 key 数量。Redis 不可用时返回 0。
func InvalidatePattern(ctx context.Context, pattern string, countPerScan int64) (int, error) {
	if pkgredis.Client == nil || pattern == "" {
		return 0, nil
	}
	if countPerScan <= 0 {
		countPerScan = 500
	}

	var cursor uint64
	deleted := 0
	for {
		keys, next, err := pkgredis.Client.Scan(ctx, cursor, pattern, countPerScan).Result()
		if err != nil {
			return deleted, err
		}
		if len(keys) > 0 {
			if derr := pkgredis.Client.Del(ctx, keys...).Err(); derr != nil {
				return deleted, derr
			}
			deleted += len(keys)
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return deleted, nil
}
