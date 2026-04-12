package model

import "time"

// PaymentProviderConfig 在线支付渠道配置（微信/支付宝/Stripe/PayPal）
type PaymentProviderConfig struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	ProviderType string    `gorm:"type:varchar(20);not null;uniqueIndex" json:"provider_type"` // WECHAT/ALIPAY/STRIPE/PAYPAL
	DisplayName  string    `gorm:"type:varchar(50);not null" json:"display_name"`
	IsActive     bool      `gorm:"default:false" json:"is_active"`
	IsSandbox    bool      `gorm:"default:true" json:"is_sandbox"`
	ConfigJSON   string    `gorm:"type:text" json:"config_json,omitempty"` // AES-256-GCM 加密存储的 JSON 配置
	SortOrder    int       `gorm:"default:0" json:"sort_order"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// TableName 指定表名
func (PaymentProviderConfig) TableName() string {
	return "payment_provider_configs"
}
