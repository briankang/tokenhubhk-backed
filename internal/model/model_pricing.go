package model

import "time"

// ModelPricing 模型定价，定义平台对外售价
// 采用双轨存储：积分定价(int64) + 人民币定价(float64)
// InputPricePerToken 单位：每百万 token 的积分价格，便于整数运算
type ModelPricing struct {
	BaseModel
	ModelID             uint    `gorm:"index;not null" json:"model_id"`                       // 关联 AI 模型 ID
	InputPricePerToken  int64   `gorm:"type:bigint;default:0" json:"input_price_per_token"`   // 输入售价 (每百万token积分)
	InputPriceRMB       float64 `gorm:"type:decimal(16,4);default:0" json:"input_price_rmb"`  // 输入售价 (每百万token人民币)
	OutputPricePerToken int64   `gorm:"type:bigint;default:0" json:"output_price_per_token"`  // 输出售价 (每百万token积分)
	OutputPriceRMB      float64 `gorm:"type:decimal(16,4);default:0" json:"output_price_rmb"` // 输出售价 (每百万token人民币)
	// OutputPriceThinkingRMB / OutputPriceThinkingPerToken:
	// 思考模式输出售价（0 = 不区分，与 OutputPriceRMB 相同）
	OutputPriceThinkingRMB      float64    `gorm:"type:decimal(16,4);default:0" json:"output_price_thinking_rmb"`
	OutputPriceThinkingPerToken int64      `gorm:"type:bigint;default:0" json:"output_price_thinking_per_token"`
	Currency                    string     `gorm:"type:varchar(10);default:'CREDIT'" json:"currency"` // 币种 CREDIT
	EffectiveFrom               *time.Time `json:"effective_from,omitempty"`                          // 生效时间
	PriceTiers                  JSON       `gorm:"type:json" json:"price_tiers,omitempty"`            // 阶梯价格配置（平台定价，JSON 格式的 PriceTiersData）

	// ==== 全局折扣引擎(v2 引入) ====
	// 一次配置,自动应用到所有价格档(基础价/阶梯价/缓存价/思考价)
	GlobalDiscountRate     float64 `gorm:"type:decimal(8,6);default:0" json:"global_discount_rate"` // 全局折扣率,0 = 未启用全局折扣,>0(如 0.85)= 官网价 × 该值
	GlobalDiscountAnchored bool    `gorm:"default:false" json:"global_discount_anchored"`           // true = 锚定模式,所有价格档自动从官网价×折扣推导;false = 自由编辑模式,各档可独立填写
	PriceLockOverrides     JSON    `gorm:"type:json" json:"price_lock_overrides,omitempty"`         // 单档解锁覆盖,如 {"cache_read": 0.10}, 解锁的档不受全局折扣影响

	// ==== 锁定汇率(模型市场双货币显示用) ====
	// 价格录入时记录当时的 USD/CNY 汇率,后续不随汇率波动,保证用户看到的美元价稳定
	PricedAtAt           *time.Time `json:"priced_at_at,omitempty"`                                             // 价格最近一次录入时间
	PricedAtExchangeRate float64    `gorm:"type:decimal(16,6);default:0" json:"priced_at_exchange_rate"`        // 录入时锁定的 USD→CNY 汇率
	PricedAtRateSource   string     `gorm:"type:varchar(50);default:''" json:"priced_at_rate_source,omitempty"` // 汇率来源(aliyun_market/manual/fallback)

	// ==== PriceMatrix(v3 引入) ====
	// 统一价格矩阵,可表达任意维度组合下的价格,见 model/price_matrix.go
	// 旧的 PriceTiers 字段保留作向后兼容,新代码优先读 PriceMatrix。
	PriceMatrix JSON `gorm:"type:json" json:"price_matrix,omitempty"`

	Model AIModel `gorm:"foreignKey:ModelID" json:"model,omitempty"` // 关联模型
}

// TableName 指定模型定价表名
func (ModelPricing) TableName() string {
	return "model_pricings"
}
