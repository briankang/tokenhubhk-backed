package model

import "time"

// ReferralAttribution 邀请归因快照
// 记录邀请关系的生命周期:被邀者 → 邀请人,归因窗口期内的消费才计入佣金
// 采用独立表存储而非仅依赖 User.ReferredBy,以支持归因过期、解锁门槛、关系失效等业务语义
type ReferralAttribution struct {
	BaseModel
	UserID        uint       `gorm:"uniqueIndex;not null" json:"user_id"`          // 被邀请用户ID(一对一)
	InviterID     uint       `gorm:"index;not null" json:"inviter_id"`             // 邀请人用户ID
	ReferralCode  string     `gorm:"size:32;index" json:"referral_code"`           // 当时使用的邀请码
	AttributedAt  time.Time  `gorm:"not null" json:"attributed_at"`                // 归因建立时间(一般 = 注册时间)
	ExpiresAt     time.Time  `gorm:"index;not null" json:"expires_at"`             // 归因到期时间(建立时 + AttributionDays)
	UnlockedAt    *time.Time `json:"unlocked_at"`                                  // 解锁时间(被邀者累计消费达阈值后写入,未解锁的不计佣金)
	IsValid       bool       `gorm:"index;default:true" json:"is_valid"`           // 是否有效(过期或人工作废后置 false)
	InvalidReason string     `gorm:"size:100" json:"invalid_reason"`               // 失效原因(EXPIRED / MANUAL / FRAUD)

	// v3.1 双向奖励幂等标记(防止重复发放)
	InviteeBonusGranted bool       `gorm:"default:false" json:"inviteeBonusGranted"` // 被邀者已领取一次性奖励(消费达 InviteeUnlockCredits 后发放)
	InviteeBonusAt      *time.Time `json:"inviteeBonusAt"`                           // 被邀者奖励发放时间
	InviterBonusGranted bool       `gorm:"default:false" json:"inviterBonusGranted"` // 邀请人已领取一次性奖励(被邀者首次付费达 InviterUnlockPaidRMB 后发放)
	InviterBonusAt      *time.Time `json:"inviterBonusAt"`                           // 邀请人奖励发放时间
}

// TableName 指定表名
func (ReferralAttribution) TableName() string {
	return "referral_attributions"
}
