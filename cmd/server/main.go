package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tokenhub-server/internal/config"
	"tokenhub-server/internal/cron"
	"tokenhub-server/internal/database"
	adminHandler "tokenhub-server/internal/handler/admin"
	pkgi18n "tokenhub-server/internal/pkg/i18n"
	"tokenhub-server/internal/pkg/logger"
	pkgredis "tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/router"
	auditsvc "tokenhub-server/internal/service/audit"
	memberSvc "tokenhub-server/internal/service/member"
	"tokenhub-server/internal/service/modeldiscovery"
	permissionsvc "tokenhub-server/internal/service/permission"
	reportSvc "tokenhub-server/internal/service/report"
)

func main() {
	// 1. Load configuration
	cfgFile := "configs/config.yaml"
	if envCfg := os.Getenv("CONFIG_FILE"); envCfg != "" {
		cfgFile = envCfg
	}
	if err := config.Load(cfgFile); err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	// 2. Initialize logger
	if err := logger.Init(logger.Config{
		Level:      config.Global.Log.Level,
		Dir:        config.Global.Log.Dir,
		MaxSize:    config.Global.Log.MaxSize,
		MaxAge:     config.Global.Log.MaxAge,
		MaxBackups: config.Global.Log.MaxBackups,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "failed to init logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()
	logger.L.Info("logger initialized")

	// 3. Initialize database
	if err := database.Init(config.Global.Database, logger.L); err != nil {
		logger.L.Fatal("failed to init database", zap.Error(err))
	}
	defer func() {
		if err := database.Close(); err != nil {
			logger.L.Error("failed to close database", zap.Error(err))
		}
	}()
	logger.L.Info("database initialized")

	// 3.1 Run seed data
	database.RunSeed(database.DB)

	// 3.2 填充文档种子数据（分类 + 文档文章）
	database.RunSeedDocs(database.DB)

	// 3.3 填充会员等级和代理等级种子数据
	database.RunSeedLevels(database.DB)

	// 3.4 填充平台标准参数和供应商参数映射
	database.RunSeedParams()

	// 3.5 数据迁移：为历史模型回填缓存定价字段（幂等，仅处理 supports_cache=false 的行）
	database.RunCachePriceMigration(database.DB)

	// 3.5.1 数据迁移：补齐二维阶梯价格（空 tiers 注入默认 (0,+∞]×(0,+∞]，已有 tiers 同步新旧字段）
	database.RunPriceTiers2DMigration(database.DB)

	// 3.5.2 数据迁移：清理 ai_models.extra_params 字段中 JSON_TYPE='STRING'/'ARRAY' 的脏数据
	// 修复 UI 上"自定义参数"区误展示单字符伪对象的问题
	database.RunExtraParamsCleanupMigration(database.DB)

	// 3.5.2a 数据迁移：清理 extra_params 中 {key: bool} 形式的能力标记脏数据
	// 修复历史 UI 将"支持 stop/voice/dimensions" 等能力标记误写成 extra_params 导致
	// 请求体污染（如上游收到 "stop": true 返回 400 malformed_json）
	database.RunExtraParamsFeatureFlagsCleanup(database.DB)

	// 3.5.2b 数据迁移：为阿里云 qwq/qvq 系列回填 features.requires_stream=true
	// 这些推理模型仅支持流式接口，非流式请求会被上游拒绝
	database.RunQwqRequiresStreamMigration(database.DB)

	// 3.5.3 数据迁移：为存量供应商回填官网定价 URL（pricing_url 字段）
	// 不覆盖管理员自定义值，仅在空值时按 code 匹配默认 URL
	database.RunSupplierPricingURLMigration(database.DB)

	// 3.5.4 数据迁移：修正旧 seed 数据中 非 LLM/VLM 模型错误启用缓存的问题
	// 规则：仅 LLM/VLM/Vision + per_million_tokens 允许 supports_cache=true，其他强制清零
	database.RunCacheTypeCleanup(database.DB)

	// 3.5.5 v3.5: 标签字典种子（首次写入 7 用户标签 + 2 系统 + 11 品牌 × 多语言）
	database.RunSeedLabelDictionary(database.DB)

	// 3.5.6 v3.5: ai_models.tags 字符串 → model_labels 表迁移（幂等白名单映射）
	database.RunMigrateTagsToLabels(database.DB)

	// 3.6 数据迁移：回填渠道 supported_capabilities 字段（按供应商推断默认能力）
	if err := database.MigrateChannelCapabilities(database.DB); err != nil {
		logger.L.Warn("channel capabilities migration failed", zap.Error(err))
	}

	// 3.7 数据迁移：v3.1 物理删除代理机制遗留表（幂等）
	if err := database.DropAgentTables(database.DB); err != nil {
		logger.L.Warn("drop agent tables migration failed", zap.Error(err))
	}

	// 3.8 种子数据：预置非 Token 计费模型 —— 已禁用
	// 原因：硬编码模型与 auto-discovery 数据冲突，且每次 fresh DB 启动产生 stale 价格
	// database.RunSeedNonTokenModels(database.DB)

	// 3.10 种子数据：增量添加百度千帆 V2 供应商/模型/渠道（幂等，已存在则跳过）
	database.RunSeedQianfan(database.DB)

	// 3.11 种子数据：增量添加腾讯混元供应商/模型/渠道（幂等，已存在则跳过）
	database.RunSeedHunyuan(database.DB)

	// 3.12 种子数据：能力测试用例（43 条，幂等 upsert，已存在则更新 display_name/notes/template）
	database.RunSeedCapabilityCases(database.DB)

	// 3.12.1 种子数据：AI 客服系统（模型配置 + 供应商文档 URL + 热门问题，各表首次写入幂等）
	database.RunSeedSupport(database.DB)

	// 3.13 种子数据：汇率 API 配置（v3.2.3；阿里云 cmapi00064402/cmapi00063890 凭证写入 system_configs）
	database.RunSeedExchangeRateConfig(database.DB)

	// 3.9 数据迁移：火山引擎第八批下线模型标记为 offline（EOS: 2026-05-11）
	// 来源：https://www.volcengine.com/docs/82379/1350667
	if err := database.MigrateVolcengineBatch8Deprecation(database.DB); err != nil {
		logger.L.Warn("volcengine batch8 deprecation migration failed", zap.Error(err))
	}

	// 3.14 种子数据：全球热门模型参考库（首次写入，表非空则跳过）
	if err := database.RunSeedTrendingModels(); err != nil {
		logger.L.Warn("seed trending models failed", zap.Error(err))
	}

	// 3.15 v4.0: RBAC 权限系统种子（permissions/roles/role_permissions/user_roles 回填）
	// 幂等，每次启动安全执行。
	if err := permissionsvc.Seed(database.DB); err != nil {
		logger.L.Warn("permission seed failed", zap.Error(err))
	}

	// 4. Initialize Redis
	if err := pkgredis.Init(pkgredis.Config{
		Addr:     config.Global.Redis.Addr,
		Username: config.Global.Redis.Username,
		Password: config.Global.Redis.Password,
		DB:       config.Global.Redis.DB,
	}); err != nil {
		logger.L.Fatal("failed to init redis", zap.Error(err))
	}
	defer func() {
		if err := pkgredis.Close(); err != nil {
			logger.L.Error("failed to close redis", zap.Error(err))
		}
	}()
	logger.L.Info("redis initialized")

	// 4.1 种子数据更新后清除等级缓存（种子在Redis初始化前运行，此处补清缓存）
	_ = pkgredis.Client.Del(context.Background(), "member:levels:all", "agent:levels:all").Err()

	// 5. Initialize i18n
	if err := pkgi18n.Init(pkgi18n.Config{
		DefaultLang: config.Global.I18n.DefaultLang,
		LocalesDir:  config.Global.I18n.LocalesDir,
	}); err != nil {
		logger.L.Fatal("failed to init i18n", zap.Error(err))
	}
	logger.L.Info("i18n initialized")

	// 6. Setup Gin
	gin.SetMode(config.Global.Server.Mode)
	engine := gin.New()
	router.Setup(engine)

	// 6.1 缓存预热：服务启动时加载高频数据到 Redis
	router.RunCacheWarmer()

	// 6.2 初始化定时任务调度器（仅在需要时启动，支持 SERVICE_ROLE 跳过）
	if config.Global.Service.ShouldRunScheduler() {
		memberLevelSvc := memberSvc.NewMemberLevelService(database.DB, pkgredis.Client)
		discoverySvc := modeldiscovery.NewDiscoveryService(database.DB)
		userDailyAggSvc := reportSvc.NewUserDailyAggService(database.DB)
		scheduler := cron.NewScheduler(database.DB, pkgredis.Client, memberLevelSvc,
			cron.WithDiscoveryService(discoverySvc),
			cron.WithAuditService(auditsvc.Default),
			cron.WithUserDailyAggService(userDailyAggSvc),
		)
		scheduler.Start()
		defer scheduler.Stop()

		// 注册定时任务管理路由
		cronHandler := adminHandler.NewCronTaskHandler(scheduler)
		adminGroup := engine.Group("/api/v1/admin")
		cronHandler.Register(adminGroup)
		logger.L.Info("scheduler started")
	} else {
		logger.L.Info("scheduler skipped (SERVICE_ROLE not monolith/worker)")
	}

	// 7. Start HTTP server
	// WriteTimeout 设为 30 分钟以支持长任务（如一键扫描预览检测全部模型 + 调用上游 /v1/models，
	// 380 个模型并发 3、限流 500ms 时最坏情况约 30 分钟）
	// 对于普通 API 仍受 ReadTimeout 30s 保护
	addr := fmt.Sprintf(":%d", config.Global.Server.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      engine,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 1800 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		logger.L.Info("server starting", zap.String("addr", addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.L.Fatal("server listen failed", zap.Error(err))
		}
	}()

	// 8. Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	logger.L.Info("shutdown signal received", zap.String("signal", sig.String()))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.L.Error("server forced to shutdown", zap.Error(err))
	}

	// 关闭审计日志异步队列（drain 剩余日志再退出，最多等 10s）
	auditsvc.ShutdownDefault()

	logger.L.Info("server exited gracefully")
}
