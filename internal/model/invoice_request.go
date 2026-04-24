package model

import "time"

// InvoiceRequest 发票开具申请
//
// 国内（region=CN）:用户提交开票资料 → 管理员在税控盘/电子税局开具 → 上传 PDF → 用户下载
// 海外（region=OVERSEAS）:前端直接生成 PDF Invoice/Receipt,不入库审批流;
//   若用户选择「需要盖章正本」等场景则入库走同一工作流。
//
// 状态流转:
//   pending  -- admin approve --> approved -- admin upload pdf --> issued
//   pending  -- admin reject  --> rejected
//   issued   -- admin void    --> voided (预留,Phase 1 不做)
type InvoiceRequest struct {
	BaseModel
	TenantID uint `gorm:"index;not null" json:"tenant_id"`
	UserID   uint `gorm:"index;not null" json:"user_id"`

	// 地区 + 凭证类型
	Region      string `gorm:"type:varchar(16);index;not null" json:"region"`       // CN / OVERSEAS
	InvoiceType string `gorm:"type:varchar(32);not null" json:"invoice_type"`       // personal / company / vat_invoice (专票)

	// 抬头信息（国内 / 海外共用部分字段）
	Title       string `gorm:"type:varchar(200);not null" json:"title"`          // 抬头名称 / Bill-to name
	TaxID       string `gorm:"type:varchar(64)" json:"tax_id,omitempty"`         // 税号 / VAT ID（企业必填）
	BankName    string `gorm:"type:varchar(200)" json:"bank_name,omitempty"`     // 开户行（专票必填）
	BankAccount string `gorm:"type:varchar(64)" json:"bank_account,omitempty"`   // 银行账号（专票必填）
	Address     string `gorm:"type:varchar(500)" json:"address,omitempty"`       // 地址
	Phone       string `gorm:"type:varchar(50)" json:"phone,omitempty"`          // 电话
	Country     string `gorm:"type:varchar(64)" json:"country,omitempty"`        // 国家（海外用）
	Email       string `gorm:"type:varchar(200);not null" json:"email"`          // 接收邮箱

	// 开票金额 + 订单关联
	OrderIDs       JSON    `gorm:"type:json" json:"order_ids"`                            // 合并的订单 Payment.ID 数组（Phase 1 仅 1 个）
	AmountRMB      float64 `gorm:"type:decimal(16,2);not null" json:"amount_rmb"`         // 人民币金额（国内必填）
	AmountOriginal float64 `gorm:"type:decimal(16,2);default:0" json:"amount_original"`   // 原币种金额
	Currency       string  `gorm:"type:varchar(10);default:'CNY'" json:"currency"`        // 原币种

	// 工作流
	Status       string     `gorm:"type:varchar(20);default:'pending';index" json:"status"` // pending/approved/issued/rejected/voided
	PDFURL       string     `gorm:"type:varchar(500)" json:"pdf_url,omitempty"`             // 开具后的 PDF 访问地址
	RejectReason string     `gorm:"type:varchar(500)" json:"reject_reason,omitempty"`
	IssuedAt     *time.Time `json:"issued_at,omitempty"`
	ApprovedBy   *uint      `gorm:"index" json:"approved_by,omitempty"` // 审批/开具的管理员 UserID
	Remark       string     `gorm:"type:varchar(500)" json:"remark,omitempty"`

	User User `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

// 发票申请状态
const (
	InvoiceStatusPending  = "pending"
	InvoiceStatusApproved = "approved"
	InvoiceStatusIssued   = "issued"
	InvoiceStatusRejected = "rejected"
	InvoiceStatusVoided   = "voided"
)

// 发票地区
const (
	InvoiceRegionCN       = "CN"
	InvoiceRegionOverseas = "OVERSEAS"
)

// 发票类型
const (
	InvoiceTypePersonal   = "personal"    // 个人普票（国内）/ Personal receipt
	InvoiceTypeCompany    = "company"     // 企业普票（国内）/ Business invoice
	InvoiceTypeVATInvoice = "vat_invoice" // 专票（国内增值税专用发票 / 海外 VAT invoice）
)

// Payment.InvoiceStatus 枚举
const (
	PaymentInvoiceStatusNone      = "none"
	PaymentInvoiceStatusRequested = "requested"
	PaymentInvoiceStatusIssued    = "issued"
	PaymentInvoiceStatusVoided    = "voided"
)

// TableName 指定发票申请表名
func (InvoiceRequest) TableName() string {
	return "invoice_requests"
}
