package router

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/config"
	"tokenhub-server/internal/database"
	"tokenhub-server/internal/handler/admin"
	authhandler "tokenhub-server/internal/handler/auth"
	chathandler "tokenhub-server/internal/handler/chat"
	paymenthandler "tokenhub-server/internal/handler/payment"
	setuphandler "tokenhub-server/internal/handler/setup"
	userhandler "tokenhub-server/internal/handler/user"
	"tokenhub-server/internal/middleware"
	pkglogger "tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/apikey"
	aimodelsvc "tokenhub-server/internal/service/aimodel"
	authsvc "tokenhub-server/internal/service/auth"
	channelsvc "tokenhub-server/internal/service/channel"
	docsvc "tokenhub-server/internal/service/doc"
	membersvc "tokenhub-server/internal/service/member"
	modelcatsvc "tokenhub-server/internal/service/modelcategory"
	orchsvc "tokenhub-server/internal/service/orchestration"
	"tokenhub-server/internal/service/permission"
	paymentsvc "tokenhub-server/internal/service/payment"
	balancesvc "tokenhub-server/internal/service/balance"
	"tokenhub-server/internal/service/pricing"
	configauditsvc "tokenhub-server/internal/service/configaudit"
	guardsvc "tokenhub-server/internal/service/guard"
	referralsvc "tokenhub-server/internal/service/referral"
	"tokenhub-server/internal/service/report"
	withdrawalsvc "tokenhub-server/internal/service/withdrawal"
	setupsvc "tokenhub-server/internal/service/setup"
	suppliersvc "tokenhub-server/internal/service/supplier"
	usersvc "tokenhub-server/internal/service/user"
	whitelabel "tokenhub-server/internal/service/whitelabel"

	openhandler "tokenhub-server/internal/handler/openapi"
	"tokenhub-server/internal/handler/public"
	v1handler "tokenhub-server/internal/handler/v1"
	mcphandler "tokenhub-server/internal/handler/mcp"
	"tokenhub-server/internal/mcp"
	openapi "tokenhub-server/internal/service/openapi"
	geosvc "tokenhub-server/internal/service/geo"
	cachesvc "tokenhub-server/internal/service/cache"
	codingsvc "tokenhub-server/internal/service/coding"
	pricescraper "tokenhub-server/internal/service/pricescraper"
	"tokenhub-server/internal/service/task"
	"tokenhub-server/internal/service/parammapping"
	"tokenhub-server/internal/taskqueue"
	"github.com/spf13/viper"
)

// taskBridge 全局 SSE 桥接器，由 SetupBackend() 初始化。
// 非 nil 时 admin 重操作 handler 将通过此桥接器委派给 Worker。
// Setup()（单体模式）中为 nil，handler 在本进程内执行。
var taskBridge *taskqueue.SSEBridge

// Setup 注册所有路由和中间件到 Gin 引擎
func Setup(r *gin.Engine) {
	// 初始化白标服务
	domainResolver := whitelabel.NewDomainResolver(database.DB, redis.Client)

	// 全局中间件
	r.Use(middleware.Recovery())
	r.Use(middleware.CORS())
	r.Use(middleware.RequestLogger())
	r.Use(middleware.I18n())
	r.Use(middleware.TenantResolveMiddleware(domainResolver, viper.GetString("server.platform_domain")))
	// 多层级限流中间件（在认证之前，基于 IP/用户/API Key 限流）
	r.Use(middleware.MultiLevelRateLimiter())

	// 健康检查端点
	r.GET("/health", func(c *gin.Context) {
		response.Success(c, gin.H{"status": "ok"})
	})

	// API v1 路由组
	v1 := r.Group("/api/v1")

	// ========== Setup Routes (安装向导) ==========
	// 安装向导路由必须在守卫中间件之前注册，以便未初始化时可访问
	registerSetupHandlers(v1)
	// 安装守卫中间件：未初始化时拦截非 setup 请求
	v1.Use(middleware.SetupGuard(database.DB))

	// --- 认证路由 ---
	registerAuthHandlers(v1)

	// --- 公开文档 ---
	docsGroup := v1.Group("/docs")
	docsGroup.Use(middleware.CacheMiddleware(cachesvc.TTLStandard)) // 文档标准缓存 1h
	{
		registerPublicDocHandlers(docsGroup)
	}

	// --- 公开白标配置 (供前端初始化) ---
	publicGroup := v1.Group("/public")
	publicGroup.Use(middleware.CacheMiddleware(cachesvc.TTLLong)) // 公开接口长缓存 2h
	{
		// --- Public payment methods ---
		registerPublicPaymentMethodsHandler(publicGroup)

		// --- IP 地理位置语言检测 ---
		registerLocaleHandler(publicGroup)

		// --- 公开模型列表（无需认证，供前端 /models 页面使用） ---
		registerPublicModelHandlers(publicGroup)

		// --- 参数支持情况查询 ---
		registerParamSupportHandler(publicGroup)

		// --- 邀请和注册配置（公开，供前端动态展示） ---
		registerPublicConfigHandlers(publicGroup)

		// --- 公开公告 Banner（供 Dashboard 头部滚动展示）---
		annPublicHandler := public.NewAnnouncementPublicHandler(database.DB)
		annPublicHandler.RegisterPublicBanner(publicGroup)
	}

	// --- 公开 POST 路由（无缓存） ---
	publicWriteGroup := v1.Group("/public")
	{
		// --- 合作伙伴线索申请 ---
		registerPartnerApplicationHandler(publicWriteGroup)
		// --- 邀请链接点击追踪 ---
		registerReferralClickHandler(publicWriteGroup)
	}

	// --- 支付回调 (无 JWT, 签名验证) ---
	registerPaymentCallbacks(v1)

	// --- 已认证路由 ---
	authorized := v1.Group("")
	authorized.Use(middleware.Auth())
	authorized.Use(middleware.MemberRateLimiter(database.DB, redis.Client))
	authorized.Use(middleware.DataScope())
	authorized.Use(middleware.Idempotent())

	// 管理员路由 (需要 ADMIN 角色)
	adminGroup := authorized.Group("/admin")
	adminGroup.Use(middleware.RequireRole("ADMIN"))
	{
		// --- Admin user management (registered via handlers) ---
		registerAdminUserHandlers(adminGroup)

		// --- Channel management (registered via handlers) ---
		registerChannelHandlers(adminGroup)

		// --- Orchestration management (registered via handlers) ---
		registerOrchestrationHandlers(adminGroup)

		// --- Doc management (registered via handlers) ---
		registerDocHandlers(adminGroup)

		// --- Report management (registered via handlers) ---
		registerAdminReportHandlers(adminGroup)

		// --- Pricing management (registered via handlers) ---
		registerPricingHandlers(adminGroup)

		// --- Quota/Balance management ---
		registerQuotaHandlers(adminGroup)

		// --- Referral config management ---
		registerAdminReferralHandlers(adminGroup)

		// --- Commission override management (v3.1 特殊用户加佣) ---
		registerAdminCommissionOverrideHandlers(adminGroup)

		// --- Guard / Disposable email (v3.1 反欺诈配置) ---
		registerAdminGuardHandlers(adminGroup)

		// --- Withdrawal v3.1 (管理员审核) ---
		registerAdminWithdrawalHandlers(adminGroup)

		// --- Config Audit Log (v3.1 统一审计日志查询) ---
		registerAdminConfigAuditHandlers(adminGroup)

		// --- Payment config management ---
		registerPaymentConfigHandlers(adminGroup)

		// ========== Cache Admin Routes (缓存管理) ==========
		registerCacheHandlers(adminGroup)

		// ========== Rate Limit & Quota Routes (限流限额管理) ==========
		registerRateLimitHandlers(adminGroup)

		// ========== 会员/代理等级管理路由 ==========
		registerLevelAdminHandlers(adminGroup)

		// ========== Exchange Rate Routes (汇率管理) ==========
		registerExchangeRateHandlers(adminGroup)

		// ========== Reconciliation Routes (对账报告) ==========
		registerReconciliationHandler(adminGroup)

		// ========== Custom Channel Routes (自定义渠道管理) ==========
		registerCustomChannelHandlers(adminGroup)

		// ========== Channel Stats Routes (渠道监控统计) ==========
		channelStatsHandler := admin.NewChannelStatsHandler(database.DB, redis.Client)
		channelStatsHandler.Register(adminGroup)

		// ========== Price Scraper Routes (价格爬虫管理) ==========
		registerPriceScraperHandlers(adminGroup)

		// ========== Model Sync Routes (模型自动发现与同步) ==========
		registerModelSyncHandlers(adminGroup)

		// ========== Background Task Routes (后台任务管理) ==========
		registerTaskHandlers(adminGroup)

		// ========== API Call Log Routes (API调用全链路日志) ==========
		apiCallLogSvc := apikey.NewApiKeyService(database.DB, redis.Client, config.Global.JWT.Secret)
		apiCallLogHandler := admin.NewApiCallLogHandler(database.DB, apiCallLogSvc)
		apiCallLogHandler.Register(adminGroup)

		// ========== Param Mapping Routes (参数映射管理) ==========
		registerParamMappingHandlers(adminGroup)

		// ========== Partner Applications Routes (合作伙伴线索管理) ==========
		partnerAdminHandler := admin.NewPartnerApplicationAdminHandler(database.DB)
		partnerAdminHandler.Register(adminGroup)

		// ========== Announcement Routes (站内公告管理) ==========
		announcementAdminHandler := admin.NewAnnouncementHandler(database.DB)
		announcementAdminHandler.Register(adminGroup)
	}

	// 代理商机制已物理移除 (v3.1)

	// 用户路由 (任何已认证用户)
	userGroup := authorized.Group("/user")
	{
		registerUserHandlers(userGroup)
		registerUserBalanceHandlers(userGroup)
		registerUserReferralHandlers(userGroup)
		registerUserAvailableChannelsHandlers(userGroup)

		// --- 会员等级相关（档案/等级列表/升级进度）---
		registerUserMemberHandlers(userGroup)

		// --- 提现申请 v3.1 ---
		registerUserWithdrawalHandlers(userGroup)

		// --- 站内通知（公告已读状态）---
		notificationHandler := userhandler.NewNotificationHandler(database.DB)
		notificationHandler.Register(userGroup)
	}

	// --- 支付路由 (JWT 认证) ---
	registerPaymentHandlers(authorized, adminGroup)

	// AI 代理路由 (API Key 认证)
	chatGroup := v1.Group("/chat")
	{
		chatGroup.Use(middleware.RateLimit())
		registerChatHandlers(chatGroup)
	}

	// ========== 对外开放 API 路由 ==========
	registerOpenAPIHandlers(v1)

	// ========== OpenAI Compatible Routes (/v1/) - Coding Plan ==========
	registerOpenAICompatibleRoutes(r)

	// ========== MCP Routes (MCP协议) ==========
	registerMCPHandlers(v1)
}

// SetupGateway 仅注册 API 网关路由 (/v1/* OpenAI 兼容接口)。
// 用于 gateway 角色的独立进程，只处理模型中转的热路径。
func SetupGateway(r *gin.Engine) {
	// 初始化白标服务（域名解析）
	domainResolver := whitelabel.NewDomainResolver(database.DB, redis.Client)

	// 全局中间件（与 Setup 一致）
	r.Use(middleware.Recovery())
	r.Use(middleware.CORS())
	r.Use(middleware.RequestLogger())
	r.Use(middleware.I18n())
	r.Use(middleware.TenantResolveMiddleware(domainResolver, viper.GetString("server.platform_domain")))
	r.Use(middleware.MultiLevelRateLimiter())

	// 健康检查
	r.GET("/health", func(c *gin.Context) {
		response.Success(c, gin.H{"status": "ok", "role": "gateway"})
	})

	// OpenAI 兼容路由 (/v1/*)
	registerOpenAICompatibleRoutes(r)
}

// SetupBackend 注册用户 + 管理后台路由 (/api/v1/*)。
// 用于 backend 角色的独立进程，处理 Dashboard API、管理后台、支付、MCP 等。
func SetupBackend(r *gin.Engine) {
	// 初始化 SSE 桥接器（委派重操作给 Worker）
	signingKey := config.Global.Service.TaskSignKey
	if signingKey == "" {
		signingKey = config.Global.JWT.Secret
	}
	publisher := taskqueue.NewPublisher(redis.Client, signingKey)
	taskBridge = taskqueue.NewSSEBridge(publisher)

	// 初始化白标服务
	domainResolver := whitelabel.NewDomainResolver(database.DB, redis.Client)

	// 全局中间件
	r.Use(middleware.Recovery())
	r.Use(middleware.CORS())
	r.Use(middleware.RequestLogger())
	r.Use(middleware.I18n())
	r.Use(middleware.TenantResolveMiddleware(domainResolver, viper.GetString("server.platform_domain")))
	r.Use(middleware.MultiLevelRateLimiter())

	// 健康检查
	r.GET("/health", func(c *gin.Context) {
		response.Success(c, gin.H{"status": "ok", "role": "backend"})
	})

	// API v1 路由组
	v1 := r.Group("/api/v1")

	// 安装向导
	registerSetupHandlers(v1)
	v1.Use(middleware.SetupGuard(database.DB))

	// 认证路由
	registerAuthHandlers(v1)

	// 公开文档
	docsGroup := v1.Group("/docs")
	docsGroup.Use(middleware.CacheMiddleware(cachesvc.TTLStandard))
	{
		registerPublicDocHandlers(docsGroup)
	}

	// 公开数据
	publicGroup := v1.Group("/public")
	publicGroup.Use(middleware.CacheMiddleware(cachesvc.TTLLong))
	{
		registerPublicPaymentMethodsHandler(publicGroup)
		registerLocaleHandler(publicGroup)
		registerPublicModelHandlers(publicGroup)
		registerParamSupportHandler(publicGroup)
		registerPublicConfigHandlers(publicGroup)
		annPublicHandler := public.NewAnnouncementPublicHandler(database.DB)
		annPublicHandler.RegisterPublicBanner(publicGroup)
	}

	// 公开 POST 路由
	publicWriteGroup := v1.Group("/public")
	{
		registerPartnerApplicationHandler(publicWriteGroup)
		registerReferralClickHandler(publicWriteGroup)
	}

	// 支付回调
	registerPaymentCallbacks(v1)

	// 已认证路由
	authorized := v1.Group("")
	authorized.Use(middleware.Auth())
	authorized.Use(middleware.MemberRateLimiter(database.DB, redis.Client))
	authorized.Use(middleware.DataScope())
	authorized.Use(middleware.Idempotent())

	// 管理员路由
	adminGroup := authorized.Group("/admin")
	adminGroup.Use(middleware.RequireRole("ADMIN"))
	{
		registerAdminUserHandlers(adminGroup)
		registerChannelHandlers(adminGroup)
		registerOrchestrationHandlers(adminGroup)
		registerDocHandlers(adminGroup)
		registerAdminReportHandlers(adminGroup)
		registerPricingHandlers(adminGroup)
		registerQuotaHandlers(adminGroup)
		registerAdminReferralHandlers(adminGroup)
		registerAdminCommissionOverrideHandlers(adminGroup)
		registerAdminGuardHandlers(adminGroup)
		registerAdminWithdrawalHandlers(adminGroup)
		registerAdminConfigAuditHandlers(adminGroup)
		registerPaymentConfigHandlers(adminGroup)
		registerCacheHandlers(adminGroup)
		registerRateLimitHandlers(adminGroup)
		registerLevelAdminHandlers(adminGroup)
		registerExchangeRateHandlers(adminGroup)
		registerReconciliationHandler(adminGroup)
		registerCustomChannelHandlers(adminGroup)

		channelStatsHandler := admin.NewChannelStatsHandler(database.DB, redis.Client)
		channelStatsHandler.Register(adminGroup)

		registerPriceScraperHandlers(adminGroup)
		registerModelSyncHandlers(adminGroup)
		registerTaskHandlers(adminGroup)

		apiCallLogSvc := apikey.NewApiKeyService(database.DB, redis.Client, config.Global.JWT.Secret)
		apiCallLogHandler := admin.NewApiCallLogHandler(database.DB, apiCallLogSvc)
		apiCallLogHandler.Register(adminGroup)

		registerParamMappingHandlers(adminGroup)

		partnerAdminHandler := admin.NewPartnerApplicationAdminHandler(database.DB)
		partnerAdminHandler.Register(adminGroup)

		announcementAdminHandler := admin.NewAnnouncementHandler(database.DB)
		announcementAdminHandler.Register(adminGroup)
	}

	// 用户路由
	userGroup := authorized.Group("/user")
	{
		registerUserHandlers(userGroup)
		registerUserBalanceHandlers(userGroup)
		registerUserReferralHandlers(userGroup)
		registerUserAvailableChannelsHandlers(userGroup)
		registerUserMemberHandlers(userGroup)
		registerUserWithdrawalHandlers(userGroup)
		notificationHandler := userhandler.NewNotificationHandler(database.DB)
		notificationHandler.Register(userGroup)
	}

	// 支付路由
	registerPaymentHandlers(authorized, adminGroup)

	// Chat 路由
	chatGroup := v1.Group("/chat")
	{
		chatGroup.Use(middleware.RateLimit())
		registerChatHandlers(chatGroup)
	}

	// Open API
	registerOpenAPIHandlers(v1)

	// MCP
	registerMCPHandlers(v1)
}

// registerOpenAPIHandlers 注册对外开放 API 路由组 (/api/v1/open/)。
// 使用 Bearer Token (API Key) 认证 + 独立限流 60 req/min。
func registerOpenAPIHandlers(v1 *gin.RouterGroup) {
	db := database.DB
	svc := openapi.NewOpenAPIService(db)

	openGroup := v1.Group("/open")
	openGroup.Use(middleware.OpenAPIAuth(db))
	openGroup.Use(middleware.OpenAPIRateLimit())

	// 消费类
	consumptionHandler := openhandler.NewConsumptionHandler(svc)
	consumptionHandler.Register(openGroup)

	// 用量类
	usageHandler := openhandler.NewUsageHandler(svc)
	usageHandler.Register(openGroup)

	// 余额类
	balanceHandler := openhandler.NewBalanceHandler(svc)
	balanceHandler.Register(openGroup)

	// 模型定价类
	modelHandler := openhandler.NewModelHandler(svc)
	modelHandler.Register(openGroup)

	// 账户/Key管理类
	accountHandler := openhandler.NewAccountHandler(svc)
	accountHandler.Register(openGroup)
}

// registerChannelHandlers 初始化渠道相关服务并注册路由
func registerChannelHandlers(rg *gin.RouterGroup) {
	db := database.DB
	redisClient := redis.Client

	// 初始化服务
	channelSvc := channelsvc.NewChannelService(db, redisClient)
	tagSvc := channelsvc.NewChannelTagService(db)
	groupSvc := channelsvc.NewChannelGroupService(db)
	backupSvc := channelsvc.NewBackupService(db, redisClient)

	// 初始化处理器并注册路由
	channelHandler := admin.NewChannelHandler(channelSvc)
	channelHandler.Register(rg)

	tagHandler := admin.NewChannelTagHandler(tagSvc)
	tagHandler.Register(rg)

	groupHandler := admin.NewChannelGroupHandler(groupSvc)
	groupHandler.Register(rg)

	backupHandler := admin.NewBackupHandler(backupSvc)
	backupHandler.Register(rg)
}

// registerOrchestrationHandlers 初始化编排服务并注册管理员路由
func registerOrchestrationHandlers(rg *gin.RouterGroup) {
	db := database.DB
	orchService := orchsvc.NewOrchestrationService(db)
	orchHandler := admin.NewOrchestrationHandler(orchService)
	orchHandler.Register(rg)
}

// registerChatHandlers 初始化聊天相关服务并注册路由
func registerChatHandlers(rg *gin.RouterGroup) {
	db := database.DB
	redisClient := redis.Client

	// 初始化渠道服务用于路由
	groupSvc := channelsvc.NewChannelGroupService(db)
	backupSvc := channelsvc.NewBackupService(db, redisClient)
	channelRouter := channelsvc.NewChannelRouter(db, redisClient, groupSvc, backupSvc)

	// 初始化编排服务
	orchService := orchsvc.NewOrchestrationService(db)
	orchEngine := orchsvc.NewOrchestrationEngine(nil, channelRouter, orchService)

	// 初始化定价和 API Key 服务
	pricingCalc := pricing.NewPricingCalculator(db)
	apiKeySvc := apikey.NewApiKeyService(db, redisClient)
	balSvc := balancesvc.NewBalanceService(db, redisClient)
	quotaLimiter := balancesvc.NewQuotaLimiter(db, redisClient)
	commCalc := referralsvc.NewCommissionCalculator(db)

	chatHandler := chathandler.NewChatHandler(db, nil, channelRouter, orchEngine, orchService, pricingCalc, apiKeySvc, balSvc, quotaLimiter, commCalc)
	chatHandler.Register(rg)
}

// registerRateLimitHandlers 初始化限流限额管理处理器并注册管理员路由
func registerRateLimitHandlers(rg *gin.RouterGroup) {
	db := database.DB
	redisClient := redis.Client

	balSvc := balancesvc.NewBalanceService(db, redisClient)
	quotaLimiter := balancesvc.NewQuotaLimiter(db, redisClient)
	handler := admin.NewRateLimitHandler(balSvc, quotaLimiter)
	handler.Register(rg)
}

// notImplemented 未实现的占位处理器
func notImplemented(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"code":    0,
		"message": "not implemented",
	})
}

// registerDocHandlers 初始化文档服务并注册管理员路由
func registerDocHandlers(rg *gin.RouterGroup) {
	db := database.DB
	docSvc := docsvc.NewDocService(db)
	catSvc := docsvc.NewDocCategoryService(db)

	docHandler := admin.NewDocHandler(docSvc, catSvc)
	docHandler.Register(rg)
}

// registerPublicDocHandlers 初始化文档服务并注册公开路由（含新版 DocArticle 接口）
func registerPublicDocHandlers(rg *gin.RouterGroup) {
	db := database.DB
	docSvc := docsvc.NewDocService(db)
	catSvc := docsvc.NewDocCategoryService(db)
	articleSvc := docsvc.NewDocArticleService(db)

	handler := public.NewDocPublicHandler(docSvc, catSvc)
	handler.SetArticleService(articleSvc)
	handler.Register(rg)
}

// registerAdminReportHandlers 初始化报表服务并注册管理员路由
func registerAdminReportHandlers(rg *gin.RouterGroup) {
	db := database.DB
	redisClient := redis.Client

	permSvc := permission.NewPermissionService(db, redisClient)
	profitCalc := report.NewProfitCalculator(db)
	reportSvc := report.NewReportService(db, redisClient, profitCalc, permSvc)

	reportHandler := admin.NewReportHandler(reportSvc, profitCalc)
	reportHandler.Register(rg)
}

// StartStatsAggregator 启动后台统计聚合任务
func StartStatsAggregator() {
	db := database.DB
	if db == nil {
		return
	}
	logger := pkglogger.L
	if logger == nil {
		return
	}
	aggregator := report.NewStatsAggregator(db, logger)
	aggregator.Start(context.Background())
}

// RunCacheWarmer 执行缓存预热，服务启动时调用
func RunCacheWarmer() {
	db := database.DB
	if db == nil || redis.Client == nil {
		return
	}
	svc := cachesvc.NewCacheService(redis.Client)
	warmer := cachesvc.NewCacheWarmer(db, svc)
	warmer.WarmAll(context.Background())
}

// registerPaymentHandlers 初始化支付服务并注册支付路由
func registerPaymentHandlers(authorized *gin.RouterGroup, adminGroup *gin.RouterGroup) {
	db := database.DB
	redisClient := redis.Client
	logger := pkglogger.L
	if logger == nil {
		return
	}

	paymentSvc := paymentsvc.NewPaymentService(db, redisClient, logger)

	// 支付路由 (已认证用户)
	paymentHandler := paymenthandler.NewPaymentHandler(paymentSvc)
	authorized.POST("/payment/create", paymentHandler.Create)
	authorized.GET("/payment/query/:orderNo", paymentHandler.Query)
	authorized.GET("/payment/list", paymentHandler.List)

	// 退款需要 ADMIN 角色
	adminGroup.POST("/payment/refund/:orderNo", paymentHandler.Refund)
}

// registerPaymentCallbacks 初始化支付回调处理器 (无 JWT, 签名验证)
func registerPaymentCallbacks(v1 *gin.RouterGroup) {
	db := database.DB
	redisClient := redis.Client
	logger := pkglogger.L
	if logger == nil {
		return
	}

	paymentSvc := paymentsvc.NewPaymentService(db, redisClient, logger)
	callbackHandler := paymenthandler.NewCallbackHandler(paymentSvc)
	callbackHandler.RegisterCallbacks(v1)
}

// registerAuthHandlers 初始化认证服务并注册路由
func registerAuthHandlers(v1 *gin.RouterGroup) {
	db := database.DB
	redisClient := redis.Client

	jwtCfg := config.Global.JWT

	authService := authsvc.NewAuthService(db, redisClient, jwtCfg)
	handler := authhandler.NewAuthHandler(authService)

	authGroup := v1.Group("/auth")
	authGroup.POST("/register", handler.Register)
	authGroup.POST("/login", handler.Login)
	authGroup.POST("/refresh", handler.Refresh)

	// 登出需要 JWT 认证
	authGroup.POST("/logout", middleware.Auth(), handler.Logout)
}

// registerAdminUserHandlers 初始化管理员用户管理处理器
func registerAdminUserHandlers(rg *gin.RouterGroup) {
	db := database.DB
	svc := usersvc.NewUserService(db)
	handler := admin.NewUserHandler(svc)

	rg.GET("/users", handler.List)
	rg.POST("/users", handler.GetByID) // placeholder; admin POST users not fully defined
	rg.GET("/users/:id", handler.GetByID)
	rg.PUT("/users/:id", handler.Update)
	rg.DELETE("/users/:id", handler.Delete)

	// ========== 批量用户管理与角色变更 ==========
	batchHandler := admin.NewBatchUserHandler(db)
	rg.POST("/users/batch", batchHandler.BatchCreateUsers)       // 批量创建用户
	rg.PUT("/users/:id/role", batchHandler.UpdateUserRole)      // 更新用户角色
	rg.POST("/users/:id/recharge-rmb", batchHandler.RechargeUserRMB) // RMB充值
	rg.PUT("/users/:id/status", batchHandler.UpdateUserStatus)  // 更新用户状态

	// 管理员租户管理
	registerAdminTenantHandlers(rg)

	// --- Supplier CRUD ---
	registerAdminSupplierHandlers(rg)

	// --- Model Category CRUD ---
	registerAdminModelCategoryHandlers(rg)

	// --- AI Model CRUD ---
	registerAdminAIModelHandlers(rg)

	// --- Misc admin endpoints ---
	registerAdminMiscHandlers(rg)
}

// registerUserHandlers 初始化用户个人信息和 API Key 处理器
func registerUserHandlers(rg *gin.RouterGroup) {
	db := database.DB
	redisClient := redis.Client

	userSvc := usersvc.NewUserService(db)
	profileHandler := userhandler.NewProfileHandler(userSvc)

	rg.GET("/profile", profileHandler.GetProfile)
	rg.PUT("/profile", profileHandler.UpdateProfile)
	rg.PUT("/password", profileHandler.ChangePassword)
	rg.POST("/change-password", profileHandler.ChangePassword)

	apiKeySvc := apikey.NewApiKeyService(db, redisClient, config.Global.JWT.Secret)
	apiKeyHandler := userhandler.NewApiKeyHandler(apiKeySvc)

	rg.GET("/api-keys", apiKeyHandler.List)
	rg.POST("/api-keys", apiKeyHandler.Generate)
	rg.GET("/api-keys/:id/reveal", apiKeyHandler.Reveal)
	rg.PUT("/api-keys/:id", apiKeyHandler.Update)
	rg.PUT("/api-keys/:id/disable", apiKeyHandler.Disable)
	rg.PUT("/api-keys/:id/enable", apiKeyHandler.Enable)
	rg.DELETE("/api-keys/:id", apiKeyHandler.Revoke)

	// --- User available models ---
	availableModelsHandler := userhandler.NewAvailableModelsHandler(db)
	availableModelsHandler.Register(rg)

	// --- User usage/billing ---
	usageHandler := userhandler.NewUsageHandlerWithBalance(db, balancesvc.NewBalanceService(db, redisClient))
	usageHandler.Register(rg)
}

// registerAdminTenantHandlers 初始化管理员租户管理处理器
func registerAdminTenantHandlers(rg *gin.RouterGroup) {
	db := database.DB
	handler := admin.NewAdminTenantHandler(db)
	rg.GET("/tenants", handler.List)
	rg.POST("/tenants", handler.Create)
}

// registerPricingHandlers 初始化定价服务并注册管理员定价路由
func registerPricingHandlers(rg *gin.RouterGroup) {
	db := database.DB
	pricingCalc := pricing.NewPricingCalculator(db)
	pricingSvc := pricing.NewPricingService(db, pricingCalc)
	handler := admin.NewPricingHandler(pricingSvc, pricingCalc)

	rg.GET("/model-pricings", handler.ListModelPricings)
	rg.POST("/model-pricings", handler.CreateModelPricing)
	rg.POST("/model-pricings/repair", handler.RepairPricing)
	rg.PUT("/model-pricings/:id", handler.UpdateModelPricing)
	rg.DELETE("/model-pricings/:id", handler.DeleteModelPricing)
	rg.GET("/price-matrix", handler.GetPriceMatrix)
	rg.POST("/price-calculate", handler.CalculatePrice)

	discountHandler := admin.NewDiscountHandler(pricingSvc)
	rg.GET("/level-discounts", discountHandler.ListLevelDiscounts)
	rg.POST("/level-discounts", discountHandler.CreateLevelDiscount)
	rg.PUT("/level-discounts/:id", discountHandler.UpdateLevelDiscount)
	rg.DELETE("/level-discounts/:id", discountHandler.DeleteLevelDiscount)
	rg.GET("/agent-pricings", discountHandler.ListAgentPricings)
	rg.POST("/agent-pricings", discountHandler.CreateAgentPricing)
	rg.PUT("/agent-pricings/:id", discountHandler.UpdateAgentPricing)
	rg.DELETE("/agent-pricings/:id", discountHandler.DeleteAgentPricing)
}

// registerAdminSupplierHandlers 初始化供应商服务并注册管理员路由
func registerAdminSupplierHandlers(rg *gin.RouterGroup) {
	db := database.DB
	svc := suppliersvc.NewSupplierService(db)
	handler := admin.NewSupplierHandler(svc)

	rg.GET("/suppliers", handler.List)
	rg.POST("/suppliers", handler.Create)
	rg.GET("/suppliers/:id", handler.GetByID)
	rg.PUT("/suppliers/:id", handler.Update)
	rg.DELETE("/suppliers/:id", handler.Delete)
}

// registerAdminModelCategoryHandlers 初始化模型分类服务并注册路由
func registerAdminModelCategoryHandlers(rg *gin.RouterGroup) {
	db := database.DB
	svc := modelcatsvc.NewModelCategoryService(db)
	handler := admin.NewModelCategoryHandler(svc)

	rg.GET("/model-categories", handler.List)
	rg.POST("/model-categories", handler.Create)
	rg.GET("/model-categories/:id", handler.GetByID)
	rg.PUT("/model-categories/:id", handler.Update)
	rg.DELETE("/model-categories/:id", handler.Delete)
}

// registerAdminAIModelHandlers 初始化 AI 模型服务并注册路由
// taskBridge 非 nil 时（SetupBackend 模式）将重操作委派给 Worker
func registerAdminAIModelHandlers(rg *gin.RouterGroup) {
	db := database.DB
	svc := aimodelsvc.NewAIModelService(db)
	handler := admin.NewAIModelHandler(svc, taskBridge)

	rg.GET("/ai-models", handler.List)
	rg.GET("/ai-models/stats", handler.Stats) // 全量统计，不受分页限制
	rg.POST("/ai-models", handler.Create)
	rg.GET("/ai-models/:id", handler.GetByID)
	rg.PUT("/ai-models/:id", handler.Update)
	rg.DELETE("/ai-models/:id", handler.Delete)
	rg.POST("/ai-models/:id/verify", handler.Verify)         // 验证模型并上线
	rg.POST("/ai-models/:id/offline", handler.SetOffline)    // 将模型下线
	rg.POST("/ai-models/:id/reactivate", handler.Reactivate) // 手动重新上线（清空失败序列）

	// 模型可用性批量检测
	checker := aimodelsvc.NewModelChecker(db)
	checkHandler := admin.NewModelCheckHandler(checker, taskBridge)
	rg.POST("/models/batch-check", checkHandler.BatchCheck)              // SSE 实时进度（旧版自动下线，定时任务用）
	rg.POST("/models/batch-check-sync", checkHandler.BatchCheckSync)     // 同步返回（旧版自动下线）
	rg.POST("/models/check-preview", checkHandler.Preview)               // SSE 实时进度（dry-run 扫描预览，前端"一键检测"用）
	rg.POST("/models/check-preview-sync", checkHandler.PreviewSync)      // 同步返回（dry-run 扫描预览）
	rg.POST("/models/check-selected", checkHandler.CheckSelected)        // 检测勾选的模型
	rg.GET("/models/check-history", checkHandler.GetCheckHistory)        // 检测历史
	rg.GET("/models/check-latest", checkHandler.GetLatestSummary)        // 最近一次汇总
	// 后台检测任务（新版：一键检测创建任务，异步执行，按供应商查看结果）
	rg.POST("/models/check-task", checkHandler.CreateCheckTask)          // 创建并启动后台检测任务
	rg.GET("/models/check-tasks", checkHandler.GetCheckTasks)            // 任务列表
	rg.GET("/models/check-tasks/:id", checkHandler.GetCheckTaskDetail)   // 任务详情（含供应商分组结果）

	// 模型下线扫描与批量下线（结合公告系统）
	rg.POST("/models/deprecation-scan", handler.DeprecationScan) // 扫描可能下线的模型
	rg.POST("/models/bulk-deprecate", handler.BulkDeprecate)     // 批量下线 + 创建公告
	rg.GET("/models/scanned-offline", handler.ScanOfflineAll)    // 所有供应商扫描下线模型汇总

	// ===== 模型 k:v 标签系统 =====
	// 注意：batch/label-keys 路由必须在 :id 参数路由前注册，避免路径冲突
	labelHandler := admin.NewModelLabelHandler(db)
	rg.GET("/models/label-keys", labelHandler.ListKeys)          // 获取所有已用标签键（自动补全）
	rg.POST("/models/batch-labels", labelHandler.BatchAssign)    // 批量添加标签
	rg.DELETE("/models/batch-labels", labelHandler.BatchRemove)  // 批量移除标签
	rg.GET("/ai-models/:id/labels", labelHandler.ListByModel)    // 获取模型标签列表
	rg.POST("/ai-models/:id/labels", labelHandler.Upsert)        // 添加标签
	rg.DELETE("/ai-models/:id/labels", labelHandler.Remove)      // 删除标签
}

// registerQuotaHandlers 初始化额度/余额管理处理器
func registerQuotaHandlers(rg *gin.RouterGroup) {
	db := database.DB
	redisClient := redis.Client

	balSvc := balancesvc.NewBalanceService(db, redisClient)
	handler := admin.NewQuotaHandler(balSvc)
	handler.Register(rg)
}

// registerUserBalanceHandlers 初始化用户余额查询处理器
func registerUserBalanceHandlers(rg *gin.RouterGroup) {
	db := database.DB
	redisClient := redis.Client

	balSvc := balancesvc.NewBalanceService(db, redisClient)
	handler := userhandler.NewBalanceHandler(balSvc)
	handler.Register(rg)
}

// registerAdminMiscHandlers 初始化其他管理员处理器 (审计日志、每日统计)
func registerAdminMiscHandlers(rg *gin.RouterGroup) {
	db := database.DB
	handler := admin.NewMiscHandler(db)

	rg.GET("/audit-logs", handler.ListAuditLogs)
	rg.GET("/stats/daily", handler.DailyStats)
}

// registerAdminReferralHandlers 初始化邀请配置管理处理器
func registerAdminReferralHandlers(rg *gin.RouterGroup) {
	db := database.DB
	svc := referralsvc.NewReferralService(db)
	handler := admin.NewReferralConfigHandler(svc)
	handler.Register(rg)
}

// registerAdminCommissionOverrideHandlers 初始化特殊用户加佣 CRUD
func registerAdminCommissionOverrideHandlers(rg *gin.RouterGroup) {
	handler := admin.NewCommissionOverrideHandler()
	handler.Register(rg)
}

// registerAdminGuardHandlers 初始化 v3.1 反欺诈配置与一次性邮箱管理路由
func registerAdminGuardHandlers(rg *gin.RouterGroup) {
	svc := guardsvc.NewService(database.DB, redis.Client)
	handler := admin.NewGuardConfigHandler(svc)
	handler.Register(rg)
}

// registerAdminWithdrawalHandlers 初始化 v3.1 提现审核路由
func registerAdminWithdrawalHandlers(rg *gin.RouterGroup) {
	balSvc := balancesvc.NewBalanceService(database.DB, redis.Client)
	svc := withdrawalsvc.NewService(database.DB, balSvc)
	handler := admin.NewWithdrawalAdminHandler(svc)
	handler.Register(rg)
}

// registerAdminConfigAuditHandlers 初始化 v3.1 配置审计日志查询路由
func registerAdminConfigAuditHandlers(rg *gin.RouterGroup) {
	svc := configauditsvc.NewService(database.DB)
	handler := admin.NewConfigAuditHandler(svc)
	handler.Register(rg)
}

// registerUserWithdrawalHandlers 初始化 v3.1 用户提现申请路由
func registerUserWithdrawalHandlers(rg *gin.RouterGroup) {
	balSvc := balancesvc.NewBalanceService(database.DB, redis.Client)
	svc := withdrawalsvc.NewService(database.DB, balSvc)
	handler := userhandler.NewWithdrawalHandler(svc)
	handler.Register(rg)
}

// registerUserReferralHandlers 初始化用户邀请处理器
func registerUserReferralHandlers(rg *gin.RouterGroup) {
	db := database.DB
	svc := referralsvc.NewReferralService(db)
	handler := userhandler.NewReferralHandler(svc)
	handler.Register(rg)
}

// registerPaymentConfigHandlers 注册支付配置管理路由（管理员）
func registerPaymentConfigHandlers(rg *gin.RouterGroup) {
	db := database.DB
	svc := paymentsvc.NewPaymentConfigService(db)
	handler := admin.NewPaymentConfigHandler(svc)
	handler.Register(rg)
}

// registerLocaleHandler 注册 IP 地理位置语言检测接口
func registerLocaleHandler(rg *gin.RouterGroup) {
	geoService := geosvc.NewGeoService(redis.Client)
	localeHandler := public.NewLocaleHandler(geoService)
	localeHandler.Register(rg)
}

// registerCacheHandlers 初始化缓存管理处理器并注册管理员路由
func registerCacheHandlers(rg *gin.RouterGroup) {
	db := database.DB
	redisClient := redis.Client

	cacheSvc := cachesvc.NewCacheService(redisClient)
	warmer := cachesvc.NewCacheWarmer(db, cacheSvc)
	handler := admin.NewCacheHandler(cacheSvc, warmer)
	handler.Register(rg)
}

// registerPublicPaymentMethodsHandler 注册公开付款方式查询接口
func registerPublicPaymentMethodsHandler(rg *gin.RouterGroup) {
	db := database.DB
	svc := paymentsvc.NewPaymentConfigService(db)
	handler := admin.NewPaymentConfigHandler(svc)
	rg.GET("/payment-methods", handler.GetActivePaymentMethods)
}

// registerPublicConfigHandlers 注册公开配置接口（邀请返佣 + 注册赠送）
func registerPublicConfigHandlers(rg *gin.RouterGroup) {
	db := database.DB
	referralSvc := referralsvc.NewReferralService(db)
	balanceSvc := balancesvc.NewBalanceService(db, redis.Client)
	handler := public.NewConfigHandler(referralSvc, balanceSvc)
	handler.Register(rg)
}

// registerSetupHandlers 注册安装向导路由组 (/api/v1/setup/)
func registerSetupHandlers(v1 *gin.RouterGroup) {
	db := database.DB
	svc := setupsvc.NewSetupService(db)
	handler := setuphandler.NewSetupHandler(svc)

	setupGroup := v1.Group("/setup")
	handler.Register(setupGroup)
}

// registerMCPHandlers 注册 MCP 协议端点
// MCP 端点使用 API Key 认证（在 handler 内部处理认证）
func registerMCPHandlers(v1 *gin.RouterGroup) {
	db := database.DB
	redisClient := redis.Client

	// 创建 MCP 协议服务端
	mcpServer := mcp.NewMCPServer(db, redisClient)

	// 初始化依赖服务
	apiKeySvc := apikey.NewApiKeyService(db, redisClient)
	balSvc := balancesvc.NewBalanceService(db, redisClient)
	openSvc := openapi.NewOpenAPIService(db)

	// 注册所有 Tools 和 Resources
	mcp.RegisterAllTools(mcpServer, db, balSvc, apiKeySvc, openSvc)
	mcp.RegisterAllResources(mcpServer, db, openSvc)

	// MCP 路由组（不使用全局认证中间件，由 handler 自行处理认证）
	mcpGroup := v1.Group("/mcp")
	{
		// MCP HTTP 处理器
		mcpH := mcphandler.NewMCPHandler(mcpServer, db)
		mcpH.Register(mcpGroup)

		// MCP 配置生成器
		configH := mcphandler.NewConfigHandler()
		configH.Register(mcpGroup)
	}
}

// registerOpenAICompatibleRoutes 注册 OpenAI 兼容的 /v1/ 路由组
// 提供 /v1/chat/completions、/v1/completions (FIM)、/v1/models、/v1/embeddings
// 使用 Bearer Token (API Key) 认证，复用现有 OpenAPI 认证中间件
func registerOpenAICompatibleRoutes(r *gin.Engine) {
	db := database.DB
	redisClient := redis.Client

	// ========== OpenAI Compatible Routes (/v1/) - Coding Plan ==========
	v1Group := r.Group("/v1")
	// 使用 Bearer Token 认证（复用 OpenAPI Auth 中间件）
	v1Group.Use(middleware.OpenAPIAuth(db))

	// --- /v1/models --- 模型列表（OpenAI 格式）
	modelsHandler := v1handler.NewModelsHandler(db)
	modelsHandler.Register(v1Group)

	// --- /v1/chat/completions + /v1/completions --- 补全端点
	groupSvc := channelsvc.NewChannelGroupService(db)
	backupSvc := channelsvc.NewBackupService(db, redisClient)
	channelRouter := channelsvc.NewChannelRouter(db, redisClient, groupSvc, backupSvc)

	pricingCalc := pricing.NewPricingCalculator(db)
	apiKeySvc := apikey.NewApiKeyService(db, redisClient)
	balSvc := balancesvc.NewBalanceService(db, redisClient)
	commCalc := referralsvc.NewCommissionCalculator(db)
	codSvc := codingsvc.NewCodingService(db)

	paramSvc := parammapping.NewParamMappingService(db)
	tpmLimiter := middleware.NewTPMLimiter(db, redisClient)
	completionsHandler := v1handler.NewCompletionsHandler(
		db, codSvc, channelRouter,
		pricingCalc, apiKeySvc, balSvc, commCalc, paramSvc, tpmLimiter,
	)
	completionsHandler.Register(v1Group)

	// --- /v1/embeddings --- 向量嵌入端点（透传至上游，按 PricingUnit 计费）
	embeddingsHandler := v1handler.NewEmbeddingsHandler(
		db, channelRouter, apiKeySvc, balSvc, pricingCalc,
	)
	embeddingsHandler.Register(v1Group)

	// --- /v1/images/generations --- 图像生成端点
	imagesHandler := v1handler.NewImagesHandler(
		db, codSvc, channelRouter, apiKeySvc, balSvc, paramSvc, pricingCalc,
	)
	imagesHandler.Register(v1Group)

	// --- /v1/videos/generations --- 视频生成端点
	videosHandler := v1handler.NewVideosHandler(
		db, codSvc, channelRouter, apiKeySvc, balSvc, paramSvc, pricingCalc,
	)
	videosHandler.Register(v1Group)

	// --- /v1/audio/speech + /v1/audio/transcriptions --- 语音合成 / 识别端点
	audioHandler := v1handler.NewAudioHandler(
		db, codSvc, channelRouter, apiKeySvc, balSvc, paramSvc, pricingCalc,
	)
	audioHandler.Register(v1Group)
}

// registerUserMemberHandlers 初始化会员等级服务并注册用户会员路由
func registerUserMemberHandlers(rg *gin.RouterGroup) {
	db := database.DB
	redisClient := redis.Client

	svc := membersvc.NewMemberLevelService(db, redisClient)
	handler := userhandler.NewMemberHandler(svc)
	handler.Register(rg)
}

// registerLevelAdminHandlers 初始化等级管理服务并注册管理员路由
func registerLevelAdminHandlers(rg *gin.RouterGroup) {
	db := database.DB
	redisClient := redis.Client

	memberSvc := membersvc.NewMemberLevelService(db, redisClient)
	handler := admin.NewLevelAdminHandler(memberSvc)
	handler.Register(rg)
}

// registerExchangeRateHandlers 初始化汇率服务并注册管理员路由
func registerExchangeRateHandlers(rg *gin.RouterGroup) {
	db := database.DB
	redisClient := redis.Client

	svc := paymentsvc.NewExchangeService(db, redisClient)
	handler := admin.NewExchangeRateHandler(svc)
	handler.Register(rg)
}

// registerReconciliationHandler 注册对账报告路由
func registerReconciliationHandler(rg *gin.RouterGroup) {
	db := database.DB
	handler := admin.NewMiscHandler(db)
	rg.GET("/reconciliation", handler.ReconciliationReport)
}

// registerPublicModelHandlers 注册公开模型列表接口（无需认证）
// 供前端 /models 页面展示所有已验证上线的 AI 模型
// 只返回 status=online 且 is_active=true 的模型
func registerPublicModelHandlers(rg *gin.RouterGroup) {
	db := database.DB
	svc := aimodelsvc.NewAIModelService(db)
	handler := admin.NewAIModelHandler(svc)
	// GET /api/v1/public/models — 公开模型列表（只返回online模型）
	rg.GET("/models", handler.PublicList)
}

// registerParamSupportHandler 注册参数支持情况查询接口（无需认证）
// GET /api/v1/public/param-support?supplier={supplier_code}
func registerParamSupportHandler(rg *gin.RouterGroup) {
	svc := parammapping.NewParamMappingService(database.DB)
	h := public.NewParamSupportHandler(svc)
	rg.GET("/param-support", h.GetParamSupport)
}

// registerPartnerApplicationHandler 注册合作伙伴线索申请接口（无需认证，无缓存）
// POST /api/v1/public/partner-applications
func registerPartnerApplicationHandler(rg *gin.RouterGroup) {
	h := public.NewPartnerApplicationHandler(database.DB)
	h.Register(rg)
}

// registerReferralClickHandler 注册邀请链接点击追踪接口（无需认证，无缓存）
// POST /api/v1/public/referral/click
func registerReferralClickHandler(rg *gin.RouterGroup) {
	h := public.NewReferralClickHandler(database.DB)
	h.Register(rg)
}

// registerModelSyncHandlers 注册模型自动发现与同步管理路由
// POST   /admin/models/sync              — 全量同步所有活跃渠道
// POST   /admin/models/sync/:channelId   — 单渠道同步
// PUT    /admin/models/batch-status      — 批量修改模型启用状态
// DELETE /admin/models/batch-delete      — 批量删除模型
// GET    /admin/channel-models           — 渠道-模型映射列表
// PUT    /admin/channel-models/:id       — 编辑映射（火山引擎 ep-xxx 映射标准模型）
func registerModelSyncHandlers(rg *gin.RouterGroup) {
	db := database.DB
	handler := admin.NewModelSyncHandler(db, taskBridge)

	// 批量操作路由必须在 :id 参数路由之前注册，避免 Gin 将 "batch-*" 匹配为 :id
	rg.PUT("/models/batch-status", handler.BatchUpdateModelStatus)
	rg.DELETE("/models/batch-delete", handler.BatchDeleteModels)
	rg.PUT("/models/batch-selling-price", handler.BatchUpdateSellingPrice)
	rg.PUT("/models/batch-discount", handler.BatchUpdateDiscount)
	rg.POST("/models/fill-selling-prices", handler.FillSellingPrices)

	rg.POST("/models/sync", handler.SyncAll)
	rg.POST("/models/sync/:channelId", handler.SyncByChannel)
	rg.GET("/channel-models", handler.ListChannelModels)
	rg.PUT("/channel-models/:id", handler.UpdateChannelModel)
}

// registerCustomChannelHandlers 注册自定义渠道管理路由
func registerCustomChannelHandlers(rg *gin.RouterGroup) {
	db := database.DB
	handler := admin.NewCustomChannelHandler(db, redis.Client)

	rg.GET("/custom-channels", handler.List)
	rg.POST("/custom-channels", handler.Create)
	// 具体路径路由必须在参数路由之前注册，避免 Gin 将 "default" 匹配为 :id
	rg.POST("/custom-channels/default/refresh", handler.RefreshDefault)
	rg.GET("/custom-channels/default/refresh/status", handler.GetRefreshStatus)
	rg.PUT("/custom-channels/:id", handler.Update)
	rg.DELETE("/custom-channels/:id", handler.Delete)
	rg.PATCH("/custom-channels/:id/toggle", handler.Toggle)
	rg.PATCH("/custom-channels/:id/set-default", handler.SetDefault)
	rg.PUT("/custom-channels/:id/access", handler.UpdateAccess)
	rg.POST("/custom-channels/:id/routes/batch", handler.BatchRoutes)
	rg.POST("/custom-channels/:id/routes/import", handler.ImportRoutes)
}

// registerUserAvailableChannelsHandlers 注册用户可用渠道查询路由
func registerUserAvailableChannelsHandlers(rg *gin.RouterGroup) {
	db := database.DB
	handler := userhandler.NewAvailableChannelsHandler(db)
	handler.Register(rg)
}

// registerPriceScraperHandlers 注册价格爬虫管理路由
// POST /admin/models/preview-prices  — 预览价格变更（爬取并对比，不写DB）
// POST /admin/models/apply-prices    — 应用价格更新（事务写入）
// GET  /admin/models/price-sync-logs — 查询价格同步历史日志
func registerPriceScraperHandlers(rg *gin.RouterGroup) {
	db := database.DB
	scraperSvc := pricescraper.NewPriceScraperService(db)
	handler := admin.NewPriceScraperHandler(scraperSvc)

	// 注意: 这些路由放在 :id 参数路由之前注册，避免路径冲突
	rg.POST("/models/preview-prices", handler.PreviewPrices)
	rg.POST("/models/apply-prices", handler.ApplyPrices)
	rg.GET("/models/price-sync-logs", handler.GetSyncLogs)
}

// registerTaskHandlers 注册后台任务管理路由
func registerTaskHandlers(rg *gin.RouterGroup) {
	db := database.DB
	taskSvc := task.NewTaskService(db)
	handler := admin.NewTaskHandler(taskSvc)

	rg.POST("/tasks", handler.CreateTask)
	rg.GET("/tasks", handler.ListTasks)
	rg.GET("/tasks/:id", handler.GetTask)
	rg.POST("/tasks/:id/cancel", handler.CancelTask)
	rg.POST("/tasks/:id/apply-prices", handler.ApplyTaskPrices)
}

// registerParamMappingHandlers 注册平台参数映射管理路由
func registerParamMappingHandlers(rg *gin.RouterGroup) {
	svc := parammapping.NewParamMappingService(database.DB)
	handler := admin.NewParamMappingHandler(svc)

	rg.GET("/param-mappings", handler.ListParams)
	rg.GET("/param-mappings/:id", handler.GetParam)
	rg.POST("/param-mappings", handler.CreateParam)
	rg.PUT("/param-mappings/:id", handler.UpdateParam)
	rg.DELETE("/param-mappings/:id", handler.DeleteParam)
	rg.POST("/param-mappings/:id/mappings", handler.UpsertMapping)
	rg.DELETE("/param-mappings/mappings/:mappingId", handler.DeleteMapping)
	rg.GET("/param-mappings/supplier/:code", handler.GetMappingsBySupplier)
	rg.PUT("/param-mappings/supplier/:code", handler.BatchUpdateMappings)
}
