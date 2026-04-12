package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/response"
	whitelabel "tokenhub-server/internal/service/whitelabel"
)

// TenantResolveMiddleware 租户解析中间件，从请求 Host 头解析当前租户
// 处理逻辑:
// 1. 从 Host 头提取域名
// 2. 平台主域名 → 跳过（平台级访问）
// 3. 自定义域名 → 通过 DomainResolver 解析
// 4. 解析成功 → 写入 "resolvedTenantID" 到上下文
// 5. 未找到 → 继续请求（路由层自行判断是否必须）
func TenantResolveMiddleware(resolver *whitelabel.DomainResolver, platformDomain string) gin.HandlerFunc {
	return func(c *gin.Context) {
		host := extractDomain(c.Request.Host)
		if host == "" {
			c.Next()
			return
		}

		// 平台主域名，无需解析租户
		if strings.EqualFold(host, platformDomain) {
			c.Next()
			return
		}

		// 本地开发环境跳过
		if host == "localhost" || host == "127.0.0.1" {
			c.Next()
			return
		}

		tenant, err := resolver.Resolve(c.Request.Context(), host)
		if err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, 50001, "failed to resolve tenant")
			c.Abort()
			return
		}

		if tenant == nil {
			// 未知域名，允许请求继续；各处理器自行决定是否必须解析租户
			c.Next()
			return
		}

		c.Set("resolvedTenantID", tenant.ID)
		c.Set("resolvedTenantDomain", tenant.Domain)
		c.Next()
	}
}

// extractDomain 从 Host 请求头提取域名（去除端口号）
func extractDomain(host string) string {
	if host == "" {
		return ""
	}
	// 如果包含端口号则去除
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}
	return strings.ToLower(strings.TrimSpace(host))
}
