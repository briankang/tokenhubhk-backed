package middleware

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/gin-gonic/gin"

	pkgredis "tokenhub-server/internal/pkg/redis"
)

// 维度常量 —— SensitiveRateLimit 的 Key 维度选择
const (
	DimensionIP   = "ip"   // Key = rl:strict:<path>:<ip>（登录前敏感操作）
	DimensionUser = "user" // Key = rl:strict:<path>:user:<uid>（登录后敏感操作）
)

// SensitiveRateLimit 敏感端点（登录/注册/改密/提现/退款/API Key 创建等）独立严格桶。
// 与 MultiLevelRateLimiter 的全局 IP/User 桶完全隔离，专门保护易被滥用的路径。
//
// dimension="ip"   → Key rl:strict:<path>:<ip>
// dimension="user" → Key rl:strict:<path>:user:<uid>（缺 userId 时降级为 IP 维度）
//
// Redis 不可用或回环/Docker 内网 IP 自动豁免；rpm<=0 直接放行。
func SensitiveRateLimit(rpm int, dimension string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if rpm <= 0 {
			c.Next()
			return
		}

		// 测试绕过：与其他限流中间件保持一致的 bypass 协议
		if bypassToken := os.Getenv("RATE_LIMIT_BYPASS_TOKEN"); bypassToken != "" {
			if c.GetHeader("X-Test-Skip-RateLimit") == bypassToken {
				c.Next()
				return
			}
		}

		redis := pkgredis.Client
		if redis == nil {
			c.Next()
			return
		}

		var key string
		switch dimension {
		case DimensionUser:
			if uidVal, exists := c.Get("userId"); exists {
				key = fmt.Sprintf("rl:strict:%s:user:%v", c.FullPath(), uidVal)
				break
			}
			// 无 userId 时降级 IP 维度
			fallthrough
		default:
			ip := c.ClientIP()
			if isExemptIP(ip) {
				c.Next()
				return
			}
			key = fmt.Sprintf("rl:strict:%s:%s", c.FullPath(), ip)
		}

		ctx := context.Background()
		if !slidingWindowCheck(ctx, redis, key, rpm, c) {
			return
		}
		c.Next()
	}
}

// StrictLoginRateLimit 保留别名（向后兼容），等价于 SensitiveRateLimit(rpm, DimensionIP)
func StrictLoginRateLimit(rpm int) gin.HandlerFunc {
	return SensitiveRateLimit(rpm, DimensionIP)
}

// isExemptIP 回环/Docker 内网豁免（与 MultiLevelRateLimiter 对齐）
func isExemptIP(ip string) bool {
	return ip == "::1" || ip == "127.0.0.1" ||
		strings.HasPrefix(ip, "172.17.") || strings.HasPrefix(ip, "172.18.")
}
