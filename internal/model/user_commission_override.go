package model

import "time"

// UserCommissionOverride 用户佣金率覆盖(特殊加佣配置)
// 管理员针对 KOL/合作方/内部员工等特殊身份设置专属佣金率(高于基础 10%)
// 支持生效期(effective_from ~ effective_to),同一用户同一时刻仅 1 条活跃记录
//
// IsActive 使用 *bool 指针:true=活跃、nil=历史记录(失效)
// MySQL 唯一索引中 NULL 不参与唯一性比较,因此同一 user 可以有多条历史失效记录
// 但同一时刻仅 1 条 is_active=true 记录(保证唯一性)
type UserCommissionOverride struct {
	BaseModel
	UserID         uint       `gorm:"uniqueIndex:idx_user_active;not null" json:"user_id"`                   // 被加佣用户(作为邀请人)
	IsActive       *bool      `gorm:"uniqueIndex:idx_user_active" json:"is_active"`                          // 是否活跃:true=活跃、NULL=已失效
	CommissionRate       float64    `gorm:"type:decimal(5,4);not null" json:"commission_rate"`                     // 专属比例,如 0.20 / 0.30,硬上限 0.80
	AttributionDays      *int       `gorm:"type:int" json:"attribution_days"`                                       // 归因周期(天) NULL=继承全局 ReferralConfig.AttributionDays; 范围 7~3650
	LifetimeCapCredits   *int64     `gorm:"type:bigint" json:"lifetime_cap_credits"`                                // 终身上限(积分) NULL=继承全局; 0=无上限; >0=自定义上限
	MinPaidCreditsUnlock *int64     `gorm:"type:bigint" json:"min_paid_credits_unlock"`                             // 解锁门槛(积分) NULL=继承全局; 0=立即解锁; >0=自定义门槛
	EffectiveFrom  time.Time  `gorm:"not null" json:"effective_from"`                                        // 生效起始日
	EffectiveTo    *time.Time `json:"effective_to"`                                                           // 生效结束日(null = 永久有效)
	Note           string     `gorm:"size:500" json:"note"`                                                   // 备注:KOL合作/内部员工/战略伙伴
	CreatedBy      uint       `gorm:"index" json:"created_by"`                                                // 配置该 override 的管理员ID
}

// TableName 指定表名
func (UserCommissionOverride) TableName() string {
	return "user_commission_overrides"
}
