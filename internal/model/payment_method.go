package model

import "time"

// PaymentMethod 付款方式展示配置
type PaymentMethod struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Type        string    `gorm:"type:varchar(20);not null;uniqueIndex" json:"type"` // WECHAT/ALIPAY/STRIPE/PAYPAL/BANK_TRANSFER
	DisplayName string    `gorm:"type:varchar(50);not null" json:"display_name"`
	Icon        string    `gorm:"type:varchar(50)" json:"icon"`         // 图标标识
	Description string    `gorm:"type:varchar(200)" json:"description"` // 说明文字
	IsActive    bool      `gorm:"default:true" json:"is_active"`
	SortOrder   int       `gorm:"default:0" json:"sort_order"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TableName 指定表名
func (PaymentMethod) TableName() string {
	return "payment_methods"
}
