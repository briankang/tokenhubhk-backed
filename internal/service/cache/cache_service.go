package cache

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/pkg/redis"
)

// CacheStats 缓存统计信息
type CacheStats struct {
	KeyCount    int64  `json:"key_count"`    // 缓存条目数
	MemoryUsed  string `json:"memory_used"`  // Redis 内存占用
	RedisInfo   string `json:"redis_info"`   // Redis 服务器信息摘要
}

// CacheService 统一缓存服务，封装 Redis 缓存的增删查和统计操作
type CacheService struct {
	client *goredis.Client
}

// NewCacheService 创建缓存服务实例
func NewCacheService(client *goredis.Client) *CacheService {
	if client == nil {
		client = redis.Client
	}
	return &CacheService{client: client}
}

// Set 设置缓存键值对，带 TTL 过期时间
// 如果 value 长度 > 1KB，自动使用 gzip 压缩存储
func (s *CacheService) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if key == "" {
		return fmt.Errorf("cache key is empty")
	}

	var data []byte
	// 响应体超过 1KB 则 gzip 压缩
	if len(value) > 1024 {
		compressed, err := gzipCompress(value)
		if err != nil {
			logger.L.Warn("gzip 压缩失败，存储原始数据", zap.Error(err))
			data = value
		} else {
			// 添加 gzip 魔数标记前缀，用于读取时判断是否需要解压
			data = append([]byte("__gz__"), compressed...)
		}
	} else {
		data = value
	}

	return s.client.Set(ctx, key, data, ttl).Err()
}

// Get 获取缓存值，自动检测并解压 gzip 数据
func (s *CacheService) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		return nil, err
	}

	// 检查是否有 gzip 压缩标记
	if len(data) > 6 && string(data[:6]) == "__gz__" {
		decompressed, err := gzipDecompress(data[6:])
		if err != nil {
			return nil, fmt.Errorf("gzip 解压失败: %w", err)
		}
		return decompressed, nil
	}

	return data, nil
}

// Delete 删除指定缓存键
func (s *CacheService) Delete(ctx context.Context, key string) error {
	return s.client.Del(ctx, key).Err()
}

// DeleteByPattern 按前缀模式批量清除缓存
// pattern 示例: "cache:/api/v1/admin/suppliers*"
func (s *CacheService) DeleteByPattern(ctx context.Context, pattern string) (int64, error) {
	if pattern == "" {
		return 0, fmt.Errorf("pattern is empty")
	}

	var totalDeleted int64
	var cursor uint64

	// 使用 SCAN 迭代匹配的键，避免 KEYS 命令阻塞 Redis
	for {
		keys, nextCursor, err := s.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return totalDeleted, fmt.Errorf("scan keys failed: %w", err)
		}

		if len(keys) > 0 {
			deleted, err := s.client.Del(ctx, keys...).Result()
			if err != nil {
				logger.L.Warn("批量删除缓存失败", zap.Error(err), zap.Strings("keys", keys))
			} else {
				totalDeleted += deleted
			}
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	return totalDeleted, nil
}

// ClearAll 清除所有 cache: 前缀的缓存
func (s *CacheService) ClearAll(ctx context.Context) (int64, error) {
	return s.DeleteByPattern(ctx, "cache:*")
}

// GetStats 获取缓存统计信息（cache: 前缀的条目数、Redis 内存占用）
func (s *CacheService) GetStats(ctx context.Context) (*CacheStats, error) {
	stats := &CacheStats{}

	// 统计 cache: 前缀的键数量
	var count int64
	var cursor uint64
	for {
		keys, nextCursor, err := s.client.Scan(ctx, cursor, "cache:*", 100).Result()
		if err != nil {
			return nil, fmt.Errorf("scan cache keys failed: %w", err)
		}
		count += int64(len(keys))
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
	stats.KeyCount = count

	// 获取 Redis 内存信息
	memInfo, err := s.client.Info(ctx, "memory").Result()
	if err == nil {
		stats.MemoryUsed = extractRedisInfoValue(memInfo, "used_memory_human")
	}

	// 获取 Redis 服务器信息摘要
	serverInfo, err := s.client.Info(ctx, "server").Result()
	if err == nil {
		stats.RedisInfo = fmt.Sprintf("version=%s, uptime=%ss",
			extractRedisInfoValue(serverInfo, "redis_version"),
			extractRedisInfoValue(serverInfo, "uptime_in_seconds"))
	}

	return stats, nil
}

// extractRedisInfoValue 从 Redis INFO 输出中提取指定字段的值
func extractRedisInfoValue(info, key string) string {
	prefix := key + ":"
	for i := 0; i < len(info); i++ {
		if i == 0 || info[i-1] == '\n' {
			if len(info[i:]) > len(prefix) && info[i:i+len(prefix)] == prefix {
				start := i + len(prefix)
				end := start
				for end < len(info) && info[end] != '\r' && info[end] != '\n' {
					end++
				}
				return info[start:end]
			}
		}
	}
	return ""
}

// gzipCompress 使用 gzip 压缩数据
func gzipCompress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// gzipDecompress 解压 gzip 数据
func gzipDecompress(data []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}
