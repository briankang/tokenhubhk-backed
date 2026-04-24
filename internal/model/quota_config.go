package model

// QuotaConfig 全局额度配置(v3.1 扩展:注册赠送 + 邀请双向奖励 + 解锁门槛)
// 所有金额采用积分(int64)存储,1 RMB = 10,000 credits
// 管理后台"注册赠送"Tab 对应此配置
type QuotaConfig struct {
	BaseModel
	// --- 基础注册赠送 ---
	DefaultFreeQuota  int64 `gorm:"type:bigint;default:3000" json:"defaultFreeQuota"`      // 注册基础赠送(积分,默认 ¥0.3)
	RegistrationBonus int64 `gorm:"type:bigint;default:0" json:"registrationBonus"`        // 注册额外奖励(积分,默认 0)

	// --- 邀请双向奖励(v3.1 新增) ---
	InviteeBonus          int64 `gorm:"type:bigint;default:5000" json:"inviteeBonus"`                // 被邀者附加赠送(积分,默认 ¥0.5)
	InviteeUnlockCredits  int64 `gorm:"type:bigint;default:10000" json:"inviteeUnlockCredits"`       // 被邀者累计消费此额度后解锁 InviteeBonus(积分,默认 ¥1)
	InviterBonus          int64 `gorm:"type:bigint;default:10000" json:"inviterBonus"`               // 邀请人奖励(积分,默认 ¥1)
	InviterUnlockPaidRMB  int64 `gorm:"type:bigint;default:100000" json:"inviterUnlockPaidRmb"`      // 被邀者首次付费达此额度后解锁 InviterBonus(积分,默认 ¥10)
	InviterMonthlyCap     int   `gorm:"default:10" json:"inviterMonthlyCap"`                         // 邀请人每月最多领奖人次(0=无限制,默认 10)

	// --- v5.1 反滥用配置 ---
	FreeQuotaExpiryDays   int   `gorm:"default:7" json:"freeQuotaExpiryDays"`                       // 免费额度过期天数(默认 7 天,0=永不过期)
	PaidThresholdCredits  int64 `gorm:"type:bigint;default:100000" json:"paidThresholdCredits"`      // 付费用户判定门槛(积分,默认 100000 = ¥10)

	IsActive    bool   `gorm:"default:true" json:"isActive"`
	Description string `gorm:"size:200" json:"description"`
}

// TableName 指定额度配置表名
func (QuotaConfig) TableName() string {
	return "quota_configs"
}
