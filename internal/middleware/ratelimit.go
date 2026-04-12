package middleware

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"

	"tokenhub-server/internal/config"
	"tokenhub-server/internal/pkg/errcode"
	pkgredis "tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/pkg/response"
)

// RateLimit 基于 IP 的滑动窗口限流中间件，使用 Redis ZSET 实现
// 配置从 config.Global.RateLimit 读取，支持开关控制
func RateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg := config.Global.RateLimit
		if !cfg.Enabled {
			c.Next()
			return
		}

		ip := c.ClientIP()
		key := fmt.Sprintf("ratelimit:%s", ip)
		window := time.Duration(cfg.WindowSeconds) * time.Second

		ctx := context.Background()
		now := time.Now().UnixMilli()

		pipe := pkgredis.Client.Pipeline()
		// 移除过期条目
		pipe.ZRemRangeByScore(ctx, key, "0", fmt.Sprintf("%d", now-window.Milliseconds()))
		// 统计当前窗口请求数
		countCmd := pipe.ZCard(ctx, key)
		// 添加当前请求
		pipe.ZAdd(ctx, key, goredis.Z{Score: float64(now), Member: fmt.Sprintf("%d", now)})
		// 设置 Key 过期时间
		pipe.Expire(ctx, key, window)

		if _, err := pipe.Exec(ctx); err != nil {
			// Redis 错误时放行 (fail-open)
			c.Next()
			return
		}

		count := countCmd.Val()
		if count >= int64(cfg.MaxRequests) {
			response.Error(c, http.StatusTooManyRequests, errcode.ErrRateLimit)
			c.Abort()
			return
		}

		c.Next()
	}
}
