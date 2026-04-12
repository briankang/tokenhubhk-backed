package model

import "time"

// User 平台用户模型
// 支持多租户、多角色 (ADMIN/AGENT_L1/AGENT_L2/AGENT_L3/USER)、邀请关系
type User struct {
	BaseModel
	TenantID     uint       `gorm:"index;not null" json:"tenant_id"`                   // 所属租户 ID
	Email        string     `gorm:"type:varchar(255);uniqueIndex;not null" json:"email"` // 用户邮箱（唯一）
	PasswordHash string     `gorm:"type:varchar(255);not null" json:"-"`                // bcrypt 密码哈希（不输出）
	Name         string     `gorm:"type:varchar(100);not null" json:"name"`              // 用户名称
	Role         string     `gorm:"type:varchar(20);not null;default:'USER'" json:"role"` // 角色: ADMIN / AGENT_L1 / AGENT_L2 / AGENT_L3 / USER
	IsActive     bool       `gorm:"default:true" json:"is_active"`                       // 是否启用
	Language     string     `gorm:"type:varchar(10);default:'en'" json:"language"`       // 偏好语言
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`                             // 最后登录时间
	ReferredBy   *uint      `gorm:"index" json:"referred_by,omitempty"`                  // 邀请人 UserID
	ReferralCode string     `gorm:"size:20;index" json:"referral_code,omitempty"`        // 用户自己的邀请码

	Tenant Tenant `gorm:"foreignKey:TenantID" json:"tenant,omitempty"` // 所属租户关联
}

// TableName 指定用户表名
func (User) TableName() string {
	return "users"
}
