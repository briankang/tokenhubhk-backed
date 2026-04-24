package model

// InvoiceTitle 用户保存的发票抬头(模板),用于开票时快捷选择,避免重复输入。
//
// 一个用户可保存多条抬头(个人 / 多家公司)；开票时前端拉取列表,选中某条即自动填充
// 申请表单字段。保存抬头时不校验税号真实性,仅做格式长度限制。
type InvoiceTitle struct {
	BaseModel
	UserID   uint `gorm:"index;not null" json:"user_id"`
	TenantID uint `gorm:"index;not null" json:"tenant_id"`

	// 地区与类型(与 InvoiceRequest 的枚举保持一致)
	Region      string `gorm:"type:varchar(16);index;not null" json:"region"`       // CN / OVERSEAS
	InvoiceType string `gorm:"type:varchar(32);not null" json:"invoice_type"`       // personal / company / vat_invoice

	// 抬头内容
	Title       string `gorm:"type:varchar(200);not null" json:"title"`
	TaxID       string `gorm:"type:varchar(64)" json:"tax_id,omitempty"`
	BankName    string `gorm:"type:varchar(200)" json:"bank_name,omitempty"`
	BankAccount string `gorm:"type:varchar(64)" json:"bank_account,omitempty"`
	Address     string `gorm:"type:varchar(500)" json:"address,omitempty"`
	Phone       string `gorm:"type:varchar(50)" json:"phone,omitempty"`
	Country     string `gorm:"type:varchar(64)" json:"country,omitempty"`
	Email       string `gorm:"type:varchar(200)" json:"email,omitempty"`

	// 展示属性
	Alias     string `gorm:"type:varchar(100)" json:"alias,omitempty"` // 备注名(如 "公司A-上海")
	IsDefault bool   `gorm:"default:false;index" json:"is_default"`    // 是否默认,同 user+region 仅一条为 true
}

// TableName 指定表名
func (InvoiceTitle) TableName() string {
	return "invoice_titles"
}
