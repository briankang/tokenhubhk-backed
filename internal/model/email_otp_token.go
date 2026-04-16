package model

import "time"

// EmailOTPToken 邮箱 OTP 验证码
// 两步注册流程中,用户提交邮箱后发送 OTP,验证通过后才允许完成注册
// OTP 使用 bcrypt 哈希存储,不持久化明文;过期后由定时任务清理
type EmailOTPToken struct {
	BaseModel
	Email      string    `gorm:"size:255;index;not null" json:"email"`     // 目标邮箱
	TokenHash  string    `gorm:"size:120;not null" json:"-"`               // bcrypt 哈希
	Purpose    string    `gorm:"size:30;default:'REGISTER'" json:"purpose"` // REGISTER / RESET_PASSWORD / CHANGE_EMAIL
	ExpiresAt  time.Time `gorm:"index;not null" json:"expires_at"`         // 过期时间
	UsedAt     *time.Time `json:"used_at"`                                 // 使用时间(null = 未用)
	Attempts   int       `gorm:"default:0" json:"attempts"`                // 已尝试次数
	MaxAttempts int      `gorm:"default:5" json:"max_attempts"`            // 最大尝试次数
	IP         string    `gorm:"size:45" json:"ip"`                        // 发送 OTP 的 IP
}

// TableName 指定表名
func (EmailOTPToken) TableName() string {
	return "email_otp_tokens"
}
