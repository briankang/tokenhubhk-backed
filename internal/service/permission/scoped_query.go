package permission

import (
	"context"

	"gorm.io/gorm"
)

// CtxKey 上下文键类型，避免键冲突
type CtxKey string

const (
	CtxTenantID       CtxKey = "tenantId"
	CtxUserID         CtxKey = "userId"
	CtxVisibleTenants CtxKey = "visibleTenantIDs"
	CtxDataScopeAll   CtxKey = "dataScopeAll"
	CtxDataScopeOwn   CtxKey = "dataScopeOwnOnly"
)

// ScopedDB 根据上下文中的数据范围策略返回带过滤的 GORM DB
// v4.0: 从 LoadSubjectPerms + DataScope 注入的 context 键读取，不再依赖 role 字符串
//
//   - dataScopeAll=true           → 无过滤（SUPER_ADMIN / FINANCE_MANAGER 等）
//   - visibleTenantIDs 非空       → WHERE tenant_id IN (...)（OPERATION_MANAGER custom_tenants）
//   - dataScopeOwnOnly=true       → WHERE user_id = current_user_id（USER）
//   - 其他                         → 拒绝访问（1=0）
func ScopedDB(ctx context.Context, db *gorm.DB) *gorm.DB {
	if db == nil {
		return nil
	}
	if isAll, ok := ctx.Value("dataScopeAll").(bool); ok && isAll {
		return db
	}
	if ids, ok := ctx.Value("visibleTenantIDs").([]uint); ok && len(ids) > 0 {
		return db.Where("tenant_id IN ?", ids)
	}
	if ownOnly, ok := ctx.Value("dataScopeOwnOnly").(bool); ok && ownOnly {
		if uid := getUintFromCtx(ctx, "userId"); uid > 0 {
			return db.Where("user_id = ?", uid)
		}
		return db.Where("1 = 0")
	}
	// 默认拒绝（未加载 SubjectPerms 的非公开接口不应触达此处）
	return db.Where("1 = 0")
}

// TenantScope 返回按可见租户 ID 过滤的 GORM Scope 函数
func TenantScope(ctx context.Context) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if isAll, ok := ctx.Value("dataScopeAll").(bool); ok && isAll {
			return db
		}
		if ids, ok := ctx.Value("visibleTenantIDs").([]uint); ok && len(ids) > 0 {
			return db.Where("tenant_id IN ?", ids)
		}
		// own_only 降级为自身租户
		if tid := getUintFromCtx(ctx, "tenantId"); tid > 0 {
			return db.Where("tenant_id = ?", tid)
		}
		return db.Where("1 = 0")
	}
}

// UserScope 返回按当前用户 ID 过滤的 GORM Scope 函数
func UserScope(ctx context.Context) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if isAll, ok := ctx.Value("dataScopeAll").(bool); ok && isAll {
			return db
		}
		if ownOnly, ok := ctx.Value("dataScopeOwnOnly").(bool); ok && ownOnly {
			if uid := getUintFromCtx(ctx, "userId"); uid > 0 {
				return db.Where("user_id = ?", uid)
			}
			return db.Where("1 = 0")
		}
		// 租户级数据范围按 TenantScope 过滤
		return TenantScope(ctx)(db)
	}
}

// getUintFromCtx 从上下文安全提取 uint 值
func getUintFromCtx(ctx context.Context, key string) uint {
	if v, ok := ctx.Value(key).(uint); ok {
		return v
	}
	return 0
}
