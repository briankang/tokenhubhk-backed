package permission

import (
	"context"
	"fmt"

	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

// PermissionService 数据访问权限检查
// v4.0: 所有决策来自 LoadSubjectPerms 注入的 ctx 键
//
//	dataScopeAll=true   → 任意目标放行
//	visibleTenantIDs    → 目标租户在列表内放行
//	dataScopeOwnOnly    → 目标 user_id==current 放行
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

func isDataScopeAll(ctx context.Context) bool {
	if v, ok := ctx.Value("dataScopeAll").(bool); ok && v {
		return true
	}
	return false
}

func isDataScopeOwnOnly(ctx context.Context) bool {
	if v, ok := ctx.Value("dataScopeOwnOnly").(bool); ok && v {
		return true
	}
	return false
}

func visibleTenantIDs(ctx context.Context) []uint {
	if ids, ok := ctx.Value("visibleTenantIDs").([]uint); ok {
		return ids
	}
	return nil
}

// CanAccessTenant 检查当前用户是否可以访问目标租户的数据
func (s *PermissionService) CanAccessTenant(ctx context.Context, targetTenantID uint) (bool, error) {
	if targetTenantID == 0 {
		return false, fmt.Errorf("target tenant ID is zero")
	}
	if isDataScopeAll(ctx) {
		return true, nil
	}
	tenantID := getUintFromCtx(ctx, "tenantId")
	if tenantID != 0 && tenantID == targetTenantID {
		return true, nil
	}
	for _, id := range visibleTenantIDs(ctx) {
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
	if isDataScopeAll(ctx) {
		return true, nil
	}
	currentUserID := getUintFromCtx(ctx, "userId")
	if isDataScopeOwnOnly(ctx) {
		return currentUserID == targetUserID, nil
	}
	// 租户级：检查目标用户所在租户是否可见
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
	if isDataScopeAll(ctx) {
		return true, nil
	}
	var key model.ApiKey
	if err := s.db.Select("tenant_id, user_id").Where("id = ?", targetKeyID).First(&key).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return false, nil
		}
		return false, fmt.Errorf("failed to lookup api key: %w", err)
	}
	if isDataScopeOwnOnly(ctx) {
		currentUserID := getUintFromCtx(ctx, "userId")
		return key.UserID == currentUserID, nil
	}
	return s.CanAccessTenant(ctx, key.TenantID)
}

// SensitiveFields 非管理员用户应隐藏的敏感字段列表
var SensitiveFields = []string{"total_cost", "channel_cost", "supplier_cost"}

// FilterSensitiveData 为非管理员用户移除敏感的成本字段
func (s *PermissionService) FilterSensitiveData(ctx context.Context, data interface{}) interface{} {
	// dataScopeAll 视为等价于"拥有全局视角"，不过滤
	if isDataScopeAll(ctx) {
		return data
	}

	if m, ok := data.(map[string]interface{}); ok {
		filtered := make(map[string]interface{}, len(m))
		for k, v := range m {
			if isSensitiveField(k) {
				continue
			}
			filtered[k] = v
		}
		return filtered
	}

	if items, ok := data.([]map[string]interface{}); ok {
		result := make([]map[string]interface{}, 0, len(items))
		for _, item := range items {
			filtered := make(map[string]interface{}, len(item))
			for k, v := range item {
				if isSensitiveField(k) {
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

// InvalidateTenantCache 兼容保留：v4.0 不再缓存租户子树，直接 no-op
// 旧代码路径仍可能调用此函数；未来版本可删除。
func (s *PermissionService) InvalidateTenantCache(ctx context.Context, tenantID uint) error {
	return nil
}

// isSensitiveField 判断字段是否应对非 SUPER_ADMIN 用户隐藏
func isSensitiveField(field string) bool {
	switch field {
	case "total_cost", "channel_cost", "supplier_cost", "cost_per_token",
		"channel_id", "channel_name", "supplier_id", "supplier_name":
		return true
	}
	return false
}
