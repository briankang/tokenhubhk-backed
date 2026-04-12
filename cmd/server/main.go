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
	pkgi18n "tokenhub-server/internal/pkg/i18n"
	"tokenhub-server/internal/pkg/logger"
	pkgredis "tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/router"
	agentSvc "tokenhub-server/internal/service/agent"
	"tokenhub-server/internal/service/balance"
	memberSvc "tokenhub-server/internal/service/member"
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

	// 3.4 数据迁移：将现有 AGENT_L1/L2/L3 用户迁移到 UserAgentProfile 体系
	if err := database.MigrateAgentData(database.DB); err != nil {
		logger.L.Warn("agent data migration failed", zap.Error(err))
	}
	// 3.5 数据迁移：为所有用户创建 UserMemberProfile（按消费自动匹配等级）
	if err := database.MigrateMemberData(database.DB); err != nil {
		logger.L.Warn("member data migration failed", zap.Error(err))
	}

	// 4. Initialize Redis
	if err := pkgredis.Init(pkgredis.Config{
		Addr:     config.Global.Redis.Addr,
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

	// 6.2 初始化定时任务调度器
	memberLevelSvc := memberSvc.NewMemberLevelService(database.DB, pkgredis.Client)
	agentLevelSvc := agentSvc.NewAgentLevelService(database.DB, pkgredis.Client)
	balanceSvc := balance.NewBalanceService(database.DB, pkgredis.Client)
	scheduler := cron.NewScheduler(database.DB, pkgredis.Client, memberLevelSvc, agentLevelSvc, balanceSvc)
	scheduler.Start()
	defer scheduler.Stop()

	// 7. Start HTTP server
	addr := fmt.Sprintf(":%d", config.Global.Server.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      engine,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
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

	logger.L.Info("server exited gracefully")
}
