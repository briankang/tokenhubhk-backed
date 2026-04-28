package model

import "time"

// User 平台用户模型
// v4.0: 移除 Role 字段，角色关系改由 UserRole 表承载
// 多租户 + 邀请关系保留
type User struct {
	BaseModel
	TenantID         uint       `gorm:"index;not null" json:"tenant_id"`                               // 所属租户 ID
	Email            string     `gorm:"type:varchar(255);uniqueIndex;not null" json:"email"`           // 用户邮箱（唯一）
	Username         *string    `gorm:"type:varchar(64);uniqueIndex" json:"username,omitempty"`        // 账号名（手机号账号必填，邮箱历史账号可为空）
	PhoneCountryCode string     `gorm:"type:varchar(2);index" json:"phone_country_code,omitempty"`     // 手机号国家/地区，第一期仅 CN
	PhoneE164        *string    `gorm:"type:varchar(20);uniqueIndex" json:"phone_e164,omitempty"`      // E.164 手机号（+86138...）
	PhoneVerifiedAt  *time.Time `json:"phone_verified_at,omitempty"`                                   // 手机号验证时间
	RegisterSource   string     `gorm:"type:varchar(20);default:'email';index" json:"register_source"` // email/phone/google/github
	PasswordHash     string     `gorm:"type:varchar(255);not null" json:"-"`                           // bcrypt 密码哈希（不输出）
	Name             string     `gorm:"type:varchar(100);not null" json:"name"`                        // 用户名称
	IsActive         bool       `gorm:"default:true" json:"is_active"`                                 // 是否启用
	Language         string     `gorm:"type:varchar(10);default:'en'" json:"language"`                 // 偏好语言
	// v5.0: 注册地区（ISO 3166-1 alpha-2，注册后不可修改）
	// 中国大陆="CN"，香港="HK"，台湾="TW"，澳门="MO"；空值视为 "CN"
	CountryCode  string     `gorm:"type:varchar(2);not null;default:'CN';index" json:"country_code"` // 注册国家/地区
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`                                         // 最后登录时间
	ReferredBy   *uint      `gorm:"index" json:"referred_by,omitempty"`                              // 邀请人 UserID
	ReferralCode string     `gorm:"size:20;index" json:"referral_code,omitempty"`                    // 用户自己的邀请码

	Tenant Tenant `gorm:"foreignKey:TenantID" json:"tenant,omitempty"` // 所属租户关联
}

// TableName 指定用户表名
func (User) TableName() string {
	return "users"
}
