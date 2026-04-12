package middleware

import (
	"bytes"
	"context"
	"crypto/md5"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/pkg/redis"
	cachesvc "tokenhub-server/internal/service/cache"
)

// responseRecorder 自定义 ResponseWriter，用于捕获响应体内容
type responseRecorder struct {
	gin.ResponseWriter
	body       *bytes.Buffer
	statusCode int
}

// Write 写入响应体，同时记录到 buffer
func (r *responseRecorder) Write(data []byte) (int, error) {
	r.body.Write(data)
	return r.ResponseWriter.Write(data)
}

// WriteHeader 记录响应状态码
func (r *responseRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

// CacheMiddleware 接口缓存中间件，拦截 GET 请求自动缓存响应
// 参数 ttl 为缓存过期时间
// 缓存 Key 格式: cache:{path}:{query_hash}:{user_id}（公开接口不含 user_id）
// 仅缓存 GET 请求且响应码 200
// 响应头: X-Cache: HIT 或 X-Cache: MISS
func CacheMiddleware(ttl time.Duration) gin.HandlerFunc {
	svc := cachesvc.NewCacheService(redis.Client)

	return func(c *gin.Context) {
		// 仅缓存 GET 请求
		if c.Request.Method != http.MethodGet {
			c.Next()
			return
		}

		// 构建缓存 Key
		cacheKey := buildCacheKey(c)

		// 尝试从缓存读取
		ctx := c.Request.Context()
		cached, err := svc.Get(ctx, cacheKey)
		if err == nil && len(cached) > 0 {
			// 缓存命中，直接返回
			c.Header("X-Cache", "HIT")
			c.Header("Content-Type", "application/json; charset=utf-8")
			c.Writer.WriteHeader(http.StatusOK)
			c.Writer.Write(cached)
			c.Abort()
			return
		}

		// 缓存未命中，记录响应
		c.Header("X-Cache", "MISS")

		recorder := &responseRecorder{
			ResponseWriter: c.Writer,
			body:           &bytes.Buffer{},
			statusCode:     http.StatusOK,
		}
		c.Writer = recorder

		// 执行后续处理器
		c.Next()

		// 仅缓存 200 响应
		if recorder.statusCode == http.StatusOK && recorder.body.Len() > 0 {
			go func() {
				// 异步写入缓存，不阻塞响应
				if err := svc.Set(ctx, cacheKey, recorder.body.Bytes(), ttl); err != nil {
					if logger.L != nil {
						logger.L.Warn("写入接口缓存失败",
							zap.String("key", cacheKey),
							zap.Error(err))
					}
				}
			}()
		}
	}
}

// buildCacheKey 根据请求路径、查询参数和用户ID构建缓存键
// 公开接口 (/public/, /docs/) 不包含用户ID
func buildCacheKey(c *gin.Context) string {
	path := c.Request.URL.Path

	// 查询参数 hash（确保相同参数不同顺序也能命中）
	queryHash := ""
	rawQuery := c.Request.URL.RawQuery
	if rawQuery != "" {
		h := md5.Sum([]byte(rawQuery))
		queryHash = fmt.Sprintf("%x", h[:8])
	}

	// 判断是否为公开接口
	isPublic := isPublicPath(path)

	if isPublic {
		if queryHash != "" {
			return fmt.Sprintf("cache:%s:%s", path, queryHash)
		}
		return fmt.Sprintf("cache:%s", path)
	}

	// 已认证接口，包含用户ID
	userID, _ := c.Get("userId")
	uid := fmt.Sprintf("%v", userID)

	if queryHash != "" {
		return fmt.Sprintf("cache:%s:%s:%s", path, queryHash, uid)
	}
	return fmt.Sprintf("cache:%s:%s", path, uid)
}

// isPublicPath 判断请求路径是否为公开接口（不需要用户维度缓存）
func isPublicPath(path string) bool {
	publicPrefixes := []string{
		"/api/v1/public/",
		"/api/v1/docs/",
		"/health",
	}
	for _, prefix := range publicPrefixes {
		if len(path) >= len(prefix) && path[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// CacheInvalidate 缓存失效辅助函数，在写操作后调用清除关联缓存
// patterns 为需要清除的缓存前缀模式列表
func CacheInvalidate(patterns ...string) {
	if redis.Client == nil {
		return
	}
	svc := cachesvc.NewCacheService(redis.Client)
	ctx := context.Background()

	for _, pattern := range patterns {
		deleted, err := svc.DeleteByPattern(ctx, pattern)
		if err != nil {
			if logger.L != nil {
				logger.L.Warn("缓存失效清除失败",
					zap.String("pattern", pattern),
					zap.Error(err))
			}
		} else if deleted > 0 && logger.L != nil {
			logger.L.Debug("缓存失效清除完成",
				zap.String("pattern", pattern),
				zap.Int64("deleted", deleted))
		}
	}
}
