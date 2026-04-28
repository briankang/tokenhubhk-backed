package model

// PriceCalculatorConfig 价格计算器配置。
// 用于把“模型分类/计费单位/试算字段/公式说明”从代码常量逐步迁移为运营可管理配置。
type PriceCalculatorConfig struct {
	BaseModel
	Code             string `gorm:"type:varchar(100);not null;uniqueIndex" json:"code"`
	Name             string `gorm:"type:varchar(200);not null" json:"name"`
	Description      string `gorm:"type:text" json:"description,omitempty"`
	ModelTypes       JSON   `gorm:"type:json" json:"model_types,omitempty"`
	PricingUnits     JSON   `gorm:"type:json" json:"pricing_units,omitempty"`
	FieldsSchema     JSON   `gorm:"type:json" json:"fields_schema,omitempty"`
	Formula          JSON   `gorm:"type:json" json:"formula,omitempty"`
	AccuracyLevel    string `gorm:"type:varchar(50);default:'compatible'" json:"accuracy_level"`
	CompatibilityTip string `gorm:"type:text" json:"compatibility_tip,omitempty"`
	IsActive         bool   `gorm:"default:true;index" json:"is_active"`
	Version          string `gorm:"type:varchar(50);default:'v1'" json:"version"`
	Source           string `gorm:"type:varchar(50);default:'builtin'" json:"source"`
}

func (PriceCalculatorConfig) TableName() string {
	return "price_calculator_configs"
}
