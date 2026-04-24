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

	"tokenhub-server/internal/bootstrap"
	"tokenhub-server/internal/config"
	"tokenhub-server/internal/cron"
	"tokenhub-server/internal/database"
	adminHandler "tokenhub-server/internal/handler/admin"
	pkgi18n "tokenhub-server/internal/pkg/i18n"
	"tokenhub-server/internal/pkg/logger"
	pkgredis "tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/router"
	auditsvc "tokenhub-server/internal/service/audit"
	"tokenhub-server/internal/service/authlog"
	ratelimitsvc "tokenhub-server/internal/service/ratelimit"
	memberSvc "tokenhub-server/internal/service/member"
	"tokenhub-server/internal/service/modeldiscovery"
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

	// 注意：启动时不再执行任何 schema 变更 / 种子写入。
	//   - 首次部署：调用 POST /api/v1/setup/import-seed 走安装向导
	//   - 版本升级：调用 POST /api/v1/admin/system/migrate
	// 启动时仅在 database.Init 内部做只读的 schema_version 校验，失败不阻塞。

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
	bootstrap.ConfigureTrustedProxies(engine)
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
			cron.WithRateLimitEventRecorder(ratelimitsvc.Default),
			cron.WithAuthLogRecorder(authlog.Default),
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
	ratelimitsvc.ShutdownDefault()
	authlog.ShutdownDefault()

	logger.L.Info("server exited gracefully")
}
