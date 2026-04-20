package model

// BalanceRecord 余额变动记录，记录每次余额变化
// 采用双轨存储：积分变动(int64) + 人民币等值(float64)
type BalanceRecord struct {
	BaseModel
	UserID         uint    `gorm:"index;not null" json:"userId"`
	TenantID       uint    `gorm:"index;not null" json:"tenantId"`
	Type           string  `gorm:"size:20;not null;index:idx_type" json:"type"` // RECHARGE/CONSUME/GIFT/INVITEE_BONUS/INVITER_BONUS/REFUND/ADMIN_ADJUST
	Amount         int64   `gorm:"type:bigint;not null" json:"amount"`            // 变动积分（正数增加,负数减少）
	AmountRMB      float64 `gorm:"type:decimal(16,4);default:0" json:"amountRmb"` // 变动等值人民币
	BeforeBalance  int64   `gorm:"type:bigint" json:"beforeBalance"`              // 变动前余额（积分）
	AfterBalance   int64   `gorm:"type:bigint" json:"afterBalance"`               // 变动后余额（积分）
	Remark         string  `gorm:"size:200" json:"remark"`
	RelatedID      string  `gorm:"size:50" json:"relatedId,omitempty"`            // 关联订单/请求ID
}

// TableName 指定余额记录表名
func (BalanceRecord) TableName() string {
	return "balance_records"
}
