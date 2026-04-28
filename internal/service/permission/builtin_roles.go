// Package permission 权限解析与种子化
//
// 本文件定义了 6 个内置角色模板，seed.go 根据此表写入 roles 与 role_permissions。
// 设计原则：通过「菜单」粒度声明权限归属，seed 阶段展开为具体权限码；
// 这样当 audit.routeMap 新增一个条目时，所属菜单的所有角色会自动获得对应权限，
// 无需逐个更新本文件。
package permission

// BuiltinRole 内置角色定义
type BuiltinRole struct {
	Code        string // 角色唯一代码
	Name        string // 展示名
	Description string // 描述
	DataScope   string // 数据范围类型：all / own_tenant / own_only
	// 权限选取方式（三选一）：
	AllPermissions bool     // 勾选全部（仅 SUPER_ADMIN）
	AllReadOnly    bool     // 所有 *_read 权限（仅 AUDITOR）
	Menus          []string // 指定菜单名（seed 时展开为菜单下所有权限码）
	ExtraCodes     []string // 额外单独权限码（不在菜单中的个别权限）
}

// BuiltinRoles 内置角色列表，按优先级排序（SUPER_ADMIN 最高）
var BuiltinRoles = []BuiltinRole{
	{
		Code:           "SUPER_ADMIN",
		Name:           "超级管理员",
		Description:    "拥有平台全部权限，是唯一能管理权限系统本身（角色/授权）的角色",
		DataScope:      "all",
		AllPermissions: true,
	},
	{
		Code:        "FINANCE_MANAGER",
		Name:        "财务管理员",
		Description: "负责订单、退款、提现、支付配置、余额调整与财务报表",
		DataScope:   "all",
		Menus: []string{
			"订单管理",
			"退款管理",
			"提现审核",
			"支付配置",
			"余额管理",
			"积分消耗查询",
			"发票管理",
		},
		ExtraCodes: []string{
			// v4.0 用户特殊折扣（财务审核/分配）
			"user_discount_list_read",
			"user_discount_get_read",
			"user_discount_batch_create",
			"user_discount_preview",
			"user_discount_update",
			"user_discount_delete",
			// 计费准确性整改：月度账单对账
			"billing_reconcile_create",
			// Phase 0 三方一致性核对（财务对账时定位偏差原因）
			"cost_three_way_check_read",
			"cost_consistency_scan_read",
		},
	},
	{
		Code:        "OPERATION_MANAGER",
		Name:        "运营管理员",
		Description: "负责用户运营、合作审批、邀请返佣、注册赠送与会员等级配置",
		DataScope:   "all",
		Menus: []string{
			"用户管理",
			"合作申请",
			"邀请返佣",
			"注册赠送",
			"会员等级",
			"注册赠送明细",
			"邀请返佣明细",
			"账户安全",
			"邮件管理",
		},
		ExtraCodes: []string{
			"rate_limit_active_read",
			"rate_limit_event_read",
			"auth_log_list_read",
			"auth_log_stats_read",
		},
	},
	{
		Code:        "AI_RESOURCE_MANAGER",
		Name:        "AI 资源管理员",
		Description: "负责供应商、渠道、模型、定价、价格分析与能力测试",
		DataScope:   "all",
		Menus: []string{
			"供应商管理",
			"渠道管理",
			"模型管理",
			"定价管理",
			"价格分析", // v3 引入,覆盖 BillingQuote 试算/PriceMatrix 编辑等
			"能力测试",
		},
		ExtraCodes: []string{
			// Phase 0 三方一致性核对（AI 资源管理员需排查"价格变更后偏差"）
			"cost_three_way_check_read",
			"cost_consistency_scan_read",
		},
	},
	{
		Code:        "AUDITOR",
		Name:        "只读审计员",
		Description: "仅拥有所有读权限与日志审计，无任何写权限",
		DataScope:   "all",
		AllReadOnly: true,
	},
	{
		Code:        "USER",
		Name:        "终端用户",
		Description: "普通用户，只能管理自身 API Key、余额、密码与个人资料",
		DataScope:   "own_only",
		ExtraCodes: []string{
			"apikey_create",
			"apikey_delete",
			"apikey_update",
			"password_change",
			"user_logout",
			// 发票：所有普通用户均可申请
			"user_invoice_create",
			"user_invoice_list_read",
			"user_invoice_get_read",
		},
	},
	{
		Code:        "FINANCIAL_USER",
		Name:        "财务用户",
		Description: "在普通用户基础上额外拥有申请提现和申请退款的权限，适用于需要开通提现/退款功能的用户",
		DataScope:   "own_only",
		ExtraCodes: []string{
			// 继承 USER 角色的全部基础权限
			"apikey_create",
			"apikey_delete",
			"apikey_update",
			"password_change",
			"user_logout",
			// 新增财务申请权限
			"user_withdrawal_create",
			"user_refund_request_create",
			// 发票申请
			"user_invoice_create",
			"user_invoice_list_read",
			"user_invoice_get_read",
		},
	},
}

// LegacyRoleMapping 旧 users.role 字符串 → 新 role code 的迁移映射
// 用于 seed 阶段根据现有 users.role 回填 user_roles 表。
// 未在映射中的 role 字符串（如 AGENT_L1/L2/L3 死代码残留）将回退到 USER 并记录警告日志。
var LegacyRoleMapping = map[string]string{
	"ADMIN":    "SUPER_ADMIN",
	"USER":     "USER",
	"AGENT_L1": "USER", // 代理商模块 v3.1 已移除，残留用户降级为普通用户
	"AGENT_L2": "USER",
	"AGENT_L3": "USER",
}

// IsBuiltinRoleCode 判断给定代码是否为内置角色（不可删除）
func IsBuiltinRoleCode(code string) bool {
	for _, r := range BuiltinRoles {
		if r.Code == code {
			return true
		}
	}
	return false
}
