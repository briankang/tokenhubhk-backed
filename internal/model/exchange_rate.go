package model

// ExchangeRate 汇率配置模型，用于外币→人民币换算
// 支持多币种汇率管理，包含手续费配置
type ExchangeRate struct {
	BaseModel
	FromCurrency string  `gorm:"type:varchar(10);not null;index" json:"from_currency"`    // 源币种 USD/EUR/GBP/JPY/HKD
	ToCurrency   string  `gorm:"type:varchar(10);not null;default:CNY" json:"to_currency"` // 目标币种 CNY
	Rate         float64 `gorm:"type:decimal(16,8);not null" json:"rate"`                 // 汇率（如 7.2500）
	FeeRate      float64 `gorm:"type:decimal(8,4);default:0" json:"fee_rate"`             // 手续费比例（如 0.0200 = 2%）
	IsActive     bool    `gorm:"default:true" json:"is_active"`                           // 是否启用
}

// TableName 指定汇率配置表名
func (ExchangeRate) TableName() string {
	return "exchange_rates"
}
