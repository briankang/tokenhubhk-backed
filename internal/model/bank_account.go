package model

import "time"

// BankAccount 对公银行账号
type BankAccount struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	AccountName   string    `gorm:"type:varchar(100);not null" json:"account_name"`   // 开户名称（公司名）
	BankName      string    `gorm:"type:varchar(100);not null" json:"bank_name"`      // 开户银行
	BranchName    string    `gorm:"type:varchar(200)" json:"branch_name"`             // 支行名称
	AccountNumber string    `gorm:"type:varchar(50);not null" json:"account_number"`  // 银行账号
	SwiftCode     string    `gorm:"type:varchar(20)" json:"swift_code"`               // SWIFT 代码
	Currency      string    `gorm:"type:varchar(10);default:'CNY'" json:"currency"`   // 币种
	Remark        string    `gorm:"type:varchar(500)" json:"remark"`                  // 备注
	IsActive      bool      `gorm:"default:true" json:"is_active"`
	SortOrder     int       `gorm:"default:0" json:"sort_order"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// TableName 指定表名
func (BankAccount) TableName() string {
	return "bank_accounts"
}
