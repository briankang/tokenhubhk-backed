package model

import "time"

// PaymentRefundRequest 用户发起的退款申请
// 工作流：pending → approved → processing → completed | failed
//                       └─ rejected
type PaymentRefundRequest struct {
	ID              uint64     `gorm:"primaryKey" json:"id"`
	PaymentID       uint64     `gorm:"not null;index" json:"payment_id"`
	UserID          uint64     `gorm:"not null;index" json:"user_id"`
	TenantID        uint64     `gorm:"index" json:"tenant_id"`
	OrderNo         string     `gorm:"type:varchar(64);not null;index" json:"order_no"`
	RefundAmountRMB float64    `gorm:"type:decimal(16,2);not null" json:"refund_amount_rmb"` // 退款金额(RMB)
	Reason          string     `gorm:"type:varchar(500);not null" json:"reason"`              // 用户填写的原因
	Attachments     string     `gorm:"type:text" json:"attachments,omitempty"`                // JSON 数组（图片URL 列表）
	Status          string     `gorm:"type:varchar(20);not null;index" json:"status"`         // pending/approved/rejected/processing/completed/failed
	AdminID         *uint64    `gorm:"index" json:"admin_id,omitempty"`
	AdminRemark     string     `gorm:"type:varchar(500)" json:"admin_remark,omitempty"`
	GatewayRefundID string     `gorm:"type:varchar(128)" json:"gateway_refund_id,omitempty"`  // 网关返回的退款单号
	GatewayResponse string     `gorm:"type:text" json:"gateway_response,omitempty"`           // 网关响应原文
	ProcessedAt     *time.Time `json:"processed_at,omitempty"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	CreatedAt       time.Time  `gorm:"index" json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// TableName 指定退款申请表名
func (PaymentRefundRequest) TableName() string {
	return "payment_refund_requests"
}

// 退款申请状态枚举
const (
	RefundStatusPending    = "pending"
	RefundStatusApproved   = "approved"
	RefundStatusRejected   = "rejected"
	RefundStatusProcessing = "processing"
	RefundStatusCompleted  = "completed"
	RefundStatusFailed     = "failed"
)
