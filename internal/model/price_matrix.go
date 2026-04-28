package model

import "time"

// PriceMatrix 是 v3 引入的统一价格矩阵结构,用于表达任意维度组合下的价格。
//
// 设计目标:
//
//  1. 统一 7 种模型类型的价格存储:LLM 阶梯 / Image 分辨率档 / Video Seedance 矩阵 /
//     TTS 音色档 / ASR 类型 / Embedding / Rerank 全部用一份 schema 表达。
//  2. 维度自描述:dimensions 字段定义有哪些维度(分辨率/含视频/在线离线/思考模式...),
//     前端按 dimensions 自动渲染表格列,管理员可见可编辑。
//  3. 单元格按需:cells 数组每条记录一个具体维度组合下的价格,
//     不存在的组合可标记 supported=false,供前端展示「暂不支持」。
//
// 存储位置:ModelPricing.PriceMatrix JSON 字段。
//
// 命中规则(BillingService 计费时):从请求 usage/params 提取 dim_values,
// 在 cells 中查找完全匹配的 cell,取其 SellingInput/SellingOutput/SellingPerUnit 计算金额。
type PriceMatrix struct {
	SchemaVersion int               `json:"schema_version"` // 当前 1
	Currency      string            `json:"currency"`       // "RMB" / "USD"
	Unit          string            `json:"unit"`           // 同 AIModel.PricingUnit:per_million_tokens / per_image / per_second / per_minute / per_10k_characters / per_million_characters / per_call / per_hour
	Dimensions    []PriceDimension  `json:"dimensions"`     // 维度定义,前端按这个渲染表格列
	Cells         []PriceMatrixCell `json:"cells"`          // 单元格数据
	// 元数据
	GlobalDiscountRate   float64    `json:"global_discount_rate,omitempty"`    // 与 ModelPricing.GlobalDiscountRate 同步
	PricedAtAt           *time.Time `json:"priced_at_at,omitempty"`            // 价格录入时间
	PricedAtExchangeRate float64    `json:"priced_at_exchange_rate,omitempty"` // USD→CNY 锁定汇率
	PricedAtRateSource   string     `json:"priced_at_rate_source,omitempty"`   // 汇率来源
}

// PriceDimension 一个维度定义。
type PriceDimension struct {
	Key    string        `json:"key"`              // 字段名,如 "resolution" / "input_has_video"
	Label  string        `json:"label"`            // 展示名,如 "输出分辨率" / "输入是否含视频"
	Type   string        `json:"type"`             // "select" / "boolean" / "range" / "free_text"
	Values []interface{} `json:"values,omitempty"` // 可选值列表,select 必填,boolean 通常 [false,true]
	Help   string        `json:"help,omitempty"`   // 字段说明(管理员看)
}

// PriceMatrixCell 一个维度组合下的价格单元。
type PriceMatrixCell struct {
	// DimValues 维度组合,key 必须是 PriceMatrix.Dimensions 中的某个 key,
	// value 必须是该维度 Values 中的一个;命中时按完全匹配
	// (后续可扩展通配符 "*" 表示该维度任意值)。
	DimValues map[string]interface{} `json:"dim_values"`

	// 价格字段:按需填,nil 表示该字段不适用(如 Embedding 没有 OutputPrice)。
	OfficialInput   *float64 `json:"official_input,omitempty"`    // 官网原价 输入价 (¥/Unit)
	OfficialOutput  *float64 `json:"official_output,omitempty"`   // 官网原价 输出价
	OfficialPerUnit *float64 `json:"official_per_unit,omitempty"` // 单价模型:官网原价 (¥/张/秒/次/万字符)
	SellingInput    *float64 `json:"selling_input,omitempty"`     // 平台售价 输入价
	SellingOutput   *float64 `json:"selling_output,omitempty"`    // 平台售价 输出价
	SellingPerUnit  *float64 `json:"selling_per_unit,omitempty"`  // 单价模型:平台售价

	// 状态字段
	Supported         bool   `json:"supported"`                    // false = 该组合不支持(展示置灰 + 显示 unsupported_reason)
	UnsupportedReason string `json:"unsupported_reason,omitempty"` // 不支持原因,如 "供应商策略不支持 1080p 离线推理"
	Note              string `json:"note,omitempty"`               // 备注(管理员可见)
}
