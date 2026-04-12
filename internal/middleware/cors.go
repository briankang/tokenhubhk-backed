package middleware

import (
	"os"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

// CORS 返回跨域资源共享 (CORS) 中间件
// 根据环境变量 APP_ENV 区分开发和生产环境配置
// 开发环境允许 localhost 来源，生产环境仅允许 www.tokenhubhk.com
// Docker 容器内部请求通过 nginx 代理，Origin 可能为空或内部地址
func CORS() gin.HandlerFunc {
	// 从环境变量获取当前环境
	appEnv := os.Getenv("APP_ENV")
	if appEnv == "" {
		appEnv = "development" // 默认开发环境
	}

	// 根据环境配置允许的来源
	var allowOrigins []string
	if appEnv == "production" {
		// 生产环境：仅允许指定域名
		allowOrigins = []string{
			"https://www.tokenhubhk.com",
			"https://tokenhubhk.com",
			// "null" origin 通过 AllowOriginFunc 动态处理
		}
	} else {
		// 开发环境：允许所有 localhost 来源
		allowOrigins = []string{
			"http://localhost:5173",
			"http://localhost:5174",
			"http://localhost:3000",
			"http://localhost:8090",
			"http://localhost:8080",
			"http://127.0.0.1:5173",
			"http://127.0.0.1:5174",
			"http://127.0.0.1:3000",
			"http://127.0.0.1:8090",
			"http://127.0.0.1:8080",
		}
	}

	return cors.New(cors.Config{
		AllowOrigins:     allowOrigins,
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "Authorization", "X-Request-ID", "Idempotency-Key", "Accept-Language"},
		ExposeHeaders:    []string{"Content-Length", "X-Request-ID"},
		AllowCredentials: true, // 允许携带凭证
		MaxAge:           12 * time.Hour,
		// AllowOriginFunc 允许动态判断 Origin
		// 用于处理 Docker 内部请求或无 Origin 的请求
		AllowOriginFunc: func(origin string) bool {
			// 空 Origin 或 "null" Origin（同源请求或 nginx 代理请求）允许通过
			if origin == "" || origin == "null" {
				return true
			}
			// 检查是否在允许列表中
			for _, allowed := range allowOrigins {
				if strings.EqualFold(origin, allowed) {
					return true
				}
			}
			// Docker 内部请求（go-server 容器名）允许通过
			if strings.HasPrefix(origin, "http://go-server") ||
				strings.HasPrefix(origin, "http://tokenhubhk") ||
				strings.HasPrefix(origin, "http://nginx") {
				return true
			}
			return false
		},
	})
}
