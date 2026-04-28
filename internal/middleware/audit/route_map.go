// Package audit 审计日志中间件 - 路由元数据映射
//
// 维护 path+method → {菜单, 功能, action, 资源类型} 的映射，由中间件根据 c.FullPath() 查表，
// 命中则记录审计日志，未命中则跳过（白名单策略，防止日志爆炸）。
package audit

import "strings"

// RouteMeta 路由元数据，用于审计日志的菜单/功能维度展示
type RouteMeta struct {
	Menu     string // 菜单名（如 "用户管理"）
	Feature  string // 功能名（如 "更新用户"）
	Action   string // 动作标识（机器可读，如 "user_update"）
	Resource string // 资源类型（如 "user"）
}

// routeMap 路由 → 元数据映射表
// Key 格式: "METHOD path"，path 使用 gin 的 FullPath() 模板（含 :param 占位符）
//
// 维护原则：
//  1. 仅纳入需要审计的写操作（POST/PUT/PATCH/DELETE）
//  2. GET 请求一律不进入此表（中间件首层就会直接放行）
//  3. 新增 admin 接口时同步追加，未在此表的请求不留痕
var routeMap = map[string]RouteMeta{
	"POST /api/v1/admin/tenants": {"User Management", "Create tenant", "tenant_create", "tenant"},
	// ==================== 用户管理 ====================
	"PUT /api/v1/admin/users/:id":                 {"用户管理", "更新用户", "user_update", "user"},
	"DELETE /api/v1/admin/users/:id":              {"用户管理", "删除用户", "user_delete", "user"},
	"PATCH /api/v1/admin/users/:id/status":        {"用户管理", "禁用启用", "user_status_change", "user"},
	"PUT /api/v1/admin/users/:id/status":          {"用户管理", "禁用启用", "user_status_change", "user"},
	"POST /api/v1/admin/users":                    {"用户管理", "创建用户", "user_create", "user"},
	"POST /api/v1/admin/users/batch":              {"用户管理", "批量创建用户", "user_batch_create", "user"},
	"PATCH /api/v1/admin/users/:id/role":          {"用户管理", "调整角色", "user_role_change", "user"},
	"PUT /api/v1/admin/users/:id/role":            {"用户管理", "调整角色", "user_role_change", "user"},
	"POST /api/v1/admin/users/:id/reset-password": {"用户管理", "重置密码", "user_reset_password", "user"},
	"PUT /api/v1/admin/users/:id/limits":          {"用户管理", "更新用户限速", "user_rate_limit_update", "user"},

	// ==================== 余额管理 ====================
	"POST /api/v1/admin/balance/adjust":             {"余额管理", "调整余额", "balance_adjust", "balance"},
	"POST /api/v1/admin/users/:id/grant-credits":    {"余额管理", "赠送积分", "balance_grant", "balance"},
	"POST /api/v1/admin/users/:id/recharge-rmb":     {"余额管理", "人民币充值", "balance_recharge_rmb", "balance"},
	"POST /api/v1/admin/users/:id/recharge":         {"余额管理", "手动充值", "balance_recharge", "balance"},
	"POST /api/v1/admin/users/:id/recharge-credits": {"余额管理", "充值积分", "balance_recharge_credits", "balance"},
	"PUT /api/v1/admin/users/:id/set-credits":       {"余额管理", "设置用户积分", "balance_set_credits", "balance"},

	// ==================== 用户自助申请（权限控制）====================
	// 这两条路由在 /user/* 组下，不经过 PermissionGate，
	// 但加入此表可使其被 seed 为 permissions 记录，配合 RequirePermission 中间件生效。
	"POST /api/v1/user/withdrawals":          {"用户申请", "申请提现", "user_withdrawal_create", "withdrawal"},
	"POST /api/v1/user/refund-requests":      {"用户申请", "申请退款", "user_refund_request_create", "refund_request"},
	"POST /api/v1/user/invoices":             {"用户申请", "申请发票", "user_invoice_create", "invoice"},
	"POST /api/v1/user/invoice-titles":       {"用户申请", "保存发票抬头", "user_invoice_title_create", "invoice_title"},
	"PUT /api/v1/user/invoice-titles/:id":    {"用户申请", "更新发票抬头", "user_invoice_title_update", "invoice_title"},
	"DELETE /api/v1/user/invoice-titles/:id": {"用户申请", "删除发票抬头", "user_invoice_title_delete", "invoice_title"},
	"DELETE /api/v1/user/withdrawals/:id":    {"用户申请", "取消提现", "user_withdrawal_cancel", "withdrawal"},

	// ==================== 发票管理 ====================
	"POST /api/v1/admin/invoices/:id/approve":    {"发票管理", "审批发票", "invoice_admin_approve", "invoice"},
	"POST /api/v1/admin/invoices/:id/reject":     {"发票管理", "拒绝发票", "invoice_admin_reject", "invoice"},
	"POST /api/v1/admin/invoices/:id/upload-pdf": {"发票管理", "上传发票PDF", "invoice_admin_upload_pdf", "invoice"},
	"DELETE /api/v1/admin/invoices/:id":          {"发票管理", "删除发票申请", "invoice_admin_delete", "invoice"},

	// ==================== AI 模型管理 ====================
	"POST /api/v1/admin/ai-models":                                     {"模型管理", "创建模型", "model_create", "ai_model"},
	"PUT /api/v1/admin/ai-models/:id":                                  {"模型管理", "更新模型", "model_update", "ai_model"},
	"DELETE /api/v1/admin/ai-models/:id":                               {"模型管理", "删除模型", "model_delete", "ai_model"},
	"POST /api/v1/admin/ai-models/:id/offline":                         {"模型管理", "下线模型", "model_offline", "ai_model"},
	"POST /api/v1/admin/ai-models/:id/verify":                          {"模型管理", "审核模型", "model_verify", "ai_model"},
	"POST /api/v1/admin/models/batch-check":                            {"模型管理", "批量检测(同步)", "model_batch_check", "ai_model"},
	"POST /api/v1/admin/models/batch-check-sync":                       {"模型管理", "批量检测(同步版)", "model_batch_check_sync", "ai_model"},
	"POST /api/v1/admin/models/check-selected":                         {"模型管理", "检测选中模型", "model_check_selected", "ai_model"},
	"POST /api/v1/admin/models/sync":                                   {"模型管理", "全量模型同步", "model_sync_all", "ai_model"},
	"POST /api/v1/admin/models/sync/:channelId":                        {"模型管理", "按渠道同步模型", "model_sync_channel_by_id", "ai_model"},
	"POST /api/v1/admin/models/deprecation-scan":                       {"模型管理", "扫描下线模型", "model_deprecation_scan", "ai_model"},
	"POST /api/v1/admin/models/bulk-deprecate":                         {"模型管理", "批量标记下线", "model_bulk_deprecate", "ai_model"},
	"POST /api/v1/admin/models/check-preview":                          {"模型管理", "预览模型检测", "model_check_preview", "ai_model"},
	"POST /api/v1/admin/models/check-preview-sync":                     {"模型管理", "同步预览模型检测", "model_check_preview_sync", "ai_model"},
	"POST /api/v1/admin/models/check-task":                             {"模型管理", "创建模型检测任务", "model_check_task_create", "model_check_task"},
	"POST /api/v1/admin/models/mark-official-deprecated/:supplierCode": {"模型管理", "标记官方下线模型", "model_mark_official_deprecated", "ai_model"},
	"POST /api/v1/admin/models/batch-labels":                           {"模型管理", "批量添加标签", "model_batch_label_add", "ai_model"},
	"DELETE /api/v1/admin/models/batch-labels":                         {"模型管理", "批量移除标签", "model_batch_label_remove", "ai_model"},
	"POST /api/v1/admin/ai-models/:id/labels":                          {"模型管理", "添加模型标签", "model_label_upsert", "model_label"},
	"DELETE /api/v1/admin/ai-models/:id/labels":                        {"模型管理", "删除模型标签", "model_label_delete", "model_label"},
	"POST /api/v1/admin/ai-models/:id/reactivate":                      {"模型管理", "重新激活模型", "model_reactivate", "ai_model"},

	// 全局折扣引擎(v2):一个折扣率覆盖所有价格档(基础/思考/缓存/阶梯)
	"POST /api/v1/admin/ai-models/:id/apply-global-discount":     {"模型管理", "应用全局折扣", "model_apply_global_discount", "model_pricing"},
	"POST /api/v1/admin/ai-models/:id/preview-global-discount":   {"模型管理", "预览全局折扣", "model_preview_global_discount", "model_pricing"},
	"PUT /api/v1/admin/ai-models/:id/lock-overrides":             {"模型管理", "解锁价格档", "model_lock_override_set", "model_pricing"},
	"DELETE /api/v1/admin/ai-models/:id/lock-overrides/:archKey": {"模型管理", "清除解锁档", "model_lock_override_clear", "model_pricing"},

	// 官方定价页 URL 解析(v2)
	"PUT /api/v1/admin/ai-models/:id/official-price-url": {"模型管理", "覆盖官方定价URL", "model_official_price_url_set", "ai_model"},

	// 统一计价试算端点(BillingQuote)
	"POST /api/v1/admin/billing/quote-preview": {"价格分析", "BillingQuote 试算", "billing_quote_preview", "billing_quote"},

	// PriceMatrix 矩阵化定价(v3)
	"PUT /api/v1/admin/ai-models/:id/price-matrix": {"模型管理", "保存价格矩阵", "model_price_matrix_update", "model_pricing"},
	"POST /api/v1/admin/models/feature-probe":      {"模型管理", "能力探测", "model_feature_probe", "ai_model"},
	"POST /api/v1/admin/models/batch-retag":        {"模型管理", "批量重算标签", "model_batch_retag", "ai_model"},
	"POST /api/v1/admin/ai-models/batch-free-tier": {"模型管理", "批量设置免费层", "model_batch_free_tier", "ai_model"},
	"PUT /api/v1/admin/models/batch-status":        {"模型管理", "批量修改模型状态", "model_batch_status_update", "ai_model"},
	"DELETE /api/v1/admin/models/batch-delete":     {"模型管理", "批量删除模型", "model_batch_delete", "ai_model"},
	"POST /api/v1/admin/model-categories":          {"模型管理", "创建模型分类", "model_category_create", "model_category"},
	"PUT /api/v1/admin/model-categories/:id":       {"模型管理", "更新模型分类", "model_category_update", "model_category"},
	"DELETE /api/v1/admin/model-categories/:id":    {"模型管理", "删除模型分类", "model_category_delete", "model_category"},

	// ==================== 模型别名与运维 ====================
	"POST /api/v1/admin/model-aliases":                        {"模型管理", "创建模型别名", "model_alias_create", "model_alias"},
	"PUT /api/v1/admin/model-aliases/:id":                     {"模型管理", "更新模型别名", "model_alias_update", "model_alias"},
	"DELETE /api/v1/admin/model-aliases/:id":                  {"模型管理", "删除模型别名", "model_alias_delete", "model_alias"},
	"POST /api/v1/admin/model-aliases/infer":                  {"模型管理", "推断模型别名", "model_alias_infer", "model_alias"},
	"POST /api/v1/admin/model-ops/calculators":                {"模型管理", "创建模型计算器", "model_ops_calculator_create", "model_ops"},
	"PUT /api/v1/admin/model-ops/calculators/:code":           {"模型管理", "更新模型计算器", "model_ops_calculator_update", "model_ops"},
	"POST /api/v1/admin/model-ops/calculators/reset-defaults": {"模型管理", "重置模型计算器", "model_ops_calculator_reset", "model_ops"},
	"POST /api/v1/admin/model-ops/calculate-preview":          {"模型管理", "预览模型计算", "model_ops_calculate_preview", "model_ops"},
	"POST /api/v1/admin/model-ops/batch-preview":              {"模型管理", "预览批量模型操作", "model_ops_batch_preview", "model_ops"},
	"POST /api/v1/admin/model-ops/batch-execute":              {"模型管理", "执行批量模型操作", "model_ops_batch_execute", "model_ops"},

	// ==================== 定价管理 ====================
	"PUT /api/v1/admin/ai-models/:id/pricing":        {"定价管理", "调整定价", "pricing_update", "model_pricing"},
	"POST /api/v1/admin/model-pricings":              {"定价管理", "新建定价", "pricing_create", "model_pricing"},
	"PUT /api/v1/admin/model-pricings/:id":           {"定价管理", "更新定价", "pricing_update", "model_pricing"},
	"DELETE /api/v1/admin/model-pricings/:id":        {"定价管理", "删除定价", "pricing_delete", "model_pricing"},
	"POST /api/v1/admin/model-pricings/repair":       {"定价管理", "一键修复售价", "pricing_repair", "model_pricing"},
	"POST /api/v1/admin/price-calculate":             {"定价管理", "试算价格", "pricing_calculate", "model_pricing"},
	// 代理折扣体系已于 2026-04-28 移除,以下 6 条 endpoint 不再注册
	// "POST /api/v1/admin/level-discounts":         (removed - AgentLevelDiscount)
	// "PUT /api/v1/admin/level-discounts/:id":      (removed)
	// "DELETE /api/v1/admin/level-discounts/:id":   (removed)
	// "POST /api/v1/admin/agent-pricings":          (removed - AgentPricing)
	// "PUT /api/v1/admin/agent-pricings/:id":       (removed)
	// "DELETE /api/v1/admin/agent-pricings/:id":    (removed)
	"POST /api/v1/admin/models/batch-update-selling": {"定价管理", "批量定价", "pricing_batch_update", "model_pricing"},
	"PUT /api/v1/admin/models/batch-selling-price":   {"定价管理", "批量调整售价", "pricing_batch_selling", "model_pricing"},
	"PUT /api/v1/admin/models/batch-discount":        {"定价管理", "批量调整折扣", "pricing_batch_discount", "model_pricing"},
	"POST /api/v1/admin/models/fill-selling-prices":  {"定价管理", "一键补全售价", "pricing_fill_selling", "model_pricing"},
	"POST /api/v1/admin/price-scrape/apply":          {"定价管理", "应用爬虫价格", "pricing_scrape_apply", "model_pricing"},
	"POST /api/v1/admin/models/preview-prices":       {"定价管理", "预览爬虫价格", "pricing_scrape_preview", "model_pricing"},
	"POST /api/v1/admin/models/apply-prices":         {"定价管理", "应用爬虫价格(v2)", "pricing_scrape_apply_v2", "model_pricing"},
	"POST /api/v1/admin/models/scrape-page":          {"定价管理", "手动抓取价格页", "pricing_scrape_page", "model_pricing"},
	"POST /api/v1/admin/models/batch-scrape":         {"定价管理", "批量按模型爬价", "pricing_batch_scrape", "model_pricing"},
	"POST /api/v1/admin/models/batch-scrape/apply":   {"定价管理", "应用批量爬价", "pricing_batch_scrape_apply", "model_pricing"},
	"POST /api/v1/admin/billing/reconcile":           {"财务管理", "月度账单对账", "billing_reconcile_create", "billing_reconcile"},

	// ==================== 渠道管理 ====================
	"POST /api/v1/admin/channels":                   {"渠道管理", "新建渠道", "channel_create", "channel"},
	"PUT /api/v1/admin/channels/:id":                {"渠道管理", "更新渠道", "channel_update", "channel"},
	"DELETE /api/v1/admin/channels/:id":             {"渠道管理", "删除渠道", "channel_delete", "channel"},
	"POST /api/v1/admin/channels/:id/test":          {"渠道管理", "测试渠道", "channel_test", "channel"},
	"POST /api/v1/admin/channel-groups":             {"渠道管理", "新建渠道组", "channel_group_create", "channel_group"},
	"PUT /api/v1/admin/channel-groups/:id":          {"渠道管理", "更新渠道组", "channel_group_update", "channel_group"},
	"DELETE /api/v1/admin/channel-groups/:id":       {"渠道管理", "删除渠道组", "channel_group_delete", "channel_group"},
	"POST /api/v1/admin/cache/clear-channel-routes": {"渠道管理", "清理路由缓存", "channel_cache_clear", "channel"},
	"POST /api/v1/admin/cache/clear":                {"渠道管理", "清理全部缓存", "cache_clear_all", "cache"},
	"POST /api/v1/admin/cache/clear-all":            {"系统设置", "清理全部缓存", "cache_clear_all", "cache"},
	"POST /api/v1/admin/cache/clear-user-cache":     {"系统设置", "清理用户缓存", "cache_clear_user", "cache"},
	"POST /api/v1/admin/cache/clear-stats-cache":    {"系统设置", "清理统计缓存", "cache_clear_stats", "cache"},
	"POST /api/v1/admin/cache/clear-public-cache":   {"系统设置", "清理公开缓存", "cache_clear_public", "cache"},
	"POST /api/v1/admin/cache/clear-pricing-cache":  {"系统设置", "清理定价缓存", "cache_clear_pricing", "cache"},
	"POST /api/v1/admin/cache/warm":                 {"系统设置", "缓存预热", "cache_warm", "cache"},

	// ==================== 供应商管理 ====================
	"POST /api/v1/admin/suppliers":       {"供应商管理", "新建供应商", "supplier_create", "supplier"},
	"PUT /api/v1/admin/suppliers/:id":    {"供应商管理", "更新供应商", "supplier_update", "supplier"},
	"DELETE /api/v1/admin/suppliers/:id": {"供应商管理", "删除供应商", "supplier_delete", "supplier"},

	// ==================== 订单管理 ====================
	"POST /api/v1/admin/payment/orders/:id/refund": {"订单管理", "手动退款", "order_refund", "order"},
	"POST /api/v1/admin/payment/mock-callback":     {"订单管理", "Mock 回调", "order_mock_callback", "order"},

	// ==================== 退款管理 ====================
	"POST /api/v1/admin/payment/refunds/:id/approve":   {"退款管理", "批准退款", "refund_approve", "refund"},
	"POST /api/v1/admin/payment/refunds/:id/reject":    {"退款管理", "驳回退款", "refund_reject", "refund"},
	"POST /api/v1/admin/payment/refunds/batch-approve": {"退款管理", "批量通过", "refund_batch_approve", "refund"},
	"POST /api/v1/admin/payment/refunds/batch-reject":  {"退款管理", "批量驳回", "refund_batch_reject", "refund"},

	// ==================== 提现审核 ====================
	"POST /api/v1/admin/withdrawals/:id/approve":   {"提现审核", "批准提现", "withdrawal_approve", "withdrawal"},
	"POST /api/v1/admin/withdrawals/:id/reject":    {"提现审核", "驳回提现", "withdrawal_reject", "withdrawal"},
	"POST /api/v1/admin/withdrawals/:id/mark-paid": {"提现审核", "标记已付", "withdrawal_mark_paid", "withdrawal"},

	// ==================== 支付配置 ====================
	"PUT /api/v1/admin/payment-config/:gateway":       {"支付配置", "更新网关", "payment_config_update", "payment_config"},
	"POST /api/v1/admin/payment-config/:gateway/test": {"支付配置", "测试网关", "payment_config_test", "payment_config"},

	// ==================== 邀请返佣 ====================
	"PUT /api/v1/admin/referral-config": {"邀请返佣", "更新返佣配置", "referral_config_update", "referral_config"},

	// ==================== 注册赠送 ====================
	"PUT /api/v1/admin/quota-config": {"注册赠送", "更新赠送配置", "quota_config_update", "quota_config"},

	// ==================== 会员等级 ====================
	"POST /api/v1/admin/member-levels":       {"会员等级", "新建等级", "member_level_create", "member_level"},
	"PUT /api/v1/admin/member-levels/:id":    {"会员等级", "更新等级", "member_level_update", "member_level"},
	"DELETE /api/v1/admin/member-levels/:id": {"会员等级", "删除等级", "member_level_delete", "member_level"},

	// ==================== 合作申请 ====================
	"PATCH /api/v1/admin/partner-applications/:id/status": {"合作申请", "状态流转", "partner_status_change", "partner_application"},
	"PATCH /api/v1/admin/partner-applications/:id/read":   {"合作申请", "标记已读", "partner_mark_read", "partner_application"},
	"PATCH /api/v1/admin/partner-applications/:id/unread": {"合作申请", "标记未读", "partner_mark_unread", "partner_application"},
	"DELETE /api/v1/admin/partner-applications/:id":       {"合作申请", "删除申请", "partner_delete", "partner_application"},

	// ==================== 定时任务 ====================
	"PUT /api/v1/admin/cron-tasks/:name/toggle": {"系统设置", "启停任务", "cron_toggle", "cron_task"},
	"PUT /api/v1/admin/cron-tasks/batch-toggle": {"系统设置", "批量启停", "cron_batch_toggle", "cron_task"},

	// ==================== 能力测试 ====================
	"POST /api/v1/admin/capability-test/run":                        {"能力测试", "执行测试", "capability_test_run", "capability_test"},
	"POST /api/v1/admin/capability-test/run-untested":               {"能力测试", "测试未覆盖模型", "capability_test_untested", "capability_test"},
	"POST /api/v1/admin/capability-test/run-hot-sample":             {"能力测试", "热卖模型抽样", "capability_test_hot_sample", "capability_test"},
	"POST /api/v1/admin/capability-test/estimate":                   {"能力测试", "估算测试成本", "capability_test_estimate", "capability_test"},
	"POST /api/v1/admin/capability-test/cases":                      {"能力测试", "新建测试用例", "capability_test_case_create", "capability_test_case"},
	"PUT /api/v1/admin/capability-test/cases/:id":                   {"能力测试", "更新测试用例", "capability_test_case_update", "capability_test_case"},
	"DELETE /api/v1/admin/capability-test/cases/:id":                {"能力测试", "删除测试用例", "capability_test_case_delete", "capability_test_case"},
	"POST /api/v1/admin/capability-test/cases/seed":                 {"能力测试", "批量导入用例", "capability_test_case_seed", "capability_test_case"},
	"POST /api/v1/admin/capability-test/tasks/:id/auto-apply":       {"能力测试", "自动应用建议", "capability_test_apply", "capability_test"},
	"POST /api/v1/admin/capability-test/tasks/:id/apply":            {"能力测试", "应用建议", "capability_test_apply_manual", "capability_test"},
	"POST /api/v1/admin/capability-test/tasks/:id/propagate":        {"能力测试", "传播到模型", "capability_test_propagate", "capability_test"},
	"POST /api/v1/admin/capability-test/tasks/:id/promote-baseline": {"能力测试", "晋升为基线", "capability_test_promote_baseline", "capability_test"},
	"DELETE /api/v1/admin/capability-test/baselines/:id":            {"能力测试", "删除基线", "capability_test_baseline_delete", "capability_test_baseline"},

	// ==================== 用户敏感操作 ====================
	"POST /api/v1/auth/login":               {"账户安全", "登录", "user_login", "auth"},
	"POST /api/v1/auth/logout":              {"账户安全", "登出", "user_logout", "auth"},
	"POST /api/v1/auth/register":            {"账户安全", "注册", "user_register", "auth"},
	"POST /api/v1/user/password":            {"账户安全", "修改密码", "password_change", "user"},
	"PUT /api/v1/user/password":             {"账户安全", "修改密码", "password_change", "user"},
	"POST /api/v1/user/change-password":     {"账户安全", "修改密码", "password_change", "user"},
	"PUT /api/v1/user/profile":              {"账户安全", "更新个人资料", "profile_update", "user"},
	"POST /api/v1/user/api-keys":            {"API Keys", "创建密钥", "apikey_create", "apikey"},
	"DELETE /api/v1/user/api-keys/:id":      {"API Keys", "删除密钥", "apikey_delete", "apikey"},
	"PUT /api/v1/user/api-keys/:id":         {"API Keys", "更新密钥", "apikey_update", "apikey"},
	"PUT /api/v1/user/api-keys/:id/disable": {"API Keys", "禁用密钥", "apikey_disable", "apikey"},
	"PUT /api/v1/user/api-keys/:id/enable":  {"API Keys", "启用密钥", "apikey_enable", "apikey"},

	// ==================== 权限管理（v4.0 RBAC） ====================
	"POST /api/v1/admin/roles":                  {"权限管理", "创建角色", "role_create", "role"},
	"PUT /api/v1/admin/roles/:id":               {"权限管理", "更新角色", "role_update", "role"},
	"DELETE /api/v1/admin/roles/:id":            {"权限管理", "删除角色", "role_delete", "role"},
	"POST /api/v1/admin/roles/:id/clone":        {"权限管理", "克隆角色", "role_clone", "role"},
	"POST /api/v1/admin/users/:id/roles":        {"权限管理", "授予角色", "user_role_assign", "user_role"},
	"DELETE /api/v1/admin/users/:id/roles/:rid": {"权限管理", "撤销角色", "user_role_revoke", "user_role"},

	// ==================== 运营报表 CSV 导出 ====================
	"POST /api/v1/admin/consumption/daily/export":    {"积分消耗查询", "导出积分消耗CSV", "consumption_export", "consumption_report"},
	"POST /api/v1/admin/registration-gifts/export":   {"注册赠送明细", "导出注册赠送CSV", "registration_gift_export", "registration_gift_report"},
	"POST /api/v1/admin/referral-commissions/export": {"邀请返佣明细", "导出邀请返佣CSV", "referral_commission_export", "referral_commission_report"},

	// ==================== 消息公告 ====================
	"POST /api/v1/admin/announcements":       {"消息公告", "创建公告", "announcement_create", "announcement"},
	"PUT /api/v1/admin/announcements/:id":    {"消息公告", "更新公告", "announcement_update", "announcement"},
	"DELETE /api/v1/admin/announcements/:id": {"消息公告", "删除公告", "announcement_delete", "announcement"},

	// ==================== 邮件管理 ====================
	"PUT /api/v1/admin/email/providers/:channel":       {"邮件管理", "更新邮件供应商配置", "email_provider_update", "email_provider"},
	"POST /api/v1/admin/email/providers/:channel/test": {"邮件管理", "测试邮件供应商连通", "email_provider_test", "email_provider"},
	"POST /api/v1/admin/email/templates":               {"邮件管理", "创建邮件模板", "email_template_create", "email_template"},
	"PUT /api/v1/admin/email/templates/:id":            {"邮件管理", "更新邮件模板", "email_template_update", "email_template"},
	"DELETE /api/v1/admin/email/templates/:id":         {"邮件管理", "删除邮件模板", "email_template_delete", "email_template"},
	"POST /api/v1/admin/email/templates/:id/preview":   {"邮件管理", "预览邮件模板", "email_template_preview", "email_template"},
	"POST /api/v1/admin/email/templates/:id/test-send": {"邮件管理", "测试发送邮件模板", "email_template_test_send", "email_template"},
	"POST /api/v1/admin/email/send":                    {"邮件管理", "发送邮件", "email_send", "email"},
	"POST /api/v1/admin/email/send-batch":              {"邮件管理", "批量发送邮件", "email_send_batch", "email"},
	"POST /api/v1/admin/email/logs/:id/resend":         {"邮件管理", "重发邮件", "email_log_resend", "email"},

	// ==================== OAuth 登录配置 ====================
	"PUT /api/v1/admin/oauth/providers/:provider": {"账户安全", "更新第三方登录配置", "oauth_provider_update", "oauth_provider"},

	// ==================== Privacy / Compliance ====================
	"PATCH /api/v1/admin/privacy/requests/:id":         {"Compliance", "Update privacy request", "privacy_request_update", "privacy_request"},
	"POST /api/v1/admin/privacy/requests/:id/complete": {"Compliance", "Complete privacy request", "privacy_request_complete", "privacy_request"},
	"POST /api/v1/admin/privacy/requests/:id/reject":   {"Compliance", "Reject privacy request", "privacy_request_reject", "privacy_request"},

	// ==================== 热门模型参考库 ====================
	"POST /api/v1/admin/trending-models":       {"模型管理", "新增热门模型", "trending_model_create", "trending_model"},
	"PUT /api/v1/admin/trending-models/:id":    {"模型管理", "更新热门模型", "trending_model_update", "trending_model"},
	"DELETE /api/v1/admin/trending-models/:id": {"模型管理", "删除热门模型", "trending_model_delete", "trending_model"},

	// ==================== 文档管理 ====================
	"POST /api/v1/admin/docs":                 {"文档管理", "创建文档", "doc_create", "doc"},
	"PUT /api/v1/admin/docs/:id":              {"文档管理", "更新文档", "doc_update", "doc"},
	"DELETE /api/v1/admin/docs/:id":           {"文档管理", "删除文档", "doc_delete", "doc"},
	"POST /api/v1/admin/docs/:id/publish":     {"文档管理", "发布文档", "doc_publish", "doc"},
	"POST /api/v1/admin/docs/:id/unpublish":   {"文档管理", "下线文档", "doc_unpublish", "doc"},
	"POST /api/v1/admin/doc-categories":       {"文档管理", "创建分类", "doc_category_create", "doc_category"},
	"PUT /api/v1/admin/doc-categories/:id":    {"文档管理", "更新分类", "doc_category_update", "doc_category"},
	"DELETE /api/v1/admin/doc-categories/:id": {"文档管理", "删除分类", "doc_category_delete", "doc_category"},

	// ==================== AI 客服 ====================
	"POST /api/v1/admin/support/hot-questions":                      {"AI 客服", "创建热门问题", "support_hot_question_create", "support_hot_question"},
	"PUT /api/v1/admin/support/hot-questions/:id":                   {"AI 客服", "更新热门问题", "support_hot_question_update", "support_hot_question"},
	"POST /api/v1/admin/support/hot-questions/:id/publish":          {"AI 客服", "发布热门问题", "support_hot_question_publish", "support_hot_question"},
	"POST /api/v1/admin/support/hot-questions/:id/unpublish":        {"AI 客服", "下线热门问题", "support_hot_question_unpublish", "support_hot_question"},
	"DELETE /api/v1/admin/support/hot-questions/:id":                {"AI 客服", "删除热门问题", "support_hot_question_delete", "support_hot_question"},
	"POST /api/v1/admin/support/model-profiles":                     {"AI 客服", "创建模型配置", "support_model_profile_create", "support_model_profile"},
	"PUT /api/v1/admin/support/model-profiles/:id":                  {"AI 客服", "更新模型配置", "support_model_profile_update", "support_model_profile"},
	"DELETE /api/v1/admin/support/model-profiles/:id":               {"AI 客服", "删除模型配置", "support_model_profile_delete", "support_model_profile"},
	"PATCH /api/v1/admin/support/model-profiles/:id/toggle":         {"AI 客服", "切换模型配置启用", "support_model_profile_toggle", "support_model_profile"},
	"POST /api/v1/admin/support/provider-docs":                      {"AI 客服", "创建供应商文档", "support_provider_doc_create", "support_provider_doc"},
	"PUT /api/v1/admin/support/provider-docs/:id":                   {"AI 客服", "更新供应商文档", "support_provider_doc_update", "support_provider_doc"},
	"DELETE /api/v1/admin/support/provider-docs/:id":                {"AI 客服", "删除供应商文档", "support_provider_doc_delete", "support_provider_doc"},
	"POST /api/v1/admin/support/tickets/:id/reply":                  {"AI 客服", "回复工单", "support_ticket_reply", "support_ticket"},
	"PATCH /api/v1/admin/support/tickets/:id/status":                {"AI 客服", "更新工单状态", "support_ticket_status", "support_ticket"},
	"PATCH /api/v1/admin/support/tickets/:id/assign":                {"AI 客服", "分配工单", "support_ticket_assign", "support_ticket"},
	"POST /api/v1/admin/support/accepted-answers/:id/approve":       {"AI 客服", "批准采纳答案", "support_answer_approve", "support_accepted_answer"},
	"POST /api/v1/admin/support/accepted-answers/:id/reject":        {"AI 客服", "拒绝采纳答案", "support_answer_reject", "support_accepted_answer"},
	"POST /api/v1/admin/support/knowledge/rebuild":                  {"AI 客服", "重建知识库", "support_knowledge_rebuild", "support_knowledge"},
	"POST /api/v1/admin/support/knowledge/rebuild/:source_type/:id": {"AI 客服", "重建单条知识", "support_knowledge_rebuild_one", "support_knowledge"},

	// ==================== 渠道管理（补全） ====================
	"PUT /api/v1/admin/channels/:id/tags":    {"渠道管理", "设置渠道标签", "channel_set_tags", "channel"},
	"POST /api/v1/admin/channels/:id/verify": {"渠道管理", "验证渠道Key", "channel_verify", "channel"},
	"PUT /api/v1/admin/channel-models/:id":   {"渠道管理", "更新渠道模型映射", "channel_model_update", "channel_model"},
	"POST /api/v1/admin/channel-tags":        {"渠道管理", "创建标签", "channel_tag_create", "channel_tag"},
	"PUT /api/v1/admin/channel-tags/:id":     {"渠道管理", "更新标签", "channel_tag_update", "channel_tag"},
	"DELETE /api/v1/admin/channel-tags/:id":  {"渠道管理", "删除标签", "channel_tag_delete", "channel_tag"},

	// ==================== 备份规则 ====================
	"POST /api/v1/admin/backup-rules":             {"渠道管理", "创建备份规则", "backup_rule_create", "backup_rule"},
	"PUT /api/v1/admin/backup-rules/:id":          {"渠道管理", "更新备份规则", "backup_rule_update", "backup_rule"},
	"DELETE /api/v1/admin/backup-rules/:id":       {"渠道管理", "删除备份规则", "backup_rule_delete", "backup_rule"},
	"POST /api/v1/admin/backup-rules/:id/switch":  {"渠道管理", "手动切换备份", "backup_rule_switch", "backup_rule"},
	"POST /api/v1/admin/backup-rules/:id/recover": {"渠道管理", "手动恢复备份", "backup_rule_recover", "backup_rule"},

	// ==================== 自定义渠道 ====================
	"POST /api/v1/admin/custom-channels":                   {"渠道管理", "创建自定义渠道", "custom_channel_create", "custom_channel"},
	"POST /api/v1/admin/custom-channels/default/refresh":   {"渠道管理", "刷新默认渠道", "custom_channel_default_refresh", "custom_channel"},
	"PUT /api/v1/admin/custom-channels/:id":                {"渠道管理", "更新自定义渠道", "custom_channel_update", "custom_channel"},
	"DELETE /api/v1/admin/custom-channels/:id":             {"渠道管理", "删除自定义渠道", "custom_channel_delete", "custom_channel"},
	"PATCH /api/v1/admin/custom-channels/:id/toggle":       {"渠道管理", "启停自定义渠道", "custom_channel_toggle", "custom_channel"},
	"PATCH /api/v1/admin/custom-channels/:id/set-default":  {"渠道管理", "设为默认渠道", "custom_channel_set_default", "custom_channel"},
	"PUT /api/v1/admin/custom-channels/:id/access":         {"渠道管理", "更新渠道访问配置", "custom_channel_access_update", "custom_channel"},
	"POST /api/v1/admin/custom-channels/:id/routes/batch":  {"渠道管理", "批量更新渠道路由", "custom_channel_routes_batch", "custom_channel_route"},
	"POST /api/v1/admin/custom-channels/:id/routes/import": {"渠道管理", "导入渠道路由", "custom_channel_routes_import", "custom_channel_route"},

	// ==================== 编排流程 ====================
	"POST /api/v1/admin/orchestrations":       {"系统设置", "创建编排流程", "orchestration_create", "orchestration"},
	"PUT /api/v1/admin/orchestrations/:id":    {"系统设置", "更新编排流程", "orchestration_update", "orchestration"},
	"DELETE /api/v1/admin/orchestrations/:id": {"系统设置", "删除编排流程", "orchestration_delete", "orchestration"},

	// ==================== 汇率管理 ====================
	"POST /api/v1/admin/exchange-rates":                {"支付配置", "创建汇率", "exchange_rate_create", "exchange_rate"},
	"PUT /api/v1/admin/exchange-rates/:id":             {"支付配置", "更新汇率", "exchange_rate_update", "exchange_rate"},
	"POST /api/v1/admin/payment/exchange-rate/refresh": {"支付配置", "刷新实时汇率", "exchange_rate_refresh", "exchange_rate"},
	"PUT /api/v1/admin/payment/exchange-rate/override": {"支付配置", "手动覆盖汇率", "exchange_rate_override", "exchange_rate"},
	"PUT /api/v1/admin/payment/exchange-rate/config":   {"支付配置", "更新汇率配置", "exchange_rate_config_update", "exchange_rate"},

	// ==================== 支付渠道配置 ====================
	"PUT /api/v1/admin/payment/providers/:type":          {"支付配置", "更新支付渠道", "payment_provider_update", "payment_provider"},
	"PATCH /api/v1/admin/payment/providers/:type/toggle": {"支付配置", "启停支付渠道", "payment_provider_toggle", "payment_provider"},
	"POST /api/v1/admin/payment/providers/:type/test":    {"支付配置", "测试支付渠道", "payment_provider_test", "payment_provider"},
	"POST /api/v1/admin/payment/bank-accounts":           {"支付配置", "创建银行账号", "payment_bank_account_create", "payment_bank_account"},
	"PUT /api/v1/admin/payment/bank-accounts/:id":        {"支付配置", "更新银行账号", "payment_bank_account_update", "payment_bank_account"},
	"DELETE /api/v1/admin/payment/bank-accounts/:id":     {"支付配置", "删除银行账号", "payment_bank_account_delete", "payment_bank_account"},
	"PUT /api/v1/admin/payment/methods/:type":            {"支付配置", "更新付款方式", "payment_method_update", "payment_method"},
	"PATCH /api/v1/admin/payment/methods/:type/toggle":   {"支付配置", "启停付款方式", "payment_method_toggle", "payment_method"},

	// ==================== 支付账户 ====================
	"POST /api/v1/admin/payment/accounts":             {"支付配置", "创建支付账户", "payment_account_create", "payment_account"},
	"PUT /api/v1/admin/payment/accounts/:id":          {"支付配置", "更新支付账户", "payment_account_update", "payment_account"},
	"DELETE /api/v1/admin/payment/accounts/:id":       {"支付配置", "删除支付账户", "payment_account_delete", "payment_account"},
	"PATCH /api/v1/admin/payment/accounts/:id/toggle": {"支付配置", "启停支付账户", "payment_account_toggle", "payment_account"},

	// ==================== 提现（批量操作补全） ====================
	"POST /api/v1/admin/withdrawals/batch-approve": {"提现审核", "批量批准提现", "withdrawal_batch_approve", "withdrawal"},
	"POST /api/v1/admin/withdrawals/batch-reject":  {"提现审核", "批量驳回提现", "withdrawal_batch_reject", "withdrawal"},

	// ==================== 订单（导出补全） ====================
	"POST /api/v1/admin/payment/orders/export": {"订单管理", "导出订单CSV", "order_export", "order"},

	// ==================== 限速配置 ====================
	"PUT /api/v1/admin/rate-limits":              {"系统设置", "更新全局限速", "rate_limit_update", "rate_limit"},
	"POST /api/v1/admin/users/batch-rate-limits": {"用户管理", "批量设置限速", "user_rate_limit_batch", "user"},

	// ==================== 守卫配置 ====================
	"PUT /api/v1/admin/guard-config":             {"系统设置", "更新守卫配置", "guard_config_update", "guard_config"},
	"POST /api/v1/admin/disposable-emails":       {"系统设置", "添加一次性邮箱", "disposable_email_add", "disposable_email"},
	"DELETE /api/v1/admin/disposable-emails/:id": {"系统设置", "删除一次性邮箱", "disposable_email_delete", "disposable_email"},

	// ==================== 佣金覆盖 ====================
	"POST /api/v1/admin/commission-overrides":       {"邀请返佣", "创建佣金覆盖", "commission_override_create", "commission_override"},
	"POST /api/v1/admin/commission-overrides/batch": {"邀请返佣", "批量佣金覆盖", "commission_override_batch", "commission_override"},
	"PUT /api/v1/admin/commission-overrides/:id":    {"邀请返佣", "更新佣金覆盖", "commission_override_update", "commission_override"},
	"DELETE /api/v1/admin/commission-overrides/:id": {"邀请返佣", "删除佣金覆盖", "commission_override_delete", "commission_override"},

	// ==================== 特殊返佣规则（用户×模型）====================
	"POST /api/v1/admin/commission-rules":            {"邀请返佣", "创建特殊返佣规则", "commission_rule_create", "commission_rule"},
	"PUT /api/v1/admin/commission-rules/:id":         {"邀请返佣", "更新特殊返佣规则", "commission_rule_update", "commission_rule"},
	"DELETE /api/v1/admin/commission-rules/:id":      {"邀请返佣", "删除特殊返佣规则", "commission_rule_delete", "commission_rule"},
	"POST /api/v1/admin/commission-rules/:id/toggle": {"邀请返佣", "启停特殊返佣规则", "commission_rule_toggle", "commission_rule"},

	// ==================== 用户特殊折扣（v4.0） ====================
	"POST /api/v1/admin/user-discounts/batch":   {"定价管理", "批量创建用户折扣", "user_discount_batch_create", "user_discount"},
	"POST /api/v1/admin/user-discounts/preview": {"定价管理", "预览用户折扣", "user_discount_preview", "user_discount"},
	"PUT /api/v1/admin/user-discounts/:id":      {"定价管理", "更新用户折扣", "user_discount_update", "user_discount"},
	"DELETE /api/v1/admin/user-discounts/:id":   {"定价管理", "删除用户折扣", "user_discount_delete", "user_discount"},

	// ==================== API 日志回放 ====================
	"POST /api/v1/admin/api-call-logs/:requestId/replay": {"系统设置", "重放API请求", "api_call_log_replay", "api_call_log"},
	"POST /api/v1/admin/reconciliation/snapshots":        {"系统设置", "生成扣费对账快照", "reconciliation_snapshot_create", "api_call_log"},

	// ==================== 后台任务 ====================
	"POST /api/v1/admin/tasks":                  {"系统设置", "创建后台任务", "task_create", "task"},
	"POST /api/v1/admin/tasks/:id/cancel":       {"系统设置", "取消后台任务", "task_cancel", "task"},
	"POST /api/v1/admin/tasks/:id/apply-prices": {"系统设置", "应用任务价格", "task_apply_prices", "task"},

	// ==================== 参数映射 ====================
	"POST /api/v1/admin/param-mappings":                             {"系统设置", "创建参数映射", "param_mapping_create", "param_mapping"},
	"PUT /api/v1/admin/param-mappings/:id":                          {"系统设置", "更新参数映射", "param_mapping_update", "param_mapping"},
	"DELETE /api/v1/admin/param-mappings/:id":                       {"系统设置", "删除参数映射", "param_mapping_delete", "param_mapping"},
	"POST /api/v1/admin/param-mappings/:id/mappings":                {"系统设置", "更新参数映射项", "param_mapping_item_upsert", "param_mapping"},
	"DELETE /api/v1/admin/param-mappings/mappings/:mappingId":       {"系统设置", "删除参数映射项", "param_mapping_item_delete", "param_mapping"},
	"PUT /api/v1/admin/param-mappings/supplier/:code":               {"系统设置", "批量更新供应商映射", "param_mapping_supplier_update", "param_mapping"},
	"POST /api/v1/admin/param-mappings/standard-params/apply":       {"系统设置", "应用标准参数", "param_mapping_standard_apply", "param_mapping"},
	"POST /api/v1/admin/param-mappings/templates/recommended/apply": {"系统设置", "应用推荐参数模板", "param_mapping_template_apply", "param_mapping"},

	// ==================== 限流监控 ====================
	"DELETE /api/v1/admin/rate-limits/active/:key": {"系统设置", "解除单个限流", "rate_limit_release", "rate_limit"},
	"POST /api/v1/admin/rate-limits/reset":         {"系统设置", "批量解除限流", "rate_limit_reset", "rate_limit"},
	"POST /api/v1/admin/cache/clear/:prefix":       {"系统设置", "按前缀清理缓存", "cache_clear_prefix", "cache"},

	// ==================== 系统升级（schema/seed 迁移）====================
	"POST /api/v1/admin/system/migrate":    {"系统设置", "触发 schema 升级", "system_migrate", "system"},
	"PUT /api/v1/admin/system/config/:key": {"系统设置", "更新系统配置", "system_config_update", "system_config"},
}

// readRouteMap 路由 → 元数据映射表（GET 读操作）
// 独立于 routeMap 维护，用于 RBAC 权限系统的读权限定义。
// 审计中间件不消费此表（GET 不写审计日志），PermissionGate 消费合并后的两张表。
//
// 仅纳入 /admin/* 下的 GET 端点（/user/* 按 JWT + ScopedDB 直接授权，不走 PermissionGate）。
// 新增 admin GET 端点时必须同步追加，否则会被 PermissionGate 直接 403。
var readRouteMap = map[string]RouteMeta{
	// ==================== 用户管理 ====================
	"GET /api/v1/admin/users":                              {"用户管理", "用户列表", "user_list_read", "user"},
	"GET /api/v1/admin/users/:id":                          {"用户管理", "用户详情", "user_get_read", "user"},
	"GET /api/v1/admin/tenants":                            {"用户管理", "租户列表", "tenant_list_read", "tenant"},
	"GET /api/v1/admin/commission-overrides":               {"用户管理", "佣金覆盖列表", "commission_override_list_read", "commission_override"},
	"GET /api/v1/admin/commission-overrides/user/:user_id": {"用户管理", "用户佣金覆盖", "commission_override_by_user_read", "commission_override"},
	"GET /api/v1/admin/commission-rules":                   {"邀请返佣", "特殊返佣规则列表", "commission_rule_list_read", "commission_rule"},
	"GET /api/v1/admin/commission-rules/:id":               {"邀请返佣", "特殊返佣规则详情", "commission_rule_get_read", "commission_rule"},

	// ==================== 用户特殊折扣读（v4.0） ====================
	"GET /api/v1/admin/user-discounts":     {"定价管理", "用户折扣列表", "user_discount_list_read", "user_discount"},
	"GET /api/v1/admin/user-discounts/:id": {"定价管理", "用户折扣详情", "user_discount_get_read", "user_discount"},

	// ==================== 余额管理 ====================
	"GET /api/v1/admin/users/:id/balance":      {"余额管理", "用户余额", "user_balance_get_read", "balance"},
	"GET /api/v1/admin/users/:id/limits":       {"用户管理", "用户限速配置", "user_rate_limit_get_read", "user"},
	"GET /api/v1/admin/balance/reconciliation": {"余额管理", "余额对账", "balance_reconciliation_read", "balance"},

	// ==================== 模型管理 ====================
	"GET /api/v1/admin/ai-models":               {"模型管理", "模型列表", "model_list_read", "ai_model"},
	"GET /api/v1/admin/ai-models/:id":           {"模型管理", "模型详情", "model_get_read", "ai_model"},
	"GET /api/v1/admin/ai-models/stats":         {"模型管理", "模型统计", "model_stats_read", "ai_model"},
	"GET /api/v1/admin/ai-models/:id/preflight": {"模型管理", "模型启用预检", "model_preflight_read", "ai_model"},
	"GET /api/v1/admin/ai-models/:id/labels":    {"模型管理", "模型标签", "model_label_list_read", "model_label"},
	// 官方定价页 URL 解析(v2)
	"GET /api/v1/admin/ai-models/:id/official-price-url": {"模型管理", "解析官方定价URL", "model_official_price_url_read", "ai_model"},
	// PriceMatrix 矩阵读取(v3)
	"GET /api/v1/admin/ai-models/:id/price-matrix": {"模型管理", "读取价格矩阵", "model_price_matrix_read", "model_pricing"},
	"GET /api/v1/admin/models/label-keys":          {"模型管理", "标签键列表", "model_label_key_list_read", "model_label"},
	"GET /api/v1/admin/model-categories":           {"模型管理", "分类列表", "model_category_list_read", "model_category"},
	"GET /api/v1/admin/model-categories/:id":       {"模型管理", "分类详情", "model_category_get_read", "model_category"},
	"GET /api/v1/admin/model-aliases":              {"模型管理", "模型别名列表", "model_alias_list_read", "model_alias"},
	"GET /api/v1/admin/model-aliases/resolve":      {"模型管理", "解析模型别名", "model_alias_resolve_read", "model_alias"},
	"GET /api/v1/admin/model-ops/profiles":         {"模型管理", "模型运维档案", "model_ops_profile_list_read", "model_ops"},
	"GET /api/v1/admin/model-ops/calculators":      {"模型管理", "模型计算器列表", "model_ops_calculator_list_read", "model_ops"},
	"GET /api/v1/admin/model-ops/scenarios":        {"模型管理", "价格场景预设列表", "model_ops_scenarios_read", "model_ops"},
	"GET /api/v1/admin/models/check-history":       {"模型管理", "检测历史", "model_check_history_read", "model_check"},
	"GET /api/v1/admin/models/check-latest":        {"模型管理", "检测汇总", "model_check_latest_read", "model_check"},
	"GET /api/v1/admin/models/check-tasks":         {"模型管理", "检测任务列表", "model_check_task_list_read", "model_check_task"},
	"GET /api/v1/admin/models/check-tasks/:id":     {"模型管理", "检测任务详情", "model_check_task_get_read", "model_check_task"},
	"GET /api/v1/admin/models/scanned-offline":     {"模型管理", "扫描下线汇总", "model_deprecation_scan_read", "ai_model"},
	"GET /api/v1/admin/trending-models":            {"模型管理", "热门模型库", "trending_model_list_read", "trending_model"},

	// ==================== 定价管理 ====================
	"GET /api/v1/admin/model-pricings":                      {"定价管理", "定价列表", "pricing_list_read", "model_pricing"},
	// "GET /api/v1/admin/agent-pricings": (removed 2026-04-28 — 代理折扣体系移除)
	"GET /api/v1/admin/models/price-sync-logs":              {"定价管理", "价格同步日志", "pricing_sync_log_read", "pricing_sync_log"},
	"GET /api/v1/admin/models/batch-scrape/:task_id/result": {"定价管理", "批量爬价结果", "pricing_batch_scrape_result_read", "model_pricing"},
	"GET /api/v1/admin/price-matrix":                        {"定价管理", "价格矩阵", "price_matrix_read", "price_matrix"},

	// ==================== 渠道管理 ====================
	"GET /api/v1/admin/channels":                               {"渠道管理", "渠道列表", "channel_list_read", "channel"},
	"GET /api/v1/admin/channels/:id":                           {"渠道管理", "渠道详情", "channel_get_read", "channel"},
	"GET /api/v1/admin/channels/custom-params/schema":          {"渠道管理", "自定义参数Schema", "channel_custom_params_schema_read", "channel"},
	"GET /api/v1/admin/channel-groups":                         {"渠道管理", "渠道组列表", "channel_group_list_read", "channel_group"},
	"GET /api/v1/admin/channel-groups/:id":                     {"渠道管理", "渠道组详情", "channel_group_get_read", "channel_group"},
	"GET /api/v1/admin/channel-groups/:id/channels":            {"渠道管理", "组内渠道列表", "channel_group_channels_read", "channel"},
	"GET /api/v1/admin/channel-stats":                          {"渠道管理", "渠道统计", "channel_stats_read", "channel_stats"},
	"GET /api/v1/admin/channel-stats/models":                   {"渠道管理", "渠道模型统计", "channel_model_stats_read", "channel_stats"},
	"GET /api/v1/admin/channel-models":                         {"渠道管理", "渠道模型映射", "channel_model_list_read", "channel_model"},
	"GET /api/v1/admin/channel-tags":                           {"渠道管理", "渠道标签列表", "channel_tag_list_read", "channel_tag"},
	"GET /api/v1/admin/channel-tags/:id/stats":                 {"渠道管理", "标签统计", "channel_tag_stats_read", "channel_tag"},
	"GET /api/v1/admin/custom-channels":                        {"渠道管理", "自定义渠道列表", "custom_channel_list_read", "custom_channel"},
	"GET /api/v1/admin/custom-channels/default/refresh/status": {"渠道管理", "默认渠道刷新状态", "custom_channel_refresh_status_read", "custom_channel"},
	"GET /api/v1/admin/backup-rules":                           {"渠道管理", "备份规则列表", "backup_rule_list_read", "backup_rule"},
	"GET /api/v1/admin/backup-rules/:id":                       {"渠道管理", "备份规则详情", "backup_rule_get_read", "backup_rule"},
	"GET /api/v1/admin/backup-rules/:id/status":                {"渠道管理", "备份状态", "backup_rule_status_read", "backup_rule"},
	"GET /api/v1/admin/backup-rules/:id/events":                {"渠道管理", "备份事件列表", "backup_event_list_read", "backup_event"},

	// ==================== 供应商管理 ====================
	"GET /api/v1/admin/suppliers":     {"供应商管理", "供应商列表", "supplier_list_read", "supplier"},
	"GET /api/v1/admin/suppliers/:id": {"供应商管理", "供应商详情", "supplier_get_read", "supplier"},

	// ==================== 订单管理 ====================
	"GET /api/v1/admin/payment/orders":       {"订单管理", "订单列表", "order_list_read", "order"},
	"GET /api/v1/admin/payment/orders/:id":   {"订单管理", "订单详情", "order_get_read", "order"},
	"GET /api/v1/admin/payment/orders/stats": {"订单管理", "订单统计", "order_stats_read", "order"},

	// ==================== 退款管理 ====================
	"GET /api/v1/admin/payment/refunds":     {"退款管理", "退款列表", "refund_list_read", "refund"},
	"GET /api/v1/admin/payment/refunds/:id": {"退款管理", "退款详情", "refund_get_read", "refund"},

	// ==================== 发票管理 ====================
	"GET /api/v1/admin/invoices":      {"发票管理", "发票列表", "invoice_admin_list_read", "invoice"},
	"GET /api/v1/admin/invoices/:id":  {"发票管理", "发票详情", "invoice_admin_get_read", "invoice"},
	"GET /api/v1/user/invoices":       {"用户申请", "发票列表", "user_invoice_list_read", "invoice"},
	"GET /api/v1/user/invoices/:id":   {"用户申请", "发票详情", "user_invoice_get_read", "invoice"},
	"GET /api/v1/user/invoice-titles": {"用户申请", "抬头列表", "user_invoice_title_list_read", "invoice_title"},

	// ==================== 用户视角 BillingQuote 查询（A4 任务）====================
	"GET /api/v1/user/api-call-logs/:requestId/quote": {"用户申请", "查看本次扣费明细", "user_quote_get_read", "api_call_log"},

	// ==================== 提现审核 ====================
	"GET /api/v1/admin/withdrawals":       {"提现审核", "提现列表", "withdrawal_list_read", "withdrawal"},
	"GET /api/v1/admin/withdrawals/:id":   {"提现审核", "提现详情", "withdrawal_get_read", "withdrawal"},
	"GET /api/v1/admin/withdrawals/stats": {"提现审核", "提现统计", "withdrawal_stats_read", "withdrawal"},

	// ==================== 支付配置 ====================
	"GET /api/v1/admin/payment/accounts":                   {"支付配置", "账户列表", "payment_account_list_read", "payment_account"},
	"GET /api/v1/admin/payment/event-logs":                 {"支付配置", "事件日志列表", "payment_event_log_list_read", "payment_event_log"},
	"GET /api/v1/admin/payment/event-logs/by-payment/:id":  {"支付配置", "订单事件日志", "payment_event_log_by_payment_read", "payment_event_log"},
	"GET /api/v1/admin/payment/event-logs/by-refund/:id":   {"支付配置", "退款事件日志", "payment_event_log_by_refund_read", "payment_event_log"},
	"GET /api/v1/admin/payment/event-logs/by-withdraw/:id": {"支付配置", "提现事件日志", "payment_event_log_by_withdraw_read", "payment_event_log"},
	"GET /api/v1/admin/payment/exchange-rate/history":      {"支付配置", "汇率历史", "exchange_rate_history_read", "exchange_rate"},
	"GET /api/v1/admin/payment/exchange-rate/config":       {"支付配置", "汇率配置", "exchange_rate_config_get_read", "exchange_rate"},
	"GET /api/v1/admin/payment/providers":                  {"支付配置", "支付渠道列表", "payment_provider_list_read", "payment_provider"},
	"GET /api/v1/admin/payment/bank-accounts":              {"支付配置", "银行账号列表", "payment_bank_account_list_read", "payment_bank_account"},
	"GET /api/v1/admin/payment/methods":                    {"支付配置", "付款方式列表", "payment_method_list_read", "payment_method"},
	"GET /api/v1/admin/users/:id/payment-profile":          {"支付配置", "用户支付档案", "user_payment_profile_read", "user_payment"},
	"GET /api/v1/admin/users/:id/credit-stats":             {"支付配置", "用户积分统计", "user_credit_stats_read", "user_credit"},
	"GET /api/v1/admin/exchange-rates":                     {"支付配置", "汇率列表", "exchange_rate_list_read", "exchange_rate"},

	// ==================== 邀请返佣 ====================
	"GET /api/v1/admin/referral-config":            {"邀请返佣", "返佣配置", "referral_config_get_read", "referral_config"},
	"GET /api/v1/admin/referral-commissions":       {"邀请返佣", "返佣明细列表", "referral_commission_list_read", "referral_commission"},
	"GET /api/v1/admin/referral-commissions/daily": {"邀请返佣", "日维度返佣", "referral_commission_daily_read", "referral_commission"},
	"GET /api/v1/admin/referral-commissions/stats": {"邀请返佣", "返佣统计", "referral_commission_stats_read", "referral_commission"},
	"GET /api/v1/admin/commissions":                {"邀请返佣", "返佣条目列表", "commission_list_read", "commission"},

	// ==================== 注册赠送 ====================
	"GET /api/v1/admin/quota-config":             {"注册赠送", "赠送配置", "quota_config_get_read", "quota_config"},
	"GET /api/v1/admin/registration-gifts":       {"注册赠送", "赠送明细列表", "registration_gift_list_read", "registration_gift"},
	"GET /api/v1/admin/registration-gifts/stats": {"注册赠送", "赠送统计", "registration_gift_stats_read", "registration_gift"},

	// ==================== 会员等级 ====================
	"GET /api/v1/admin/member-levels":   {"会员等级", "等级列表", "member_level_list_read", "member_level"},
	// "GET /api/v1/admin/level-discounts": (removed 2026-04-28 — 代理折扣体系移除)

	// ==================== 合作申请 ====================
	"GET /api/v1/admin/partner-applications":       {"合作申请", "申请列表", "partner_application_list_read", "partner_application"},
	"GET /api/v1/admin/partner-applications/:id":   {"合作申请", "申请详情", "partner_application_get_read", "partner_application"},
	"GET /api/v1/admin/partner-applications/stats": {"合作申请", "申请统计", "partner_application_stats_read", "partner_application"},

	// ==================== 系统设置 ====================
	"GET /api/v1/admin/system/schema-version":                   {"系统设置", "查看 schema 版本", "system_schema_version_read", "system"},
	"GET /api/v1/admin/system/config/:key":                      {"系统设置", "读取系统配置", "system_config_read", "system_config"},
	"GET /api/v1/admin/cron-tasks":                              {"系统设置", "定时任务列表", "cron_task_list_read", "cron_task"},
	"GET /api/v1/admin/cron-task-runs":                          {"系统设置", "定时任务运行记录", "cron_task_run_list_read", "cron_task_run"},
	"GET /api/v1/admin/cron-tasks/:name/runs":                   {"系统设置", "单任务运行记录", "cron_task_run_by_task_read", "cron_task_run"},
	"GET /api/v1/admin/audit-logs":                              {"系统设置", "审计日志列表", "audit_log_list_read", "audit_log"},
	"GET /api/v1/admin/audit-logs/menus":                        {"系统设置", "审计菜单列表", "audit_menu_list_read", "audit_menu"},
	"GET /api/v1/admin/config-audit":                            {"系统设置", "配置审计列表", "config_audit_list_read", "config_audit"},
	"GET /api/v1/admin/cache/stats":                             {"系统设置", "缓存统计", "cache_stats_read", "cache"},
	"GET /api/v1/admin/param-mappings":                          {"系统设置", "参数映射列表", "param_mapping_list_read", "param_mapping"},
	"GET /api/v1/admin/param-mappings/:id":                      {"系统设置", "参数映射详情", "param_mapping_get_read", "param_mapping"},
	"GET /api/v1/admin/param-mappings/supplier/:code":           {"系统设置", "供应商参数映射", "param_mapping_by_supplier_read", "param_mapping"},
	"GET /api/v1/admin/param-mappings/coverage":                 {"系统设置", "参数映射覆盖率", "param_mapping_coverage_read", "param_mapping"},
	"GET /api/v1/admin/param-mappings/standard-params":          {"系统设置", "标准参数列表", "param_mapping_standard_read", "param_mapping"},
	"GET /api/v1/admin/param-mappings/templates/recommended":    {"系统设置", "推荐参数模板", "param_mapping_template_read", "param_mapping"},
	"GET /api/v1/admin/param-support":                           {"系统设置", "参数支持列表", "param_support_read", "param_support"},
	"GET /api/v1/admin/guard-config":                            {"系统设置", "守卫配置", "guard_config_read", "guard_config"},
	"GET /api/v1/admin/disposable-emails":                       {"系统设置", "一次性邮箱列表", "disposable_email_list_read", "disposable_email"},
	"GET /api/v1/admin/orchestrations":                          {"系统设置", "编排流程列表", "orchestration_list_read", "orchestration"},
	"GET /api/v1/admin/orchestrations/:id":                      {"系统设置", "编排流程详情", "orchestration_get_read", "orchestration"},
	"GET /api/v1/admin/tasks":                                   {"系统设置", "后台任务列表", "task_list_read", "task"},
	"GET /api/v1/admin/tasks/:id":                               {"系统设置", "后台任务详情", "task_get_read", "task"},
	"GET /api/v1/admin/rate-limits":                             {"系统设置", "限流配置读取", "rate_limit_config_read", "rate_limit"},
	"GET /api/v1/admin/rate-limits/active":                      {"系统设置", "活跃限流桶", "rate_limit_active_read", "rate_limit"},
	"GET /api/v1/admin/rate-limits/events":                      {"系统设置", "限流事件日志", "rate_limit_event_read", "rate_limit"},
	"GET /api/v1/admin/api-call-logs":                           {"系统设置", "API调用日志列表", "api_call_log_list_read", "api_call_log"},
	"GET /api/v1/admin/api-call-logs/summary":                   {"系统设置", "API调用日志汇总", "api_call_log_summary_read", "api_call_log"},
	"GET /api/v1/admin/api-call-logs/reconciliation":            {"System", "API call reconciliation", "api_call_log_summary_read", "api_call_log"},
	"GET /api/v1/admin/api-call-logs/reconciliation/export":     {"System", "API call reconciliation export", "api_call_log_summary_read", "api_call_log"},
	"GET /api/v1/admin/api-call-logs/:requestId":                {"系统设置", "API调用日志详情", "api_call_log_get_read", "api_call_log"},
	"GET /api/v1/admin/api-call-logs/:requestId/chain":          {"系统设置", "API调用链路", "api_call_log_chain_read", "api_call_log"},
	"GET /api/v1/admin/api-call-logs/:requestId/cost-breakdown":  {"系统设置", "API调用成本分解", "api_call_log_cost_read", "api_call_log"},
	"GET /api/v1/admin/api-call-logs/:requestId/three-way-check": {"系统设置", "三方一致性核对", "cost_three_way_check_read", "api_call_log"},
	"GET /api/v1/admin/cost-consistency/scan":                    {"系统设置", "三方一致性批量扫描", "cost_consistency_scan_read", "api_call_log"},
	"GET /api/v1/admin/reconciliation":                          {"系统设置", "扣费对账摘要", "api_call_log_summary_read", "api_call_log"},
	"GET /api/v1/admin/reconciliation/snapshots":                {"系统设置", "扣费对账快照列表", "api_call_log_summary_read", "api_call_log"},

	// ==================== 账户安全（登录日志） ====================
	"GET /api/v1/admin/auth-logs":       {"账户安全", "登录日志列表", "auth_log_list_read", "auth_log"},
	"GET /api/v1/admin/auth-logs/stats": {"账户安全", "登录日志统计", "auth_log_stats_read", "auth_log"},

	// ==================== 能力测试 ====================
	"GET /api/v1/admin/capability-test/cases":                 {"能力测试", "测试用例列表", "capability_test_case_list_read", "capability_test_case"},
	"GET /api/v1/admin/capability-test/untested-count":        {"能力测试", "未测试模型计数", "capability_test_untested_count_read", "capability_test"},
	"GET /api/v1/admin/capability-test/hot-sample-info":       {"能力测试", "热点样本信息", "capability_test_hot_sample_info_read", "capability_test"},
	"GET /api/v1/admin/capability-test/tasks":                 {"能力测试", "测试任务列表", "capability_test_task_list_read", "capability_test_task"},
	"GET /api/v1/admin/capability-test/tasks/:id":             {"能力测试", "测试任务详情", "capability_test_task_get_read", "capability_test_task"},
	"GET /api/v1/admin/capability-test/tasks/:id/results":     {"能力测试", "任务测试结果", "capability_test_result_read", "capability_test_result"},
	"GET /api/v1/admin/capability-test/tasks/:id/suggestions": {"能力测试", "任务建议列表", "capability_test_suggestion_read", "capability_test_suggestion"},
	"GET /api/v1/admin/capability-test/tasks/:id/regressions": {"能力测试", "任务退化列表", "capability_test_regression_read", "capability_test_regression"},
	"GET /api/v1/admin/capability-test/baselines":             {"能力测试", "基线列表", "capability_test_baseline_read", "capability_test_baseline"},

	// ==================== API Keys ====================
	"GET /api/v1/admin/api-keys": {"API Keys", "系统密钥列表", "admin_apikey_list_read", "apikey"},

	// ==================== 积分消耗查询 ====================
	"GET /api/v1/admin/consumption/daily":           {"积分消耗查询", "日消耗明细", "consumption_daily_read", "consumption"},
	"GET /api/v1/admin/consumption/model-breakdown": {"积分消耗查询", "模型分解", "consumption_model_breakdown_read", "consumption"},

	// ==================== 运营报表 ====================
	"GET /api/v1/admin/reports/overview":                   {"运营报表", "报表概览", "report_overview_read", "report"},
	"GET /api/v1/admin/reports/usage":                      {"运营报表", "用量报告", "report_usage_read", "report"},
	"GET /api/v1/admin/reports/revenue":                    {"运营报表", "收入报告", "report_revenue_read", "report"},
	"GET /api/v1/admin/reports/profit":                     {"运营报表", "利润报告", "report_profit_read", "report"},
	"GET /api/v1/admin/reports/profit/trend":               {"运营报表", "利润趋势", "report_profit_trend_read", "report"},
	"GET /api/v1/admin/reports/profit/top-agents":          {"运营报表", "顶级代理", "report_top_agents_read", "report"},
	"GET /api/v1/admin/reports/profit/drilldown/:tenantId": {"运营报表", "利润下钻", "report_profit_drilldown_read", "report"},
	"GET /api/v1/admin/stats/registrations":                {"运营报表", "注册统计", "stats_registrations_read", "stats"},
	"GET /api/v1/admin/stats/daily":                        {"运营报表", "每日统计", "stats_daily_read", "stats"},
	"GET /api/v1/admin/stats/referrals":                    {"运营报表", "推荐统计", "stats_referrals_read", "stats"},
	"GET /api/v1/admin/stats/pnl":                          {"运营报表", "损益表", "stats_pnl_read", "stats"},
	"GET /api/v1/admin/stats/payment-analysis":             {"运营报表", "支付分析", "stats_payment_analysis_read", "stats"},

	// ==================== 消息公告 ====================
	"GET /api/v1/admin/announcements":       {"消息公告", "公告列表", "announcement_list_read", "announcement"},
	"GET /api/v1/admin/announcements/stats": {"消息公告", "公告统计", "announcement_stats_read", "announcement"},

	// ==================== 邮件管理 ====================
	"GET /api/v1/admin/email/providers":     {"邮件管理", "邮件供应商列表", "email_provider_list_read", "email_provider"},
	"GET /api/v1/admin/email/templates":     {"邮件管理", "邮件模板列表", "email_template_list_read", "email_template"},
	"GET /api/v1/admin/email/templates/:id": {"邮件管理", "邮件模板详情", "email_template_get_read", "email_template"},
	"GET /api/v1/admin/email/logs":          {"邮件管理", "邮件发送记录", "email_log_list_read", "email"},

	// ==================== OAuth 登录配置 ====================
	"GET /api/v1/admin/oauth/providers": {"账户安全", "第三方登录配置", "oauth_provider_list_read", "oauth_provider"},

	// ==================== Privacy / Compliance ====================
	"GET /api/v1/admin/privacy/requests":     {"Compliance", "Privacy request list", "privacy_request_list_read", "privacy_request"},
	"GET /api/v1/admin/privacy/requests/:id": {"Compliance", "Privacy request detail", "privacy_request_get_read", "privacy_request"},

	// ==================== 文档管理 ====================
	"GET /api/v1/admin/docs":           {"文档管理", "文档列表", "doc_list_read", "doc"},
	"GET /api/v1/admin/docs/:id":       {"文档管理", "文档详情", "doc_get_read", "doc"},
	"GET /api/v1/admin/doc-categories": {"文档管理", "文档分类列表", "doc_category_list_read", "doc_category"},

	// ==================== AI 客服 ====================
	"GET /api/v1/admin/support/status":           {"AI 客服", "客服状态", "support_status_read", "support"},
	"GET /api/v1/admin/support/hot-questions":    {"AI 客服", "热门问题列表", "support_hot_question_read", "support_hot_question"},
	"GET /api/v1/admin/support/provider-docs":    {"AI 客服", "供应商文档列表", "support_provider_doc_read", "support_provider_doc"},
	"GET /api/v1/admin/support/tickets":          {"AI 客服", "工单列表", "support_ticket_list_read", "support_ticket"},
	"GET /api/v1/admin/support/tickets/:id":      {"AI 客服", "工单详情", "support_ticket_get_read", "support_ticket"},
	"GET /api/v1/admin/support/accepted-answers": {"AI 客服", "采纳答案列表", "support_accepted_answer_read", "support_accepted_answer"},
	"GET /api/v1/admin/support/knowledge/stats":  {"AI 客服", "知识库统计", "support_knowledge_stats_read", "support_knowledge"},
	"GET /api/v1/admin/support/model-profiles":   {"AI 客服", "模型配置列表", "support_model_profile_read", "support_model_profile"},

	// ==================== 权限管理（Phase 6 落地，此处仅预声明） ====================
	"GET /api/v1/admin/permissions":     {"权限管理", "权限目录", "perm_list_read", "permission"},
	"GET /api/v1/admin/roles":           {"权限管理", "角色列表", "role_list_read", "role"},
	"GET /api/v1/admin/roles/:id":       {"权限管理", "角色详情", "role_get_read", "role"},
	"GET /api/v1/admin/roles/:id/users": {"权限管理", "角色用户列表", "role_user_list_read", "role"},
	"GET /api/v1/admin/users/:id/roles": {"权限管理", "用户角色", "user_role_list_read", "user_role"},
}

// Lookup 按 method+path 查找路由元数据，未命中返回 ok=false
// 先查写操作表（含审计语义），再查读操作表（仅权限语义）。
func Lookup(method, fullPath string) (RouteMeta, bool) {
	key := method + " " + fullPath
	if m, ok := routeMap[key]; ok {
		return m, true
	}
	if m, ok := readRouteMap[key]; ok {
		return m, true
	}
	return RouteMeta{}, false
}

// LookupAuditMeta 返回写操作审计元数据。
// 已登记路由使用精确配置；少量用户侧敏感写操作漏配时，退回到统一兜底分类，避免完全无痕。
func LookupAuditMeta(method, fullPath string) (RouteMeta, bool) {
	if !isWriteMethod(method) {
		return RouteMeta{}, false
	}
	if m, ok := routeMap[method+" "+fullPath]; ok {
		return m, true
	}
	return fallbackWriteMeta(method, fullPath)
}

// IsAuditRelevant 返回给定路由是否应记录审计日志
// 语义：写操作表命中 → true；读操作表命中或未命中 → false
func IsAuditRelevant(method, fullPath string) bool {
	_, ok := routeMap[method+" "+fullPath]
	return ok
}

func isWriteMethod(method string) bool {
	switch method {
	case "POST", "PUT", "PATCH", "DELETE":
		return true
	default:
		return false
	}
}

func fallbackWriteMeta(method, fullPath string) (RouteMeta, bool) {
	switch {
	case strings.HasPrefix(fullPath, "/api/v1/auth/"):
		return RouteMeta{"账户安全", "账户敏感操作", "auth_sensitive_write", "auth"}, true
	case strings.HasPrefix(fullPath, "/api/v1/user/"):
		return RouteMeta{"用户中心", "用户敏感操作", "user_sensitive_write", "user"}, true
	case strings.HasPrefix(fullPath, "/api/v1/payment/"):
		return RouteMeta{"订单管理", "支付敏感操作", "payment_sensitive_write", "payment"}, true
	default:
		return RouteMeta{}, false
	}
}

// Entry 单条路由条目（含方法/路径 + 元数据 + 读写标志），供 RBAC seed 使用
type Entry struct {
	Method string
	Path   string
	Meta   RouteMeta
	IsRead bool // true = GET 读权限，不写审计日志
}

// RouteMapEntries 返回所有已登记的路由条目（写 + 读合并）
// 用于权限系统 seed：为每条路由生成对应的 permissions 行。
func RouteMapEntries() []Entry {
	out := make([]Entry, 0, len(routeMap)+len(readRouteMap))
	for key, meta := range routeMap {
		method, path := splitKey(key)
		out = append(out, Entry{Method: method, Path: path, Meta: meta, IsRead: false})
	}
	for key, meta := range readRouteMap {
		method, path := splitKey(key)
		out = append(out, Entry{Method: method, Path: path, Meta: meta, IsRead: true})
	}
	return out
}

// splitKey 将 "METHOD /path" 拆分为 method 和 path
func splitKey(key string) (string, string) {
	for i := 0; i < len(key); i++ {
		if key[i] == ' ' {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

// AllMenus 返回去重的菜单名列表，供前端筛选下拉框使用
func AllMenus() []string {
	seen := make(map[string]struct{}, len(routeMap)+len(readRouteMap))
	for _, meta := range routeMap {
		if meta.Menu != "" {
			seen[meta.Menu] = struct{}{}
		}
	}
	for _, meta := range readRouteMap {
		if meta.Menu != "" {
			seen[meta.Menu] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}
