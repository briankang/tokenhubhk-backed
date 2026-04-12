package middleware

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
	pkgredis "tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/pkg/response"
)

const (
	idempotencyHeader = "Idempotency-Key"
	idempotencyTTL    = 24 * time.Hour
	idempotencyPrefix = "idempotent:"
)

// Idempotent 幂等性保护中间件，基于 Redis + Idempotency-Key 请求头
// 仅对非 GET/HEAD/OPTIONS 的变更请求生效，幂等 Key 缓存 24 小时
func Idempotent() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 仅对变更类请求生效
		method := c.Request.Method
		if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
			c.Next()
			return
		}

		key := c.GetHeader(idempotencyHeader)
		if key == "" {
			// 未提供幂等 Key，跳过检查
			c.Next()
			return
		}

		redisKey := fmt.Sprintf("%s%s", idempotencyPrefix, key)
		ctx := context.Background()

		// 尝试设置 Key（NX = 仅不存在时设置）
		ok, err := pkgredis.Client.SetNX(ctx, redisKey, "processing", idempotencyTTL).Result()
		if err != nil {
			// Redis 错误时放行 (fail-open)
			c.Next()
			return
		}

		if !ok {
			// Key 已存在 → 重复请求
			response.Error(c, http.StatusConflict, errcode.ErrIdempotentRepeat)
			c.Abort()
			return
		}

		c.Next()

		// 请求成功后更新 Key 状态为 "done"
		if c.Writer.Status() < 400 {
			_ = pkgredis.Client.Set(ctx, redisKey, "done", idempotencyTTL).Err()
		} else {
			// 请求失败时删除 Key，允许重试
			_ = pkgredis.Client.Del(ctx, redisKey).Err()
		}
	}
}
