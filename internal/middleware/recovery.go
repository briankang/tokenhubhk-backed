package middleware

import (
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/pkg/response"
)

// Recovery 返回 panic 恢复中间件，捕获 panic 并记录堆栈信息到日志
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				requestID, _ := c.Get("X-Request-ID")
				rid, _ := requestID.(string)

				logger.L.Error("panic recovered",
					zap.Any("error", r),
					zap.String("request_id", rid),
					zap.String("method", c.Request.Method),
					zap.String("path", c.Request.URL.Path),
					zap.String("stack", string(debug.Stack())),
				)

				response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
				c.Abort()
			}
		}()
		c.Next()
	}
}
