package permission

import (
	"context"
	"fmt"

	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

// PermissionService 权限服务，处理数据访问权限检查
type PermissionService struct {
	db    *gorm.DB
	redis *goredis.Client
}

// NewPermissionService 创建权限服务实例
func NewPermissionService(db *gorm.DB, redis *goredis.Client) *PermissionService {
	if db == nil {
		panic("permission service: db is nil")
	}
	return &PermissionService{db: db, redis: redis}
}

// CanAccessTenant 检查当前用户是否可以访问目标租户的数据
func (s *PermissionService) CanAccessTenant(ctx context.Context, targetTenantID uint) (bool, error) {
	if targetTenantID == 0 {
		return false, fmt.Errorf("target tenant ID is zero")
	}

	role := getStringFromCtx(ctx, "role")
	if role == "ADMIN" {
		return true, nil
	}

	tenantID := getUintFromCtx(ctx, "tenantId")
	if tenantID == 0 {
		return false, nil
	}

	// 同一租户
	if tenantID == targetTenantID {
		return true, nil
	}

	// 从上下文获取可见租户 ID（由 datascope 中间件设置）
	if ids, ok := ctx.Value("visibleTenantIDs").([]uint); ok {
		for _, id := range ids {
			if id == targetTenantID {
				return true, nil
			}
		}
	}

	// 回退: 从数据库计算可见租户
	visible, err := GetVisibleTenantIDs(ctx, s.db, s.redis, tenantID)
	if err != nil {
		return false, fmt.Errorf("failed to check tenant access: %w", err)
	}
	for _, id := range visible {
		if id == targetTenantID {
			return true, nil
		}
	}

	return false, nil
}

// CanAccessUser 检查当前用户是否可以访问目标用户的数据
func (s *PermissionService) CanAccessUser(ctx context.Context, targetUserID uint) (bool, error) {
	if targetUserID == 0 {
		return false, fmt.Errorf("target user ID is zero")
	}

	role := getStringFromCtx(ctx, "role")
	if role == "ADMIN" {
		return true, nil
	}

	currentUserID := getUintFromCtx(ctx, "userId")

	// USER 角色只能访问自己的数据
	if role == "USER" {
		return currentUserID == targetUserID, nil
	}

	// 代理角色: 检查目标用户是否属于可见租户
	var targetUser model.User
	if err := s.db.Select("tenant_id").Where("id = ?", targetUserID).First(&targetUser).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return false, nil
		}
		return false, fmt.Errorf("failed to lookup target user: %w", err)
	}

	return s.CanAccessTenant(ctx, targetUser.TenantID)
}

// CanAccessApiKey 检查当前用户是否可以查看目标 API Key
func (s *PermissionService) CanAccessApiKey(ctx context.Context, targetKeyID uint) (bool, error) {
	if targetKeyID == 0 {
		return false, fmt.Errorf("target key ID is zero")
	}

	role := getStringFromCtx(ctx, "role")
	if role == "ADMIN" {
		return true, nil
	}

	var key model.ApiKey
	if err := s.db.Select("tenant_id, user_id").Where("id = ?", targetKeyID).First(&key).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return false, nil
		}
		return false, fmt.Errorf("failed to lookup api key: %w", err)
	}

	// USER: 只能查看自己的 Key
	if role == "USER" {
		currentUserID := getUintFromCtx(ctx, "userId")
		return key.UserID == currentUserID, nil
	}

	// 代理: 检查 Key 的租户是否在可见子树中
	return s.CanAccessTenant(ctx, key.TenantID)
}

// SensitiveFields 非管理员用户应隐藏的敏感字段列表
var SensitiveFields = []string{"total_cost", "channel_cost", "supplier_cost"}

// FilterSensitiveData 为非管理员用户移除敏感的成本字段
func (s *PermissionService) FilterSensitiveData(ctx context.Context, data interface{}) interface{} {
	role := getStringFromCtx(ctx, "role")
	if role == "ADMIN" {
		return data
	}

	// 对于 map 类型数据，移除敏感键
	if m, ok := data.(map[string]interface{}); ok {
		filtered := make(map[string]interface{}, len(m))
		for k, v := range m {
			if isSensitiveField(k, role) {
				continue
			}
			filtered[k] = v
		}
		return filtered
	}

	// 对于 map 切片
	if items, ok := data.([]map[string]interface{}); ok {
		result := make([]map[string]interface{}, 0, len(items))
		for _, item := range items {
			filtered := make(map[string]interface{}, len(item))
			for k, v := range item {
				if isSensitiveField(k, role) {
					continue
				}
				filtered[k] = v
			}
			result = append(result, filtered)
		}
		return result
	}

	return data
}

// InvalidateTenantCache 清除指定租户及其祖先的缓存子树
func (s *PermissionService) InvalidateTenantCache(ctx context.Context, tenantID uint) error {
	if s.redis == nil {
		return nil
	}

	// 清除该租户的缓存
	cacheKey := fmt.Sprintf("%s%d", tenantSubtreeCachePrefix, tenantID)
	if err := s.redis.Del(ctx, cacheKey).Err(); err != nil {
		return fmt.Errorf("failed to invalidate cache for tenant %d: %w", tenantID, err)
	}

	// 同时清除父租户链
	var tenant model.Tenant
	if err := s.db.Select("parent_id").Where("id = ?", tenantID).First(&tenant).Error; err != nil {
		return nil // not critical
	}
	if tenant.ParentID != nil && *tenant.ParentID > 0 {
		parentKey := fmt.Sprintf("%s%d", tenantSubtreeCachePrefix, *tenant.ParentID)
		_ = s.redis.Del(ctx, parentKey).Err()
		// 向上递归清除祖父缓存（最多 3 层）
		_ = s.InvalidateTenantCache(ctx, *tenant.ParentID)
	}

	return nil
}

// isSensitiveField 判断字段是否应对指定角色隐藏
func isSensitiveField(field string, role string) bool {
	if role == "ADMIN" {
		return false
	}
	// 代理和普通用户不应看到上游渠道成本
	if isAgentRole(role) || role == "USER" {
		switch field {
		case "total_cost", "channel_cost", "supplier_cost", "cost_per_token",
			"channel_id", "channel_name", "supplier_id", "supplier_name":
			return true
		}
	}
	return false
}
