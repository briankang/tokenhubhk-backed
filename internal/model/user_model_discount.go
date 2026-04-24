package model

import "time"

// UserModelDiscount 用户×模型 维度的特殊折扣覆盖
// 用途：对单个用户的单个模型设置特殊售价（折扣 / 固定价 / 加价）
// 覆盖优先级：UserModelDiscount > AgentPricing > AgentLevelDiscount > 平台默认
//
// PricingType 说明：
//   - DISCOUNT: 基于平台售价按 DiscountRate 打折（0.7 = 7折）
//   - FIXED:    直接使用 InputPrice / OutputPrice 作为最终售价（人民币/百万tokens）
//   - MARKUP:   基于平台售价按 MarkupRate 加价（1.2 = 加20%）
type UserModelDiscount struct {
	BaseModel
	UserID       uint       `gorm:"uniqueIndex:uk_user_model;not null" json:"user_id"`
	ModelID      uint       `gorm:"uniqueIndex:uk_user_model;not null" json:"model_id"`
	PricingType  string     `gorm:"type:varchar(20);not null;default:'DISCOUNT'" json:"pricing_type"`
	DiscountRate *float64   `gorm:"type:decimal(6,4)" json:"discount_rate,omitempty"` // 0.8000 = 8折
	InputPrice   *float64   `gorm:"type:decimal(20,10)" json:"input_price,omitempty"` // FIXED 使用（人民币/百万tokens）
	OutputPrice  *float64   `gorm:"type:decimal(20,10)" json:"output_price,omitempty"`
	MarkupRate   *float64   `gorm:"type:decimal(6,4)" json:"markup_rate,omitempty"` // 1.2000 = +20%
	EffectiveAt  *time.Time `json:"effective_at,omitempty"`                         // 生效起点，NULL 立即生效
	ExpireAt     *time.Time `json:"expire_at,omitempty"`                            // 过期时间，NULL 永久
	Note         string     `gorm:"type:varchar(500)" json:"note"`
	IsActive     bool       `gorm:"default:true;index" json:"is_active"`
	OperatorID   uint       `json:"operator_id"` // 最后创建/修改者

	User  *User    `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Model *AIModel `gorm:"foreignKey:ModelID" json:"model,omitempty"`
}

// TableName 指定表名
func (UserModelDiscount) TableName() string {
	return "user_model_discounts"
}

// IsEffective 判断当前记录是否在生效期内且启用
func (d *UserModelDiscount) IsEffective(now time.Time) bool {
	if !d.IsActive {
		return false
	}
	if d.EffectiveAt != nil && now.Before(*d.EffectiveAt) {
		return false
	}
	if d.ExpireAt != nil && now.After(*d.ExpireAt) {
		return false
	}
	return true
}
