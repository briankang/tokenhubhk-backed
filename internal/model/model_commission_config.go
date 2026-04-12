package model

// ModelCommissionConfig 模型级别佣金配置
// 支持按"供应商+模型"维度配置不同代理等级（A0-A4）的佣金比例
// 优先级：模型+供应商级别配置 > 仅模型名配置 > 代理等级默认比例
type ModelCommissionConfig struct {
	BaseModel
	SupplierID   uint    `gorm:"uniqueIndex:uidx_supplier_model_commission;not null" json:"supplier_id"` // 供应商ID，联合唯一索引
	SupplierName string  `gorm:"-" json:"supplier_name"`                                                 // 非存储字段，查询时通过Preload填充
	ModelName    string  `gorm:"type:varchar(100);uniqueIndex:uidx_supplier_model_commission;not null" json:"model_name"` // 模型名称，如 "gpt-4o"
	A0Rate       float64 `gorm:"type:decimal(5,4);not null;default:0" json:"a0_rate"`                    // A0推广员佣金比例
	A1Rate       float64 `gorm:"type:decimal(5,4);not null;default:0" json:"a1_rate"`                    // A1青铜佣金比例
	A2Rate       float64 `gorm:"type:decimal(5,4);not null;default:0" json:"a2_rate"`                    // A2白银佣金比例
	A3Rate       float64 `gorm:"type:decimal(5,4);not null;default:0" json:"a3_rate"`                    // A3黄金佣金比例
	A4Rate       float64 `gorm:"type:decimal(5,4);not null;default:0" json:"a4_rate"`                    // A4铂金佣金比例
	IsActive     bool    `gorm:"default:true" json:"is_active"`                                          // 是否启用
	Remark       string  `gorm:"type:varchar(255)" json:"remark"`                                        // 备注

	// 关联供应商，查询时自动填充
	Supplier Supplier `gorm:"foreignKey:SupplierID" json:"supplier,omitempty"`
}

// TableName 指定表名
func (ModelCommissionConfig) TableName() string {
	return "model_commission_configs"
}

// GetRateByLevel 根据代理等级代码获取对应的佣金比例
// 参数 levelCode: A0, A1, A2, A3, A4
// 返回值: 对应等级的佣金比例，如果等级代码无效返回0
func (m *ModelCommissionConfig) GetRateByLevel(levelCode string) float64 {
	switch levelCode {
	case "A0":
		return m.A0Rate
	case "A1":
		return m.A1Rate
	case "A2":
		return m.A2Rate
	case "A3":
		return m.A3Rate
	case "A4":
		return m.A4Rate
	default:
		return 0
	}
}
