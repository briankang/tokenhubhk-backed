package model

// ReferralConfig 全局邀请返现与佣金比例配置
// MinWithdrawAmount 使用积分(int64)存储，1 RMB = 10,000 credits
type ReferralConfig struct {
	BaseModel
	PersonalCashbackRate float64 `gorm:"type:decimal(5,4);default:0.05" json:"personalCashbackRate"` // 个人邀请返现比例 5%
	L1CommissionRate     float64 `gorm:"type:decimal(5,4);default:0.10" json:"l1CommissionRate"`     // 一级代理佣金 10%
	L2CommissionRate     float64 `gorm:"type:decimal(5,4);default:0.05" json:"l2CommissionRate"`     // 二级代理佣金 5%
	L3CommissionRate     float64 `gorm:"type:decimal(5,4);default:0.02" json:"l3CommissionRate"`     // 三级代理佣金 2%
	MinWithdrawAmount    int64   `gorm:"type:bigint;default:100000" json:"minWithdrawAmount"`        // 最低提现金额（积分 credits），默认10元=100000积分
	IsActive             bool    `gorm:"default:true" json:"isActive"`
}

// TableName 指定邀请配置表名
func (ReferralConfig) TableName() string {
	return "referral_configs"
}
