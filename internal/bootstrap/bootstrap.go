// Package bootstrap 提供各服务角色共享的初始化逻辑。
// gateway / backend / worker / monolith 四种角色均通过此包完成基础设施初始化，
// 避免在各 cmd/ 入口中重复初始化代码。
package bootstrap

import (
	"context"
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"

	"tokenhub-server/internal/config"
	"tokenhub-server/internal/database"
	pkgi18n "tokenhub-server/internal/pkg/i18n"
	"tokenhub-server/internal/pkg/logger"
	pkgredis "tokenhub-server/internal/pkg/redis"
)

// Result 保存初始化后的基础设施引用，供调用方获取清理函数
type Result struct {
	// Cleanup 在 graceful shutdown 时调用，依次关闭 DB → Redis → Logger
	Cleanup func()
}

// InitConfig 加载配置文件，支持 CONFIG_FILE 环境变量覆盖路径
func InitConfig() error {
	cfgFile := "configs/config.yaml"
	if envCfg := os.Getenv("CONFIG_FILE"); envCfg != "" {
		cfgFile = envCfg
	}
	return config.Load(cfgFile)
}

// InitLogger 初始化 Zap 结构化日志
func InitLogger() error {
	return logger.Init(logger.Config{
		Level:      config.Global.Log.Level,
		Dir:        config.Global.Log.Dir,
		MaxSize:    config.Global.Log.MaxSize,
		MaxAge:     config.Global.Log.MaxAge,
		MaxBackups: config.Global.Log.MaxBackups,
	})
}

// InitDatabase 初始化 MySQL 连接 —— **只建连，不做任何 schema 变更 / 种子写入**。
//
// 参数 runMigrations 已废弃（保留签名仅为向后兼容）：
//   - AutoMigrate / dropLegacy / 种子写入 均已从启动路径移除
//   - 首次部署：走安装向导 POST /api/v1/setup/import-seed
//   - 版本升级：走管理员接口 POST /api/v1/admin/system/migrate
//   - 启动时仅做只读的 schema_version 校验（在 database.Init 内部），失败不阻塞
func InitDatabase(runMigrations bool) error {
	_ = runMigrations // 已废弃但保留签名
	if err := database.Init(config.Global.Database, logger.L); err != nil {
		return fmt.Errorf("database init: %w", err)
	}
	return nil
}

// RunDataMigrations 执行所有幂等的结构/数据迁移。
//
// **本函数不再在启动时调用**，仅由以下渠道触发：
//   - 管理员升级接口 POST /api/v1/admin/system/migrate
//   - 安装向导（可选）
//   - CLI 工具（未来扩展）
//
// 内容说明：只包含"修复/迁移已有数据"的操作，不写入任何新的业务种子数据：
//   - 字段回填：缓存定价、TPM 提升、extra_params 清洗、requires_stream、pricing_url、缓存类型
//   - 数据迁移：tags → labels、渠道能力回填
//   - 表结构：删除代理遗留表、FULLTEXT 索引、PolarDB 向量索引
//   - 下线标记：volcengine batch8 deprecation
//
// 不包含的内容（在 RunAllSeeds 中）：
//
//	业务种子（suppliers/levels/params/roles/permissions 等任何写新行的操作）
func RunDataMigrations() {
	// Preserve source USD prices before RMB conversion.
	database.RunPriceSourceColumnsMigration(database.DB)
	// 缓存定价回填
	database.RunCachePriceMigration(database.DB)
	database.RunCachePriceCompletenessMigration(database.DB)
	// 2026-04-28: 全量重算 api_call_logs.platform_cost_rmb,应用供应商折扣
	// 修复历史 BUG: 旧代码用售价当成本算 platform_cost,导致毛利永远 0
	if err := database.RunRecomputePlatformCostMigration(database.DB); err != nil {
		// 不阻塞启动,UI 仍能用 recomputed_platform_cost_rmb 兜底显示
		_ = err
	}

	// PriceMatrix v3:把 PriceTiers + 顶层售价数据迁移为 PriceMatrix JSON,
	// 幂等(已有 price_matrix 数据的记录跳过),失败行单独日志
	if err := database.RunPriceMatrixMigration(database.DB); err != nil {
		// 不阻塞启动,旧字段路径仍可工作
		_ = err
	}

	// 一次性：提升 V0-V4 默认 TPM（仅未被管理员修改的行）
	database.RunBumpMemberLevelTPM(database.DB)

	// 补齐二维阶梯价格
	database.RunPriceTiers2DMigration(database.DB)

	// 清理 extra_params 字段中 JSON_TYPE='STRING'/'ARRAY' 的脏数据
	database.RunExtraParamsCleanupMigration(database.DB)

	// 清理 extra_params 中 {key: bool} 形式的能力标记脏数据
	database.RunExtraParamsFeatureFlagsCleanup(database.DB)

	// 为阿里云 qwq/qvq 系列回填 features.requires_stream=true
	database.RunQwqRequiresStreamMigration(database.DB)

	// 将 price_tiers[0] 同步到 ai_models 顶层字段（input_cost_rmb / output_cost_rmb /
	// output_cost_thinking_rmb），修复爬虫路径漂移和批量售价被跳过的问题
	database.RunTierOneSyncMigration(database.DB)

	// 补齐 Doubao Seed 2.0 系列的阶梯价、阶梯缓存价和平台售价阶梯
	database.RunTalkingDataSeed20PricingMigration(database.DB)

	// 将所有成本阶梯同步到平台售价阶梯，并补齐阶梯缓存价。
	database.RunPriceTierSellingSyncMigration(database.DB)

	// F4 (2026-04-28): 思考模式价格锁定迁移
	// 对 features.supports_thinking=true 的活跃模型显式锁定 thinking 价（默认与 output 同价，
	// 已知差价模型走 thinkingPriceOverrides 表）。修复 22+ 思考模型当前 output_cost_thinking_rmb=0
	// 导致即使 thinkingMode=true 也按非思考价计费的静默漏扣风险。
	database.RunThinkingPriceLockMigration(database.DB)

	// S4 + F1 + F2(A) (2026-04-28): Seedance 数据迁移到 DimValues 形态
	// 把 magic-InputMin 编码维度的旧 PriceTiers 重写为显式 DimValues 阶梯
	// （resolution / input_has_video / inference_mode / audio_mode），
	// 修复 Seedance 1.5-pro/2.0/1.0 系列因 SelectTierOrLargest fallback 到最便宜档
	// 导致的 33-75% 漏扣 bug。配合 S2 selectPriceForTokens 的三步匹配生效。
	database.RunSeedanceDimValuesMigration(database.DB)

	// M2 (2026-04-28): 为 AIModel 填充 DimensionConfig（业务维度声明）
	// 必须在 S4 之后，从已迁移的 PriceTiers.DimValues 反推维度结构。
	// 让前端可按 schema 自动渲染矩阵编辑器，计费链路按 config 校验请求 dims。
	database.RunDimensionConfigMigration(database.DB)

	// 为存量供应商回填官网定价 URL
	database.RunSupplierPricingURLMigration(database.DB)

	// 修正非 LLM/VLM 模型错误启用缓存的问题
	database.RunCacheTypeCleanup(database.DB)

	// tags 字符串 → model_labels 表迁移
	database.RunMigrateTagsToLabels(database.DB)

	// 回填渠道 supported_capabilities 字段
	if err := database.MigrateChannelCapabilities(database.DB); err != nil {
		logger.L.Warn("channel capabilities migration failed", zap.Error(err))
	}

	// v3.1 物理删除代理机制遗留表
	if err := database.DropAgentTables(database.DB); err != nil {
		logger.L.Warn("drop agent tables migration failed", zap.Error(err))
	}

	// knowledge_chunks FULLTEXT 索引（ngram 分词）
	database.RunSupportFullTextMigration(database.DB)

	// PolarDB 向量检索迁移（仅在 SUPPORT_VECTOR_STORE=polardb 时执行）
	if strings.EqualFold(config.Global.Support.WithDefaults().VectorStore, "polardb") {
		database.RunPolarDBVectorMigration(database.DB)
	}

	// 火山引擎第八批下线模型标记
	if err := database.MigrateVolcengineBatch8Deprecation(database.DB); err != nil {
		logger.L.Warn("volcengine batch8 deprecation migration failed", zap.Error(err))
	}

	// 网宿模型官网权威价强制更新（幂等 UPDATE，覆盖推测价 → OpenAI/Anthropic/Google 官网价）
	database.RunWangsuOfficialPricingMigration(database.DB)
	database.RunUSDPriceSourceBackfillMigration(database.DB)
	database.RunPriceTierInheritanceMigration(database.DB)
	database.RunPriceTierSellingSyncMigration(database.DB)
	database.RunModelPriceAccuracyMigration(database.DB)
	if err := database.RunPublicModelDescriptionCleanupMigration(database.DB); err != nil {
		logger.L.Warn("public model description cleanup migration failed", zap.Error(err))
	}
	database.RunFixQwenFeaturesMigration(database.DB)
	// Wangsu 官方价迁移会刷新海外模型阶梯，再跑一次通用同步以兼容旧字段格式。
	database.RunPriceTierSellingSyncMigration(database.DB)

	// 修复 Qwen/QwQ 等模型的能力标记（v5.1 补丁）
	database.RunFixQwenFeaturesMigration(database.DB)
	if err := database.RunModelCapabilityDefaultsMigration(database.DB); err != nil {
		logger.L.Warn("model capability defaults migration failed", zap.Error(err))
	}

	// 清理历史模板/测试供应商，只保留当前真实接入的中文供应商。
	if err := database.RunPruneUnusedSuppliersMigration(database.DB); err != nil {
		logger.L.Warn("supplier prune migration failed", zap.Error(err))
	}
}

// runStartupPatches 启动时执行的轻量幂等补丁（与 RunDataMigrations 区分）
//
// 设计原则：
//   - **必须幂等**且对已存在数据 no-op（如 FirstOrCreate / WHERE NOT EXISTS）
//   - **必须轻量**（毫秒级，不阻塞启动），不做大量 ALTER TABLE / 全表扫描
//   - **不写入业务种子数据**，仅补字典/小补丁，与功能渲染强耦合
//   - 与 RunDataMigrations 不同：本函数每次启动都会运行，无需管理员手动触发
//
// 加入条件：补丁缺失时会导致前端 UI 退化（如标签徽标颜色丢失）或基础字段不可用。
// 不该加入：业务种子（suppliers/levels/permissions 等）—— 必须走安装向导。
func runStartupPatches() {
	// needs_review 字典项：自动入库模型 NeedsReview 标签的字典 JOIN 依赖
	database.RunMigrateLabelDictNeedsReview(database.DB)
}

// InitRedis 初始化 Redis 连接
func InitRedis() error {
	if err := pkgredis.Init(pkgredis.Config{
		Addr:     config.Global.Redis.Addr,
		Username: config.Global.Redis.Username,
		Password: config.Global.Redis.Password,
		DB:       config.Global.Redis.DB,
	}); err != nil {
		return fmt.Errorf("redis init: %w", err)
	}
	return nil
}

// PostRedisInit 在 Redis 初始化后执行的清理操作（种子数据更新后清除等级缓存）
func PostRedisInit() {
	_ = pkgredis.Client.Del(context.Background(), "member:levels:all", "agent:levels:all").Err()
}

// InitI18n 初始化国际化
func InitI18n() error {
	return pkgi18n.Init(pkgi18n.Config{
		DefaultLang: config.Global.I18n.DefaultLang,
		LocalesDir:  config.Global.I18n.LocalesDir,
	})
}

// InitAll 按顺序初始化所有基础设施，返回 Result 供 graceful shutdown 使用。
// 这是最常用的一站式初始化方法。
func InitAll() (*Result, error) {
	// 1. 配置
	if err := InitConfig(); err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	// 2. 日志
	if err := InitLogger(); err != nil {
		return nil, fmt.Errorf("init logger: %w", err)
	}
	logger.L.Info("logger initialized",
		zap.String("role", config.Global.Service.Role),
	)

	// 3. 数据库（仅建连，不做 schema 变更 / 种子写入）
	if err := InitDatabase(false); err != nil {
		logger.L.Fatal("init database failed", zap.Error(err))
	}
	logger.L.Info("database initialized")

	// 3.1 启动时自动执行的轻量幂等补丁
	// 这些补丁与服务功能强耦合（不补会导致 UI 退化或字段缺失），无需走管理员手动迁移端点
	// 已存在数据时为 no-op，幂等安全
	runStartupPatches()

	// 4. Redis
	if err := InitRedis(); err != nil {
		logger.L.Fatal("init redis failed", zap.Error(err))
	}
	logger.L.Info("redis initialized")

	// 5. i18n（gateway 不需要 locale 文件，但初始化不影响）
	if err := InitI18n(); err != nil {
		logger.L.Fatal("init i18n failed", zap.Error(err))
	}
	logger.L.Info("i18n initialized")

	return &Result{
		Cleanup: func() {
			if err := database.Close(); err != nil {
				logger.L.Error("close database failed", zap.Error(err))
			}
			if err := pkgredis.Close(); err != nil {
				logger.L.Error("close redis failed", zap.Error(err))
			}
			logger.Sync()
		},
	}, nil
}
