package database

import (
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/pkg/logger"
)

// RunDropUserRoleColumn v4.0 RBAC 迁移：删除 users.role 字段
//
// 前置条件（由调用方确保）：
//   - permissions / roles / role_permissions / user_roles 表已创建
//   - permission.Seed() 已运行，所有现有用户都有 user_roles 记录
//
// 本迁移幂等：列不存在时直接返回。
//
// 风险：rollback 需要 `ALTER TABLE users ADD COLUMN role VARCHAR(20)` 并从 user_roles 反推 role 字符串；
// 反推映射 (SUPER_ADMIN→ADMIN, FINANCE_MANAGER→ADMIN, USER→USER, ...) 需人工维护。
func RunDropUserRoleColumn(db *gorm.DB) error {
	start := time.Now()

	// 1. 检查列是否存在
	var count int64
	err := db.Raw(`
		SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'users' AND COLUMN_NAME = 'role'
	`).Scan(&count).Error
	if err != nil {
		return fmt.Errorf("check role column: %w", err)
	}
	if count == 0 {
		if logger.L != nil {
			logger.L.Debug("migrate_drop_user_role: column already absent, skip")
		}
		return nil
	}

	// 2. 前置校验：用户数 vs user_roles 覆盖
	var userCount, rolesCount int64
	db.Raw("SELECT COUNT(*) FROM users WHERE deleted_at IS NULL").Scan(&userCount)
	db.Raw("SELECT COUNT(DISTINCT user_id) FROM user_roles").Scan(&rolesCount)
	if userCount > 0 && rolesCount < userCount {
		// 不阻塞执行，但记录告警
		if logger.L != nil {
			logger.L.Warn("migrate_drop_user_role: some users have no user_roles; they will default to USER via resolver",
				zap.Int64("users", userCount),
				zap.Int64("users_with_roles", rolesCount),
			)
		}
	}

	// 3. DROP COLUMN
	if err := db.Exec("ALTER TABLE `users` DROP COLUMN `role`").Error; err != nil {
		return fmt.Errorf("drop users.role: %w", err)
	}

	if logger.L != nil {
		logger.L.Info("migrate_drop_user_role: complete",
			zap.Int64("users", userCount),
			zap.Int64("users_with_roles", rolesCount),
			zap.Duration("duration", time.Since(start)),
		)
	}
	return nil
}
