package model

import "time"

// RegistrationEvent 注册行为审计日志
// 记录每次注册尝试的完整上下文,便于风控追溯与对抗样本分析
// 无论成功/失败/拦截均写入,Decision 字段区分
type RegistrationEvent struct {
	BaseModel
	Email         string    `gorm:"size:255;index" json:"email"`              // 注册邮箱(拦截时可能为空)
	UserID        uint      `gorm:"index;default:0" json:"user_id"`           // 注册成功后的 User.ID,失败为 0
	IP            string    `gorm:"size:45;index" json:"ip"`                  // 客户端 IP(支持 IPv6)
	UserAgent     string    `gorm:"size:500" json:"user_agent"`               // UA 字符串
	Fingerprint   string    `gorm:"size:100;index" json:"fingerprint"`        // 设备指纹 hash
	Country       string    `gorm:"size:10" json:"country"`                   // 国家代码(IP 地理)
	ASN           string    `gorm:"size:50" json:"asn"`                       // ASN 信息
	IPType        string    `gorm:"size:20" json:"ip_type"`                   // residential / datacenter / vpn / tor
	DwellSeconds  int       `gorm:"default:0" json:"dwell_seconds"`           // 表单停留秒数
	ReferralCode  string    `gorm:"size:32" json:"referral_code"`             // 使用的邀请码
	Decision      string    `gorm:"size:20;index" json:"decision"`            // PASS / BLOCKED / SHADOW
	BlockedReason string    `gorm:"size:100" json:"blocked_reason"`           // 拦截原因(如 CAPTCHA_FAIL / IP_LIMIT / DISPOSABLE_EMAIL / TOR)
	HoneypotHit   bool      `gorm:"default:false" json:"honeypot_hit"`        // 是否命中 honeypot 字段
	EventTime     time.Time `gorm:"index;not null" json:"event_time"`         // 事件时间
}

// TableName 指定表名
func (RegistrationEvent) TableName() string {
	return "registration_events"
}
