package permission

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

const (
	tenantSubtreeCachePrefix = "tenant_subtree:"
	tenantSubtreeTTL         = 10 * time.Minute
)

// CtxKey 上下文键类型，避免键冲突
type CtxKey string

const (
	CtxTenantID       CtxKey = "tenantId"
	CtxRole           CtxKey = "role"
	CtxUserID         CtxKey = "userId"
	CtxVisibleTenants CtxKey = "visibleTenantIDs"
	CtxDataScopeAll   CtxKey = "dataScopeAll"
)

// ScopedDB 根据上下文中的角色信息返回带租户级过滤的 GORM DB
// ADMIN: 无过滤; AGENT: 按可见租户子树过滤; USER: 按用户 ID 过滤
func ScopedDB(ctx context.Context, db *gorm.DB) *gorm.DB {
	if db == nil {
		return nil
	}

	role := getStringFromCtx(ctx, "role")
	if role == "" {
		return db
	}

	// ADMIN 可以看到所有数据
	if role == "ADMIN" {
		return db
	}

	// 检查是否设置了全局数据范围
	if isAll, ok := ctx.Value("dataScopeAll").(bool); ok && isAll {
		return db
	}

	// 代理角色: 按可见租户 ID 过滤
	if isAgentRole(role) {
		if ids, ok := ctx.Value("visibleTenantIDs").([]uint); ok && len(ids) > 0 {
			return db.Where("tenant_id IN ?", ids)
		}
		// 回退: 仅限制为自己的租户
		if tid := getUintFromCtx(ctx, "tenantId"); tid > 0 {
			return db.Where("tenant_id = ?", tid)
		}
	}

	// USER: 按用户 ID 过滤
	if role == "USER" {
		if uid := getUintFromCtx(ctx, "userId"); uid > 0 {
			return db.Where("user_id = ?", uid)
		}
	}

	// 无有效范围时拒绝所有访问
	return db.Where("1 = 0")
}

// GetVisibleTenantIDs 获取指定租户的可见租户 ID 列表
// 使用 BFS 遍历租户树形结构，结果缓存在 Redis 中（TTL 10 分钟）
func GetVisibleTenantIDs(ctx context.Context, db *gorm.DB, redis *goredis.Client, tenantID uint) ([]uint, error) {
	if tenantID == 0 {
		return nil, fmt.Errorf("tenantID must not be zero")
	}
	if db == nil {
		return []uint{tenantID}, nil
	}

	// 优先从缓存读取
	if redis != nil {
		cacheKey := fmt.Sprintf("%s%d", tenantSubtreeCachePrefix, tenantID)
		val, err := redis.Get(ctx, cacheKey).Result()
		if err == nil && val != "" {
			var cached []uint
			if jsonErr := json.Unmarshal([]byte(val), &cached); jsonErr == nil && len(cached) > 0 {
				return cached, nil
			}
		}
	}

	// BFS 遍历收集完整子树
	ids, err := bfsSubtree(db, tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to get subtree for tenant %d: %w", tenantID, err)
	}

	// 缓存结果
	if redis != nil {
		cacheKey := fmt.Sprintf("%s%d", tenantSubtreeCachePrefix, tenantID)
		data, _ := json.Marshal(ids)
		_ = redis.Set(ctx, cacheKey, string(data), tenantSubtreeTTL).Err()
	}

	return ids, nil
}

// TenantScope 返回按可见租户 ID 过滤的 GORM Scope 函数
func TenantScope(ctx context.Context) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		role := getStringFromCtx(ctx, "role")
		if role == "ADMIN" {
			return db
		}
		if isAll, ok := ctx.Value("dataScopeAll").(bool); ok && isAll {
			return db
		}

		if isAgentRole(role) {
			if ids, ok := ctx.Value("visibleTenantIDs").([]uint); ok && len(ids) > 0 {
				return db.Where("tenant_id IN ?", ids)
			}
			if tid := getUintFromCtx(ctx, "tenantId"); tid > 0 {
				return db.Where("tenant_id = ?", tid)
			}
		}

		if role == "USER" {
			if tid := getUintFromCtx(ctx, "tenantId"); tid > 0 {
				return db.Where("tenant_id = ?", tid)
			}
		}

		return db.Where("1 = 0")
	}
}

// UserScope 返回按当前用户 ID 过滤的 GORM Scope 函数
func UserScope(ctx context.Context) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		role := getStringFromCtx(ctx, "role")
		if role == "ADMIN" {
			return db
		}
		if role == "USER" {
			if uid := getUintFromCtx(ctx, "userId"); uid > 0 {
				return db.Where("user_id = ?", uid)
			}
			return db.Where("1 = 0")
		}
		// 代理角色: 使用租户范围代替用户范围
		return TenantScope(ctx)(db)
	}
}

// bfsSubtree BFS 遍历收集所有后代租户 ID
func bfsSubtree(db *gorm.DB, rootID uint) ([]uint, error) {
	result := []uint{rootID}
	queue := []uint{rootID}

	for len(queue) > 0 {
		currentBatch := queue
		queue = nil

		var children []model.Tenant
		if err := db.Select("id").Where("parent_id IN ?", currentBatch).Find(&children).Error; err != nil {
			return nil, err
		}

		for _, child := range children {
			result = append(result, child.ID)
			queue = append(queue, child.ID)
		}
	}

	return result, nil
}

// getStringFromCtx 从上下文安全提取字符串值
func getStringFromCtx(ctx context.Context, key string) string {
	if v, ok := ctx.Value(key).(string); ok {
		return v
	}
	return ""
}

// getUintFromCtx 从上下文安全提取 uint 值
func getUintFromCtx(ctx context.Context, key string) uint {
	if v, ok := ctx.Value(key).(uint); ok {
		return v
	}
	return 0
}

// isAgentRole 检查角色是否为代理角色
func isAgentRole(role string) bool {
	return role == "AGENT_L1" || role == "AGENT_L2" || role == "AGENT_L3"
}
