package model

// ReferralConfig 全局邀请返佣配置(v3.1 简化:单层固定比例 + 归因窗口 + 终身上限)
// 代理机制已移除,此表仅支撑普通用户邀请返佣的全局默认配置
// MinWithdrawAmount / LifetimeCapCredits / MinPaidCreditsUnlock 使用积分(int64)存储,1 RMB = 10,000 credits
type ReferralConfig struct {
	BaseModel
	// v3.1 核心字段
	CommissionRate       float64 `gorm:"type:decimal(5,4);default:0.10" json:"commissionRate"`           // 单层佣金比例(默认 10%,上限 80%)
	AttributionDays      int     `gorm:"default:90" json:"attributionDays"`                              // 归因窗口天数(默认 90 天,范围 7~3650)
	LifetimeCapCredits   int64   `gorm:"type:bigint;default:30000000" json:"lifetimeCapCredits"`         // 单邀请关系终身佣金上限(积分,默认 ¥3000,0=无上限)
	MinPaidCreditsUnlock int64   `gorm:"type:bigint;default:100000" json:"minPaidCreditsUnlock"`         // 被邀者累计消费满此额度才解锁佣金(积分,默认 ¥10)
	MinWithdrawAmount    int64   `gorm:"type:bigint;default:1000000" json:"minWithdrawAmount"`           // 最低提现金额(积分,默认 ¥100)
	SettleDays           int     `gorm:"default:7" json:"settleDays"`                                    // PENDING→SETTLED 自动结算天数(默认 7 天)
	RequireInviteCode    bool    `gorm:"column:require_invite_code;default:false" json:"requireInviteCode"` // 注册时是否强制要求邀请码(默认关闭)

	// 兼容字段(v3.x 已弃用,保留避免 DB 迁移破坏性改动)
	PersonalCashbackRate float64 `gorm:"type:decimal(5,4);default:0.10" json:"personalCashbackRate"` // Deprecated: 使用 CommissionRate
	L1CommissionRate     float64 `gorm:"type:decimal(5,4);default:0" json:"l1CommissionRate"`        // Deprecated: 代理机制已移除
	L2CommissionRate     float64 `gorm:"type:decimal(5,4);default:0" json:"l2CommissionRate"`        // Deprecated
	L3CommissionRate     float64 `gorm:"type:decimal(5,4);default:0" json:"l3CommissionRate"`        // Deprecated

	IsActive bool `gorm:"default:true" json:"isActive"`
}

// TableName 指定邀请配置表名
func (ReferralConfig) TableName() string {
	return "referral_configs"
}
