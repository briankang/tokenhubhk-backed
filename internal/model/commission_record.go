package model

// CommissionRecord 佣金记录，存储由邀请消费产生的佣金明细
// 采用双轨存储：积分(int64) + 人民币等值(float64)
type CommissionRecord struct {
	BaseModel
	TenantID            uint    `gorm:"index;not null" json:"tenantId"`
	UserID              uint    `gorm:"index;not null" json:"userId"`              // 佣金接收者
	SourceUserID        uint    `gorm:"index" json:"sourceUserId"`                 // 消费用户
	SourceTenantID      uint    `gorm:"index" json:"sourceTenantId"`
	OrderAmount         int64   `gorm:"type:bigint;default:0" json:"orderAmount"`  // 原始消费金额（积分 credits）
	OrderAmountRMB      float64 `gorm:"type:decimal(16,4);default:0" json:"orderAmountRmb"`  // 原始消费金额（人民币）
	CommissionRate      float64 `gorm:"type:decimal(5,4)" json:"commissionRate"`   // 佣金比例
	CommissionAmount    int64   `gorm:"type:bigint;default:0" json:"commissionAmount"`       // 佣金金额（积分 credits）
	CommissionAmountRMB float64 `gorm:"type:decimal(16,4);default:0" json:"commissionAmountRmb"` // 佣金金额（人民币）
	Type                string  `gorm:"size:20;not null" json:"type"`              // REFERRAL/L1/L2/L3
	Status              string  `gorm:"size:20;default:PENDING" json:"status"`     // PENDING/SETTLED/WITHDRAWN
}

// TableName 指定佣金记录表名
func (CommissionRecord) TableName() string {
	return "commission_records"
}
