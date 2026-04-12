package model

// ReferralLink 用户邀请链接模型，存储唯一邀请码及统计
type ReferralLink struct {
	BaseModel
	UserID        uint   `gorm:"index;not null" json:"userId"`
	TenantID      uint   `gorm:"index" json:"tenantId"`
	Code          string `gorm:"size:20;uniqueIndex;not null" json:"code"` // 唯一邀请码
	ClickCount    int    `gorm:"default:0" json:"clickCount"`
	RegisterCount int    `gorm:"default:0" json:"registerCount"`

	User User `gorm:"foreignKey:UserID" json:"-"`
}

// TableName 指定邀请链接表名
func (ReferralLink) TableName() string {
	return "referral_links"
}
