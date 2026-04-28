package router

import (
	"context"
	"net/http"
	"time"

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
	auditmw "tokenhub-server/internal/middleware/audit"
	"tokenhub-server/internal/pkg/health"
	pkglogger "tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/pkg/redis"
	aimodelsvc "tokenhub-server/internal/service/aimodel"
	"tokenhub-server/internal/service/apikey"
	auditsvc "tokenhub-server/internal/service/audit"
	authsvc "tokenhub-server/internal/service/auth"
	"tokenhub-server/internal/service/authlog"
	balancesvc "tokenhub-server/internal/service/balance"
	channelsvc "tokenhub-server/internal/service/channel"
	configauditsvc "tokenhub-server/internal/service/configaudit"
	docsvc "tokenhub-server/internal/service/doc"
	emailsvc "tokenhub-server/internal/service/email"
	exchangesvc "tokenhub-server/internal/service/exchange"
	guardsvc "tokenhub-server/internal/service/guard"
	invoicesvc "tokenhub-server/internal/service/invoice"
	membersvc "tokenhub-server/internal/service/member"
	modelaliassvc "tokenhub-server/internal/service/modelalias"
	modelapidocsvc "tokenhub-server/internal/service/modelapidoc"
	modelcatsvc "tokenhub-server/internal/service/modelcategory"
	orchsvc "tokenhub-server/internal/service/orchestration"
	paymentsvc "tokenhub-server/internal/service/payment"
	"tokenhub-server/internal/service/permission"
	"tokenhub-server/internal/service/pricing"
	privacysvc "tokenhub-server/internal/service/privacy"
	ratelimitsvc "tokenhub-server/internal/service/ratelimit"
	referralsvc "tokenhub-server/internal/service/referral"
	"tokenhub-server/internal/service/report"
	setupsvc "tokenhub-server/internal/service/setup"
	smssvc "tokenhub-server/internal/service/sms"
	suppliersvc "tokenhub-server/internal/service/supplier"
	usersvc "tokenhub-server/internal/service/user"
	whitelabel "tokenhub-server/internal/service/whitelabel"
	withdrawalsvc "tokenhub-server/internal/service/withdrawal"

	"github.com/spf13/viper"
	"gorm.io/gorm"
	mcphandler "tokenhub-server/internal/handler/mcp"
	openhandler "tokenhub-server/internal/handler/openapi"
	"tokenhub-server/internal/handler/public"
	supporthandler "tokenhub-server/internal/handler/support"
	v1handler "tokenhub-server/internal/handler/v1"
	"tokenhub-server/internal/mcp"
	cachesvc "tokenhub-server/internal/service/cache"
	codingsvc "tokenhub-server/internal/service/coding"
	geosvc "tokenhub-server/internal/service/geo"
	openapi "tokenhub-server/internal/service/openapi"
	"tokenhub-server/internal/service/parammapping"
	pricescraper "tokenhub-server/internal/service/pricescraper"
	supportsvc "tokenhub-server/internal/service/support"
	"tokenhub-server/internal/service/task"
	"tokenhub-server/internal/taskqueue"
)

// taskBridge 全局 SSE 桥接器，由 SetupBackend() 初始化。
// 非 nil 时 admin 重操作 handler 将通过此桥接器委派给 Worker。
// Setup()（单体模式）中为 nil，handler 在本进程内执行。
var taskBridge *taskqueue.SSEBridge

// buildExchangeRateConfig 构造 ExchangeRateConfig
// v3.2.3: 先从 DB (system_configs 表) 读取，覆盖 config.Global 默认值
// 敏感字段 AppSecret 通过 paymentCfgSvc 解密
func buildExchangeRateConfig(db *gorm.DB, paymentCfgSvc *paymentsvc.PaymentConfigService) exchangesvc.Config {
	fallback := exchangesvc.Config{
		PrimaryURL:     config.Global.ExchangeRate.PrimaryURL,
		BackupURL:      config.Global.ExchangeRate.BackupURL,
		PublicURL:      config.Global.ExchangeRate.PublicURL,
		AppCode:        config.Global.ExchangeRate.AppCode,
		AppKey:         config.Global.ExchangeRate.AppKey,
		AppSecret:      config.Global.ExchangeRate.AppSecret,
		CacheTTL:       time.Duration(config.Global.ExchangeRate.CacheTTL) * time.Second,
		DefaultRate:    config.Global.ExchangeRate.DefaultRate,
		RequestTimeout: time.Duration(config.Global.ExchangeRate.RequestTimeout) * time.Second,
	}
	var decryptFn exchangesvc.DecryptFn
	if paymentCfgSvc != nil {
		decryptFn = paymentCfgSvc.DecryptCiphertext
	}
	return exchangesvc.LoadConfigFromDB(db, fallback, decryptFn)
}

// Setup 注册所有路由和中间件到 Gin 引擎
func Setup(r *gin.Engine) {
	// 初始化全局 CommissionRule resolver（返佣特殊规则决策器）
	ensureCommissionResolver()

	// 初始化白标服务
	domainResolver := whitelabel.NewDomainResolver(database.DB, redis.Client)

	// 初始化全局审计服务（启动异步 consumer goroutine）
	auditSvc := auditsvc.InitDefault(database.DB, context.Background())

	// 初始化全局限流事件记录器（启动异步 consumer goroutine）
	ratelimitsvc.InitDefault(database.DB, context.Background())

	// 初始化全局用户认证日志记录器（启动异步 consumer goroutine）
	authlog.InitDefault(database.DB, context.Background())

	// v5.1: 初始化风控服务，供反滥用中间件使用
	gSvc := guardsvc.NewService(database.DB, redis.Client)

	// 全局中间件
	r.Use(middleware.Recovery())
	r.Use(middleware.CORS())
	r.Use(middleware.RequestLogger())
	r.Use(middleware.I18n())
	r.Use(middleware.PerfMark("i18n"))
	r.Use(middleware.TenantResolveMiddleware(domainResolver, viper.GetString("server.platform_domain")))
	r.Use(middleware.PerfMark("tenant_resolve"))
	// 多层级限流中间件（在认证之前，基于 IP/用户/API Key 限流）
	r.Use(middleware.MultiLevelRateLimiter())
	r.Use(middleware.PerfMark("rate_limit"))
	// 审计日志中间件（白名单路由命中才记录，仅 2xx 写操作；异步入队不阻塞）
	r.Use(auditmw.AuditLog(auditSvc))
	r.Use(middleware.PerfMark("audit"))
	r.GET("/livez", health.LivenessHandler("monolith"))
	r.GET("/readyz", health.ReadinessHandler("monolith", database.DB, redis.Client))

	// 健康检查端点 — 分层语义 (2026-04-21)
	//  /health  = liveness（兼容历史 probe），永远 200，只要进程活着
	//  /livez   = liveness 显式端点
	//  /readyz  = readiness，浅层 Ping DB+Redis，失败 503 → K8s 摘流量但不重启
	r.GET("/health", health.LivenessHandler("monolith"))

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

		// --- 公开模型列表（无需认证，供前端 /models 页面使用） ---
		registerPublicModelHandlers(publicGroup)

		// --- 参数支持情况查询 ---
		registerParamSupportHandler(publicGroup)
		registerModelAPIDocPublicHandlers(publicGroup)

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
		// --- IP 地理位置语言检测（无缓存：每个 IP 需独立判定，
		//     Redis 层 geo:ip:{ip} 已提供 1 年命中级缓存）---
		registerLocaleHandler(publicWriteGroup)
		// --- 邮箱预检（注册页 onBlur 调用） ---
		registerCheckEmailHandler(publicWriteGroup)
	}

	// --- 支付回调 (无 JWT, 签名验证) ---
	registerPaymentCallbacks(v1)

	// --- 已认证路由 ---
	// v4.0: 初始化全局 Resolver 单例（幂等）
	if permission.Default == nil {
		permission.Default = permission.NewResolver(database.DB, redis.Client)
	}

	authorized := v1.Group("")
	authorized.Use(middleware.NoStore())
	authorized.Use(middleware.Auth())
	authorized.Use(middleware.AntiAbuseMiddleware(database.DB, gSvc)) // v5.1: 反滥用中间件
	authorized.Use(middleware.LoadSubjectPerms(permission.Default))
	// 注：MemberRateLimiter 不再全局挂载，Dashboard 读请求靠 MultiLevelRateLimiter 的用户级 RPM 兜底。
	// 会员等级 RPM 只在敏感写路径（提现/退款/改密）单独挂载，避免 V0 用户 30 RPM 阻塞首屏并发。
	authorized.Use(middleware.DataScope())
	authorized.Use(middleware.Idempotent())

	// 管理员路由（v4.0: 基于 audit.routeMap 的细粒度权限网关）
	adminGroup := authorized.Group("/admin")
	adminGroup.Use(middleware.PermissionGate())
	{
		// --- v4.0 RBAC 角色管理 + 用户授权（仅 SUPER_ADMIN 可见）---
		registerRoleAdminHandlers(adminGroup)

		// --- Admin user management (registered via handlers) ---
		registerAdminUserHandlers(adminGroup)

		// --- 发票管理 ---
		registerInvoiceAdminHandlers(adminGroup)

		// --- Channel management (registered via handlers) ---
		registerChannelHandlers(adminGroup)

		// --- Orchestration management (registered via handlers) ---
		registerOrchestrationHandlers(adminGroup)

		// --- Doc management (registered via handlers) ---
		registerDocHandlers(adminGroup)

		// --- Report management (registered via handlers) ---
		registerAdminReportHandlers(adminGroup)

		// --- Stats management (registered via handlers) ---
		registerAdminStatsHandlers(adminGroup)

		// --- Pricing management (registered via handlers) ---
		registerPricingHandlers(adminGroup)

		// --- Quota/Balance management ---
		registerQuotaHandlers(adminGroup)

		// --- Referral config management ---
		registerAdminReferralHandlers(adminGroup)

		// --- Commission override management (v3.1 特殊用户加佣) ---
		registerAdminCommissionOverrideHandlers(adminGroup)

		// --- Commission rule management (v4.3 用户×模型特殊返佣规则) ---
		registerAdminCommissionRuleHandlers(adminGroup)

		// --- User discount management (v4.0 用户特殊折扣) ---
		registerAdminUserDiscountHandlers(adminGroup)

		// --- Guard / Disposable email (v3.1 反欺诈配置) ---
		registerAdminGuardHandlers(adminGroup)

		// --- Config Audit Log (v3.1 统一审计日志查询) ---
		registerAdminConfigAuditHandlers(adminGroup)

		// --- Payment config management ---
		registerPaymentConfigHandlers(adminGroup)

		// --- Email management (v4.3) ---
		registerEmailAdminHandlers(adminGroup)
		registerOAuthAdminHandlers(adminGroup)
		registerAdminPrivacyHandlers(adminGroup)

		// --- v3.2 Payment/Order/Refund/Withdrawal/EventLog/ExchangeRate (聚合) ---
		// NOTE: 包含提现管理，不再单独注册 registerAdminWithdrawalHandlers（已被 v3.2 超集）
		registerAdminPaymentV2Handlers(adminGroup)

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

		// ========== Billing Reconcile Routes (月度供应商账单对账) ==========
		admin.NewBillingReconcileHandler(database.DB).Register(adminGroup)

		// ========== Model Sync Routes (模型自动发现与同步) ==========
		registerModelSyncHandlers(adminGroup)
		registerModelAliasHandlers(adminGroup)

		// ========== Background Task Routes (后台任务管理) ==========
		registerTaskHandlers(adminGroup)

		// ========== API Call Log Routes (API调用全链路日志) ==========
		apiCallLogSvc := apikey.NewApiKeyService(database.DB, redis.Client, config.Global.JWT.Secret)
		apiCallLogHandler := admin.NewApiCallLogHandler(database.DB, apiCallLogSvc)
		apiCallLogHandler.Register(adminGroup)

		// ========== Cost Consistency Routes (三方一致性核对) ==========
		// 提供 GET /admin/api-call-logs/:requestId/three-way-check
		// 与 GET /admin/cost-consistency/scan,用于对账时定位 A/B/C 偏差原因
		costConsistencyHandler := admin.NewCostConsistencyHandler(database.DB, pricing.NewPricingCalculator(database.DB))
		costConsistencyHandler.Register(adminGroup)

		// ========== Param Mapping Routes (参数映射管理) ==========
		registerParamMappingHandlers(adminGroup)
		registerModelAPIDocHandlers(adminGroup)

		// ========== Partner Applications Routes (合作伙伴线索管理) ==========
		partnerAdminHandler := admin.NewPartnerApplicationAdminHandler(database.DB)
		partnerAdminHandler.Register(adminGroup)

		// ========== Announcement Routes (站内公告管理) ==========
		announcementAdminHandler := admin.NewAnnouncementHandler(database.DB)
		announcementAdminHandler.Register(adminGroup)

		// ========== Trending Models Routes (全球热门模型参考库) ==========
		trendingModelHandler := admin.NewTrendingModelHandler(database.DB)
		trendingModelHandler.Register(adminGroup)

		// ========== Operations Reports (运营报表：积分消耗 / 注册赠送 / 邀请返佣) ==========
		admin.NewCreditConsumptionHandler(database.DB).Register(adminGroup)
		admin.NewRegistrationGiftHandler(database.DB).Register(adminGroup)
		admin.NewReferralCommissionHandler(database.DB).Register(adminGroup)

		// ========== AI 客服管理 ==========
		admin.NewSupportAdminHandler(database.DB, getSupportServices()).Register(adminGroup)
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

		// --- v3.2 用户退款申请 ---
		registerUserRefundHandlers(userGroup)

		// --- 发票申请 ---
		registerUserInvoiceHandlers(userGroup)

		// --- 站内通知（公告已读状态）---
		notificationHandler := userhandler.NewNotificationHandler(database.DB)
		notificationHandler.Register(userGroup)

		// --- 图片上传（Playground 视觉调试使用，代理到 catbox.moe / 0x0.st）---
		imageUploadHandler := userhandler.NewImageUploadHandler()
		imageUploadHandler.Register(userGroup)

		// --- 用户视角 BillingQuote 查询（A4 任务）---
		quoteHandler := userhandler.NewQuoteHandler(database.DB)
		quoteHandler.Register(userGroup)
	}

	// --- 支付路由 (JWT 认证) ---
	registerPaymentHandlers(authorized, adminGroup)

	// --- AI 客服（需登录） ---
	supportGroup := authorized.Group("/support")
	{
		registerSupportHandlers(supportGroup)
	}

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
	// 初始化全局 CommissionRule resolver（返佣特殊规则决策器）—— Gateway 需要，用于 completions 调用 commissionCalc
	ensureCommissionResolver()

	// 初始化白标服务（域名解析）
	domainResolver := whitelabel.NewDomainResolver(database.DB, redis.Client)

	// 全局中间件（与 Setup 一致）
	r.Use(middleware.Recovery())
	r.Use(middleware.CORS())
	r.Use(middleware.RequestLogger())
	r.Use(middleware.I18n())
	r.Use(middleware.PerfMark("i18n"))
	r.Use(middleware.TenantResolveMiddleware(domainResolver, viper.GetString("server.platform_domain")))
	r.Use(middleware.PerfMark("tenant_resolve"))
	r.Use(middleware.MultiLevelRateLimiter())
	r.Use(middleware.PerfMark("rate_limit"))

	// 健康检查 — Gateway 只依赖 Redis（用于限流）和 DB（用于 API key 校验）
	r.GET("/health", health.LivenessHandler("gateway"))
	r.GET("/livez", health.LivenessHandler("gateway"))
	r.GET("/readyz", health.ReadinessHandler("gateway", database.DB, redis.Client))

	// OpenAI 兼容路由 (/v1/*)
	registerOpenAICompatibleRoutes(r)
}

// SetupBackend 注册用户 + 管理后台路由 (/api/v1/*)。
// 用于 backend 角色的独立进程，处理 Dashboard API、管理后台、支付、MCP 等。
func SetupBackend(r *gin.Engine) {
	// 初始化全局 CommissionRule resolver（返佣特殊规则决策器）
	ensureCommissionResolver()

	// 初始化 SSE 桥接器（委派重操作给 Worker）
	signingKey := config.Global.Service.TaskSignKey
	if signingKey == "" {
		signingKey = config.Global.JWT.Secret
	}
	publisher := taskqueue.NewPublisher(redis.Client, signingKey)
	taskBridge = taskqueue.NewSSEBridge(publisher)

	// 初始化白标服务
	domainResolver := whitelabel.NewDomainResolver(database.DB, redis.Client)

	// 初始化全局审计服务
	auditSvc := auditsvc.InitDefault(database.DB, context.Background())

	// 初始化全局限流事件记录器（启动异步 consumer goroutine）
	ratelimitsvc.InitDefault(database.DB, context.Background())

	// 初始化全局用户认证日志记录器（启动异步 consumer goroutine）
	authlog.InitDefault(database.DB, context.Background())

	// v5.1: 初始化风控服务
	gSvc := guardsvc.NewService(database.DB, redis.Client)

	// 全局中间件
	r.Use(middleware.Recovery())
	r.Use(middleware.CORS())
	r.Use(middleware.RequestLogger())
	r.Use(middleware.I18n())
	r.Use(middleware.PerfMark("i18n"))
	r.Use(middleware.TenantResolveMiddleware(domainResolver, viper.GetString("server.platform_domain")))
	r.Use(middleware.PerfMark("tenant_resolve"))
	r.Use(middleware.MultiLevelRateLimiter())
	r.Use(middleware.PerfMark("rate_limit"))
	// 审计日志中间件
	r.Use(auditmw.AuditLog(auditSvc))

	// 健康检查 — Backend 依赖 DB + Redis
	r.GET("/health", health.LivenessHandler("backend"))
	r.GET("/livez", health.LivenessHandler("backend"))
	r.GET("/readyz", health.ReadinessHandler("backend", database.DB, redis.Client))

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
		registerPublicModelHandlers(publicGroup)
		registerParamSupportHandler(publicGroup)
		registerModelAPIDocPublicHandlers(publicGroup)
		registerPublicConfigHandlers(publicGroup)
		annPublicHandler := public.NewAnnouncementPublicHandler(database.DB)
		annPublicHandler.RegisterPublicBanner(publicGroup)
	}

	// 公开 POST 路由 / 不缓存的 GET
	publicWriteGroup := v1.Group("/public")
	{
		registerPartnerApplicationHandler(publicWriteGroup)
		registerReferralClickHandler(publicWriteGroup)
		// IP 地理位置语言检测（per-IP 响应，不走公共 URL 缓存；Redis 层 geo:ip:{ip} 已提供 1 年缓存）
		registerLocaleHandler(publicWriteGroup)
		// --- 邮箱预检（注册页 onBlur 调用） ---
		registerCheckEmailHandler(publicWriteGroup)
	}

	// 支付回调
	registerPaymentCallbacks(v1)

	// 已认证路由
	// v4.0: 初始化全局 Resolver 单例（幂等）
	if permission.Default == nil {
		permission.Default = permission.NewResolver(database.DB, redis.Client)
	}

	authorized := v1.Group("")
	authorized.Use(middleware.NoStore())
	authorized.Use(middleware.Auth())
	authorized.Use(middleware.AntiAbuseMiddleware(database.DB, gSvc)) // v5.1: 反滥用中间件
	authorized.Use(middleware.LoadSubjectPerms(permission.Default))
	// 注：MemberRateLimiter 不再全局挂载，只在敏感写路径单独挂载（见 register*Handlers）。
	authorized.Use(middleware.DataScope())
	authorized.Use(middleware.Idempotent())

	// 管理员路由（v4.0: 基于 audit.routeMap 的细粒度权限网关）
	adminGroup := authorized.Group("/admin")
	adminGroup.Use(middleware.PermissionGate())
	{
		// v4.0 RBAC 角色管理
		registerRoleAdminHandlers(adminGroup)

		registerAdminUserHandlers(adminGroup)
		registerInvoiceAdminHandlers(adminGroup)
		registerChannelHandlers(adminGroup)
		registerOrchestrationHandlers(adminGroup)
		registerDocHandlers(adminGroup)
		registerAdminReportHandlers(adminGroup)
		registerAdminStatsHandlers(adminGroup)
		registerPricingHandlers(adminGroup)
		registerQuotaHandlers(adminGroup)
		registerAdminReferralHandlers(adminGroup)
		registerAdminCommissionOverrideHandlers(adminGroup)
		registerAdminCommissionRuleHandlers(adminGroup)
		registerAdminGuardHandlers(adminGroup)
		registerAdminConfigAuditHandlers(adminGroup)
		registerPaymentConfigHandlers(adminGroup)
		registerEmailAdminHandlers(adminGroup)
		registerOAuthAdminHandlers(adminGroup)
		registerAdminPrivacyHandlers(adminGroup)
		// v3.2 聚合：包含提现管理（超集替代旧 registerAdminWithdrawalHandlers）
		registerAdminPaymentV2Handlers(adminGroup)
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
		registerModelAliasHandlers(adminGroup)
		registerTaskHandlers(adminGroup)

		apiCallLogSvc := apikey.NewApiKeyService(database.DB, redis.Client, config.Global.JWT.Secret)
		apiCallLogHandler := admin.NewApiCallLogHandler(database.DB, apiCallLogSvc)
		apiCallLogHandler.Register(adminGroup)

		// 三方一致性核对(三服务模式同步注册)
		costConsistencyHandlerBE := admin.NewCostConsistencyHandler(database.DB, pricing.NewPricingCalculator(database.DB))
		costConsistencyHandlerBE.Register(adminGroup)

		registerParamMappingHandlers(adminGroup)
		registerModelAPIDocHandlers(adminGroup)

		partnerAdminHandler := admin.NewPartnerApplicationAdminHandler(database.DB)
		partnerAdminHandler.Register(adminGroup)

		announcementAdminHandler := admin.NewAnnouncementHandler(database.DB)
		announcementAdminHandler.Register(adminGroup)

		// ========== Trending Models Routes (全球热门模型参考库) ==========
		trendingModelHandler := admin.NewTrendingModelHandler(database.DB)
		trendingModelHandler.Register(adminGroup)

		// ========== Operations Reports (运营报表：积分消耗 / 注册赠送 / 邀请返佣) ==========
		admin.NewCreditConsumptionHandler(database.DB).Register(adminGroup)
		admin.NewRegistrationGiftHandler(database.DB).Register(adminGroup)
		admin.NewReferralCommissionHandler(database.DB).Register(adminGroup)

		// ========== AI 客服管理 ==========
		admin.NewSupportAdminHandler(database.DB, getSupportServices()).Register(adminGroup)
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
		registerUserRefundHandlers(userGroup)
		registerUserInvoiceHandlers(userGroup)
		notificationHandler := userhandler.NewNotificationHandler(database.DB)
		notificationHandler.Register(userGroup)

		// 图片上传（Playground 视觉调试）
		imageUploadHandler := userhandler.NewImageUploadHandler()
		imageUploadHandler.Register(userGroup)

		// 用户视角 BillingQuote 查询（A4 任务）
		quoteHandler := userhandler.NewQuoteHandler(database.DB)
		quoteHandler.Register(userGroup)
	}

	// 支付路由
	registerPaymentHandlers(authorized, adminGroup)

	// AI 客服（需登录）
	supportGroup := authorized.Group("/support")
	{
		registerSupportHandlers(supportGroup)
	}

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

// supportServicesSingleton 单例：首次调用时 Bootstrap，后续重用。
// Setup 与 SetupBackend 可能在同一进程调用（单体 vs 拆分），此处保证只构造一次。
var supportServicesSingleton *supportsvc.Services

func getSupportServices() *supportsvc.Services {
	if supportServicesSingleton == nil {
		supportServicesSingleton = supportsvc.Bootstrap(database.DB, redis.Client)
	}
	return supportServicesSingleton
}

// registerSupportHandlers 注册 AI 客服路由 (/api/v1/support/*)。
// 登录用户均可访问：聊天 SSE、会话/消息、工单、热门问题、供应商文档。
func registerSupportHandlers(rg *gin.RouterGroup) {
	svc := getSupportServices()

	chatH := supporthandler.NewChatHandler(svc)
	chatH.Register(rg)

	sessionH := supporthandler.NewSessionHandler(svc)
	sessionH.Register(rg)

	ticketH := supporthandler.NewTicketHandler(svc)
	ticketH.Register(rg)
}

// registerOpenAPIHandlers 注册对外开放 API 路由组 (/api/v1/open/)。
// 使用 Bearer Token (API Key) 认证 + 独立限流 60 req/min。
func registerOpenAPIHandlers(v1 *gin.RouterGroup) {
	db := database.DB
	svc := openapi.NewOpenAPIService(db)

	openGroup := v1.Group("/open")
	openGroup.Use(middleware.NoStore())
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

	// Legacy /api/v1/chat/* aliases.
	// Keep orchestration on the old handler because it is a TokenHub-specific API.
	// Route OpenAI-compatible chat/models/embeddings through the /v1 main handlers so
	// billing, failover and api_call_logs stay on a single implementation path.
	codSvc := codingsvc.NewCodingService(db)
	paramSvc := parammapping.NewParamMappingService(db)
	tpmLimiter := middleware.NewTPMLimiter(db, redisClient)
	completionsHandler := v1handler.NewCompletionsHandler(
		db, codSvc, channelRouter,
		pricingCalc, apiKeySvc, balSvc, commCalc, paramSvc, tpmLimiter,
	)
	embeddingsHandler := v1handler.NewEmbeddingsHandler(db, channelRouter, apiKeySvc, balSvc, pricingCalc)
	modelsHandler := v1handler.NewModelsHandler(db)
	addLegacyDeprecationHeaders := func(c *gin.Context, successor string) {
		c.Header("Deprecation", "true")
		c.Header("X-TokenHub-Deprecated", "/api/v1/chat/* is deprecated; use /v1/*")
		c.Header("Link", "<"+successor+">; rel=\"successor-version\"")
	}

	rg.POST("/completions", func(c *gin.Context) {
		addLegacyDeprecationHeaders(c, "/v1/chat/completions")
		completionsHandler.ChatCompletions(c)
	})
	rg.POST("/embeddings", func(c *gin.Context) {
		addLegacyDeprecationHeaders(c, "/v1/embeddings")
		embeddingsHandler.CreateEmbedding(c)
	})
	rg.GET("/models", func(c *gin.Context) {
		addLegacyDeprecationHeaders(c, "/v1/models")
		modelsHandler.ListModels(c)
	})
	rg.POST("/orchestrated", chatHandler.Orchestrated)
}

// registerRateLimitHandlers 初始化限流限额管理处理器并注册管理员路由
func registerRateLimitHandlers(rg *gin.RouterGroup) {
	db := database.DB
	redisClient := redis.Client

	balSvc := balancesvc.NewBalanceService(db, redisClient)
	quotaLimiter := balancesvc.NewQuotaLimiter(db, redisClient)
	handler := admin.NewRateLimitHandler(balSvc, quotaLimiter)
	handler.Register(rg)

	// 限流监控（活跃桶 + 429 事件）
	admin.NewRateLimitAdminHandler(ratelimitsvc.Default).Register(rg)

	// 用户认证行为日志查询
	admin.NewAuthLogHandler(authlog.Default).Register(rg)
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

// registerAdminStatsHandlers 初始化运营统计 Handler 并注册路由
func registerAdminStatsHandlers(rg *gin.RouterGroup) {
	statsH := admin.NewStatsHandler(database.DB)
	statsH.Register(rg)
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

	// v3.2: 注入多账号路由 + 事件日志 + 汇率服务（让 CreatePaymentWithRouting 真实生效）
	accountRouter := paymentsvc.NewAccountRouter(db, redisClient)
	eventLogger := paymentsvc.NewEventLogger(db)
	// v3.2.3: 汇率配置从 DB 种子读取（system_configs），config.Global 作兜底
	pcsForFx := paymentsvc.NewPaymentConfigService(db)
	fxCfg := buildExchangeRateConfig(db, pcsForFx)
	fxSvc := exchangesvc.New(db, redisClient, fxCfg)
	fxSvc.SetEventSink(eventLogger)
	paymentSvc.SetAccountRouter(accountRouter)
	paymentSvc.SetEventLogger(eventLogger)
	paymentSvc.SetExchangeFetcher(fxSvc)

	// 支付路由 (已认证用户)
	paymentHandler := paymenthandler.NewPaymentHandler(paymentSvc)
	authorized.POST("/payment/create", paymentHandler.Create)
	authorized.GET("/payment/query/:orderNo", paymentHandler.Query)
	authorized.GET("/payment/list", paymentHandler.List)
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

	// v3.2: 注入事件日志 + 多账号路由，使回调事件完整入库
	eventLogger := paymentsvc.NewEventLogger(db)
	paymentSvc.SetEventLogger(eventLogger)

	callbackHandler := paymenthandler.NewCallbackHandler(paymentSvc)
	callbackHandler.RegisterCallbacks(v1)
}

// registerAuthHandlers 初始化认证服务并注册路由
func registerAuthHandlers(v1 *gin.RouterGroup) {
	db := database.DB
	redisClient := redis.Client

	jwtCfg := config.Global.JWT

	authService := authsvc.NewAuthService(db, redisClient, jwtCfg)
	oauthService := authsvc.NewOAuthService(db, redisClient, jwtCfg)
	smsService := smssvc.NewService(db, redisClient)
	authGeoSvc := geosvc.NewGeoService(redis.Client, config.Global.Geo)
	handler := authhandler.NewAuthHandler(authService, authGeoSvc).WithOAuthService(oauthService)
	phoneHandler := authhandler.NewPhoneHandler(authService, smsService)

	authGroup := v1.Group("/auth")
	authGroup.Use(middleware.NoStore())
	// 敏感端点独立 IP+路径严格桶（防暴力破解/机器注册），与全局 IP 桶隔离
	authGroup.POST("/register", middleware.StrictLoginRateLimit(5), handler.Register)
	authGroup.POST("/login", middleware.StrictLoginRateLimit(10), handler.Login)
	authGroup.POST("/refresh", handler.Refresh)
	authGroup.GET("/oauth/providers", handler.OAuthProviders)
	authGroup.GET("/oauth/:provider/start", middleware.StrictLoginRateLimit(20), handler.OAuthStart)
	authGroup.GET("/oauth/:provider/callback", middleware.StrictLoginRateLimit(20), handler.OAuthCallback)

	// 登出需要 JWT 认证
	authGroup.POST("/logout", middleware.Auth(), handler.Logout)

	// 邮箱验证码发送（注册/密码重置），公开端点，内部做 IP+email 防刷
	if emailsvc.Default == nil {
		emailsvc.InitDefault(db)
	}
	emailCodeHandler := authhandler.NewEmailCodeHandler()
	emailCodeHandler.Register(authGroup)
	phoneHandler.Register(authGroup)
}

// registerRoleAdminHandlers v4.0 RBAC: 角色管理 + 用户授权
func registerRoleAdminHandlers(rg *gin.RouterGroup) {
	db := database.DB
	if permission.Default == nil {
		permission.Default = permission.NewResolver(db, redis.Client)
	}
	roleSvc := permission.NewRoleService(db, permission.Default)
	handler := admin.NewRoleAdminHandler(roleSvc)
	handler.Register(rg)
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
	rg.POST("/users/batch", batchHandler.BatchCreateUsers)           // 批量创建用户
	rg.PUT("/users/:id/role", batchHandler.UpdateUserRole)           // 更新用户角色
	rg.POST("/users/:id/recharge-rmb", batchHandler.RechargeUserRMB) // RMB充值
	rg.PUT("/users/:id/status", batchHandler.UpdateUserStatus)       // 更新用户状态

	// 管理员租户管理
	registerAdminTenantHandlers(rg)

	// --- Supplier CRUD ---
	registerAdminSupplierHandlers(rg)

	// --- Model Category CRUD ---
	registerAdminModelCategoryHandlers(rg)

	// --- AI Model CRUD ---
	registerAdminAIModelHandlers(rg)

	// --- Capability Testing ---
	registerAdminCapabilityTestHandlers(rg)

	// --- Misc admin endpoints ---
	registerAdminMiscHandlers(rg)

	// --- System admin endpoints (schema migrate / version check) ---
	registerAdminSystemHandlers(rg)
}

// registerAdminSystemHandlers 注册系统级管理员接口（schema 升级 / 版本查询）
// 所有启动时被剥离的 DB 初始化动作都通过这些接口手动触发。
func registerAdminSystemHandlers(rg *gin.RouterGroup) {
	handler := admin.NewSystemHandler(database.DB)
	rg.GET("/system/schema-version", handler.GetSchemaVersion)
	rg.POST("/system/migrate", handler.Migrate)
	rg.GET("/system/config/:key", handler.GetSystemConfig)
	rg.PUT("/system/config/:key", handler.UpdateSystemConfig)
}

// registerAdminCapabilityTestHandlers 注册模型能力测试路由
func registerAdminCapabilityTestHandlers(rg *gin.RouterGroup) {
	db := database.DB
	checker := aimodelsvc.NewModelChecker(db)
	tester := aimodelsvc.NewCapabilityTester(db, checker)
	baseline := aimodelsvc.NewBaselineService(db)
	handler := admin.NewCapabilityTestHandler(db, tester, baseline, taskBridge)
	handler.Register(rg)
}

// registerUserHandlers 初始化用户个人信息和 API Key 处理器
func registerUserHandlers(rg *gin.RouterGroup) {
	db := database.DB
	redisClient := redis.Client

	userSvc := usersvc.NewUserService(db)
	profileHandler := userhandler.NewProfileHandler(userSvc)

	// v4.0: 全局 Resolver 单例（幂等：仅首次初始化）
	// 供 /user/profile 返回 permissions/data_scope/role_codes
	if permission.Default == nil {
		permission.Default = permission.NewResolver(db, redisClient)
	}
	profileHandler.SetResolver(permission.Default)

	rg.GET("/profile", profileHandler.GetProfile)
	rg.PUT("/profile", profileHandler.UpdateProfile)
	// 改密：独立严格桶（User 维度 10 RPM 防枚举攻击），不走会员等级 RPM
	rg.PUT("/password", middleware.SensitiveRateLimit(10, middleware.DimensionUser), profileHandler.ChangePassword)
	rg.POST("/change-password", middleware.SensitiveRateLimit(10, middleware.DimensionUser), profileHandler.ChangePassword)

	apiKeySvc := apikey.NewApiKeyService(db, redisClient, config.Global.JWT.Secret)
	apiKeyHandler := userhandler.NewApiKeyHandler(apiKeySvc)

	rg.GET("/api-keys", apiKeyHandler.List)
	// 创建 API Key：独立严格桶（User 维度 30 RPM 防刷创建）
	rg.POST("/api-keys", middleware.SensitiveRateLimit(30, middleware.DimensionUser), apiKeyHandler.Generate)
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

	privacyHandler := userhandler.NewPrivacyHandler(privacysvc.New(db))
	privacyHandler.Register(rg)
}

func registerAdminPrivacyHandlers(rg *gin.RouterGroup) {
	admin.NewPrivacyHandler(privacysvc.New(database.DB)).Register(rg)
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

	// ── 代理折扣体系已于 2026-04-28 移除 ──
	// 历史 endpoint:
	//   GET/POST/PUT/DELETE /level-discounts (AgentLevelDiscount CRUD)
	//   GET/POST/PUT/DELETE /agent-pricings (AgentPricing CRUD)
	// 用户级特殊折扣 user-discounts 端点保留(由 user_discount_handler 提供)
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
	rg.POST("/ai-models/batch-free-tier", handler.BatchSetFreeTier) // v5.1: 批量设置新用户可用模型
	rg.GET("/ai-models/:id/preflight", handler.PreflightEnable)     // 启用前静态检查
	rg.POST("/ai-models/:id/verify", handler.Verify)                // 验证模型并上线
	rg.POST("/ai-models/:id/offline", handler.SetOffline)           // 将模型下线
	rg.POST("/ai-models/:id/reactivate", handler.Reactivate)        // 手动重新上线（清空失败序列）

	// 官方定价页 URL 解析与覆盖(模型级覆盖 > 供应商多页 type_hint > 供应商默认)
	rg.GET("/ai-models/:id/official-price-url", handler.GetOfficialPriceURL)
	rg.PUT("/ai-models/:id/official-price-url", handler.SetOfficialPriceURL)

	// 全局折扣引擎(v2):一个折扣率自动应用到所有价格档(基础/阶梯/缓存/思考)
	pcsForDiscount := paymentsvc.NewPaymentConfigService(db)
	fxCfgForDiscount := buildExchangeRateConfig(db, pcsForDiscount)
	fxSvcForDiscount := exchangesvc.New(db, redis.Client, fxCfgForDiscount)
	discountHandler := admin.NewGlobalDiscountHandler(db, fxSvcForDiscount)
	rg.POST("/ai-models/:id/apply-global-discount", discountHandler.ApplyGlobalDiscount)
	rg.POST("/ai-models/:id/preview-global-discount", discountHandler.PreviewGlobalDiscount)
	rg.PUT("/ai-models/:id/lock-overrides", discountHandler.SetLockOverride)
	rg.DELETE("/ai-models/:id/lock-overrides/:archKey", discountHandler.ClearLockOverride)

	// PriceMatrix 矩阵化定价(v3):统一表达任意维度组合,
	// 取代旧 PriceTiers + 各 PricingForm 字段散落式的存储
	priceMatrixHandler := admin.NewPriceMatrixHandler(db)
	rg.GET("/ai-models/:id/price-matrix", priceMatrixHandler.GetPriceMatrix)
	rg.PUT("/ai-models/:id/price-matrix", priceMatrixHandler.UpdatePriceMatrix)

	modelOpsHandler := admin.NewModelOpsHandler(db)
	modelOpsHandler.Register(rg)

	// 统一计价试算端点(BillingQuoteService.Calculate 公开入口)。
	// 与真实扣费 snapshot.quote、成本分析渲染共用同一份计价口径,避免试算/扣费/分析三方漂移。
	billingQuoteHandler := admin.NewBillingQuoteHandler(db, pricing.NewPricingCalculator(db))
	rg.POST("/billing/quote-preview", billingQuoteHandler.QuotePreview)

	// 模型可用性批量检测
	checker := aimodelsvc.NewModelChecker(db)
	checkHandler := admin.NewModelCheckHandler(checker, taskBridge)
	rg.POST("/models/batch-check", checkHandler.BatchCheck)          // SSE 实时进度（旧版自动下线，定时任务用）
	rg.POST("/models/batch-check-sync", checkHandler.BatchCheckSync) // 同步返回（旧版自动下线）
	rg.POST("/models/check-preview", checkHandler.Preview)           // SSE 实时进度（dry-run 扫描预览，前端"一键检测"用）
	rg.POST("/models/check-preview-sync", checkHandler.PreviewSync)  // 同步返回（dry-run 扫描预览）
	rg.POST("/models/check-selected", checkHandler.CheckSelected)    // 检测勾选的模型
	rg.GET("/models/check-history", checkHandler.GetCheckHistory)    // 检测历史
	rg.GET("/models/check-latest", checkHandler.GetLatestSummary)    // 最近一次汇总
	// 后台检测任务（新版：一键检测创建任务，异步执行，按供应商查看结果）
	rg.POST("/models/check-task", checkHandler.CreateCheckTask)        // 创建并启动后台检测任务
	rg.GET("/models/check-tasks", checkHandler.GetCheckTasks)          // 任务列表
	rg.GET("/models/check-tasks/:id", checkHandler.GetCheckTaskDetail) // 任务详情（含供应商分组结果）

	// 模型下线扫描与批量下线（结合公告系统）
	rg.POST("/models/deprecation-scan", handler.DeprecationScan) // 扫描可能下线的模型
	rg.POST("/models/bulk-deprecate", handler.BulkDeprecate)     // 批量下线 + 创建公告
	rg.GET("/models/scanned-offline", handler.ScanOfflineAll)    // 所有供应商扫描下线模型汇总
	// 根据供应商官方下线公告批量标记本地模型为 offline（当前仅支持 baidu_qianfan）
	rg.POST("/models/mark-official-deprecated/:supplierCode", handler.MarkOfficialDeprecated)

	// 能力探测 + 批量重算标签
	featureProbeHandler := admin.NewModelFeatureProbeHandler(db)
	rg.POST("/models/feature-probe", featureProbeHandler.FeatureProbe)
	rg.POST("/models/batch-retag", handler.BatchRetag)

	// ===== 模型 k:v 标签系统 =====
	// 注意：batch/label-keys 路由必须在 :id 参数路由前注册，避免路径冲突
	labelHandler := admin.NewModelLabelHandler(db)
	rg.GET("/models/label-keys", labelHandler.ListKeys)         // 获取所有已用标签键（自动补全）
	rg.POST("/models/batch-labels", labelHandler.BatchAssign)   // 批量添加标签
	rg.DELETE("/models/batch-labels", labelHandler.BatchRemove) // 批量移除标签
	rg.GET("/ai-models/:id/labels", labelHandler.ListByModel)   // 获取模型标签列表
	rg.POST("/ai-models/:id/labels", labelHandler.Upsert)       // 添加标签
	rg.DELETE("/ai-models/:id/labels", labelHandler.Remove)     // 删除标签
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
	rg.GET("/audit-logs/menus", handler.ListAuditMenus)
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

// registerAdminCommissionRuleHandlers 初始化 v4.3 特殊返佣规则 CRUD（用户×模型维度）
func registerAdminCommissionRuleHandlers(rg *gin.RouterGroup) {
	ensureCommissionResolver()
	svc := referralsvc.NewCommissionRuleService(database.DB, referralsvc.Default)
	handler := admin.NewCommissionRuleHandler(svc)
	handler.Register(rg)
}

// ensureCommissionResolver 幂等地初始化全局 CommissionRule resolver 单例
func ensureCommissionResolver() {
	if referralsvc.Default != nil {
		return
	}
	referralsvc.Default = referralsvc.NewRuleResolver(database.DB, redis.Client)
}

// registerAdminUserDiscountHandlers 初始化用户特殊折扣 CRUD
func registerAdminUserDiscountHandlers(rg *gin.RouterGroup) {
	db := database.DB
	calc := pricing.NewPricingCalculator(db)
	svc := pricing.NewUserDiscountService(db, calc)
	handler := admin.NewUserDiscountHandler(db, svc)
	handler.Register(rg)
}

// registerAdminGuardHandlers 初始化 v3.1 反欺诈配置与一次性邮箱管理路由
func registerAdminGuardHandlers(rg *gin.RouterGroup) {
	svc := guardsvc.NewService(database.DB, redis.Client)
	handler := admin.NewGuardConfigHandler(svc)
	handler.Register(rg)
	smsHandler := admin.NewSMSConfigHandler(database.DB, smssvc.NewService(database.DB, redis.Client))
	smsHandler.Register(rg)
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
	svc.SetRedis(redis.Client)
	// 注入事件日志
	eventLogger := paymentsvc.NewEventLogger(database.DB)
	svc.SetEventSink(withdrawalsvc.FuncEventSink{F: func(ctx context.Context, evt withdrawalsvc.WithdrawalEvent) {
		eventLogger.LogWithdrawalEventInput(ctx, paymentsvc.WithdrawalEventInput{
			WithdrawID: evt.WithdrawID,
			UserID:     evt.UserID,
			EventType:  evt.EventType,
			ActorType:  evt.ActorType,
			ActorID:    evt.ActorID,
			IP:         evt.IP,
			Payload:    evt.Payload,
			Success:    evt.Success,
			ErrorMsg:   evt.ErrorMsg,
		})
	}})
	handler := userhandler.NewWithdrawalHandlerWithDB(svc, database.DB)
	// POST /withdrawals 需要 user_withdrawal_create 权限 + 独立严格桶（User 维度 20 RPM 防刷）
	rg.POST("/withdrawals",
		middleware.SensitiveRateLimit(20, middleware.DimensionUser),
		middleware.RequirePermission("user_withdrawal_create"),
		handler.Create)
	rg.GET("/withdrawals", handler.List)
	rg.GET("/withdrawals/stats", handler.Stats)
	rg.GET("/withdrawals/config", handler.Config)
	rg.GET("/withdrawals/:id", handler.Get)
	rg.DELETE("/withdrawals/:id", handler.Cancel)
}

// registerUserRefundHandlers v3.2 用户退款申请
func registerUserRefundHandlers(rg *gin.RouterGroup) {
	db := database.DB
	eventLogger := paymentsvc.NewEventLogger(db)
	refundSvc := paymentsvc.NewRefundService(db, redis.Client, eventLogger)
	// 网关调用：构建 PaymentService
	paymentSvc := paymentsvc.NewPaymentService(db, redis.Client, pkglogger.L)
	paymentSvc.SetEventLogger(eventLogger)
	refundSvc.SetGatewayInvoker(paymentSvc)

	handler := userhandler.NewRefundHandler(refundSvc)
	// POST /refund-requests 需要 user_refund_request_create 权限 + 独立严格桶（User 维度 20 RPM 防刷）
	rg.POST("/refund-requests",
		middleware.SensitiveRateLimit(20, middleware.DimensionUser),
		middleware.RequirePermission("user_refund_request_create"),
		handler.Submit)
	rg.GET("/refund-requests", handler.List)
	rg.GET("/refund-requests/:id", handler.Get)
}

// registerUserInvoiceHandlers v4.2 用户开票申请
func registerUserInvoiceHandlers(rg *gin.RouterGroup) {
	svc := invoicesvc.New(database.DB)
	handler := userhandler.NewInvoiceHandler(svc)
	rg.POST("/invoices",
		middleware.SensitiveRateLimit(20, middleware.DimensionUser),
		middleware.RequirePermission("user_invoice_create"),
		handler.Submit)
	rg.GET("/invoices", handler.List)
	rg.GET("/invoices/:id", handler.Get)

	// 发票抬头管理(快捷选择)
	rg.GET("/invoice-titles", handler.ListTitles)
	rg.POST("/invoice-titles", middleware.RequirePermission("user_invoice_create"), handler.CreateTitle)
	rg.PUT("/invoice-titles/:id", middleware.RequirePermission("user_invoice_create"), handler.UpdateTitle)
	rg.DELETE("/invoice-titles/:id", middleware.RequirePermission("user_invoice_create"), handler.DeleteTitle)
}

// registerInvoiceAdminHandlers 发票管理（管理员）
func registerInvoiceAdminHandlers(rg *gin.RouterGroup) {
	svc := invoicesvc.New(database.DB)
	handler := admin.NewInvoiceAdminHandler(svc)
	handler.Register(rg)
}

// registerUserReferralHandlers 初始化用户邀请处理器
func registerUserReferralHandlers(rg *gin.RouterGroup) {
	db := database.DB
	svc := referralsvc.NewReferralService(db)
	ensureCommissionResolver()
	ruleSvc := referralsvc.NewCommissionRuleService(db, referralsvc.Default)
	handler := userhandler.NewReferralHandler(svc).WithRuleService(ruleSvc)
	handler.Register(rg)
}

// registerPaymentConfigHandlers 注册支付配置管理路由（管理员）
func registerPaymentConfigHandlers(rg *gin.RouterGroup) {
	db := database.DB
	svc := paymentsvc.NewPaymentConfigService(db)
	handler := admin.NewPaymentConfigHandler(svc)
	handler.Register(rg)
}

// registerEmailAdminHandlers 注册邮件管理（管理员）
// 依赖全局单例 emailsvc.Default（由 Setup / SetupBackend 调用 emailsvc.InitDefault 初始化）
func registerEmailAdminHandlers(rg *gin.RouterGroup) {
	if emailsvc.Default == nil {
		emailsvc.InitDefault(database.DB)
	}
	handler := admin.NewEmailHandler(
		emailsvc.Default.Config(),
		emailsvc.Default.Template(),
		emailsvc.Default,
	)
	handler.Register(rg)
}

// registerOAuthAdminHandlers 注册 Google/GitHub 登录配置管理路由。
func registerOAuthAdminHandlers(rg *gin.RouterGroup) {
	handler := admin.NewOAuthConfigHandler(authsvc.NewOAuthService(database.DB, redis.Client, config.Global.JWT))
	handler.Register(rg)
}

// registerLocaleHandler 注册 IP 地理位置语言检测接口
func registerLocaleHandler(rg *gin.RouterGroup) {
	geoService := geosvc.NewGeoService(redis.Client, config.Global.Geo)
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

// registerAdminPaymentV2Handlers 注册 v3.2 聚合支付管理路由
func registerAdminPaymentV2Handlers(rg *gin.RouterGroup) {
	db := database.DB

	// EventLogger
	eventLogger := paymentsvc.NewEventLogger(db)

	// RefundService
	refundSvc := paymentsvc.NewRefundService(db, redis.Client, eventLogger)

	// AccountRouter
	accRouter := paymentsvc.NewAccountRouter(db, redis.Client)

	// PaymentConfigService (for encryption)
	paymentCfgSvc := paymentsvc.NewPaymentConfigService(db)

	// WithdrawalService（带事件日志）
	balSvc := balancesvc.NewBalanceService(db, redis.Client)
	wdSvc := withdrawalsvc.NewService(db, balSvc)
	wdSvc.SetRedis(redis.Client)
	wdSvc.SetEventSink(withdrawalsvc.FuncEventSink{F: func(ctx context.Context, evt withdrawalsvc.WithdrawalEvent) {
		eventLogger.LogWithdrawalEventInput(ctx, paymentsvc.WithdrawalEventInput{
			WithdrawID: evt.WithdrawID,
			UserID:     evt.UserID,
			EventType:  evt.EventType,
			ActorType:  evt.ActorType,
			ActorID:    evt.ActorID,
			IP:         evt.IP,
			Payload:    evt.Payload,
			Success:    evt.Success,
			ErrorMsg:   evt.ErrorMsg,
		})
	}})

	// ExchangeRateService（v3.2.3：从 DB 种子读取，config.Global 兜底）
	fxCfg := buildExchangeRateConfig(db, paymentCfgSvc)
	fxSvc := exchangesvc.New(db, redis.Client, fxCfg)
	fxSvc.SetEventSink(eventLogger)

	// PaymentService（扩展：注入 accountRouter + eventLogger + exchangeFetcher）
	paymentSvc := paymentsvc.NewPaymentService(db, redis.Client, pkglogger.L)
	paymentSvc.SetAccountRouter(accRouter)
	paymentSvc.SetEventLogger(eventLogger)
	paymentSvc.SetExchangeFetcher(fxSvc)
	refundSvc.SetGatewayInvoker(paymentSvc)

	handler := admin.NewPaymentAdminHandler(admin.PaymentAdminHandlerOpts{
		DB:            db,
		PaymentSvc:    paymentSvc,
		RefundSvc:     refundSvc,
		WithdrawalSvc: wdSvc,
		AccountRouter: accRouter,
		EventLogger:   eventLogger,
		ExchangeSvc:   fxSvc,
		PaymentConfig: paymentCfgSvc,
	})
	handler.Register(rg)
}

// registerPublicConfigHandlers 注册公开配置接口（邀请返佣 + 注册赠送 + 汇率）
func registerPublicConfigHandlers(rg *gin.RouterGroup) {
	db := database.DB
	referralSvc := referralsvc.NewReferralService(db)
	balanceSvc := balancesvc.NewBalanceService(db, redis.Client)
	handler := public.NewConfigHandler(referralSvc, balanceSvc)
	handler.Register(rg)

	// v3.2 汇率接口（v3.2.3：从 DB 种子读取，config.Global 兜底）
	pcsForPublic := paymentsvc.NewPaymentConfigService(db)
	fxCfg := buildExchangeRateConfig(db, pcsForPublic)
	fxSvc := exchangesvc.New(db, redis.Client, fxCfg)
	fxHandler := public.NewExchangeRateHandler(fxSvc)
	fxHandler.Register(rg)
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
	mcpGroup.Use(middleware.NoStore())
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
	publicEstimateHandler := v1handler.NewEstimateHandler(db, pricing.NewPricingCalculator(db))
	publicEstimateHandler.Register(v1Group)
	// 价格预估是只读计算接口，供公开 Playground 自动刷新展示使用，不要求 API Key。
	// 使用 Bearer Token 认证（复用 OpenAPI Auth 中间件）
	v1Group.Use(middleware.NoStore())
	v1Group.Use(middleware.OpenAPIAuth(db))
	// API Key 异常快速熔断：60 秒内累计 >=20 次错误自动封禁 5 分钟，避免 Key 被盗/滥用影响其他用户
	v1Group.Use(middleware.APIKeyAnomalyGuard())
	v1Group.Use(middleware.FreeTierAccessGuard(db))

	generationHandler := v1handler.NewGenerationHandler(db)
	generationHandler.Register(v1Group)

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

	// --- /v1/messages --- Anthropic Messages API 兼容入站（共享 completionsHandler 依赖）
	messagesHandler := v1handler.NewMessagesHandler(completionsHandler)
	messagesHandler.Register(v1Group)
	responsesHandler := v1handler.NewResponsesHandler(completionsHandler)
	responsesHandler.Register(v1Group)

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
	rg.GET("/reconciliation/snapshots", handler.BillingReconciliationSnapshots)
	rg.POST("/reconciliation/snapshots", handler.CreateBillingReconciliationSnapshot)
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

func registerModelAPIDocPublicHandlers(rg *gin.RouterGroup) {
	svc := modelapidocsvc.New(database.DB)
	h := public.NewModelAPIDocHandler(svc)
	h.Register(rg)
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

// registerCheckEmailHandler 注册邮箱预检接口（无需认证，无缓存，IP 限流 30/min）
// POST /api/v1/public/check-email
func registerCheckEmailHandler(rg *gin.RouterGroup) {
	h := public.NewCheckEmailHandler(database.DB)
	h.Register(rg)
	usernameHandler := public.NewCheckUsernameHandler(database.DB)
	usernameHandler.Register(rg)
	phoneHandler := public.NewCheckPhoneHandler(database.DB)
	phoneHandler.Register(rg)
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

func registerModelAliasHandlers(rg *gin.RouterGroup) {
	handler := admin.NewModelAliasHandler(modelaliassvc.NewService(database.DB))
	handler.Register(rg)
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
	rg.POST("/models/scrape-page", handler.ScrapePage)
	// 批量按模型ID精准抓取
	rg.POST("/models/batch-scrape", handler.BatchScrape)
	rg.GET("/models/batch-scrape/:task_id/result", handler.GetBatchScrapeResult)
	rg.POST("/models/batch-scrape/apply", handler.ApplyBatchScrape)
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
	rg.GET("/param-mappings/coverage", handler.Coverage)
	rg.GET("/param-mappings/standard-params", handler.StandardParams)
	rg.POST("/param-mappings/standard-params/apply", handler.ApplyStandardParams)
	rg.GET("/param-mappings/templates/recommended", handler.RecommendedTemplate)
	rg.POST("/param-mappings/templates/recommended/apply", handler.ApplyRecommendedTemplate)
	rg.GET("/param-mappings/:id", handler.GetParam)
	rg.POST("/param-mappings", handler.CreateParam)
	rg.PUT("/param-mappings/:id", handler.UpdateParam)
	rg.DELETE("/param-mappings/:id", handler.DeleteParam)
	rg.POST("/param-mappings/:id/mappings", handler.UpsertMapping)
	rg.DELETE("/param-mappings/mappings/:mappingId", handler.DeleteMapping)
	rg.GET("/param-mappings/supplier/:code", handler.GetMappingsBySupplier)
	rg.PUT("/param-mappings/supplier/:code", handler.BatchUpdateMappings)
}

func registerModelAPIDocHandlers(rg *gin.RouterGroup) {
	svc := modelapidocsvc.New(database.DB)
	handler := admin.NewModelAPIDocHandler(svc)
	handler.Register(rg)
}
