package permission

import (
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/middleware/audit"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// Seed 幂等种子化：permissions → roles → role_permissions → user_roles 回填
// 在每次启动（backend/monolith 角色）的 bootstrap.runSeeds 中调用。
func Seed(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("seed: db is nil")
	}

	start := time.Now()
	permsByCode, err := seedPermissions(db)
	if err != nil {
		return fmt.Errorf("seed permissions: %w", err)
	}

	rolesByCode, err := seedRoles(db)
	if err != nil {
		return fmt.Errorf("seed roles: %w", err)
	}

	if err := seedRolePermissions(db, rolesByCode, permsByCode); err != nil {
		return fmt.Errorf("seed role_permissions: %w", err)
	}

	backfilled, err := backfillUserRoles(db, rolesByCode)
	if err != nil {
		return fmt.Errorf("backfill user_roles: %w", err)
	}

	if logger.L != nil {
		logger.L.Info("permission seed complete",
			zap.Int("permissions", len(permsByCode)),
			zap.Int("roles", len(rolesByCode)),
			zap.Int("user_roles_backfilled", backfilled),
			zap.Duration("duration", time.Since(start)),
		)
	}
	return nil
}

// seedPermissions 从 audit.RouteMapEntries() 写入 permissions 表
// 幂等：已存在的 code 仅补齐可能变化的 menu/feature/method/path 字段
func seedPermissions(db *gorm.DB) (map[string]*model.Permission, error) {
	entries := audit.RouteMapEntries()
	permsByCode := make(map[string]*model.Permission, len(entries))

	for _, e := range entries {
		// 防御：routeMap 中可能存在重复 action（同一 code 对应多个 HTTP 端点），仅保留第一个
		if _, dup := permsByCode[e.Meta.Action]; dup {
			continue
		}

		p := model.Permission{
			Code:       e.Meta.Action,
			Menu:       e.Meta.Menu,
			Feature:    e.Meta.Feature,
			Resource:   e.Meta.Resource,
			HTTPMethod: e.Method,
			HTTPPath:   e.Path,
			IsRead:     e.IsRead,
			IsSystem:   true,
		}
		// FirstOrCreate 保证幂等；attrs 仅在创建时生效，已存在记录不会覆盖
		var existing model.Permission
		err := db.Where("code = ?", p.Code).Attrs(p).FirstOrCreate(&existing).Error
		if err != nil {
			return nil, fmt.Errorf("upsert permission %s: %w", p.Code, err)
		}
		// 补齐已存在记录可能缺失的元数据字段（幂等）
		updates := map[string]interface{}{}
		if existing.Menu == "" && p.Menu != "" {
			updates["menu"] = p.Menu
		}
		if existing.Feature == "" && p.Feature != "" {
			updates["feature"] = p.Feature
		}
		if existing.HTTPMethod == "" && p.HTTPMethod != "" {
			updates["http_method"] = p.HTTPMethod
		}
		if existing.HTTPPath == "" && p.HTTPPath != "" {
			updates["http_path"] = p.HTTPPath
		}
		if len(updates) > 0 {
			db.Model(&model.Permission{}).Where("id = ?", existing.ID).Updates(updates)
		}
		p.ID = existing.ID
		permsByCode[p.Code] = &p
	}

	return permsByCode, nil
}

// seedRoles 从 BuiltinRoles 写入 roles 表
func seedRoles(db *gorm.DB) (map[string]*model.Role, error) {
	rolesByCode := make(map[string]*model.Role, len(BuiltinRoles))

	for _, br := range BuiltinRoles {
		scopeBytes, err := json.Marshal(map[string]interface{}{
			"type": br.DataScope,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal data_scope for %s: %w", br.Code, err)
		}

		r := model.Role{
			Code:        br.Code,
			Name:        br.Name,
			Description: br.Description,
			IsSystem:    true,
			DataScope:   scopeBytes,
		}
		var existing model.Role
		err = db.Where("code = ?", r.Code).Attrs(r).FirstOrCreate(&existing).Error
		if err != nil {
			return nil, fmt.Errorf("upsert role %s: %w", r.Code, err)
		}
		r.ID = existing.ID
		rolesByCode[r.Code] = &r
	}

	return rolesByCode, nil
}

// seedRolePermissions 为每个内置角色填充 role_permissions 关联
// 规则：
//   - AllPermissions=true  → 关联全部 permissions
//   - AllReadOnly=true     → 关联所有 is_read=true 的 permissions
//   - 否则                  → 菜单下所有 permissions 的 code ∪ ExtraCodes
func seedRolePermissions(db *gorm.DB, rolesByCode map[string]*model.Role, permsByCode map[string]*model.Permission) error {
	for _, br := range BuiltinRoles {
		role, ok := rolesByCode[br.Code]
		if !ok {
			continue
		}
		wanted := collectPermissionIDs(br, permsByCode)
		if len(wanted) == 0 {
			continue
		}

		// 查询该角色已有的 permission_id 集合
		var existingIDs []uint
		if err := db.Model(&model.RolePermission{}).
			Where("role_id = ?", role.ID).
			Pluck("permission_id", &existingIDs).Error; err != nil {
			return fmt.Errorf("list existing role_permissions for %s: %w", br.Code, err)
		}
		existingSet := make(map[uint]struct{}, len(existingIDs))
		for _, id := range existingIDs {
			existingSet[id] = struct{}{}
		}

		// 仅插入差集（幂等）
		toInsert := make([]model.RolePermission, 0, len(wanted))
		for _, pid := range wanted {
			if _, exists := existingSet[pid]; exists {
				continue
			}
			toInsert = append(toInsert, model.RolePermission{
				RoleID:       role.ID,
				PermissionID: pid,
				CreatedAt:    time.Now(),
			})
		}
		if len(toInsert) > 0 {
			if err := db.CreateInBatches(toInsert, 200).Error; err != nil {
				return fmt.Errorf("insert role_permissions for %s: %w", br.Code, err)
			}
		}
	}
	return nil
}

// collectPermissionIDs 根据 BuiltinRole 声明返回其应拥有的权限 ID 列表
func collectPermissionIDs(br BuiltinRole, permsByCode map[string]*model.Permission) []uint {
	ids := make(map[uint]struct{})

	if br.AllPermissions {
		for _, p := range permsByCode {
			ids[p.ID] = struct{}{}
		}
	} else if br.AllReadOnly {
		for _, p := range permsByCode {
			if p.IsRead {
				ids[p.ID] = struct{}{}
			}
		}
	} else {
		menuSet := make(map[string]struct{}, len(br.Menus))
		for _, m := range br.Menus {
			menuSet[m] = struct{}{}
		}
		for _, p := range permsByCode {
			if _, ok := menuSet[p.Menu]; ok {
				ids[p.ID] = struct{}{}
			}
		}
		for _, code := range br.ExtraCodes {
			if p, ok := permsByCode[code]; ok {
				ids[p.ID] = struct{}{}
			}
		}
	}

	out := make([]uint, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	return out
}

// backfillUserRoles 根据 users.role 字符串为未分配角色的用户回填 user_roles
// 仅对 user_roles 表无记录的用户执行，保证幂等。
func backfillUserRoles(db *gorm.DB, rolesByCode map[string]*model.Role) (int, error) {
	// 查询尚未有任何 user_roles 记录的用户列表
	type userRow struct {
		ID   uint
		Role string
	}
	var users []userRow
	sql := `
		SELECT u.id, u.role
		FROM users u
		WHERE NOT EXISTS (SELECT 1 FROM user_roles ur WHERE ur.user_id = u.id)
	`
	if err := db.Raw(sql).Scan(&users).Error; err != nil {
		return 0, fmt.Errorf("query users without roles: %w", err)
	}

	if len(users) == 0 {
		return 0, nil
	}

	rows := make([]model.UserRole, 0, len(users))
	now := time.Now()
	for _, u := range users {
		targetCode, ok := LegacyRoleMapping[u.Role]
		if !ok {
			targetCode = "USER"
			if logger.L != nil {
				logger.L.Warn("unknown legacy role, falling back to USER",
					zap.Uint("user_id", u.ID),
					zap.String("legacy_role", u.Role),
				)
			}
		}
		targetRole, ok := rolesByCode[targetCode]
		if !ok {
			if logger.L != nil {
				logger.L.Warn("target role not seeded, skip backfill",
					zap.Uint("user_id", u.ID),
					zap.String("target_code", targetCode),
				)
			}
			continue
		}
		rows = append(rows, model.UserRole{
			UserID:    u.ID,
			RoleID:    targetRole.ID,
			GrantedBy: 0, // 0 = 系统回填
			GrantedAt: now,
		})
	}

	if len(rows) == 0 {
		return 0, nil
	}
	if err := db.CreateInBatches(rows, 500).Error; err != nil {
		return 0, fmt.Errorf("insert user_roles: %w", err)
	}
	return len(rows), nil
}
