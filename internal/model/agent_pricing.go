package model

// AgentPricing 代理商自定义定价，支持 4 种定价策略
// FIXED(固定价)/MARKUP(加价)/DISCOUNT(折扣)/INHERIT(继承上级)
type AgentPricing struct {
	BaseModel
	TenantID     uint     `gorm:"index;not null" json:"tenant_id"`
	ModelID      uint     `gorm:"index;not null" json:"model_id"`
	PricingType  string   `gorm:"type:varchar(20);not null;default:'INHERIT'" json:"pricing_type"` // FIXED / MARKUP / DISCOUNT / INHERIT
	InputPrice   *float64 `gorm:"type:decimal(20,10)" json:"input_price,omitempty"`
	OutputPrice  *float64 `gorm:"type:decimal(20,10)" json:"output_price,omitempty"`
	MarkupRate   *float64 `gorm:"type:decimal(5,2)" json:"markup_rate,omitempty"`
	DiscountRate *float64 `gorm:"type:decimal(5,2)" json:"discount_rate,omitempty"`

	Tenant Tenant  `gorm:"foreignKey:TenantID" json:"tenant,omitempty"`
	Model  AIModel `gorm:"foreignKey:ModelID" json:"model,omitempty"`
}

// TableName 指定代理商定价表名
func (AgentPricing) TableName() string {
	return "agent_pricings"
}
