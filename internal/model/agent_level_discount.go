package model

// AgentLevelDiscount 代理层级折扣模型，根据代理等级设置不同折扣率
type AgentLevelDiscount struct {
	BaseModel
	Level          int      `gorm:"not null" json:"level"`                                // 代理层级 1-3
	ModelID        *uint    `gorm:"index" json:"model_id,omitempty"`                      // 模型 ID（空=全局折扣）
	InputDiscount  float64  `gorm:"type:decimal(5,2);default:1.0" json:"input_discount"`  // 输入折扣率 (0.8=打8折)
	OutputDiscount float64  `gorm:"type:decimal(5,2);default:1.0" json:"output_discount"` // 输出折扣率

	Model *AIModel `gorm:"foreignKey:ModelID" json:"model,omitempty"` // 关联模型
}

// TableName 指定代理层级折扣表名
func (AgentLevelDiscount) TableName() string {
	return "agent_level_discounts"
}
