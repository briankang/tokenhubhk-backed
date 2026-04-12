package model

import "time"

// PriceTier 单个阶梯价格
// 用于表示供应商按 token 用量分段计价的单个阶梯
type PriceTier struct {
	Name        string  `json:"name"`                  // 阶梯名称，如 "0-1M"
	MinTokens   int64   `json:"min_tokens"`            // 最小 token 数
	MaxTokens   *int64  `json:"max_tokens"`            // 最大 token 数，nil 表示无上限
	InputPrice  float64 `json:"input_price"`           // 输入价格（RMB/百万token），精确到小数点后4位
	OutputPrice float64 `json:"output_price"`          // 输出价格（RMB/百万token），精确到小数点后4位
}

// PriceTiersData 完整阶梯价格数据
// 包含所有阶梯、币种信息和数据来源
type PriceTiersData struct {
	Tiers     []PriceTier `json:"tiers"`                // 阶梯价格列表，按 MinTokens 升序排列
	Currency  string      `json:"currency"`             // 币种，默认 "CNY"
	UpdatedAt time.Time   `json:"updated_at"`           // 价格更新时间
	SourceURL string      `json:"source_url"`           // 数据来源 URL
}
