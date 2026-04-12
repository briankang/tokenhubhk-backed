package model

// Supplier AI 模型供应商模型 (OpenAI/Anthropic/DeepSeek 等)
// 一个供应商可以同时有 api 类型和 coding_plan 类型的接入点，表现为两条记录
type Supplier struct {
	BaseModel
	Name        string `gorm:"type:varchar(100);not null" json:"name"` // 供应商名称
	Code        string `gorm:"type:varchar(50);not null;uniqueIndex:uidx_supplier_code_type" json:"code"` // 唯一编码（与AccessType联合唯一）
	BaseURL     string `gorm:"type:varchar(500)" json:"base_url,omitempty"`   // API 基础 URL
	Description string `gorm:"type:text" json:"description,omitempty"`       // 说明
	IsActive    bool   `gorm:"default:true" json:"is_active"`                 // 是否启用
	SortOrder   int    `gorm:"default:0" json:"sort_order"`                   // 排序权重
	// 新增字段
	AccessType      string  `gorm:"type:varchar(20);not null;default:'api';uniqueIndex:uidx_supplier_code_type" json:"access_type"`      // 接入点类型: api / coding_plan
	InputPricePerM  float64 `gorm:"type:decimal(10,4);default:0" json:"input_price_per_m"`    // 输入tokens官网价格（每百万tokens，人民币）
	OutputPricePerM float64 `gorm:"type:decimal(10,4);default:0" json:"output_price_per_m"`   // 输出tokens官网价格（每百万tokens，人民币）
	Discount        float64 `gorm:"type:decimal(5,4);default:1.0" json:"discount"`            // 折扣比例，如0.85表示85折，1.0表示无折扣
	Status          string  `gorm:"type:varchar(20);default:'active'" json:"status"`          // 状态: active / inactive / maintenance
}

// TableName 指定供应商表名
func (Supplier) TableName() string {
	return "suppliers"
}
