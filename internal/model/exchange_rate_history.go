package model

import "time"

// ExchangeRateHistory 汇率历史快照
// 每次从外部 API 拉取的汇率结果都会写入此表用于审计与降级 fallback
type ExchangeRateHistory struct {
	ID           uint64    `gorm:"primaryKey" json:"id"`
	FromCurrency string    `gorm:"type:varchar(10);not null;index:idx_fx_pair_date,priority:1" json:"from_currency"`
	ToCurrency   string    `gorm:"type:varchar(10);not null;index:idx_fx_pair_date,priority:2" json:"to_currency"`
	Rate         float64   `gorm:"type:decimal(16,8);not null" json:"rate"`
	Source       string    `gorm:"type:varchar(32);not null;index" json:"source"` // aliyun_primary / aliyun_backup / manual / default
	RawResponse  string    `gorm:"type:text" json:"raw_response,omitempty"`       // 原始响应 JSON（审计用）
	FetchedAt    time.Time `gorm:"index:idx_fx_pair_date,priority:3;not null" json:"fetched_at"`
	CreatedAt    time.Time `json:"created_at"`
}

// TableName 指定汇率历史表名
func (ExchangeRateHistory) TableName() string {
	return "exchange_rate_histories"
}
