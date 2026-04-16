// Package main 是 TokenHub HK 的统一入口。
// 通过环境变量 SERVICE_ROLE 控制运行模式：
//   - "gateway"  → API 网关（/v1/* 模型中转）
//   - "backend"  → 用户 + 管理后台（/api/v1/*）
//   - "worker"   → 后台任务 + 重操作执行器
//   - ""(空/缺省) → 单体模式（全功能，向后兼容）
package main

import (
	"context"
	"encoding/json"
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
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/pkg/metrics"
	pkgredis "tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/router"
	aimodelsvc "tokenhub-server/internal/service/aimodel"
	"tokenhub-server/internal/service/balance"
	channelsvc "tokenhub-server/internal/service/channel"
	memberSvc "tokenhub-server/internal/service/member"
	"tokenhub-server/internal/service/modeldiscovery"
	"tokenhub-server/internal/service/pricescraper"
	"tokenhub-server/internal/taskqueue"
)

func main() {
	// 初始化所有基础设施
	result, err := bootstrap.InitAll()
	if err != nil {
		fmt.Fprintf(os.Stderr, "initialization failed: %v\n", err)
		os.Exit(1)
	}
	defer result.Cleanup()

	role := config.Global.Service.Role
	logger.L.Info("starting tokenhub service", zap.String("role", role))

	switch role {
	case config.RoleGateway:
		runGateway()
	case config.RoleBackend:
		runBackend()
	case config.RoleWorker:
		runWorker()
	default:
		runMonolith()
	}
}

// runGateway 运行 API 网关角色
// 只注册 /v1/* 路由，WriteTimeout=300s
func runGateway() {
	gin.SetMode(config.Global.Server.Mode)
	engine := gin.New()
	engine.Use(metrics.RequestCounterMiddleware())
	router.SetupGateway(engine)
	engine.GET("/metrics", metrics.Handler(database.DB, pkgredis.Client, "gateway"))

	startServer(engine, 300*time.Second)
}

// runBackend 运行用户 + 管理后台角色
// 注册 /api/v1/* 路由，执行缓存预热
func runBackend() {
	gin.SetMode(config.Global.Server.Mode)
	engine := gin.New()
	engine.Use(metrics.RequestCounterMiddleware())
	router.SetupBackend(engine)
	router.RunCacheWarmer()
	engine.GET("/metrics", metrics.Handler(database.DB, pkgredis.Client, "backend"))

	startServer(engine, 600*time.Second)
}

// runWorker 运行后台任务角色
// 启动 Scheduler + TaskConsumer，仅暴露 /health + /metrics
func runWorker() {
	gin.SetMode(config.Global.Server.Mode)
	engine := gin.New()
	engine.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "role": "worker"})
	})
	engine.GET("/metrics", metrics.Handler(database.DB, pkgredis.Client, "worker"))

	// 启动 Scheduler
	scheduler := createScheduler()
	scheduler.Start()
	defer scheduler.Stop()
	logger.L.Info("worker scheduler started")

	// 启动 TaskConsumer（消费 Backend 委派的异步任务）
	signingKey := config.Global.Service.TaskSignKey
	if signingKey == "" {
		signingKey = config.Global.JWT.Secret // 默认复用 JWT Secret
	}
	hostname, _ := os.Hostname()
	consumer := taskqueue.NewConsumer(pkgredis.Client, signingKey, "worker-"+hostname)
	registerTaskHandlers(consumer)
	consumerCtx, consumerCancel := context.WithCancel(context.Background())
	defer consumerCancel()
	go consumer.Start(consumerCtx)
	logger.L.Info("worker task consumer started")

	startServer(engine, 30*time.Second)
}

// registerTaskHandlers 注册 Worker 的异步任务处理函数，接入实际业务服务
func registerTaskHandlers(consumer *taskqueue.Consumer) {
	db := database.DB

	modelChecker := aimodelsvc.NewModelChecker(db)
	discoverySvc := modeldiscovery.NewDiscoveryService(db)

	// 模型批量检测 — 接入 ModelChecker.BatchCheck()
	consumer.RegisterHandler(taskqueue.TaskBatchCheck, func(ctx context.Context, payload string, progress taskqueue.ProgressReporter) error {
		progress.Report(taskqueue.StatusRunning, 5, "初始化模型检测...")

		// BatchCheck 通过 channel 推送进度
		progressCh := make(chan aimodelsvc.BatchCheckProgress, 100)
		var results []aimodelsvc.ModelCheckResult
		var checkErr error

		go func() {
			results, checkErr = modelChecker.BatchCheck(ctx, progressCh)
		}()

		// 转发进度到 Redis Pub/Sub
		for p := range progressCh {
			pct := 0
			if p.Total > 0 {
				pct = p.Checked * 95 / p.Total // 留 5% 给收尾
			}
			progress.Report(taskqueue.StatusRunning, 5+pct,
				fmt.Sprintf("已检测 %d/%d，可用 %d，失败 %d", p.Checked, p.Total, p.Available, p.Failed))
		}

		if checkErr != nil {
			return checkErr
		}

		// 汇总结果
		summary := map[string]int{
			"total":     len(results),
			"available": 0,
			"failed":    0,
		}
		for _, r := range results {
			if r.Available {
				summary["available"]++
			} else {
				summary["failed"]++
			}
		}
		progress.ReportData(taskqueue.StatusCompleted, 100, "模型检测完成", summary)
		return nil
	})

	// 模型按渠道同步 — 接入 DiscoveryService.SyncFromChannel()
	consumer.RegisterHandler(taskqueue.TaskModelSync, func(ctx context.Context, payload string, progress taskqueue.ProgressReporter) error {
		var params taskqueue.ModelSyncPayload
		if err := json.Unmarshal([]byte(payload), &params); err != nil {
			return fmt.Errorf("解析参数失败: %w", err)
		}
		progress.Report(taskqueue.StatusRunning, 10, fmt.Sprintf("同步渠道 %d 的模型...", params.ChannelID))

		result, err := discoverySvc.SyncFromChannel(params.ChannelID)
		if err != nil {
			return fmt.Errorf("同步失败: %w", err)
		}

		progress.ReportData(taskqueue.StatusCompleted, 100, "模型同步完成", result)
		return nil
	})

	// 全量模型同步 — 接入 DiscoveryService.SyncAllActive()
	consumer.RegisterHandler(taskqueue.TaskModelSyncAll, func(ctx context.Context, payload string, progress taskqueue.ProgressReporter) error {
		progress.Report(taskqueue.StatusRunning, 10, "启动全量模型同步...")

		result, err := discoverySvc.SyncAllActive()
		if err != nil {
			return fmt.Errorf("全量同步失败: %w", err)
		}

		progress.ReportData(taskqueue.StatusCompleted, 100, "全量同步完成", result)
		return nil
	})

	// 默认路由刷新 — 接入 channel.RefreshDefaultRoutes()
	consumer.RegisterHandler(taskqueue.TaskRouteRefresh, func(ctx context.Context, payload string, progress taskqueue.ProgressReporter) error {
		progress.Report(taskqueue.StatusRunning, 30, "刷新默认路由...")

		err := channelsvc.RefreshDefaultRoutes(ctx, db, pkgredis.Client, nil)
		if err != nil {
			return fmt.Errorf("路由刷新失败: %w", err)
		}

		progress.Report(taskqueue.StatusCompleted, 100, "路由刷新完成")
		return nil
	})

	// 全量下线模型扫描 — 使用 DeprecationScan 逻辑
	consumer.RegisterHandler(taskqueue.TaskScanOffline, func(ctx context.Context, payload string, progress taskqueue.ProgressReporter) error {
		progress.Report(taskqueue.StatusRunning, 10, "扫描所有供应商下线模型...")

		// 查询所有活跃 API 型供应商
		var suppliers []model.Supplier
		if err := db.Where("status = ? AND is_active = ? AND (access_type = ? OR access_type = ?)",
			"active", true, "api", "").Find(&suppliers).Error; err != nil {
			return fmt.Errorf("查询供应商失败: %w", err)
		}

		type scanResult struct {
			SupplierName string `json:"supplier_name"`
			SupplierCode string `json:"supplier_code"`
			ModelCount   int    `json:"model_count"`
		}
		var results []scanResult
		for i, sup := range suppliers {
			pct := 10 + (i*80)/len(suppliers)
			progress.Report(taskqueue.StatusRunning, pct,
				fmt.Sprintf("扫描 %s (%d/%d)...", sup.Name, i+1, len(suppliers)))

			// 统计该供应商的已下线模型数
			var offlineCount int64
			db.Model(&model.AIModel{}).
				Where("supplier_id = ? AND status = ?", sup.ID, "offline").
				Count(&offlineCount)
			if offlineCount > 0 {
				results = append(results, scanResult{
					SupplierName: sup.Name,
					SupplierCode: sup.Code,
					ModelCount:   int(offlineCount),
				})
			}
		}

		progress.ReportData(taskqueue.StatusCompleted, 100, "扫描完成", results)
		return nil
	})
}

// runMonolith 运行全功能单体模式（向后兼容 cmd/server/main.go）
func runMonolith() {
	gin.SetMode(config.Global.Server.Mode)
	engine := gin.New()
	router.Setup(engine)
	router.RunCacheWarmer()

	// 启动 Scheduler
	scheduler := createScheduler()
	scheduler.Start()
	defer scheduler.Stop()

	// 注册 cron 管理路由
	cronHandler := adminHandler.NewCronTaskHandler(scheduler)
	adminGroup := engine.Group("/api/v1/admin")
	cronHandler.Register(adminGroup)

	startServer(engine, 1800*time.Second)
}

// createScheduler 创建并配置定时任务调度器
func createScheduler() *cron.Scheduler {
	memberLevelSvc := memberSvc.NewMemberLevelService(database.DB, pkgredis.Client)
	balanceSvc := balance.NewBalanceService(database.DB, pkgredis.Client)
	discoverySvc := modeldiscovery.NewDiscoveryService(database.DB)
	scraperSvc := pricescraper.NewPriceScraperService(database.DB)
	return cron.NewScheduler(database.DB, pkgredis.Client, memberLevelSvc, balanceSvc,
		cron.WithDiscoveryService(discoverySvc),
		cron.WithPriceScraperService(scraperSvc),
	)
}

// startServer 启动 HTTP 服务器并等待优雅关闭
func startServer(engine *gin.Engine, writeTimeout time.Duration) {
	addr := fmt.Sprintf(":%d", config.Global.Server.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      engine,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: writeTimeout,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		logger.L.Info("server starting",
			zap.String("addr", addr),
			zap.String("role", config.Global.Service.Role),
			zap.Duration("write_timeout", writeTimeout),
		)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.L.Fatal("server listen failed", zap.Error(err))
		}
	}()

	// 优雅关闭
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
