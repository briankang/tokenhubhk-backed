package model

import "time"

// ModelPricing 模型定价，定义平台对外售价
// 采用双轨存储：积分定价(int64) + 人民币定价(float64)
// InputPricePerToken 单位：每百万 token 的积分价格，便于整数运算
type ModelPricing struct {
	BaseModel
	ModelID             uint       `gorm:"index;not null" json:"model_id"`                             // 关联 AI 模型 ID
	InputPricePerToken  int64      `gorm:"type:bigint;default:0" json:"input_price_per_token"`          // 输入售价 (每百万token积分)
	InputPriceRMB       float64    `gorm:"type:decimal(16,4);default:0" json:"input_price_rmb"`         // 输入售价 (每百万token人民币)
	OutputPricePerToken int64      `gorm:"type:bigint;default:0" json:"output_price_per_token"`         // 输出售价 (每百万token积分)
	OutputPriceRMB      float64    `gorm:"type:decimal(16,4);default:0" json:"output_price_rmb"`        // 输出售价 (每百万token人民币)
	// OutputPriceThinkingRMB / OutputPriceThinkingPerToken:
	// 思考模式输出售价（0 = 不区分，与 OutputPriceRMB 相同）
	OutputPriceThinkingRMB      float64 `gorm:"type:decimal(16,4);default:0" json:"output_price_thinking_rmb"`
	OutputPriceThinkingPerToken int64   `gorm:"type:bigint;default:0" json:"output_price_thinking_per_token"`
	Currency            string     `gorm:"type:varchar(10);default:'CREDIT'" json:"currency"`           // 币种 CREDIT
	EffectiveFrom       *time.Time `json:"effective_from,omitempty"`                                    // 生效时间
	PriceTiers          JSON       `gorm:"type:json" json:"price_tiers,omitempty"`                     // 阶梯价格配置（平台定价，JSON 格式的 PriceTiersData）

	Model AIModel `gorm:"foreignKey:ModelID" json:"model,omitempty"` // 关联模型
}

// TableName 指定模型定价表名
func (ModelPricing) TableName() string {
	return "model_pricings"
}
