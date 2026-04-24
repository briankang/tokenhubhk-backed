package middleware

import (
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/response"
	whitelabel "tokenhub-server/internal/service/whitelabel"
)

// privateCIDRs 私网/保留 IP 网段（来自 K8s Pod CIDR + VPC 内网 + loopback）
// 命中时直接跳过 tenants 表查询，避免 ALB/kubelet 健康检查触发的 noisy SELECT
var privateCIDRs = func() []*net.IPNet {
	blocks := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"127.0.0.0/8",
		"169.254.0.0/16",
		"::1/128",
		"fc00::/7",
	}
	out := make([]*net.IPNet, 0, len(blocks))
	for _, c := range blocks {
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// isPrivateHost 判断 host 是否为私网/保留 IP（Pod IP / VPC ENI / loopback）
// 非 IP 字符串（如正式域名）返回 false
func isPrivateHost(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, block := range privateCIDRs {
		if block.Contains(ip) {
			return true
		}
	}
	return false
}

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

		// 私网 IP（Pod IP、VPC 内网地址等）跳过 tenants 表查询
		// 这类 Host 通常来自 ALB/kubelet 健康检查，永远不会是合法的租户域名
		if isPrivateHost(host) {
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
