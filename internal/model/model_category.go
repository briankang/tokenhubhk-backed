package model

// ModelCategory AI 模型分类，用于将同一供应商的模型分组
type ModelCategory struct {
	BaseModel
	SupplierID  uint   `gorm:"index;not null" json:"supplier_id"`                       // 供应商 ID
	Name        string `gorm:"type:varchar(100);not null" json:"name"`                   // 分类名称
	Code        string `gorm:"type:varchar(50);uniqueIndex;not null" json:"code"`         // 唯一编码
	Description string `gorm:"type:text" json:"description,omitempty"`                   // 描述
	SortOrder   int    `gorm:"default:0" json:"sort_order"`                               // 排序

	Supplier Supplier `gorm:"foreignKey:SupplierID" json:"supplier,omitempty"` // 关联供应商
}

// TableName 指定模型分类表名
func (ModelCategory) TableName() string {
	return "model_categories"
}
