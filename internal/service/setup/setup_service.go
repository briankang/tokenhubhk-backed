package setup

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"tokenhub-server/internal/database"
	"tokenhub-server/internal/model"
	pkgredis "tokenhub-server/internal/pkg/redis"
)

// SetupService 安装向导服务，负责系统首次部署的初始化流程
type SetupService struct {
	db *gorm.DB
}

// NewSetupService 创建安装向导服务实例
func NewSetupService(db *gorm.DB) *SetupService {
	return &SetupService{db: db}
}

// CheckInitialized 检查系统是否已完成初始化
// 查询 system_configs 表中 key=initialized 的记录，值为 "true" 表示已初始化
func (s *SetupService) CheckInitialized() (bool, error) {
	var cfg model.SystemConfig
	err := s.db.Where("`key` = ?", "initialized").First(&cfg).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return false, nil
		}
		// 表可能不存在（首次启动），视为未初始化
		return false, nil
	}
	return cfg.Value == "true", nil
}

// TestDatabaseConnection 测试数据库连接
// 执行 Ping 检测数据库可达性
func (s *SetupService) TestDatabaseConnection() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return fmt.Errorf("获取底层数据库连接失败: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		return fmt.Errorf("数据库连接失败: %w", err)
	}
	return nil
}

// RunMigrations 执行数据库 schema 初始化
// 委派给 database.RunSchemaInit：清理遗留 + AutoMigrate 全部 80+ 表
// （比本文件硬编码列表完整且维护点唯一）
func (s *SetupService) RunMigrations() error {
	return database.RunSchemaInit(s.db)
}

// TestRedisConnection 测试 Redis 连接
// 执行 Ping 检测 Redis 可达性
func (s *SetupService) TestRedisConnection() error {
	if pkgredis.Client == nil {
		return fmt.Errorf("Redis 客户端未初始化")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pkgredis.Client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("Redis 连接失败: %w", err)
	}
	return nil
}

// InitializeCache 创建基础 Redis 缓存结构
// 写入系统缓存标记，确认 Redis 可正常读写
func (s *SetupService) InitializeCache() error {
	if pkgredis.Client == nil {
		return fmt.Errorf("Redis 客户端未初始化")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 写入系统缓存标记
	err := pkgredis.Set(ctx, "tokenhub:system:cache_initialized", "true", 0)
	if err != nil {
		return fmt.Errorf("初始化缓存失败: %w", err)
	}
	return nil
}

// CreateAdminAccount 创建管理员账号
// 参数: username-用户名, password-密码, email-邮箱
func (s *SetupService) CreateAdminAccount(username, password, email string) error {
	// 检查是否已存在管理员
	var count int64
	if err := s.db.Model(&model.User{}).Where("role = ?", "ADMIN").Count(&count).Error; err != nil {
		return fmt.Errorf("检查管理员账号失败: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("管理员账号已存在")
	}

	// 确保默认租户存在
	var tenant model.Tenant
	err := s.db.Where("parent_id IS NULL AND level = 1").First(&tenant).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			// 创建默认平台租户
			tenant = model.Tenant{
				Name:         "Platform",
				Domain:       "platform",
				Level:        1,
				IsActive:     true,
				ContactEmail: email,
			}
			if err := s.db.Create(&tenant).Error; err != nil {
				return fmt.Errorf("创建默认租户失败: %w", err)
			}
		} else {
			return fmt.Errorf("查询租户失败: %w", err)
		}
	}

	// 生成密码哈希 (bcrypt cost=12)
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return fmt.Errorf("密码哈希生成失败: %w", err)
	}

	// 创建管理员用户（v4.0: 角色通过 user_roles 表分配）
	adminUser := model.User{
		TenantID:     tenant.ID,
		Email:        email,
		PasswordHash: string(hash),
		Name:         username,
		IsActive:     true,
		Language:     "zh",
	}
	if err := s.db.Create(&adminUser).Error; err != nil {
		return fmt.Errorf("创建管理员账号失败: %w", err)
	}

	// 分配 SUPER_ADMIN 角色（若 roles 表已种子化）
	var superAdminID uint
	s.db.Table("roles").Where("code = ?", "SUPER_ADMIN").Select("id").Scan(&superAdminID)
	if superAdminID != 0 {
		_ = s.db.Create(&model.UserRole{
			UserID: adminUser.ID, RoleID: superAdminID,
		}).Error
	}

	return nil
}

// ImportSeedData 导入全量种子数据
// 调用 database.RunAllSeeds 写入所有预置的业务数据（供应商/分类/渠道/等级/参数/用例等）
func (s *SetupService) ImportSeedData() error {
	database.RunAllSeeds(s.db)
	return nil
}

// SaveBasicConfig 保存基础配置
// 将站点名称等基础信息写入 system_configs 表
func (s *SetupService) SaveBasicConfig(siteName string) error {
	configs := []model.SystemConfig{
		{Key: "site_name", Value: siteName},
	}
	for _, cfg := range configs {
		// 使用 upsert 逻辑：存在则更新，不存在则创建
		var existing model.SystemConfig
		err := s.db.Where("`key` = ?", cfg.Key).First(&existing).Error
		if err == gorm.ErrRecordNotFound {
			if err := s.db.Create(&cfg).Error; err != nil {
				return fmt.Errorf("保存配置 %s 失败: %w", cfg.Key, err)
			}
		} else if err == nil {
			existing.Value = cfg.Value
			if err := s.db.Save(&existing).Error; err != nil {
				return fmt.Errorf("更新配置 %s 失败: %w", cfg.Key, err)
			}
		} else {
			return fmt.Errorf("查询配置 %s 失败: %w", cfg.Key, err)
		}
	}
	return nil
}

// MarkInitialized 标记系统已完成初始化
// 写入 initialized=true 到 system_configs 表
func (s *SetupService) MarkInitialized() error {
	var cfg model.SystemConfig
	err := s.db.Where("`key` = ?", "initialized").First(&cfg).Error
	if err == gorm.ErrRecordNotFound {
		cfg = model.SystemConfig{
			Key:   "initialized",
			Value: "true",
		}
		return s.db.Create(&cfg).Error
	} else if err == nil {
		cfg.Value = "true"
		return s.db.Save(&cfg).Error
	}
	return err
}
