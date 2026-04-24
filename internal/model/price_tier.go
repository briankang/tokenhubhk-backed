package model

import "time"

// PriceTier 单个阶梯价格
// 用于表示供应商按 token 用量分段计价的单个阶梯
// 对于非 Token 计费单位（per_image 等），可用 Variant 区分质量档（如 "1024x1024"/"hd"），
// 此时 Input/Output Min/Max 可忽略，InputPrice 表示该档位单价。
//
// 阶梯命中判定同时考虑输入长度区间和输出长度区间（二维 AND 条件）。
// 未设置任何区间的阶梯默认语义为 (0, +∞] × (0, +∞]（全覆盖）。
type PriceTier struct {
	Name    string `json:"name"`              // 阶梯名称，如 "(0, 32k]" / "hd"
	Variant string `json:"variant,omitempty"` // 变体/质量档（非 Token 单位使用）

	// ---- 输入长度区间 (InputMin, InputMax] 默认 (0, +∞] ----
	InputMin          int64  `json:"input_min"`                     // 下界
	InputMinExclusive bool   `json:"input_min_exclusive,omitempty"` // true=开 (, false=闭 [
	InputMax          *int64 `json:"input_max,omitempty"`           // nil=+∞
	InputMaxExclusive bool   `json:"input_max_exclusive,omitempty"` // true=开 ), false=闭 ]

	// ---- 输出长度区间 (OutputMin, OutputMax] 默认 (0, +∞] ----
	OutputMin          int64  `json:"output_min"`
	OutputMinExclusive bool   `json:"output_min_exclusive,omitempty"`
	OutputMax          *int64 `json:"output_max,omitempty"`
	OutputMaxExclusive bool   `json:"output_max_exclusive,omitempty"`

	// ---- DEPRECATED：旧字段，由 Normalize() 自动同步到 InputMin/InputMax ----
	// 保留一个大版本以兼容历史数据；写入路径优先使用新字段。
	MinTokens int64  `json:"min_tokens,omitempty"` // = InputMin（兼容）
	MaxTokens *int64 `json:"max_tokens,omitempty"` // = InputMax（兼容）

	// ---- 成本价（供应商官方价格）----
	InputPrice      float64 `json:"input_price"`                 // 输入价格
	OutputPrice     float64 `json:"output_price"`                // 输出价格
	CacheInputPrice float64 `json:"cache_input_price,omitempty"` // 缓存命中输入价
	CacheWritePrice float64 `json:"cache_write_price,omitempty"` // 缓存写入价
	// OutputPriceThinking: 思考模式输出成本价（0 = 不区分，与 OutputPrice 相同）
	// 当阿里云等供应商在同一阶梯内将输出价拆分「非思考模式/思考模式」两档时使用
	OutputPriceThinking float64 `json:"output_price_thinking,omitempty"`

	// ---- 新增：阶梯独立售价覆盖（nil=走模型级 selling price，可叠加 DISCOUNT 折扣）----
	SellingInputPrice         *float64 `json:"selling_input_price,omitempty"`
	SellingOutputPrice        *float64 `json:"selling_output_price,omitempty"`
	SellingOutputThinkingPrice *float64 `json:"selling_output_thinking_price,omitempty"` // 思考模式输出售价覆盖
}

// PriceTiersData 完整阶梯价格数据
// 包含所有阶梯、币种信息和数据来源
type PriceTiersData struct {
	Tiers     []PriceTier `json:"tiers"`                // 阶梯价格列表，按 InputMin 升序排列
	Currency  string      `json:"currency"`             // 币种，默认 "CNY"
	UnitLabel string      `json:"unit_label,omitempty"` // 单位标签（如 "元/百万token"/"元/张"/"元/小时"），便于前端直接展示
	UpdatedAt time.Time   `json:"updated_at"`           // 价格更新时间
	SourceURL string      `json:"source_url"`           // 数据来源 URL
}
