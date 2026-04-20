package model

import "time"

// PaymentProviderAccount 多账号支付配置
// Stripe/PayPal 支持配置多个账号，按权重 + 币种 + 地区路由分发；
// 微信/支付宝监管要求保持单账号（仍可使用本表，但通常仅 1 条记录）
type PaymentProviderAccount struct {
	ID                  uint64     `gorm:"primaryKey" json:"id"`
	ProviderType        string     `gorm:"type:varchar(20);not null;index" json:"provider_type"` // WECHAT/ALIPAY/STRIPE/PAYPAL
	AccountName         string     `gorm:"type:varchar(100);not null" json:"account_name"`       // 管理员自定义名称（如 "Stripe US 主"）
	ConfigJSON          string     `gorm:"type:text;not null" json:"config_json,omitempty"`      // AES-256-GCM 加密
	Weight              int        `gorm:"default:10" json:"weight"`                              // 权重（1-100）
	Priority            int        `gorm:"default:0;index" json:"priority"`                       // 优先级（数字越小越优先）
	SupportedCurrencies string     `gorm:"type:varchar(200)" json:"supported_currencies"`         // 逗号分隔 "USD,EUR,GBP"，空=全部
	SupportedRegions    string     `gorm:"type:varchar(200)" json:"supported_regions"`            // 逗号分隔 "US,EU,APAC"，空=全部
	IsActive            bool       `gorm:"default:true;index" json:"is_active"`
	IsSandbox           bool       `gorm:"default:false" json:"is_sandbox"`
	FailureCount        int        `gorm:"default:0" json:"failure_count"`
	LastFailedAt        *time.Time `json:"last_failed_at,omitempty"`
	TotalOrders         int64      `gorm:"default:0" json:"total_orders"`
	TotalAmountRMB      float64    `gorm:"type:decimal(20,2);default:0" json:"total_amount_rmb"`
	Remark              string     `gorm:"type:varchar(500)" json:"remark,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// TableName 指定多账号表名
func (PaymentProviderAccount) TableName() string {
	return "payment_provider_accounts"
}
