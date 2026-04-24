package permission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/service/usercache"
)

// RoleService 角色管理服务：自定义角色 CRUD + 用户授权
// v4.0: 与 Resolver 协作 —— 任何修改都显式失效受影响用户的 Redis 缓存
type RoleService struct {
	db       *gorm.DB
	resolver *Resolver
}

// NewRoleService 创建实例；resolver 可为 nil（降级为无缓存）
func NewRoleService(db *gorm.DB, resolver *Resolver) *RoleService {
	if db == nil {
		panic("role service: db is nil")
	}
	return &RoleService{db: db, resolver: resolver}
}

// RoleDTO 角色视图（包含权限码 + 数据范围）
type RoleDTO struct {
	ID          uint            `json:"id"`
	Code        string          `json:"code"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	IsSystem    bool            `json:"is_system"`
	DataScope   DataScopePolicy `json:"data_scope"`
	Permissions []string        `json:"permissions"` // 权限码数组
	UserCount   int64           `json:"user_count"`  // 已绑定该角色的用户数
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// CreateRoleRequest 创建/克隆自定义角色请求
type CreateRoleRequest struct {
	Code        string          `json:"code" binding:"required"` // 形如 "custom_xxx"
	Name        string          `json:"name" binding:"required"`
	Description string          `json:"description"`
	DataScope   DataScopePolicy `json:"data_scope"`
	Permissions []string        `json:"permissions"` // 权限码数组
}

// UpdateRoleRequest 更新角色
type UpdateRoleRequest struct {
	Name        *string          `json:"name,omitempty"`
	Description *string          `json:"description,omitempty"`
	DataScope   *DataScopePolicy `json:"data_scope,omitempty"`
	Permissions *[]string        `json:"permissions,omitempty"` // 覆盖式：传入即替换全部
}

// List 分页返回所有角色
func (s *RoleService) List(ctx context.Context) ([]RoleDTO, error) {
	var roles []model.Role
	if err := s.db.WithContext(ctx).Order("is_system DESC, id ASC").Find(&roles).Error; err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}

	// 一次查询所有 role_permissions，按 role_id 分组
	type rpRow struct {
		RoleID uint
		Code   string
	}
	var rps []rpRow
	if err := s.db.WithContext(ctx).Raw(`
		SELECT rp.role_id, p.code
		FROM role_permissions rp
		JOIN permissions p ON p.id = rp.permission_id
		WHERE p.deleted_at IS NULL
	`).Scan(&rps).Error; err != nil {
		return nil, fmt.Errorf("load role permissions: %w", err)
	}
	codesByRole := make(map[uint][]string)
	for _, r := range rps {
		codesByRole[r.RoleID] = append(codesByRole[r.RoleID], r.Code)
	}

	// 统计每个角色的用户数
	type countRow struct {
		RoleID uint
		Cnt    int64
	}
	var counts []countRow
	s.db.WithContext(ctx).Raw(`
		SELECT role_id, COUNT(*) AS cnt FROM user_roles GROUP BY role_id
	`).Scan(&counts)
	userCountByRole := make(map[uint]int64)
	for _, c := range counts {
		userCountByRole[c.RoleID] = c.Cnt
	}

	out := make([]RoleDTO, 0, len(roles))
	for _, r := range roles {
		dto := RoleDTO{
			ID:          r.ID,
			Code:        r.Code,
			Name:        r.Name,
			Description: r.Description,
			IsSystem:    r.IsSystem,
			Permissions: codesByRole[r.ID],
			UserCount:   userCountByRole[r.ID],
			CreatedAt:   r.CreatedAt,
			UpdatedAt:   r.UpdatedAt,
		}
		if len(r.DataScope) > 0 {
			_ = json.Unmarshal(r.DataScope, &dto.DataScope)
		}
		out = append(out, dto)
	}
	return out, nil
}

// Get 返回单个角色详情
func (s *RoleService) Get(ctx context.Context, roleID uint) (*RoleDTO, error) {
	var r model.Role
	if err := s.db.WithContext(ctx).First(&r, roleID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("role not found")
		}
		return nil, err
	}
	var codes []string
	s.db.WithContext(ctx).Raw(`
		SELECT p.code FROM role_permissions rp
		JOIN permissions p ON p.id = rp.permission_id
		WHERE rp.role_id = ? AND p.deleted_at IS NULL
	`, roleID).Scan(&codes)
	var cnt int64
	s.db.WithContext(ctx).Model(&model.UserRole{}).Where("role_id = ?", roleID).Count(&cnt)

	dto := &RoleDTO{
		ID:          r.ID,
		Code:        r.Code,
		Name:        r.Name,
		Description: r.Description,
		IsSystem:    r.IsSystem,
		Permissions: codes,
		UserCount:   cnt,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
	if len(r.DataScope) > 0 {
		_ = json.Unmarshal(r.DataScope, &dto.DataScope)
	}
	return dto, nil
}

// Create 创建自定义角色
func (s *RoleService) Create(ctx context.Context, req CreateRoleRequest, grantedBy uint) (*RoleDTO, error) {
	if err := validateRoleCode(req.Code); err != nil {
		return nil, err
	}
	if IsBuiltinRoleCode(req.Code) {
		return nil, fmt.Errorf("code %q is reserved for built-in role", req.Code)
	}
	// 唯一性检查
	var count int64
	s.db.WithContext(ctx).Model(&model.Role{}).Where("code = ?", req.Code).Count(&count)
	if count > 0 {
		return nil, fmt.Errorf("role code already exists")
	}

	if req.DataScope.Type == "" {
		req.DataScope.Type = DataScopeOwnTenant
	}
	if !isValidScopeType(req.DataScope.Type) {
		return nil, fmt.Errorf("invalid data_scope.type %q", req.DataScope.Type)
	}
	scopeBytes, _ := json.Marshal(req.DataScope)

	role := model.Role{
		Code:        req.Code,
		Name:        req.Name,
		Description: req.Description,
		IsSystem:    false,
		DataScope:   scopeBytes,
	}
	if err := s.db.WithContext(ctx).Create(&role).Error; err != nil {
		return nil, fmt.Errorf("create role: %w", err)
	}

	if err := s.setPermissions(ctx, role.ID, req.Permissions); err != nil {
		return nil, err
	}

	_ = grantedBy // 预留：后续可在 audit log 记录创建者

	return s.Get(ctx, role.ID)
}

// Clone 基于已有角色创建自定义副本
func (s *RoleService) Clone(ctx context.Context, sourceID uint, newCode, newName string, grantedBy uint) (*RoleDTO, error) {
	src, err := s.Get(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	return s.Create(ctx, CreateRoleRequest{
		Code:        newCode,
		Name:        newName,
		Description: fmt.Sprintf("Cloned from %s", src.Code),
		DataScope:   src.DataScope,
		Permissions: src.Permissions,
	}, grantedBy)
}

// Update 更新角色（系统角色仅允许更新 name/description，不允许修改权限/数据范围）
func (s *RoleService) Update(ctx context.Context, roleID uint, req UpdateRoleRequest) (*RoleDTO, error) {
	var r model.Role
	if err := s.db.WithContext(ctx).First(&r, roleID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("role not found")
		}
		return nil, err
	}
	updates := map[string]interface{}{}
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	// 系统角色：明确拒绝修改权限/数据范围（FINDING-2 修复：从静默忽略改为报错）
	if r.IsSystem {
		if req.Permissions != nil {
			return nil, fmt.Errorf("cannot modify permissions of built-in system role %q; clone it to create a custom variant", r.Code)
		}
		if req.DataScope != nil {
			return nil, fmt.Errorf("cannot modify data_scope of built-in system role %q; clone it to create a custom variant", r.Code)
		}
	} else {
		if req.DataScope != nil {
			if !isValidScopeType(req.DataScope.Type) {
				return nil, fmt.Errorf("invalid data_scope.type %q", req.DataScope.Type)
			}
			scopeBytes, _ := json.Marshal(req.DataScope)
			updates["data_scope"] = scopeBytes
		}
	}

	if len(updates) > 0 {
		if err := s.db.WithContext(ctx).Model(&model.Role{}).Where("id = ?", roleID).Updates(updates).Error; err != nil {
			return nil, fmt.Errorf("update role: %w", err)
		}
	}

	if !r.IsSystem && req.Permissions != nil {
		if err := s.setPermissions(ctx, roleID, *req.Permissions); err != nil {
			return nil, err
		}
	}

	// 失效所有持有该角色的用户缓存
	if s.resolver != nil {
		_ = s.resolver.InvalidateByRoleID(ctx, roleID)
	}
	// 同步失效受影响用户的 /user/profile 缓存
	var affected []uint
	s.db.WithContext(ctx).Model(&model.UserRole{}).Where("role_id = ?", roleID).Pluck("user_id", &affected)
	for _, uid := range affected {
		usercache.InvalidateProfile(ctx, uid)
	}
	return s.Get(ctx, roleID)
}

// Delete 删除自定义角色（系统角色不可删）
func (s *RoleService) Delete(ctx context.Context, roleID uint) error {
	var r model.Role
	if err := s.db.WithContext(ctx).First(&r, roleID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("role not found")
		}
		return err
	}
	if r.IsSystem {
		return fmt.Errorf("cannot delete system role %q", r.Code)
	}

	// 收集受影响的用户 ID（用于缓存失效）
	var affectedUsers []uint
	s.db.WithContext(ctx).Model(&model.UserRole{}).Where("role_id = ?", roleID).Pluck("user_id", &affectedUsers)

	// 事务删除：user_roles + role_permissions + roles
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("role_id = ?", roleID).Delete(&model.UserRole{}).Error; err != nil {
			return err
		}
		if err := tx.Where("role_id = ?", roleID).Delete(&model.RolePermission{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Role{}, roleID).Error
	})
	if err != nil {
		return fmt.Errorf("delete role: %w", err)
	}

	if s.resolver != nil && len(affectedUsers) > 0 {
		_ = s.resolver.InvalidateUserPerms(ctx, affectedUsers...)
	}
	for _, uid := range affectedUsers {
		usercache.InvalidateProfile(ctx, uid)
	}
	return nil
}

// AssignUserRole 给用户授予角色（幂等）
func (s *RoleService) AssignUserRole(ctx context.Context, userID, roleID, grantedBy uint) error {
	if userID == 0 || roleID == 0 {
		return fmt.Errorf("invalid user or role id")
	}
	// 校验角色存在
	var exists int64
	s.db.WithContext(ctx).Model(&model.Role{}).Where("id = ?", roleID).Count(&exists)
	if exists == 0 {
		return fmt.Errorf("role not found")
	}
	ur := model.UserRole{UserID: userID, RoleID: roleID, GrantedBy: grantedBy, GrantedAt: time.Now()}
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND role_id = ?", userID, roleID).
		FirstOrCreate(&ur).Error
	if err != nil {
		return fmt.Errorf("assign user role: %w", err)
	}
	if s.resolver != nil {
		_ = s.resolver.InvalidateUserPerms(ctx, userID)
	}
	// 失效 /user/profile 缓存（含 permissions/role_codes 字段）
	usercache.InvalidateProfile(ctx, userID)
	return nil
}

// RevokeUserRole 撤销用户角色
// 保护：撤销 SUPER_ADMIN 时必须确保系统内至少剩一个 SUPER_ADMIN（防锁死）
func (s *RoleService) RevokeUserRole(ctx context.Context, userID, roleID uint) error {
	if userID == 0 || roleID == 0 {
		return fmt.Errorf("invalid user or role id")
	}

	var role model.Role
	if err := s.db.WithContext(ctx).First(&role, roleID).Error; err != nil {
		return fmt.Errorf("role not found")
	}
	if role.Code == "SUPER_ADMIN" {
		var cnt int64
		s.db.WithContext(ctx).Model(&model.UserRole{}).Where("role_id = ?", roleID).Count(&cnt)
		if cnt <= 1 {
			return fmt.Errorf("cannot revoke last SUPER_ADMIN assignment")
		}
	}

	if err := s.db.WithContext(ctx).
		Where("user_id = ? AND role_id = ?", userID, roleID).
		Delete(&model.UserRole{}).Error; err != nil {
		return fmt.Errorf("revoke user role: %w", err)
	}
	if s.resolver != nil {
		_ = s.resolver.InvalidateUserPerms(ctx, userID)
	}
	// 失效 /user/profile 缓存
	usercache.InvalidateProfile(ctx, userID)
	return nil
}

// RoleUserItem 角色下的用户摘要
type RoleUserItem struct {
	UserID    uint      `json:"user_id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	GrantedBy uint      `json:"granted_by"`
	GrantedAt time.Time `json:"granted_at"`
}

// ListRoleUsers 查询持有某角色的用户列表
func (s *RoleService) ListRoleUsers(ctx context.Context, roleID uint) ([]RoleUserItem, error) {
	var rows []struct {
		UserID    uint
		Email     string
		Name      string
		GrantedBy uint
		GrantedAt time.Time
	}
	err := s.db.WithContext(ctx).Raw(`
		SELECT ur.user_id, u.email, u.name, ur.granted_by, ur.granted_at
		FROM user_roles ur
		JOIN users u ON u.id = ur.user_id
		WHERE ur.role_id = ?
		ORDER BY ur.granted_at DESC
	`, roleID).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list role users: %w", err)
	}
	out := make([]RoleUserItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, RoleUserItem{
			UserID: r.UserID, Email: r.Email, Name: r.Name,
			GrantedBy: r.GrantedBy, GrantedAt: r.GrantedAt,
		})
	}
	return out, nil
}

// ListUserRoles 查看某用户的所有角色
func (s *RoleService) ListUserRoles(ctx context.Context, userID uint) ([]RoleDTO, error) {
	var roleIDs []uint
	if err := s.db.WithContext(ctx).Model(&model.UserRole{}).
		Where("user_id = ?", userID).Pluck("role_id", &roleIDs).Error; err != nil {
		return nil, err
	}
	if len(roleIDs) == 0 {
		return []RoleDTO{}, nil
	}
	var roles []model.Role
	if err := s.db.WithContext(ctx).Where("id IN ?", roleIDs).Find(&roles).Error; err != nil {
		return nil, err
	}
	out := make([]RoleDTO, 0, len(roles))
	for _, r := range roles {
		dto := RoleDTO{
			ID: r.ID, Code: r.Code, Name: r.Name, Description: r.Description,
			IsSystem: r.IsSystem, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		}
		if len(r.DataScope) > 0 {
			_ = json.Unmarshal(r.DataScope, &dto.DataScope)
		}
		out = append(out, dto)
	}
	return out, nil
}

// ListPermissions 返回权限目录（按菜单分组）
type PermissionDTO struct {
	ID         uint   `json:"id"`
	Code       string `json:"code"`
	Menu       string `json:"menu"`
	Feature    string `json:"feature"`
	Resource   string `json:"resource"`
	HTTPMethod string `json:"http_method"`
	HTTPPath   string `json:"http_path"`
	IsRead     bool   `json:"is_read"`
}

func (s *RoleService) ListPermissions(ctx context.Context) ([]PermissionDTO, error) {
	var perms []model.Permission
	if err := s.db.WithContext(ctx).Order("menu ASC, is_read ASC, code ASC").Find(&perms).Error; err != nil {
		return nil, err
	}
	out := make([]PermissionDTO, 0, len(perms))
	for _, p := range perms {
		out = append(out, PermissionDTO{
			ID: p.ID, Code: p.Code, Menu: p.Menu, Feature: p.Feature, Resource: p.Resource,
			HTTPMethod: p.HTTPMethod, HTTPPath: p.HTTPPath, IsRead: p.IsRead,
		})
	}
	return out, nil
}

// setPermissions 覆盖式重设角色的权限码列表（transaction）
func (s *RoleService) setPermissions(ctx context.Context, roleID uint, codes []string) error {
	// 将 codes 去重并过滤空值
	uniq := map[string]struct{}{}
	for _, c := range codes {
		c = strings.TrimSpace(c)
		if c != "" {
			uniq[c] = struct{}{}
		}
	}
	codeList := make([]string, 0, len(uniq))
	for c := range uniq {
		codeList = append(codeList, c)
	}

	// 查询 code → id，并校验是否全部存在（FINDING-3 修复：未知权限码立即报错）
	var perms []struct {
		ID   uint
		Code string
	}
	if len(codeList) > 0 {
		if err := s.db.WithContext(ctx).Model(&model.Permission{}).
			Where("code IN ?", codeList).
			Select("id, code").Scan(&perms).Error; err != nil {
			return fmt.Errorf("lookup permissions: %w", err)
		}
		// 比对：检查 codeList 中是否有在 DB 里找不到的权限码
		foundSet := make(map[string]struct{}, len(perms))
		for _, p := range perms {
			foundSet[p.Code] = struct{}{}
		}
		var unknown []string
		for _, c := range codeList {
			if _, ok := foundSet[c]; !ok {
				unknown = append(unknown, c)
			}
		}
		if len(unknown) > 0 {
			return fmt.Errorf("unknown permission codes: %s", strings.Join(unknown, ", "))
		}
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("role_id = ?", roleID).Delete(&model.RolePermission{}).Error; err != nil {
			return err
		}
		if len(perms) == 0 {
			return nil
		}
		rows := make([]model.RolePermission, 0, len(perms))
		now := time.Now()
		for _, p := range perms {
			rows = append(rows, model.RolePermission{
				RoleID: roleID, PermissionID: p.ID, CreatedAt: now,
			})
		}
		return tx.CreateInBatches(rows, 200).Error
	})
}

func validateRoleCode(code string) error {
	code = strings.TrimSpace(code)
	if len(code) < 3 || len(code) > 50 {
		return fmt.Errorf("role code length must be 3..50")
	}
	for _, c := range code {
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
		if !ok {
			return fmt.Errorf("role code contains invalid char %q; allow [a-zA-Z0-9_]", c)
		}
	}
	return nil
}

func isValidScopeType(t string) bool {
	switch t {
	case DataScopeAll, DataScopeOwnTenant, DataScopeCustomTenants, DataScopeOwnOnly:
		return true
	}
	return false
}
