package middleware

import (
	"context"

	"github.com/gin-gonic/gin"

	permissionsvc "tokenhub-server/internal/service/permission"
)

// DataScope 数据权限中间件（v4.0 重构版）
// 依赖 LoadSubjectPerms 已写入的 effectiveDataScope；按 policy.Type 分发：
//   - all            → dataScopeAll=true（管理员类）
//   - own_tenant     → visibleTenantIDs=[user.TenantID]
//   - custom_tenants → visibleTenantIDs=policy.TenantIDs
//   - own_only       → dataScopeOwnOnly=true（UserScope 按 user_id 过滤）
//
// 原 AGENT_* 子树 BFS 逻辑已随代理商模块下线而移除。
func DataScope() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 从 LoadSubjectPerms 注入的 context 键读取
		scopeAny, exists := c.Get(ctxKeyEffectiveScope)
		if !exists {
			// 未注入 SubjectPerms（例如公开接口） → 直接放行
			c.Next()
			return
		}
		policy, ok := scopeAny.(permissionsvc.DataScopePolicy)
		if !ok {
			c.Next()
			return
		}

		ctx := c.Request.Context()
		switch policy.Type {
		case permissionsvc.DataScopeAll:
			c.Set("dataScopeAll", true)
			ctx = context.WithValue(ctx, "dataScopeAll", true)
		case permissionsvc.DataScopeCustomTenants:
			c.Set("visibleTenantIDs", policy.TenantIDs)
			ctx = context.WithValue(ctx, "visibleTenantIDs", policy.TenantIDs)
			ctx = context.WithValue(ctx, "dataScopeAll", false)
		case permissionsvc.DataScopeOwnTenant:
			tid := readTenantID(c)
			visible := []uint{}
			if tid != 0 {
				visible = []uint{tid}
			}
			c.Set("visibleTenantIDs", visible)
			ctx = context.WithValue(ctx, "visibleTenantIDs", visible)
			ctx = context.WithValue(ctx, "dataScopeAll", false)
		case permissionsvc.DataScopeOwnOnly:
			c.Set("dataScopeOwnOnly", true)
			ctx = context.WithValue(ctx, "dataScopeOwnOnly", true)
			ctx = context.WithValue(ctx, "dataScopeAll", false)
		default:
			// 未知类型，保守起见按 own_only 处理
			c.Set("dataScopeOwnOnly", true)
			ctx = context.WithValue(ctx, "dataScopeOwnOnly", true)
		}
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func readTenantID(c *gin.Context) uint {
	v, ok := c.Get("tenantId")
	if !ok {
		return 0
	}
	if tid, ok := v.(uint); ok {
		return tid
	}
	return 0
}

// GetVisibleTenantIDs 从上下文中获取可见租户 ID 列表
// 返回 (tenantIDs, isAll)：isAll=true 表示可见所有租户
func GetVisibleTenantIDs(c *gin.Context) ([]uint, bool) {
	if all, exists := c.Get("dataScopeAll"); exists {
		if isAll, ok := all.(bool); ok && isAll {
			return nil, true
		}
	}
	if ids, exists := c.Get("visibleTenantIDs"); exists {
		if tenantIDs, ok := ids.([]uint); ok {
			return tenantIDs, false
		}
	}
	return []uint{}, false
}
