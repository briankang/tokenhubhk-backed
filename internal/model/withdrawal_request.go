package model

import "time"

// WithdrawalRequest 提现申请记录 - v3.1 后直接由 user 发起,不再关联代理 profile
// 审核流程: PENDING → APPROVED → COMPLETED 或 PENDING → REJECTED
// 金额存储为元 (decimal 16,6),对应 credits 通过 credits.RMBToCredits/CreditsToRMB 转换
type WithdrawalRequest struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	UserID      uint       `gorm:"index;not null" json:"user_id"`
	Amount      float64    `gorm:"type:decimal(16,6);not null" json:"amount"`
	Status      string     `gorm:"size:20;not null;default:'PENDING'" json:"status"`
	BankInfo    string     `gorm:"size:500" json:"bank_info"`
	AdminID     uint       `gorm:"default:0" json:"admin_id"`
	AdminRemark string     `gorm:"size:500" json:"admin_remark"`
	ProcessedAt *time.Time `json:"processed_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}
