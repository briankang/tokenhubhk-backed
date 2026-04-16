package model

import "time"

// PriceTier 单个阶梯价格
// 用于表示供应商按 token 用量分段计价的单个阶梯
// 对于非 Token 计费单位（per_image 等），可用 Variant 区分质量档（如 "1024x1024"/"hd"），
// 此时 MinTokens/MaxTokens 可忽略，InputPrice 表示该档位单价。
type PriceTier struct {
	Name              string  `json:"name"`                          // 阶梯名称，如 "0-1M" / "hd"
	Variant           string  `json:"variant,omitempty"`             // 变体/质量档（非 Token 单位使用，如 "1024x1024"/"hd"/"low-latency"）
	MinTokens         int64   `json:"min_tokens"`                    // 最小 token 数
	MaxTokens         *int64  `json:"max_tokens"`                    // 最大 token 数，nil 表示无上限
	InputPrice        float64 `json:"input_price"`                   // 输入价格（单位由 PriceTiersData.UnitLabel 决定）
	OutputPrice       float64 `json:"output_price"`                  // 输出价格（单位由 PriceTiersData.UnitLabel 决定）
	CacheInputPrice   float64 `json:"cache_input_price,omitempty"`   // 缓存命中输入价，0表示该阶梯不支持缓存
	CacheWritePrice   float64 `json:"cache_write_price,omitempty"`   // 缓存写入价，Anthropic/阿里云显式专用
}

// PriceTiersData 完整阶梯价格数据
// 包含所有阶梯、币种信息和数据来源
type PriceTiersData struct {
	Tiers     []PriceTier `json:"tiers"`                // 阶梯价格列表，按 MinTokens 升序排列
	Currency  string      `json:"currency"`             // 币种，默认 "CNY"
	UnitLabel string      `json:"unit_label,omitempty"` // 单位标签（如 "元/百万token"/"元/张"/"元/小时"），便于前端直接展示
	UpdatedAt time.Time   `json:"updated_at"`           // 价格更新时间
	SourceURL string      `json:"source_url"`           // 数据来源 URL
}
