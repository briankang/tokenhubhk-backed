package middleware

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/database"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/service/permission"
)

// DataScope 数据权限中间件，计算当前用户可见的租户子树并写入上下文
// ADMIN 可见所有数据，代理商只能看到自己及子租户的数据
// 使用 Redis 缓存租户子树（TTL 10min）
func DataScope() gin.HandlerFunc {
	return func(c *gin.Context) {
		role, exists := c.Get("role")
		if !exists {
			c.Next()
			return
		}
		roleStr, _ := role.(string)

		// ADMIN 可见所有数据
		if roleStr == "ADMIN" {
			c.Set("dataScopeAll", true)
			ctx := c.Request.Context()
			ctx = context.WithValue(ctx, "dataScopeAll", true)
			c.Request = c.Request.WithContext(ctx)
			c.Next()
			return
		}

		tenantID, exists := c.Get("tenantId")
		if !exists {
			c.Next()
			return
		}
		tid, ok := tenantID.(uint)
		if !ok || tid == 0 {
			c.Next()
			return
		}

		// 计算可见租户 ID 列表（自身 + 所有子租户），使用 Redis 缓存
		visibleIDs := getSubtreeIDsCached(c.Request.Context(), tid)
		c.Set("visibleTenantIDs", visibleIDs)

		// 写入 request context 供 Service 层使用
		ctx := c.Request.Context()
		ctx = context.WithValue(ctx, "visibleTenantIDs", visibleIDs)
		ctx = context.WithValue(ctx, "dataScopeAll", false)
		c.Request = c.Request.WithContext(ctx)

		c.Next()
	}
}

// getSubtreeIDsCached 使用权限服务的缓存 BFS 获取租户子树 ID
// 当 Redis 不可用时降级为直接 DB 查询
func getSubtreeIDsCached(ctx context.Context, rootID uint) []uint {
	if database.DB == nil {
		return []uint{rootID}
	}

	// 尝试使用 Redis 缓存版本
	ids, err := permission.GetVisibleTenantIDs(ctx, database.DB, redis.Client, rootID)
	if err == nil && len(ids) > 0 {
		return ids
	}

	// 降级: 直接递归查询（无缓存）
	return getSubtreeIDs(rootID)
}

// getSubtreeIDs 通过递归 DB 查询获取租户 ID 及其所有后代 ID
func getSubtreeIDs(rootID uint) []uint {
	ids := []uint{rootID}
	if database.DB == nil {
		return ids
	}

	var children []model.Tenant
	if err := database.DB.Select("id").Where("parent_id = ?", rootID).Find(&children).Error; err != nil {
		return ids
	}

	for _, child := range children {
		ids = append(ids, getSubtreeIDs(child.ID)...)
	}
	return ids
}

// GetVisibleTenantIDs 从上下文中获取可见租户 ID 列表
// 返回 (tenantIDs, isAll)：isAll=true 表示可见所有租户
func GetVisibleTenantIDs(c *gin.Context) ([]uint, bool) {
	if all, exists := c.Get("dataScopeAll"); exists {
		if isAll, ok := all.(bool); ok && isAll {
			return nil, true // nil means all tenants visible
		}
	}
	if ids, exists := c.Get("visibleTenantIDs"); exists {
		if tenantIDs, ok := ids.([]uint); ok {
			return tenantIDs, false
		}
	}
	return []uint{}, false
}

// IsAgentRole 检查角色是否为代理商角色 (AGENT_L1/L2/L3)
func IsAgentRole(role string) bool {
	return strings.HasPrefix(role, "AGENT_L")
}
