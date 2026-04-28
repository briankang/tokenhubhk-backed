package database

import (
	"strings"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/config"
	"tokenhub-server/internal/pkg/logger"
	permissionsvc "tokenhub-server/internal/service/permission"
)

// RunAllSeeds 运行全部种子数据（供安装向导和管理员手动触发）。
//
// 设计原则：
//   - 包含所有"写入新行"的业务种子，每个函数内部做幂等检查，重复调用安全
//   - 不做 schema 变更（那是 RunSchemaInit 的职责）
//   - 最后写入 schema_version 标记本次部署的版本号
//
// 调用场景（**均非启动路径**）：
//   - 安装向导 POST /setup/import-seed
//   - 管理员升级 POST /admin/system/migrate
//   - 本地开发 fresh-install 脚本
//
// 与 RunSchemaInit 的配合：
//   - 安装向导 / 管理员迁移接口应按顺序调用：RunSchemaInit → RunAllSeeds
//   - RunAllSeeds 自己不做 AutoMigrate，调用方必须先保证表已存在
func RunAllSeeds(db *gorm.DB) {
	logger.L.Info("running all seed data...", zap.String("version", CurrentSchemaVersion))

	// 0. 默认管理员 + 平台租户 + 默认自定义渠道（原 database.Init 里的 seedDefaults）
	if err := SeedDefaults(db); err != nil {
		logger.L.Warn("seed defaults failed", zap.Error(err))
	}

	// 1. 基础供应商 / 分类 / 渠道 / 默认管理员
	RunSeed(db)

	// 2. 支付网关空配置（独立于供应商检查）
	RunSeedPaymentConfig(db)

	// 3. 文档中心（分类 + 文章）
	RunSeedDocs(db)
	RunDocArticleLocaleMigration(db)
	RunCleanPlaceholderDocCategories(db)
	RunSeedCustomParamDocs(db)

	// 4. 会员等级 + 汇率
	RunSeedLevels(db)

	// 5. 平台标准参数 + 供应商参数映射
	RunSeedParams()

	// 6. 标签字典（多语言 + 颜色 + 图标）
	RunSeedLabelDictionary(db)

	// 7. 增量供应商：千帆 / 混元 / TalkingData / 网宿 AI 网关
	RunSeedQianfan(db)
	RunSeedHunyuan(db)
	RunSeedTalkingData(db)
	RunSeedWangsu(db)
	RunSeedWangsuImageGateway(db)
	RunSeedWangsuVideo(db)
	RunSeedNonTokenModels(db)
	if err := RunPruneUnusedSuppliersMigration(db); err != nil {
		logger.L.Warn("prune unused suppliers failed", zap.Error(err))
	}

	// 8. 能力测试用例（43 条，upsert）
	RunSeedCapabilityCases(db)

	// 9. AI 客服系统（模型配置 + 供应商文档 URL + 热门问题）
	RunSeedSupport(db)
	RunSupportFullTextMigration(db)

	// 10. PolarDB 向量检索迁移（仅在 SUPPORT_VECTOR_STORE=polardb 时执行）
	if strings.EqualFold(config.Global.Support.WithDefaults().VectorStore, "polardb") {
		RunPolarDBVectorMigration(db)
	}

	// 11. 汇率 API 配置（SystemConfig 表）
	RunSeedExchangeRateConfig(db)

	// 12. 全球热门模型参考库
	if err := RunSeedTrendingModels(); err != nil {
		logger.L.Warn("seed trending models failed", zap.Error(err))
	}
	if err := RunExpandTrendingModels(db); err != nil {
		logger.L.Warn("expand trending models migration failed", zap.Error(err))
	}
	if err := RunHotModelVisibilityMigration(db); err != nil {
		logger.L.Warn("hot model visibility migration failed", zap.Error(err))
	}
	RunSeedModelAliases(db)

	// 13. 模型 k:v 标签（热卖/开源/优惠，原 database.Init 里的 seedModelLabels）
	if err := SeedModelLabels(db); err != nil {
		logger.L.Warn("seed model labels failed", zap.Error(err))
	}

	// 13.5. 邮件系统预设模板（6 条：注册验证 / 欢迎 / 密码重置 / 发票 / 提现通过/拒绝）
	if err := RunSeedEmailTemplates(db); err != nil {
		logger.L.Warn("seed email templates failed", zap.Error(err))
	}
	// 13.6. 邮件多语言变体（register_verify/password_reset × en/zh/zh_TW）
	if err := RunSeedEmailTemplatesI18n(db); err != nil {
		logger.L.Warn("seed email i18n templates failed", zap.Error(err))
	}

	// 13.7. Google/GitHub OAuth login defaults; disabled until an admin adds Client ID/Secret.
	RunSeedOAuthProviders(db)

	// 14. RBAC 权限系统（permissions / roles / role_permissions / user_roles）
	// 放在最后：依赖前面的用户种子（admin 账号）已存在
	if err := permissionsvc.Seed(db); err != nil {
		logger.L.Warn("permission seed failed", zap.Error(err))
	}

	// 15. 删除废弃的 users.role 字段（幂等，列不存在则静默跳过）
	// 注意：必须在 permission.Seed() 之后执行，确保所有用户都已迁移到 user_roles
	if err := RunDropUserRoleColumn(db); err != nil {
		logger.L.Warn("drop users.role column failed", zap.Error(err))
	}

	// 16. 修复 Qwen/QwQ 等模型的能力标记（v5.1 补丁）
	RunFixQwenFeaturesMigration(db)
	if err := RunModelCapabilityDefaultsMigration(db); err != nil {
		logger.L.Warn("model capability defaults migration failed", zap.Error(err))
	}
	if err := RunAliyunDeprecationMigration(db); err != nil {
		logger.L.Warn("aliyun deprecation migration failed", zap.Error(err))
	}
	RunSeedModelAPIDocs(db)

	// 17. 标记 schema_version 为当前代码版本（供启动时只读校验）
	if err := MarkSchemaVersion(db); err != nil {
		logger.L.Warn("mark schema version failed", zap.Error(err))
	}

	logger.L.Info("all seed data done", zap.String("version", CurrentSchemaVersion))
}
