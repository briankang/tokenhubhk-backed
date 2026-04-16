package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"tokenhub-server/internal/pkg/logger"
)

// slowThreshold 慢请求阈值，超过此时间的请求会记录到 slow.log
const slowThreshold = 3 * time.Second

// RequestLogger 请求日志中间件
// 记录每个请求的信息到 access.log，慢请求额外记录到 slow.log
func RequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 生成或复用 X-Request-ID（支持跨服务传递：Nginx/Gateway → Backend）
		requestID := c.GetHeader("X-Request-ID")
		if requestID == "" {
			requestID = uuid.New().String()
		}
		c.Set("X-Request-ID", requestID)
		c.Set("requestId", requestID) // 兼容 handler 中 c.GetString("requestId") 的用法
		c.Header("X-Request-ID", requestID)

		start := time.Now()

		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()

		fields := []zap.Field{
			zap.String("request_id", requestID),
			zap.String("method", c.Request.Method),
			zap.String("path", c.Request.URL.Path),
			zap.String("query", c.Request.URL.RawQuery),
			zap.Int("status", status),
			zap.Duration("latency", latency),
			zap.String("client_ip", c.ClientIP()),
			zap.String("user_agent", c.Request.UserAgent()),
		}

		// 记录到 access.log
		logger.Access().Info("request", fields...)

		// 超过阈值的慢请求记录到 slow.log
		if latency > slowThreshold {
			logger.Slow().Warn("slow request", fields...)
		}
	}
}
