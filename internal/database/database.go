package database

import (
	"context"
	"fmt"
	"net"
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

// logInfo / logWarn / logError 小工具：logger 为 nil 时也不 panic
func logInfo(logger *zap.Logger, msg string, fields ...zap.Field) {
	if logger != nil {
		logger.Info(msg, fields...)
	}
}
func logError(logger *zap.Logger, msg string, fields ...zap.Field) {
	if logger != nil {
		logger.Error(msg, fields...)
	}
}

// CurrentSchemaVersion 当前代码期望的数据库 schema 版本。
// 每次有破坏性 schema 变更（新增/修改表结构、字段类型变更）时递增。
// 管理员可通过 POST /api/v1/admin/system/migrate 触发升级脚本。
const CurrentSchemaVersion = "v5.1.0"

// schemaVersionKey system_configs 表中存储版本号的 key
const schemaVersionKey = "schema_version"

// Init 初始化 GORM MySQL 连接 —— **仅建立连接，不做任何 schema 变更 / 种子写入**。
//
// 启动时只做必要的事情：TCP 预检 → GORM 连接 → Ping → 连接池配置 → schema_version 只读校验。
//
// 所有 schema 迁移 / 种子数据的写入均通过以下渠道触发：
//   - 首次部署：安装向导 POST /api/v1/setup/import-seed 调用 RunSchemaInit + RunAllSeeds
//   - 版本升级：管理员接口 POST /api/v1/admin/system/migrate
//   - CLI 工具（未来扩展）
//
// 参数:
//   - cfg: 数据库配置
//   - logger: Zap 日志实例
func Init(cfg config.DatabaseConfig, logger *zap.Logger) error {
	dsn := cfg.DSN()
	if dsn == "" {
		return fmt.Errorf("database DSN is empty")
	}

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	logInfo(logger, "database: starting init",
		zap.String("host", cfg.Host),
		zap.Int("port", cfg.Port),
		zap.String("user", cfg.User),
		zap.String("dbname", cfg.DBName),
	)

	// Step 1: TCP 预检（5s 超时）
	logInfo(logger, "database: tcp preflight", zap.String("addr", addr))
	tcpStart := time.Now()
	conn, tcpErr := net.DialTimeout("tcp", addr, 5*time.Second)
	if tcpErr != nil {
		logError(logger, "database: tcp preflight FAILED",
			zap.String("addr", addr),
			zap.Error(tcpErr),
			zap.String("hint", "检查 RDS 白名单是否放通 Pod IP 段（如 172.16.0.0/12），以及实例状态"),
		)
		return fmt.Errorf("database tcp preflight failed (%s): %w", addr, tcpErr)
	}
	_ = conn.Close()
	logInfo(logger, "database: tcp preflight OK",
		zap.String("addr", addr),
		zap.Duration("cost", time.Since(tcpStart)),
	)

	logLevel := gormlogger.Warn
	if cfg.Host == "" {
		logLevel = gormlogger.Silent
	}

	// Step 2: gorm.Open（内部按需建连）
	logInfo(logger, "database: gorm.Open")
	var err error
	DB, err = gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger:                                   gormlogger.Default.LogMode(logLevel),
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		logError(logger, "database: gorm.Open FAILED", zap.Error(err))
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	logInfo(logger, "database: gorm.Open OK")

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
		logError(logger, "database: get sql.DB FAILED", zap.Error(err))
		return fmt.Errorf("failed to get underlying sql.DB: %w", err)
	}

	// Step 3: Ping 验证凭证 + 实际建连（5s 超时）
	logInfo(logger, "database: ping (auth check)")
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pingCancel()
	if pingErr := sqlDB.PingContext(pingCtx); pingErr != nil {
		logError(logger, "database: ping FAILED",
			zap.Error(pingErr),
			zap.String("hint", "若 TCP 通但 ping 失败：多半是账号密码错、账号没授权到 DB、或 RDS 实例未 Ready"),
		)
		return fmt.Errorf("database ping failed: %w", pingErr)
	}
	logInfo(logger, "database: ping OK")

	// 立即设置当前连接的字符集
	DB.Exec("SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci")

	// 连接池配置
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(time.Duration(cfg.ConnMaxLifetime) * time.Second)
	// 空闲连接回收（2026-04-22 加固）：
	//   防止 idle 连接被 RDS 侧主动关闭（默认 wait_timeout=28800s）后，
	//   下次 handler 拿到"坏连接"导致 `invalid connection` / `bad connection` → 502。
	//   5 分钟 < RDS wait_timeout，安全回收。
	connMaxIdle := cfg.ConnMaxIdleTime
	if connMaxIdle <= 0 {
		connMaxIdle = 300 // 默认 5 分钟
	}
	sqlDB.SetConnMaxIdleTime(time.Duration(connMaxIdle) * time.Second)
	logInfo(logger, "database: connected",
		zap.Int("max_open", cfg.MaxOpenConns),
		zap.Int("max_idle", cfg.MaxIdleConns),
		zap.Int("conn_max_idle_time_sec", connMaxIdle),
	)

	// 只读 schema_version 校验（不阻塞启动，仅在不匹配时告警）
	// 表不存在 → 视为首次部署，提示走安装向导
	// 表存在但版本落后 → 提示管理员触发升级接口
	checkSchemaVersion(logger)
	if err := ensureBillingCostUnitColumns(); err != nil {
		return err
	}

	return nil
}

func ensureBillingCostUnitColumns() error {
	if DB == nil {
		return nil
	}
	if err := ensureModelColumns(&model.ApiCallLog{}, []string{
		"CostUnits",
		"EstimatedCostUnits",
		"FrozenUnits",
		"ActualCostUnits",
		"PlatformCostUnits",
		"UnderCollectedUnits",
	}); err != nil {
		return err
	}
	return ensureModelColumns(&model.BillingReconciliationSnapshot{}, []string{
		"ActualRevenueUnits",
		"EstimatedCostUnits",
		"EstimateVarianceCredits",
		"EstimateVarianceUnits",
		"FrozenUnits",
		"UnderCollectedUnits",
		"PlatformCostUnits",
		"ExpiredFreezeUnits",
		"OpenFrozenUnits",
	})
}

func ensureModelColumns(dst interface{}, fields []string) error {
	if !DB.Migrator().HasTable(dst) {
		return nil
	}
	for _, field := range fields {
		if DB.Migrator().HasColumn(dst, field) {
			continue
		}
		if err := DB.Migrator().AddColumn(dst, field); err != nil {
			return fmt.Errorf("add billing column %s failed: %w", field, err)
		}
	}
	return nil
}

// checkSchemaVersion 只读检查 system_configs.schema_version 是否与当前代码版本匹配。
// 失败不阻塞启动（已有部署可能根本没建表）；只记 WARN 日志指引运维。
func checkSchemaVersion(logger *zap.Logger) {
	// 防御：表不存在时直接返回（全新库，等待安装向导执行 RunSchemaInit）
	if !DB.Migrator().HasTable(&model.SystemConfig{}) {
		logInfo(logger, "database: system_configs table missing, awaiting setup wizard",
			zap.String("expected_version", CurrentSchemaVersion))
		return
	}
	var cfg model.SystemConfig
	err := DB.Where("`key` = ?", schemaVersionKey).First(&cfg).Error
	if err != nil {
		logInfo(logger, "database: schema_version not set (first deployment?)",
			zap.String("expected_version", CurrentSchemaVersion),
			zap.String("hint", "首次部署请走 /api/v1/setup/import-seed；升级部署请调用 /api/v1/admin/system/migrate"),
		)
		return
	}
	if cfg.Value != CurrentSchemaVersion {
		if logger != nil {
			logger.Warn("database: schema version mismatch",
				zap.String("db_version", cfg.Value),
				zap.String("expected", CurrentSchemaVersion),
				zap.String("hint", "管理员需调用 POST /api/v1/admin/system/migrate 触发 schema 升级"),
			)
		}
		return
	}
	logInfo(logger, "database: schema version OK", zap.String("version", cfg.Value))
}

// RunSchemaInit 执行完整的 schema 初始化（清理遗留 + AutoMigrate 全部表）。
//
// 调用场景（均非启动路径）：
//   - 安装向导：POST /api/v1/setup/import-seed
//   - 管理员升级：POST /api/v1/admin/system/migrate
//   - 本地 fresh-install 脚本
//
// 幂等保证：dropLegacy* 只删旧结构，AutoMigrate 只补字段不删字段。
func RunSchemaInit(db *gorm.DB) error {
	prevDB := DB
	DB = db
	defer func() { DB = prevDB }()

	// 1. 清理旧版 Prisma 遗留的外键和表
	dropLegacyConstraints()
	dropLegacyTables()
	// 2. 删除旧的模型佣金配置表（结构变更不兼容，需要重建）
	dropOldModelCommissionTable()
	// 3. 删除 suppliers 表的旧唯一索引（code 列）-> 新的联合索引（code + access_type）
	dropSupplierOldIndex()
	// 4. AutoMigrate 全部表
	if err := autoMigrate(); err != nil {
		return fmt.Errorf("auto-migrate failed: %w", err)
	}
	return nil
}

// MarkSchemaVersion 将当前代码的 schema 版本号写入 system_configs（upsert）。
// 通常由 RunAllSeeds / 管理员迁移接口在完成 schema + seed 后调用。
func MarkSchemaVersion(db *gorm.DB) error {
	var existing model.SystemConfig
	err := db.Where("`key` = ?", schemaVersionKey).First(&existing).Error
	if err == gorm.ErrRecordNotFound {
		return db.Create(&model.SystemConfig{
			Key:   schemaVersionKey,
			Value: CurrentSchemaVersion,
		}).Error
	}
	if err != nil {
		return err
	}
	if existing.Value == CurrentSchemaVersion {
		return nil
	}
	existing.Value = CurrentSchemaVersion
	return db.Save(&existing).Error
}

// SeedDefaults 对外暴露的管理员/平台租户种子函数。
// 仅在 RunAllSeeds / 安装向导中调用，不再由 Init 自动触发。
func SeedDefaults(db *gorm.DB) error {
	prevDB := DB
	DB = db
	defer func() { DB = prevDB }()
	return seedDefaults()
}

// SeedModelLabels 对外暴露的模型标签种子函数。
// 仅在 RunAllSeeds / 安装向导中调用，不再由 Init 自动触发。
func SeedModelLabels(db *gorm.DB) error {
	prevDB := DB
	DB = db
	defer func() { DB = prevDB }()
	return seedModelLabels()
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
		&model.UserModelDiscount{},
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
		&model.CustomChannel{},                 // 自定义渠道
		&model.CustomChannelRoute{},            // 自定义渠道路由规则
		&model.CustomChannelAccess{},           // 自定义渠道访问控制
		&model.ChannelModel{},                  // 渠道-模型映射（标准模型名 <-> 供应商模型ID）
		&model.PriceSyncLog{},                  // 价格同步历史日志
		&model.ModelCheckLog{},                 // 模型可用性检测记录
		&model.ModelCheckTask{},                // 模型检测后台任务
		&model.CapabilityTestCase{},            // 能力测试用例模板（可复用）
		&model.CapabilityTestTask{},            // 能力测试批量运行任务
		&model.CapabilityTestResult{},          // 能力测试单条结果（model×case）
		&model.CapabilityTestBaseline{},        // 能力测试回归基线
		&model.BackgroundTask{},                // 后台异步任务
		&model.ApiCallLog{},                    // API 调用全链路日志
		&model.BillingReconciliationSnapshot{}, // 每日扣费对账快照
		&model.PlatformParam{},                 // 平台标准参数定义
		&model.SupplierParamMapping{},          // 供应商参数映射

		// --- v3.1 新增模型:邀请返佣 / 特殊加佣 / 风控 / 配置审计 ---
		&model.ReferralAttribution{},    // 邀请归因快照
		&model.UserCommissionOverride{}, // 特殊用户加佣配置
		&model.CommissionRule{},         // 特殊返佣规则（用户×模型）
		&model.CommissionRuleUser{},     // 规则-用户关联
		&model.CommissionRuleModel{},    // 规则-模型关联
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
		&model.ModelLabel{},      // 模型标签（热卖/开源/优惠等，支持自定义 k:v）
		&model.LabelDictionary{}, // v3.5 标签字典（多语言 + 颜色 + 图标 + 排序权重）

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

		// --- 限流 429 事件审计 ---
		&model.RateLimitEvent{},

		// --- 用户认证行为日志（register/login/logout/refresh）---
		&model.UserAuthLog{},

		// --- 发票系统（v4.2 新增）---
		&model.InvoiceRequest{},
		&model.InvoiceTitle{},

		// --- 邮件管理（v4.3 新增）---
		&model.EmailProviderConfig{},
		&model.EmailTemplate{},
		&model.EmailSendLog{},
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
	var existing model.User
	if err := DB.Where("email = ?", adminEmail).First(&existing).Error; err == nil {
		return ensureDefaultAdminPasswordHash(DB, &existing, adminPass)
	} else if err.Error() != "record not found" {
		return fmt.Errorf("check admin user: %w", err)
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
	hash, err := bcrypt.GenerateFromPassword([]byte(clientPasswordHash(adminEmail, adminPass)), 12)
	if err != nil {
		return fmt.Errorf("hash admin password: %w", err)
	}

	adminUser := model.User{
		TenantID:     tenant.ID,
		Email:        adminEmail,
		PasswordHash: string(hash),
		Name:         adminName,
		IsActive:     true,
		Language:     "en",
	}
	if err := DB.Create(&adminUser).Error; err != nil {
		return fmt.Errorf("create admin user: %w", err)
	}
	// v4.0: 用户角色由 permission.Seed() 稍后回填（此时 roles 表可能尚未种子化，
	// 无法在此直接插入 user_roles；permission.Seed 的 backfillUserRoles 会兜底）

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
