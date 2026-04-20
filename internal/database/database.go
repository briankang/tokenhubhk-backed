package database

import (
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"tokenhub-server/internal/config"
	"tokenhub-server/internal/model"
)

// DB 全局 GORM 数据库实例
var DB *gorm.DB

// Init 初始化 GORM MySQL 连接并执行自动迁移
// 参数:
//   - cfg: 数据库配置
//   - logger: Zap 日志实例
func Init(cfg config.DatabaseConfig, logger *zap.Logger) error {
	dsn := cfg.DSN()
	if dsn == "" {
		return fmt.Errorf("database DSN is empty")
	}

	logLevel := gormlogger.Warn
	if cfg.Host == "" {
		logLevel = gormlogger.Silent
	}

	var err error
	DB, err = gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger:                                   gormlogger.Default.LogMode(logLevel),
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}

	// 注册连接初始化回调，确保每个新连接都使用 utf8mb4
	DB.Callback().Create().Before("gorm:create").Register("set_charset", func(db *gorm.DB) {
		db.Exec("SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci")
	})
	DB.Callback().Query().Before("gorm:query").Register("set_charset", func(db *gorm.DB) {
		db.Exec("SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci")
	})
	DB.Callback().Update().Before("gorm:update").Register("set_charset", func(db *gorm.DB) {
		db.Exec("SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci")
	})

	sqlDB, err := DB.DB()
	if err != nil {
		return fmt.Errorf("failed to get underlying sql.DB: %w", err)
	}

	// 立即设置当前连接的字符集
	DB.Exec("SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci")

	// 连接池配置
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(time.Duration(cfg.ConnMaxLifetime) * time.Second)

	if logger != nil {
		logger.Info("database connected",
			zap.String("host", cfg.Host),
			zap.Int("port", cfg.Port),
			zap.String("dbname", cfg.DBName),
		)
	}

	// 自动迁移所有模型
	// 先清理旧版 Prisma 遗留的外键和表
	dropLegacyConstraints()
	dropLegacyTables()
	// 删除旧的模型佣金配置表（结构变更不兼容，需要重建）
	dropOldModelCommissionTable()
	// 删除 suppliers 表的旧唯一索引（code 列）-> 新的联合索引（code + access_type）
	dropSupplierOldIndex()
	if err := autoMigrate(); err != nil {
		return fmt.Errorf("auto-migrate failed: %w", err)
	}

	// 初始化默认管理员用户和租户
	if err := seedDefaults(); err != nil {
		if logger != nil {
			logger.Warn("seed defaults failed", zap.Error(err))
		}
	}

	// 幂等写入模型标签种子数据（热卖/开源/优惠）
	if err := seedModelLabels(); err != nil {
		if logger != nil {
			logger.Warn("seed model labels failed", zap.Error(err))
		}
	}

	return nil
}

// dropSupplierOldIndex 删除 suppliers 表的旧唯一索引（code 列）
// 为新的联合唯一索引（code + access_type）让路
func dropSupplierOldIndex() {
	// 检查是否存在旧的唯一索引（仅 code 列）
	var indexExists int64
	DB.Raw(`
		SELECT COUNT(*) FROM information_schema.STATISTICS 
		WHERE TABLE_SCHEMA = DATABASE() 
		AND TABLE_NAME = 'suppliers' 
		AND INDEX_NAME = 'uidx_supplier_code'
	`).Scan(&indexExists)

	if indexExists > 0 {
		DB.Exec("ALTER TABLE `suppliers` DROP INDEX `uidx_supplier_code`")
	}

	// 也检查 GORM 默认生成的 idx_suppliers_code
	DB.Raw(`
		SELECT COUNT(*) FROM information_schema.STATISTICS 
		WHERE TABLE_SCHEMA = DATABASE() 
		AND TABLE_NAME = 'suppliers' 
		AND INDEX_NAME = 'idx_suppliers_code'
	`).Scan(&indexExists)

	if indexExists > 0 {
		DB.Exec("ALTER TABLE `suppliers` DROP INDEX `idx_suppliers_code`")
	}
}

// dropLegacyConstraints 清理旧版 Prisma 遗留的外键约束
func dropLegacyConstraints() {
	// 已知的遗留外键约束列表
	legacyConstraints := []struct {
		table      string
		constraint string
	}{
		{"users", "users_referred_by_id_fkey"},
		{"api_keys", "api_keys_user_id_fkey"},
		{"commissions", "commissions_user_id_fkey"},
		{"notifications", "notifications_user_id_fkey"},
		{"tenants", "fk_tenants_children"},
		{"transactions", "transactions_user_id_fkey"},
		{"usage_logs", "usage_logs_api_key_id_fkey"},
		{"usage_logs", "usage_logs_model_id_fkey"},
		{"usage_logs", "usage_logs_user_id_fkey"},
	}
	for _, lc := range legacyConstraints {
		DB.Exec(fmt.Sprintf("ALTER TABLE `%s` DROP FOREIGN KEY `%s`", lc.table, lc.constraint))
	}
}

// dropLegacyTables 清理旧版 Prisma 遗留的不兼容表
// 仅删除使用 varchar(191) 作为 ID 列类型的表
func dropLegacyTables() {
	legacyTables := []string{
		"commissions", "notifications", "transactions", "usage_logs",
		"system_config", "api_keys", "ai_models", "users", "tenants",
	}
	// 禁用外键检查以便任意顺序删除
	DB.Exec("SET FOREIGN_KEY_CHECKS = 0")
	for _, t := range legacyTables {
		// 仅删除使用 varchar(191) ID 的遗留表
		var colType string
		row := DB.Raw("SELECT COLUMN_TYPE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = 'id'", t).Row()
		if row != nil && row.Scan(&colType) == nil && colType == "varchar(191)" {
			DB.Exec(fmt.Sprintf("DROP TABLE IF EXISTS `%s`", t))
		}
	}
	DB.Exec("SET FOREIGN_KEY_CHECKS = 1")
}

// dropOldModelCommissionTable 删除旧的模型佣金配置表
// 由于表结构变更（从单一佣金比例改为A0-A4多等级），需要重建表
func dropOldModelCommissionTable() {
	// 检查表是否存在且使用旧的列结构（有 commission_rate 列但没有 a0_rate 列）
	var colCount int64
	DB.Raw(`
		SELECT COUNT(*) FROM information_schema.COLUMNS 
		WHERE TABLE_SCHEMA = DATABASE() 
		AND TABLE_NAME = 'model_commission_configs' 
		AND COLUMN_NAME IN ('commission_rate', 'a0_rate')
	`).Scan(&colCount)

	// 如果存在 commission_rate 列但不存在 a0_rate 列，说明是旧表结构，需要删除重建
	var hasOldColumn, hasNewColumn int64
	DB.Raw(`
		SELECT COUNT(*) FROM information_schema.COLUMNS 
		WHERE TABLE_SCHEMA = DATABASE() 
		AND TABLE_NAME = 'model_commission_configs' 
		AND COLUMN_NAME = 'commission_rate'
	`).Scan(&hasOldColumn)
	DB.Raw(`
		SELECT COUNT(*) FROM information_schema.COLUMNS 
		WHERE TABLE_SCHEMA = DATABASE() 
		AND TABLE_NAME = 'model_commission_configs' 
		AND COLUMN_NAME = 'a0_rate'
	`).Scan(&hasNewColumn)

	if hasOldColumn > 0 && hasNewColumn == 0 {
		DB.Exec("SET FOREIGN_KEY_CHECKS = 0")
		DB.Exec("DROP TABLE IF EXISTS `model_commission_configs`")
		DB.Exec("SET FOREIGN_KEY_CHECKS = 1")
	}
}

// autoMigrate 对所有已注册模型执行 GORM 自动迁移
func autoMigrate() error {
	return DB.AutoMigrate(
		&model.SystemConfig{},
		&model.Tenant{},
		&model.User{},
		&model.ApiKey{},
		&model.Supplier{},
		&model.ModelCategory{},
		&model.AIModel{},
		&model.ChannelTag{},
		&model.Channel{},
		&model.ChannelGroup{},
		&model.BackupRule{},
		&model.ModelPricing{},
		&model.AgentLevelDiscount{},
		&model.AgentPricing{},
		&model.ChannelLog{},
		&model.DailyStats{},
		&model.Orchestration{},
		&model.DocCategory{},
		&model.Doc{},
		&model.DocArticle{},
		&model.Payment{},
		&model.AuditLog{},
		&model.UserBalance{},
		&model.QuotaConfig{},
		&model.BalanceRecord{},
		&model.ReferralConfig{},
		&model.ReferralLink{},
		&model.CommissionRecord{},
		&model.PaymentProviderConfig{},
		&model.BankAccount{},
		&model.PaymentMethod{},
		&model.RateLimitConfig{},
		&model.UserQuotaConfig{},
		&model.FreezeRecord{},
		&model.MemberLevel{},
		&model.UserMemberProfile{},
		&model.WithdrawalRequest{},
		&model.ExchangeRate{},
		&model.CustomChannel{},       // 自定义渠道
		&model.CustomChannelRoute{},  // 自定义渠道路由规则
		&model.CustomChannelAccess{}, // 自定义渠道访问控制
		&model.ChannelModel{},        // 渠道-模型映射（标准模型名 <-> 供应商模型ID）
		&model.PriceSyncLog{},        // 价格同步历史日志
		&model.ModelCheckLog{},       // 模型可用性检测记录
		&model.ModelCheckTask{},      // 模型检测后台任务
		&model.CapabilityTestCase{},    // 能力测试用例模板（可复用）
		&model.CapabilityTestTask{},    // 能力测试批量运行任务
		&model.CapabilityTestResult{},  // 能力测试单条结果（model×case）
		&model.CapabilityTestBaseline{}, // 能力测试回归基线
		&model.BackgroundTask{},     // 后台异步任务
		&model.ApiCallLog{},         // API 调用全链路日志
		&model.PlatformParam{},      // 平台标准参数定义
		&model.SupplierParamMapping{}, // 供应商参数映射

		// --- v3.1 新增模型:邀请返佣 / 特殊加佣 / 风控 / 配置审计 ---
		&model.ReferralAttribution{},    // 邀请归因快照
		&model.UserCommissionOverride{}, // 特殊用户加佣配置
		&model.RegistrationGuard{},      // 注册风控配置
		&model.RegistrationEvent{},      // 注册行为审计日志
		&model.EmailOTPToken{},          // 邮箱 OTP 验证码
		&model.ConfigAuditLog{},         // 配置变更审计日志
		&model.DisposableEmailDomain{},  // 一次性邮箱域名黑名单

		&model.PartnerApplication{}, // 合作伙伴线索申请

		// --- 站内公告/通知系统 ---
		&model.Announcement{},         // 管理员发布的站内公告
		&model.UserAnnouncementRead{}, // 用户公告已读记录

		// --- 模型 k:v 标签系统 ---
		&model.ModelLabel{},       // 模型标签（热卖/开源/优惠等，支持自定义 k:v）
		&model.LabelDictionary{},  // v3.5 标签字典（多语言 + 颜色 + 图标 + 排序权重）

		// --- v3.2 支付/订单/财务系统重构 ---
		&model.ExchangeRateHistory{},    // 汇率历史快照（审计 + 降级 fallback）
		&model.PaymentProviderAccount{}, // 多账号支付配置（Stripe/PayPal 权重路由）
		&model.PaymentRefundRequest{},   // 用户退款申请
		&model.PaymentEventLog{},        // 支付/退款/提现/汇率全链路事件日志

		&model.TrendingModel{}, // 全球热门模型参考库

		// --- 用户调用日表聚合（性能优化：api_call_logs 7天清理前持久化用户维度数据）---
		&model.UserDailyStat{}, // 按用户×模型×日期聚合的调用统计

		// --- AI 客服 + 工单系统（9 张表）---
		&model.SupportSession{},       // AI 客服会话
		&model.SupportMessage{},       // 会话消息
		&model.KnowledgeChunk{},       // RAG 知识切片（统一表，source_type 区分）
		&model.ProviderDocReference{}, // 供应商官方文档 URL 引用
		&model.AcceptedAnswer{},       // 用户采纳的答案（管理员审核后入知识库）
		&model.HotQuestion{},          // 管理员编辑的热门问题（带标准答案）
		&model.UserSupportMemory{},    // 用户个人长期记忆
		&model.SupportModelProfile{},  // 客服模型候选配置（多模型 Fallback）
		&model.SupportTicket{},        // 用户工单
		&model.SupportTicketReply{},   // 工单回复

		// --- RBAC 权限系统（v4.0）---
		&model.Permission{},     // 权限目录（从 audit.routeMap 种子化）
		&model.Role{},           // 角色定义（内置 + 自定义）
		&model.RolePermission{}, // 角色-权限关联
		&model.UserRole{},       // 用户-角色关联
	)
}

// seedDefaults 创建默认管理员租户和用户（仅在不存在时创建）
func seedDefaults() error {
	const (
		adminEmail = "admin@tokenhubhk.com"
		adminPass  = "admin123456"
		adminName  = "Admin"
		tenantName = "Platform"
	)

	// 检查管理员用户是否已存在
	var count int64
	if err := DB.Model(&model.User{}).Where("email = ?", adminEmail).Count(&count).Error; err != nil {
		return fmt.Errorf("check admin user: %w", err)
	}
	if count > 0 {
		return nil // already seeded
	}

	// 确保默认租户存在
	var tenant model.Tenant
	err := DB.Where("parent_id IS NULL AND level = 1").First(&tenant).Error
	if err != nil {
		if err.Error() != "record not found" {
			return fmt.Errorf("find default tenant: %w", err)
		}
		tenant = model.Tenant{
			Name:         tenantName,
			Domain:       "platform",
			Level:        1,
			IsActive:     true,
			ContactEmail: adminEmail,
		}
		if err := DB.Create(&tenant).Error; err != nil {
			return fmt.Errorf("create default tenant: %w", err)
		}
	}

	// 创建管理员用户
	hash, err := bcrypt.GenerateFromPassword([]byte(adminPass), 12)
	if err != nil {
		return fmt.Errorf("hash admin password: %w", err)
	}

	adminUser := model.User{
		TenantID:     tenant.ID,
		Email:        adminEmail,
		PasswordHash: string(hash),
		Name:         adminName,
		Role:         "ADMIN",
		IsActive:     true,
		Language:     "en",
	}
	if err := DB.Create(&adminUser).Error; err != nil {
		return fmt.Errorf("create admin user: %w", err)
	}

	// 创建默认自定义渠道（如果不存在 is_default=true 的记录）
	var defaultChannelCount int64
	DB.Model(&model.CustomChannel{}).Where("is_default = ?", true).Count(&defaultChannelCount)
	if defaultChannelCount == 0 {
		defaultChannel := model.CustomChannel{
			Name:       "默认渠道",
			Strategy:   "cost_first",
			IsDefault:  true,
			AutoRoute:  true,
			Visibility: "all",
			IsActive:   true,
		}
		if err := DB.Create(&defaultChannel).Error; err != nil {
			return fmt.Errorf("create default custom channel: %w", err)
		}
	}

	return nil
}

// Close 关闭数据库连接
func Close() error {
	if DB != nil {
		sqlDB, err := DB.DB()
		if err != nil {
			return err
		}
		return sqlDB.Close()
	}
	return nil
}

// seedModelLabels 幂等写入模型标签种子数据（value-only 简化版）
// 所有标签统一使用 label_key="tag"，前端只展示 value
// Phase 0 迁移所有历史变体（英文 k:v、中文 k:v）→ 统一 tag:热卖/tag:优惠/tag:开源
// 每次启动安全重复执行（FirstOrCreate）
func seedModelLabels() error {
	// ── Phase 0：迁移历史变体 ────────────────────────────────────────────────
	// 映射：(old_key, old_value) → new_value（统一 key="tag"）
	type migrateRule struct {
		oldKey   string
		oldValue string
		newValue string
	}
	migrations := []migrateRule{
		// 英文 k:v（最早版本）
		{"tag", "hot", "热卖"},
		{"tag", "discount", "优惠"},
		{"license", "open-source", "开源"},
		// 中文 k:v（中间版本）
		{"受欢迎程度", "热卖", "热卖"},
		{"价格", "优惠", "优惠"},
		{"是否开源", "开源", "开源"},
	}

	for _, m := range migrations {
		// 先找出所有匹配旧 key/value 的 model_id 列表
		var modelIDs []uint
		DB.Model(&model.ModelLabel{}).Unscoped().
			Where("label_key = ? AND label_value = ?", m.oldKey, m.oldValue).
			Distinct("model_id").
			Pluck("model_id", &modelIDs)

		if len(modelIDs) == 0 {
			continue
		}

		// 硬删除旧记录（含软删除），避免唯一索引冲突
		DB.Unscoped().
			Where("label_key = ? AND label_value = ?", m.oldKey, m.oldValue).
			Delete(&model.ModelLabel{})

		// 写入新的统一格式 tag:<value>
		for _, mid := range modelIDs {
			label := model.ModelLabel{
				ModelID:    mid,
				LabelKey:   "tag",
				LabelValue: m.newValue,
			}
			DB.FirstOrCreate(&label, model.ModelLabel{
				ModelID:    mid,
				LabelKey:   "tag",
				LabelValue: m.newValue,
			})
		}
	}

	// ── Phase 1：种子规则（统一 key="tag"） ────────────────────────────────
	type rule struct {
		patterns []string
		value    string
	}
	rules := []rule{
		// 热卖 — DeepSeek V3/R1、Qwen3系列、Moonshot/Kimi、MiniMax
		{[]string{"deepseek-v3", "deepseek-r1", "qwen3", "moonshot", "kimi", "minimax"}, "热卖"},
		// 开源 — DeepSeek 全系、Qwen 全系、Yi、Baichuan、GLM
		{[]string{"deepseek-", "qwen", "yi-", "baichuan", "glm-"}, "开源"},
		// 优惠 — DeepSeek V3/R1-Distill、Qwen Turbo/Plus
		{[]string{"deepseek-v3", "deepseek-r1-distill", "qwen-turbo", "qwen-plus"}, "优惠"},
	}

	var models []model.AIModel
	if err := DB.Select("id, model_name").Find(&models).Error; err != nil {
		return err
	}

	for _, m := range models {
		name := strings.ToLower(m.ModelName)
		for _, r := range rules {
			for _, p := range r.patterns {
				if strings.Contains(name, strings.ToLower(p)) {
					label := model.ModelLabel{
						ModelID:    m.ID,
						LabelKey:   "tag",
						LabelValue: r.value,
					}
					// FirstOrCreate 保证幂等：已存在则跳过
					DB.FirstOrCreate(&label, model.ModelLabel{
						ModelID:    m.ID,
						LabelKey:   "tag",
						LabelValue: r.value,
					})
					break // 同一规则每个模型只写一次
				}
			}
		}
	}
	return nil
}
