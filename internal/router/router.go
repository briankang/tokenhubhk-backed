package router

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/config"
	"tokenhub-server/internal/database"
	"tokenhub-server/internal/handler/admin"
	agenthandler "tokenhub-server/internal/handler/agent"
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
	agentlevelsvc "tokenhub-server/internal/service/agent"
	modelcatsvc "tokenhub-server/internal/service/modelcategory"
	orchsvc "tokenhub-server/internal/service/orchestration"
	"tokenhub-server/internal/service/permission"
	paymentsvc "tokenhub-server/internal/service/payment"
	balancesvc "tokenhub-server/internal/service/balance"
	"tokenhub-server/internal/service/pricing"
	referralsvc "tokenhub-server/internal/service/referral"
	"tokenhub-server/internal/service/report"
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
	"github.com/spf13/viper"
)

// Setup 注册所有路由和中间件到 Gin 引擎
func Setup(r *gin.Engine) {
	// 初始化白标服务
	domainResolver := whitelabel.NewDomainResolver(database.DB, redis.Client)
	wlService := whitelabel.NewWhiteLabelService(database.DB, redis.Client)

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
		wlHandler := agenthandler.NewWhiteLabelHandler(wlService, domainResolver)
		wlHandler.RegisterPublic(publicGroup)

		// --- Public payment methods ---
		registerPublicPaymentMethodsHandler(publicGroup)

		// --- IP 地理位置语言检测 ---
		registerLocaleHandler(publicGroup)

		// --- 公开模型列表（无需认证，供前端 /models 页面使用） ---
		registerPublicModelHandlers(publicGroup)

		// --- 代理申请公开接口 ---
		registerAgentApplicationPublicHandlers(publicGroup)

		// --- 代理等级公开接口（无需认证，供 /agent-panel 页面展示） ---
		registerPublicAgentLevelHandlers(publicGroup)
	}

	// --- 支付回调 (无 JWT, 签名验证) ---
	registerPaymentCallbacks(v1)

	// --- 已认证路由 ---
	authorized := v1.Group("")
	authorized.Use(middleware.Auth())
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

		// ========== Model Commission Routes (模型佣金配置) ==========
		registerModelCommissionHandlers(adminGroup)

		// ========== Agent Application Routes (代理申请审核) ==========
		registerAgentApplicationAdminHandlers(adminGroup)

		// ========== Custom Channel Routes (自定义渠道管理) ==========
		registerCustomChannelHandlers(adminGroup)

		// ========== Price Scraper Routes (价格爬虫管理) ==========
		registerPriceScraperHandlers(adminGroup)

		// ========== Model Sync Routes (模型自动发现与同步) ==========
		registerModelSyncHandlers(adminGroup)
	}

	// 代理商路由 (需要 AGENT_L* 角色)
	agentGroup := authorized.Group("/agent")
	agentGroup.Use(middleware.RequireRole("AGENT_L1", "AGENT_L2", "AGENT_L3"))
	{
		// --- Agent dashboard/pricing/usage/stats ---
		registerAgentDashboardHandlers(agentGroup)

		// --- Whitelabel configuration ---
		registerAgentWhiteLabelHandlers(agentGroup, wlService, domainResolver)

		// --- Sub-agent management ---
		registerAgentTenantHandlers(agentGroup)

		// --- Agent user management ---
		registerAgentUserHandlers(agentGroup)

		// --- Agent report, keys, and consumption ---
		registerAgentReportHandlers(agentGroup)

		// --- Agent commissions ---
		registerAgentCommissionHandlers(agentGroup)

		// --- 代理等级相关（申请/档案/进度/团队/提现）---
		registerAgentLevelHandlers(agentGroup)
	}

	// 用户路由 (任何已认证用户)
	userGroup := authorized.Group("/user")
	{
		registerUserHandlers(userGroup)
		registerUserBalanceHandlers(userGroup)
		registerUserReferralHandlers(userGroup)
		registerUserAvailableChannelsHandlers(userGroup)

		// --- 会员等级相关（档案/等级列表/升级进度）---
		registerUserMemberHandlers(userGroup)
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

// registerAgentWhiteLabelHandlers 初始化白标处理器并注册代理商路由
func registerAgentWhiteLabelHandlers(rg *gin.RouterGroup, svc *whitelabel.WhiteLabelService, resolver *whitelabel.DomainResolver) {
	handler := agenthandler.NewWhiteLabelHandler(svc, resolver)
	handler.RegisterAgent(rg)
}

// registerAgentTenantHandlers 初始化子代理管理处理器
func registerAgentTenantHandlers(rg *gin.RouterGroup) {
	handler := agenthandler.NewAgentTenantHandler(database.DB)
	handler.Register(rg)
}

// registerAgentUserHandlers 初始化代理商用户管理处理器
func registerAgentUserHandlers(rg *gin.RouterGroup) {
	handler := agenthandler.NewAgentUserHandler(database.DB)
	handler.Register(rg)
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

// registerAgentReportHandlers 初始化报表服务并注册代理商报表/密钥/消费路由
func registerAgentReportHandlers(rg *gin.RouterGroup) {
	db := database.DB
	redisClient := redis.Client

	permSvc := permission.NewPermissionService(db, redisClient)
	profitCalc := report.NewProfitCalculator(db)
	reportSvc := report.NewReportService(db, redisClient, profitCalc, permSvc)

	agentReportHandler := agenthandler.NewReportHandler(reportSvc, profitCalc, permSvc)
	agentReportHandler.Register(rg)

	keysHandler := agenthandler.NewKeysHandler(db, permSvc, reportSvc)
	keysHandler.Register(rg)

	consumptionHandler := agenthandler.NewConsumptionHandler(reportSvc)
	consumptionHandler.Register(rg)
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

	apiKeySvc := apikey.NewApiKeyService(db, redisClient)
	apiKeyHandler := userhandler.NewApiKeyHandler(apiKeySvc)

	rg.GET("/api-keys", apiKeyHandler.List)
	rg.POST("/api-keys", apiKeyHandler.Generate)
	rg.PUT("/api-keys/:id", apiKeyHandler.Update)
	rg.DELETE("/api-keys/:id", apiKeyHandler.Revoke)

	// --- User available models ---
	availableModelsHandler := userhandler.NewAvailableModelsHandler(db)
	availableModelsHandler.Register(rg)

	// --- User usage/billing ---
	usageHandler := userhandler.NewUsageHandler(db)
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
func registerAdminAIModelHandlers(rg *gin.RouterGroup) {
	db := database.DB
	svc := aimodelsvc.NewAIModelService(db)
	handler := admin.NewAIModelHandler(svc)

	rg.GET("/ai-models", handler.List)
	rg.POST("/ai-models", handler.Create)
	rg.GET("/ai-models/:id", handler.GetByID)
	rg.PUT("/ai-models/:id", handler.Update)
	rg.DELETE("/ai-models/:id", handler.Delete)
	rg.POST("/ai-models/:id/verify", handler.Verify)     // 验证模型并上线
	rg.POST("/ai-models/:id/offline", handler.SetOffline) // 将模型下线
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

// registerAgentDashboardHandlers 初始化代理商仪表盘/定价/用量/统计处理器
func registerAgentDashboardHandlers(rg *gin.RouterGroup) {
	db := database.DB
	redisClient := redis.Client

	permSvc := permission.NewPermissionService(db, redisClient)
	profitCalc := report.NewProfitCalculator(db)
	reportSvc := report.NewReportService(db, redisClient, profitCalc, permSvc)

	pricingCalc := pricing.NewPricingCalculator(db)
	pricingSvc := pricing.NewPricingService(db, pricingCalc)

	handler := agenthandler.NewDashboardHandler(reportSvc, pricingSvc)
	handler.Register(rg)
}

// registerAdminReferralHandlers 初始化邀请配置管理处理器
func registerAdminReferralHandlers(rg *gin.RouterGroup) {
	db := database.DB
	svc := referralsvc.NewReferralService(db)
	handler := admin.NewReferralConfigHandler(svc)
	handler.Register(rg)
}

// registerAgentCommissionHandlers 初始化代理商佣金处理器
func registerAgentCommissionHandlers(rg *gin.RouterGroup) {
	db := database.DB
	svc := referralsvc.NewReferralService(db)
	handler := agenthandler.NewCommissionHandler(svc)
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

	// --- /v1/embeddings --- 向量嵌入（占位）
	embeddingsHandler := v1handler.NewEmbeddingsHandler()
	embeddingsHandler.Register(v1Group)

	// --- /v1/chat/completions + /v1/completions --- 补全端点
	groupSvc := channelsvc.NewChannelGroupService(db)
	backupSvc := channelsvc.NewBackupService(db, redisClient)
	channelRouter := channelsvc.NewChannelRouter(db, redisClient, groupSvc, backupSvc)

	pricingCalc := pricing.NewPricingCalculator(db)
	apiKeySvc := apikey.NewApiKeyService(db, redisClient)
	balSvc := balancesvc.NewBalanceService(db, redisClient)
	commCalc := referralsvc.NewCommissionCalculator(db)
	codSvc := codingsvc.NewCodingService(db)

	completionsHandler := v1handler.NewCompletionsHandler(
		db, codSvc, channelRouter,
		pricingCalc, apiKeySvc, balSvc, commCalc,
	)
	completionsHandler.Register(v1Group)
}

// registerUserMemberHandlers 初始化会员等级服务并注册用户会员路由
func registerUserMemberHandlers(rg *gin.RouterGroup) {
	db := database.DB
	redisClient := redis.Client

	svc := membersvc.NewMemberLevelService(db, redisClient)
	handler := userhandler.NewMemberHandler(svc)
	handler.Register(rg)
}

// registerAgentLevelHandlers 初始化代理等级服务并注册代理等级路由
func registerAgentLevelHandlers(rg *gin.RouterGroup) {
	db := database.DB
	redisClient := redis.Client

	svc := agentlevelsvc.NewAgentLevelService(db, redisClient)
	handler := agenthandler.NewAgentLevelHandler(svc)
	handler.Register(rg)
}

// registerLevelAdminHandlers 初始化等级管理服务并注册管理员路由
func registerLevelAdminHandlers(rg *gin.RouterGroup) {
	db := database.DB
	redisClient := redis.Client

	memberSvc := membersvc.NewMemberLevelService(db, redisClient)
	agentSvc := agentlevelsvc.NewAgentLevelService(db, redisClient)
	handler := admin.NewLevelAdminHandler(memberSvc, agentSvc)
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

// registerModelCommissionHandlers 注册模型佣金配置管理路由
func registerModelCommissionHandlers(rg *gin.RouterGroup) {
	db := database.DB
	handler := admin.NewModelCommissionHandler(db)
	handler.Register(rg)
}

// registerAgentApplicationPublicHandlers 注册代理申请公开接口
// POST /api/v1/public/agent-applications — 提交申请（无需认证）
func registerAgentApplicationPublicHandlers(rg *gin.RouterGroup) {
	db := database.DB
	handler := public.NewAgentApplicationHandler(db)
	handler.RegisterPublic(rg)
}

// registerAgentApplicationAdminHandlers 注册代理申请管理接口
// GET  /api/v1/admin/agent-applications — 申请列表
// PUT  /api/v1/admin/agent-applications/:id/review — 审核
func registerAgentApplicationAdminHandlers(rg *gin.RouterGroup) {
	db := database.DB
	handler := public.NewAgentApplicationHandler(db)
	handler.RegisterAdmin(rg)
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
	handler := admin.NewModelSyncHandler(db)

	// 批量操作路由必须在 :id 参数路由之前注册，避免 Gin 将 "batch-*" 匹配为 :id
	rg.PUT("/models/batch-status", handler.BatchUpdateModelStatus)
	rg.DELETE("/models/batch-delete", handler.BatchDeleteModels)

	rg.POST("/models/sync", handler.SyncAll)
	rg.POST("/models/sync/:channelId", handler.SyncByChannel)
	rg.GET("/channel-models", handler.ListChannelModels)
	rg.PUT("/channel-models/:id", handler.UpdateChannelModel)
}

// registerCustomChannelHandlers 注册自定义渠道管理路由
func registerCustomChannelHandlers(rg *gin.RouterGroup) {
	db := database.DB
	handler := admin.NewCustomChannelHandler(db)

	rg.GET("/custom-channels", handler.List)
	rg.POST("/custom-channels", handler.Create)
	// 具体路径路由必须在参数路由之前注册，避免 Gin 将 "default" 匹配为 :id
	rg.POST("/custom-channels/default/refresh", handler.RefreshDefault)
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

// registerPublicAgentLevelHandlers 注册代理等级公开接口
// GET /api/v1/public/agent-levels — 查询所有启用的代理等级（无需认证）
func registerPublicAgentLevelHandlers(rg *gin.RouterGroup) {
	db := database.DB
	handler := public.NewAgentLevelPublicHandler(db)
	rg.GET("/agent-levels", handler.GetPublicAgentLevels)
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
