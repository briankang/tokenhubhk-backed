package model

import "time"

// ApiKey API 密钥模型，用于程序化接入
// Key 以 "sk-" 开头，存储 SHA256 哈希，支持过期时间和权限控制
type ApiKey struct {
	BaseModel
	TenantID        uint       `gorm:"index;not null" json:"tenant_id"`                     // 所属租户
	UserID          uint       `gorm:"index;not null" json:"user_id"`                       // 所属用户
	Name            string     `gorm:"type:varchar(100);not null" json:"name"`              // 密钥名称
	KeyHash         string     `gorm:"type:varchar(255);index;not null" json:"-"`           // SHA256 哈希（不输出）
	KeyPrefix       string     `gorm:"type:varchar(8);not null" json:"key_prefix"`          // 前 8 位用于展示
	IsActive        bool       `gorm:"default:true" json:"is_active"`                       // 是否启用
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`                                // 过期时间
	LastUsedAt      *time.Time `json:"last_used_at,omitempty"`                              // 最后使用时间
	Permissions     JSON       `gorm:"type:json" json:"permissions,omitempty"`              // 权限配置 (JSON)
	CustomChannelID *uint          `gorm:"column:custom_channel_id;index" json:"custom_channel_id,omitempty"`
	// 关联的自定义渠道ID，nil表示使用默认渠道
	CustomChannel   *CustomChannel `gorm:"foreignKey:CustomChannelID" json:"custom_channel,omitempty"`
	CreditLimit     int64          `gorm:"column:credit_limit;type:bigint;default:0" json:"credit_limit"` // 此 Key 的总积分消费上限，0=无限
	CreditUsed      int64      `gorm:"column:credit_used;type:bigint;default:0" json:"credit_used"`                // 此 Key 已消费积分
	AllowedModels   string     `gorm:"column:allowed_models;type:text" json:"allowed_models"`                      // JSON 数组字符串，限制可调用模型范围，空=所有
	RateLimitRPM    int        `gorm:"column:rate_limit_rpm;type:int;default:0" json:"rate_limit_rpm"`             // 每分钟最大请求次数，0=使用系统默认
	RateLimitTPM    int        `gorm:"column:rate_limit_tpm;type:int;default:0" json:"rate_limit_tpm"`             // 每分钟最大Token数，0=使用系统默认

	Tenant Tenant `gorm:"foreignKey:TenantID" json:"tenant,omitempty"`
	User   User   `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

// TableName 指定 API 密钥表名
func (ApiKey) TableName() string {
	return "api_keys"
}
