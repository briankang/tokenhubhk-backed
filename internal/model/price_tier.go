package model

import "time"

// PriceTier describes one official/selling price tier.
//
// Token tiers match on input/output token ranges; non-token units can use Variant.
//
// 多维度匹配（S1, 2026-04-28 起）：
//
//	DimValues 是模型业务维度的显式编码，例如：
//	  {"resolution": "1080p", "input_has_video": "true"}  → Seedance 2.0
//	  {"context_tier": "0-128k", "thinking_mode": "true"} → LLM 思考阶梯
//
//	匹配优先级（见 SelectTierByDims / SelectTierOrLargest）：
//	  1. DimValues 全字段匹配（请求侧也提供 dims 时；空值键不参与）
//	  2. token 区间 InputMin/InputMax × OutputMin/OutputMax 匹配
//	  3. 兜底取 InputMin 最大档（避免低估）
//
//	向后兼容：旧 tier `DimValues=nil` 直接走 token 区间路径，行为不变。
//	空字符串值被视为"该维度不限定"，例如 {"resolution":""} 等价于不声明该维度。
type PriceTier struct {
	Name    string `json:"name"`
	Variant string `json:"variant,omitempty"`

	// DimValues 显式声明该 tier 适用的业务维度组合（多维矩阵编码）
	//
	// 维度键应与模型的 ModelDimensionConfig.Dimensions[*].Key 对齐（运行期校验）。
	// 值统一用 string（bool 用 "true"/"false"，与 PriceMatrix.PriceMatrixCell.DimValues
	// 兼容，避免 JSON 反序列化的 number/string/bool 类型抖动）。
	DimValues map[string]string `json:"dim_values,omitempty"`

	InputMin          int64  `json:"input_min"`
	InputMinExclusive bool   `json:"input_min_exclusive,omitempty"`
	InputMax          *int64 `json:"input_max,omitempty"`
	InputMaxExclusive bool   `json:"input_max_exclusive,omitempty"`

	// ---- 输出长度区间 (OutputMin, OutputMax] 默认 (0, +∞] ----
	OutputMin          int64  `json:"output_min"`
	OutputMinExclusive bool   `json:"output_min_exclusive,omitempty"`
	OutputMax          *int64 `json:"output_max,omitempty"`
	OutputMaxExclusive bool   `json:"output_max_exclusive,omitempty"`

	InputPrice          float64 `json:"input_price"`
	OutputPrice         float64 `json:"output_price"`
	CacheInputPrice     float64 `json:"cache_input_price,omitempty"`
	CacheWritePrice     float64 `json:"cache_write_price,omitempty"`
	OutputPriceThinking float64 `json:"output_price_thinking,omitempty"`

	// ---- 阶梯独立售价覆盖（nil=走模型级 selling price，可叠加 DISCOUNT 折扣）----
	SellingInputPrice          *float64 `json:"selling_input_price,omitempty"`
	SellingOutputPrice         *float64 `json:"selling_output_price,omitempty"`
	SellingOutputThinkingPrice *float64 `json:"selling_output_thinking_price,omitempty"`
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
