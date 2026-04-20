// Package audit 审计日志中间件 - 路由元数据映射
//
// 维护 path+method → {菜单, 功能, action, 资源类型} 的映射，由中间件根据 c.FullPath() 查表，
// 命中则记录审计日志，未命中则跳过（白名单策略，防止日志爆炸）。
package audit

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
	// ==================== 用户管理 ====================
	"PUT /api/v1/admin/users/:id":             {"用户管理", "更新用户", "user_update", "user"},
	"DELETE /api/v1/admin/users/:id":          {"用户管理", "删除用户", "user_delete", "user"},
	"PATCH /api/v1/admin/users/:id/status":    {"用户管理", "禁用启用", "user_status_change", "user"},
	"POST /api/v1/admin/users":                {"用户管理", "创建用户", "user_create", "user"},
	"POST /api/v1/admin/users/batch":          {"用户管理", "批量创建用户", "user_batch_create", "user"},
	"PATCH /api/v1/admin/users/:id/role":      {"用户管理", "调整角色", "user_role_change", "user"},
	"POST /api/v1/admin/users/:id/reset-password": {"用户管理", "重置密码", "user_reset_password", "user"},

	// ==================== 余额管理 ====================
	"POST /api/v1/admin/balance/adjust":          {"余额管理", "调整余额", "balance_adjust", "balance"},
	"POST /api/v1/admin/users/:id/grant-credits": {"余额管理", "赠送积分", "balance_grant", "balance"},

	// ==================== AI 模型管理 ====================
	"POST /api/v1/admin/ai-models":                  {"模型管理", "创建模型", "model_create", "ai_model"},
	"PUT /api/v1/admin/ai-models/:id":               {"模型管理", "更新模型", "model_update", "ai_model"},
	"DELETE /api/v1/admin/ai-models/:id":            {"模型管理", "删除模型", "model_delete", "ai_model"},
	"POST /api/v1/admin/ai-models/:id/offline":      {"模型管理", "下线模型", "model_offline", "ai_model"},
	"POST /api/v1/admin/ai-models/:id/verify":       {"模型管理", "审核模型", "model_verify", "ai_model"},
	"POST /api/v1/admin/ai-models/batch-check":      {"模型管理", "批量检测", "model_batch_check", "ai_model"},
	"POST /api/v1/admin/models/sync-all":            {"模型管理", "全量同步", "model_sync_all", "ai_model"},
	"POST /api/v1/admin/models/sync":                {"模型管理", "按渠道同步", "model_sync_channel", "ai_model"},

	// ==================== 定价管理 ====================
	"PUT /api/v1/admin/ai-models/:id/pricing":         {"定价管理", "调整定价", "pricing_update", "model_pricing"},
	"POST /api/v1/admin/model-pricings":               {"定价管理", "新建定价", "pricing_create", "model_pricing"},
	"PUT /api/v1/admin/model-pricings/:id":            {"定价管理", "更新定价", "pricing_update", "model_pricing"},
	"POST /api/v1/admin/models/batch-update-selling":  {"定价管理", "批量定价", "pricing_batch_update", "model_pricing"},
	"POST /api/v1/admin/price-scrape/apply":           {"定价管理", "应用爬虫价格", "pricing_scrape_apply", "model_pricing"},

	// ==================== 渠道管理 ====================
	"POST /api/v1/admin/channels":                  {"渠道管理", "新建渠道", "channel_create", "channel"},
	"PUT /api/v1/admin/channels/:id":               {"渠道管理", "更新渠道", "channel_update", "channel"},
	"DELETE /api/v1/admin/channels/:id":            {"渠道管理", "删除渠道", "channel_delete", "channel"},
	"POST /api/v1/admin/channels/:id/test":         {"渠道管理", "测试渠道", "channel_test", "channel"},
	"POST /api/v1/admin/channel-groups":            {"渠道管理", "新建渠道组", "channel_group_create", "channel_group"},
	"PUT /api/v1/admin/channel-groups/:id":         {"渠道管理", "更新渠道组", "channel_group_update", "channel_group"},
	"DELETE /api/v1/admin/channel-groups/:id":      {"渠道管理", "删除渠道组", "channel_group_delete", "channel_group"},
	"POST /api/v1/admin/cache/clear-channel-routes": {"渠道管理", "清理路由缓存", "channel_cache_clear", "channel"},
	"POST /api/v1/admin/cache/clear":                {"渠道管理", "清理全部缓存", "cache_clear_all", "cache"},

	// ==================== 供应商管理 ====================
	"POST /api/v1/admin/suppliers":      {"供应商管理", "新建供应商", "supplier_create", "supplier"},
	"PUT /api/v1/admin/suppliers/:id":   {"供应商管理", "更新供应商", "supplier_update", "supplier"},
	"DELETE /api/v1/admin/suppliers/:id": {"供应商管理", "删除供应商", "supplier_delete", "supplier"},

	// ==================== 订单管理 ====================
	"POST /api/v1/admin/payment/orders/:id/refund":    {"订单管理", "手动退款", "order_refund", "order"},
	"POST /api/v1/admin/payment/mock-callback":        {"订单管理", "Mock 回调", "order_mock_callback", "order"},

	// ==================== 退款管理 ====================
	"POST /api/v1/admin/payment/refunds/:id/approve":  {"退款管理", "批准退款", "refund_approve", "refund"},
	"POST /api/v1/admin/payment/refunds/:id/reject":   {"退款管理", "驳回退款", "refund_reject", "refund"},
	"POST /api/v1/admin/payment/refunds/batch-approve": {"退款管理", "批量通过", "refund_batch_approve", "refund"},
	"POST /api/v1/admin/payment/refunds/batch-reject":  {"退款管理", "批量驳回", "refund_batch_reject", "refund"},

	// ==================== 提现审核 ====================
	"POST /api/v1/admin/withdrawals/:id/approve":    {"提现审核", "批准提现", "withdrawal_approve", "withdrawal"},
	"POST /api/v1/admin/withdrawals/:id/reject":     {"提现审核", "驳回提现", "withdrawal_reject", "withdrawal"},
	"POST /api/v1/admin/withdrawals/:id/mark-paid":  {"提现审核", "标记已付", "withdrawal_mark_paid", "withdrawal"},

	// ==================== 支付配置 ====================
	"PUT /api/v1/admin/payment-config/:gateway":     {"支付配置", "更新网关", "payment_config_update", "payment_config"},
	"POST /api/v1/admin/payment-config/:gateway/test": {"支付配置", "测试网关", "payment_config_test", "payment_config"},

	// ==================== 邀请返佣 ====================
	"PUT /api/v1/admin/referral-config":      {"邀请返佣", "更新返佣配置", "referral_config_update", "referral_config"},

	// ==================== 注册赠送 ====================
	"PUT /api/v1/admin/quota-config":         {"注册赠送", "更新赠送配置", "quota_config_update", "quota_config"},

	// ==================== 会员等级 ====================
	"POST /api/v1/admin/member-levels":       {"会员等级", "新建等级", "member_level_create", "member_level"},
	"PUT /api/v1/admin/member-levels/:id":    {"会员等级", "更新等级", "member_level_update", "member_level"},
	"DELETE /api/v1/admin/member-levels/:id": {"会员等级", "删除等级", "member_level_delete", "member_level"},

	// ==================== 合作申请 ====================
	"PATCH /api/v1/admin/partner-applications/:id/status":   {"合作申请", "状态流转", "partner_status_change", "partner_application"},
	"PATCH /api/v1/admin/partner-applications/:id/read":     {"合作申请", "标记已读", "partner_mark_read", "partner_application"},
	"PATCH /api/v1/admin/partner-applications/:id/unread":   {"合作申请", "标记未读", "partner_mark_unread", "partner_application"},
	"DELETE /api/v1/admin/partner-applications/:id":         {"合作申请", "删除申请", "partner_delete", "partner_application"},

	// ==================== 定时任务 ====================
	"PUT /api/v1/admin/cron-tasks/:name/toggle":       {"系统设置", "启停任务", "cron_toggle", "cron_task"},
	"PUT /api/v1/admin/cron-tasks/batch-toggle":       {"系统设置", "批量启停", "cron_batch_toggle", "cron_task"},

	// ==================== 能力测试 ====================
	"POST /api/v1/admin/capability-test/run":              {"能力测试", "执行测试", "capability_test_run", "capability_test"},
	"POST /api/v1/admin/capability-test/run-untested":     {"能力测试", "测试未覆盖模型", "capability_test_untested", "capability_test"},
	"POST /api/v1/admin/capability-test/tasks/:id/auto-apply": {"能力测试", "自动应用建议", "capability_test_apply", "capability_test"},

	// ==================== 用户敏感操作 ====================
	"POST /api/v1/auth/login":              {"账户安全", "登录", "user_login", "auth"},
	"POST /api/v1/auth/logout":             {"账户安全", "登出", "user_logout", "auth"},
	"POST /api/v1/auth/register":           {"账户安全", "注册", "user_register", "auth"},
	"POST /api/v1/user/password":           {"账户安全", "修改密码", "password_change", "user"},
	"POST /api/v1/user/api-keys":           {"API Keys", "创建密钥", "apikey_create", "apikey"},
	"DELETE /api/v1/user/api-keys/:id":     {"API Keys", "删除密钥", "apikey_delete", "apikey"},
	"PUT /api/v1/user/api-keys/:id":        {"API Keys", "更新密钥", "apikey_update", "apikey"},

	// ==================== 运营报表 CSV 导出 ====================
	"POST /api/v1/admin/consumption/daily/export":    {"积分消耗查询", "导出积分消耗CSV", "consumption_export", "consumption_report"},
	"POST /api/v1/admin/registration-gifts/export":   {"注册赠送明细", "导出注册赠送CSV", "registration_gift_export", "registration_gift_report"},
	"POST /api/v1/admin/referral-commissions/export": {"邀请返佣明细", "导出邀请返佣CSV", "referral_commission_export", "referral_commission_report"},
}

// readRouteMap 路由 → 元数据映射表（GET 读操作）
// 独立于 routeMap 维护，用于 RBAC 权限系统的读权限定义。
// 审计中间件不消费此表（GET 不写审计日志），PermissionGate 消费合并后的两张表。
//
// 仅纳入 /admin/* 下的 GET 端点（/user/* 按 JWT + ScopedDB 直接授权，不走 PermissionGate）。
// 新增 admin GET 端点时必须同步追加，否则会被 PermissionGate 直接 403。
var readRouteMap = map[string]RouteMeta{
	// ==================== 用户管理 ====================
	"GET /api/v1/admin/users":                                   {"用户管理", "用户列表", "user_list_read", "user"},
	"GET /api/v1/admin/users/:id":                               {"用户管理", "用户详情", "user_get_read", "user"},
	"GET /api/v1/admin/tenants":                                 {"用户管理", "租户列表", "tenant_list_read", "tenant"},
	"GET /api/v1/admin/commission-overrides":                    {"用户管理", "佣金覆盖列表", "commission_override_list_read", "commission_override"},
	"GET /api/v1/admin/commission-overrides/user/:user_id":      {"用户管理", "用户佣金覆盖", "commission_override_by_user_read", "commission_override"},

	// ==================== 余额管理 ====================
	"GET /api/v1/admin/users/:id/balance":         {"余额管理", "用户余额", "user_balance_get_read", "balance"},
	"GET /api/v1/admin/balance/reconciliation":    {"余额管理", "余额对账", "balance_reconciliation_read", "balance"},

	// ==================== 模型管理 ====================
	"GET /api/v1/admin/ai-models":                     {"模型管理", "模型列表", "model_list_read", "ai_model"},
	"GET /api/v1/admin/ai-models/:id":                 {"模型管理", "模型详情", "model_get_read", "ai_model"},
	"GET /api/v1/admin/ai-models/stats":               {"模型管理", "模型统计", "model_stats_read", "ai_model"},
	"GET /api/v1/admin/ai-models/:id/labels":          {"模型管理", "模型标签", "model_label_list_read", "model_label"},
	"GET /api/v1/admin/models/label-keys":             {"模型管理", "标签键列表", "model_label_key_list_read", "model_label"},
	"GET /api/v1/admin/model-categories":              {"模型管理", "分类列表", "model_category_list_read", "model_category"},
	"GET /api/v1/admin/model-categories/:id":          {"模型管理", "分类详情", "model_category_get_read", "model_category"},
	"GET /api/v1/admin/models/check-history":          {"模型管理", "检测历史", "model_check_history_read", "model_check"},
	"GET /api/v1/admin/models/check-latest":           {"模型管理", "检测汇总", "model_check_latest_read", "model_check"},
	"GET /api/v1/admin/models/check-tasks":            {"模型管理", "检测任务列表", "model_check_task_list_read", "model_check_task"},
	"GET /api/v1/admin/models/check-tasks/:id":        {"模型管理", "检测任务详情", "model_check_task_get_read", "model_check_task"},
	"GET /api/v1/admin/models/scanned-offline":        {"模型管理", "扫描下线汇总", "model_deprecation_scan_read", "ai_model"},
	"GET /api/v1/admin/trending-models":               {"模型管理", "热门模型库", "trending_model_list_read", "trending_model"},

	// ==================== 定价管理 ====================
	"GET /api/v1/admin/model-pricings":           {"定价管理", "定价列表", "pricing_list_read", "model_pricing"},
	"GET /api/v1/admin/agent-pricings":           {"定价管理", "代理定价列表", "agent_pricing_list_read", "agent_pricing"},
	"GET /api/v1/admin/models/price-sync-logs":   {"定价管理", "价格同步日志", "pricing_sync_log_read", "pricing_sync_log"},
	"GET /api/v1/admin/price-matrix":             {"定价管理", "价格矩阵", "price_matrix_read", "price_matrix"},

	// ==================== 渠道管理 ====================
	"GET /api/v1/admin/channels":                                 {"渠道管理", "渠道列表", "channel_list_read", "channel"},
	"GET /api/v1/admin/channels/:id":                             {"渠道管理", "渠道详情", "channel_get_read", "channel"},
	"GET /api/v1/admin/channel-groups":                           {"渠道管理", "渠道组列表", "channel_group_list_read", "channel_group"},
	"GET /api/v1/admin/channel-groups/:id":                       {"渠道管理", "渠道组详情", "channel_group_get_read", "channel_group"},
	"GET /api/v1/admin/channel-groups/:id/channels":              {"渠道管理", "组内渠道列表", "channel_group_channels_read", "channel"},
	"GET /api/v1/admin/channel-stats":                            {"渠道管理", "渠道统计", "channel_stats_read", "channel_stats"},
	"GET /api/v1/admin/channel-stats/models":                     {"渠道管理", "渠道模型统计", "channel_model_stats_read", "channel_stats"},
	"GET /api/v1/admin/channel-models":                           {"渠道管理", "渠道模型映射", "channel_model_list_read", "channel_model"},
	"GET /api/v1/admin/channel-tags":                             {"渠道管理", "渠道标签列表", "channel_tag_list_read", "channel_tag"},
	"GET /api/v1/admin/channel-tags/:id/stats":                   {"渠道管理", "标签统计", "channel_tag_stats_read", "channel_tag"},
	"GET /api/v1/admin/custom-channels":                          {"渠道管理", "自定义渠道列表", "custom_channel_list_read", "custom_channel"},
	"GET /api/v1/admin/custom-channels/default/refresh/status":   {"渠道管理", "默认渠道刷新状态", "custom_channel_refresh_status_read", "custom_channel"},
	"GET /api/v1/admin/backup-rules":                             {"渠道管理", "备份规则列表", "backup_rule_list_read", "backup_rule"},
	"GET /api/v1/admin/backup-rules/:id":                         {"渠道管理", "备份规则详情", "backup_rule_get_read", "backup_rule"},
	"GET /api/v1/admin/backup-rules/:id/status":                  {"渠道管理", "备份状态", "backup_rule_status_read", "backup_rule"},
	"GET /api/v1/admin/backup-rules/:id/events":                  {"渠道管理", "备份事件列表", "backup_event_list_read", "backup_event"},

	// ==================== 供应商管理 ====================
	"GET /api/v1/admin/suppliers":     {"供应商管理", "供应商列表", "supplier_list_read", "supplier"},
	"GET /api/v1/admin/suppliers/:id": {"供应商管理", "供应商详情", "supplier_get_read", "supplier"},

	// ==================== 订单管理 ====================
	"GET /api/v1/admin/payment/orders":        {"订单管理", "订单列表", "order_list_read", "order"},
	"GET /api/v1/admin/payment/orders/:id":    {"订单管理", "订单详情", "order_get_read", "order"},
	"GET /api/v1/admin/payment/orders/stats":  {"订单管理", "订单统计", "order_stats_read", "order"},

	// ==================== 退款管理 ====================
	"GET /api/v1/admin/payment/refunds":     {"退款管理", "退款列表", "refund_list_read", "refund"},
	"GET /api/v1/admin/payment/refunds/:id": {"退款管理", "退款详情", "refund_get_read", "refund"},

	// ==================== 提现审核 ====================
	"GET /api/v1/admin/withdrawals":       {"提现审核", "提现列表", "withdrawal_list_read", "withdrawal"},
	"GET /api/v1/admin/withdrawals/:id":   {"提现审核", "提现详情", "withdrawal_get_read", "withdrawal"},
	"GET /api/v1/admin/withdrawals/stats": {"提现审核", "提现统计", "withdrawal_stats_read", "withdrawal"},

	// ==================== 支付配置 ====================
	"GET /api/v1/admin/payment/accounts":                       {"支付配置", "账户列表", "payment_account_list_read", "payment_account"},
	"GET /api/v1/admin/payment/event-logs":                     {"支付配置", "事件日志列表", "payment_event_log_list_read", "payment_event_log"},
	"GET /api/v1/admin/payment/event-logs/by-payment/:id":      {"支付配置", "订单事件日志", "payment_event_log_by_payment_read", "payment_event_log"},
	"GET /api/v1/admin/payment/event-logs/by-refund/:id":       {"支付配置", "退款事件日志", "payment_event_log_by_refund_read", "payment_event_log"},
	"GET /api/v1/admin/payment/event-logs/by-withdraw/:id":     {"支付配置", "提现事件日志", "payment_event_log_by_withdraw_read", "payment_event_log"},
	"GET /api/v1/admin/payment/exchange-rate/history":          {"支付配置", "汇率历史", "exchange_rate_history_read", "exchange_rate"},
	"GET /api/v1/admin/payment/exchange-rate/config":           {"支付配置", "汇率配置", "exchange_rate_config_get_read", "exchange_rate"},
	"GET /api/v1/admin/users/:id/payment-profile":              {"支付配置", "用户支付档案", "user_payment_profile_read", "user_payment"},
	"GET /api/v1/admin/users/:id/credit-stats":                 {"支付配置", "用户积分统计", "user_credit_stats_read", "user_credit"},
	"GET /api/v1/admin/exchange-rates":                         {"支付配置", "汇率列表", "exchange_rate_list_read", "exchange_rate"},

	// ==================== 邀请返佣 ====================
	"GET /api/v1/admin/referral-config":                    {"邀请返佣", "返佣配置", "referral_config_get_read", "referral_config"},
	"GET /api/v1/admin/referral-commissions":               {"邀请返佣", "返佣明细列表", "referral_commission_list_read", "referral_commission"},
	"GET /api/v1/admin/referral-commissions/daily":         {"邀请返佣", "日维度返佣", "referral_commission_daily_read", "referral_commission"},
	"GET /api/v1/admin/referral-commissions/stats":         {"邀请返佣", "返佣统计", "referral_commission_stats_read", "referral_commission"},
	"GET /api/v1/admin/commissions":                        {"邀请返佣", "返佣条目列表", "commission_list_read", "commission"},

	// ==================== 注册赠送 ====================
	"GET /api/v1/admin/quota-config":              {"注册赠送", "赠送配置", "quota_config_get_read", "quota_config"},
	"GET /api/v1/admin/registration-gifts":        {"注册赠送", "赠送明细列表", "registration_gift_list_read", "registration_gift"},
	"GET /api/v1/admin/registration-gifts/stats":  {"注册赠送", "赠送统计", "registration_gift_stats_read", "registration_gift"},

	// ==================== 会员等级 ====================
	"GET /api/v1/admin/member-levels":    {"会员等级", "等级列表", "member_level_list_read", "member_level"},
	"GET /api/v1/admin/level-discounts":  {"会员等级", "等级折扣列表", "member_level_discount_read", "member_level_discount"},

	// ==================== 合作申请 ====================
	"GET /api/v1/admin/partner-applications":       {"合作申请", "申请列表", "partner_application_list_read", "partner_application"},
	"GET /api/v1/admin/partner-applications/:id":   {"合作申请", "申请详情", "partner_application_get_read", "partner_application"},
	"GET /api/v1/admin/partner-applications/stats": {"合作申请", "申请统计", "partner_application_stats_read", "partner_application"},

	// ==================== 系统设置 ====================
	"GET /api/v1/admin/cron-tasks":                          {"系统设置", "定时任务列表", "cron_task_list_read", "cron_task"},
	"GET /api/v1/admin/audit-logs":                          {"系统设置", "审计日志列表", "audit_log_list_read", "audit_log"},
	"GET /api/v1/admin/audit-logs/menus":                    {"系统设置", "审计菜单列表", "audit_menu_list_read", "audit_menu"},
	"GET /api/v1/admin/config-audit":                        {"系统设置", "配置审计列表", "config_audit_list_read", "config_audit"},
	"GET /api/v1/admin/cache/stats":                         {"系统设置", "缓存统计", "cache_stats_read", "cache"},
	"GET /api/v1/admin/param-mappings":                      {"系统设置", "参数映射列表", "param_mapping_list_read", "param_mapping"},
	"GET /api/v1/admin/param-mappings/:id":                  {"系统设置", "参数映射详情", "param_mapping_get_read", "param_mapping"},
	"GET /api/v1/admin/param-mappings/supplier/:code":       {"系统设置", "供应商参数映射", "param_mapping_by_supplier_read", "param_mapping"},
	"GET /api/v1/admin/param-support":                       {"系统设置", "参数支持列表", "param_support_read", "param_support"},
	"GET /api/v1/admin/guard-config":                        {"系统设置", "守卫配置", "guard_config_read", "guard_config"},
	"GET /api/v1/admin/disposable-emails":                   {"系统设置", "一次性邮箱列表", "disposable_email_list_read", "disposable_email"},
	"GET /api/v1/admin/orchestrations":                      {"系统设置", "编排流程列表", "orchestration_list_read", "orchestration"},
	"GET /api/v1/admin/orchestrations/:id":                  {"系统设置", "编排流程详情", "orchestration_get_read", "orchestration"},
	"GET /api/v1/admin/tasks":                               {"系统设置", "后台任务列表", "task_list_read", "task"},
	"GET /api/v1/admin/tasks/:id":                           {"系统设置", "后台任务详情", "task_get_read", "task"},

	// ==================== 能力测试 ====================
	"GET /api/v1/admin/capability-test/cases":                    {"能力测试", "测试用例列表", "capability_test_case_list_read", "capability_test_case"},
	"GET /api/v1/admin/capability-test/untested-count":           {"能力测试", "未测试模型计数", "capability_test_untested_count_read", "capability_test"},
	"GET /api/v1/admin/capability-test/hot-sample-info":          {"能力测试", "热点样本信息", "capability_test_hot_sample_info_read", "capability_test"},
	"GET /api/v1/admin/capability-test/tasks":                    {"能力测试", "测试任务列表", "capability_test_task_list_read", "capability_test_task"},
	"GET /api/v1/admin/capability-test/tasks/:id":                {"能力测试", "测试任务详情", "capability_test_task_get_read", "capability_test_task"},
	"GET /api/v1/admin/capability-test/tasks/:id/results":        {"能力测试", "任务测试结果", "capability_test_result_read", "capability_test_result"},
	"GET /api/v1/admin/capability-test/tasks/:id/suggestions":    {"能力测试", "任务建议列表", "capability_test_suggestion_read", "capability_test_suggestion"},
	"GET /api/v1/admin/capability-test/tasks/:id/regressions":    {"能力测试", "任务退化列表", "capability_test_regression_read", "capability_test_regression"},
	"GET /api/v1/admin/capability-test/baselines":                {"能力测试", "基线列表", "capability_test_baseline_read", "capability_test_baseline"},

	// ==================== API Keys ====================
	"GET /api/v1/admin/api-keys": {"API Keys", "系统密钥列表", "admin_apikey_list_read", "apikey"},

	// ==================== 积分消耗查询 ====================
	"GET /api/v1/admin/consumption/daily":            {"积分消耗查询", "日消耗明细", "consumption_daily_read", "consumption"},
	"GET /api/v1/admin/consumption/model-breakdown":  {"积分消耗查询", "模型分解", "consumption_model_breakdown_read", "consumption"},

	// ==================== 运营报表 ====================
	"GET /api/v1/admin/reports/overview":                      {"运营报表", "报表概览", "report_overview_read", "report"},
	"GET /api/v1/admin/reports/usage":                         {"运营报表", "用量报告", "report_usage_read", "report"},
	"GET /api/v1/admin/reports/revenue":                       {"运营报表", "收入报告", "report_revenue_read", "report"},
	"GET /api/v1/admin/reports/profit":                        {"运营报表", "利润报告", "report_profit_read", "report"},
	"GET /api/v1/admin/reports/profit/trend":                  {"运营报表", "利润趋势", "report_profit_trend_read", "report"},
	"GET /api/v1/admin/reports/profit/top-agents":             {"运营报表", "顶级代理", "report_top_agents_read", "report"},
	"GET /api/v1/admin/reports/profit/drilldown/:tenantId":    {"运营报表", "利润下钻", "report_profit_drilldown_read", "report"},
	"GET /api/v1/admin/stats/registrations":                   {"运营报表", "注册统计", "stats_registrations_read", "stats"},
	"GET /api/v1/admin/stats/referrals":                       {"运营报表", "推荐统计", "stats_referrals_read", "stats"},
	"GET /api/v1/admin/stats/pnl":                             {"运营报表", "损益表", "stats_pnl_read", "stats"},
	"GET /api/v1/admin/stats/payment-analysis":                {"运营报表", "支付分析", "stats_payment_analysis_read", "stats"},

	// ==================== 消息公告 ====================
	"GET /api/v1/admin/announcements":          {"消息公告", "公告列表", "announcement_list_read", "announcement"},
	"GET /api/v1/admin/announcements/stats":    {"消息公告", "公告统计", "announcement_stats_read", "announcement"},

	// ==================== 文档管理 ====================
	"GET /api/v1/admin/docs":             {"文档管理", "文档列表", "doc_list_read", "doc"},
	"GET /api/v1/admin/docs/:id":         {"文档管理", "文档详情", "doc_get_read", "doc"},
	"GET /api/v1/admin/doc-categories":   {"文档管理", "文档分类列表", "doc_category_list_read", "doc_category"},

	// ==================== AI 客服 ====================
	"GET /api/v1/admin/support/status":              {"AI 客服", "客服状态", "support_status_read", "support"},
	"GET /api/v1/admin/support/hot-questions":       {"AI 客服", "热门问题列表", "support_hot_question_read", "support_hot_question"},
	"GET /api/v1/admin/support/provider-docs":       {"AI 客服", "供应商文档列表", "support_provider_doc_read", "support_provider_doc"},
	"GET /api/v1/admin/support/tickets":             {"AI 客服", "工单列表", "support_ticket_list_read", "support_ticket"},
	"GET /api/v1/admin/support/tickets/:id":         {"AI 客服", "工单详情", "support_ticket_get_read", "support_ticket"},
	"GET /api/v1/admin/support/accepted-answers":    {"AI 客服", "采纳答案列表", "support_accepted_answer_read", "support_accepted_answer"},
	"GET /api/v1/admin/support/knowledge/stats":     {"AI 客服", "知识库统计", "support_knowledge_stats_read", "support_knowledge"},

	// ==================== 权限管理（Phase 6 落地，此处仅预声明） ====================
	"GET /api/v1/admin/permissions":           {"权限管理", "权限目录", "perm_list_read", "permission"},
	"GET /api/v1/admin/roles":                 {"权限管理", "角色列表", "role_list_read", "role"},
	"GET /api/v1/admin/roles/:id":             {"权限管理", "角色详情", "role_get_read", "role"},
	"GET /api/v1/admin/users/:id/roles":       {"权限管理", "用户角色", "user_role_list_read", "user_role"},
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

// IsAuditRelevant 返回给定路由是否应记录审计日志
// 语义：写操作表命中 → true；读操作表命中或未命中 → false
func IsAuditRelevant(method, fullPath string) bool {
	_, ok := routeMap[method+" "+fullPath]
	return ok
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
