package model

import "time"

// CommissionRecord 佣金记录(v3.1 扩展:归因关联 + 加佣覆盖 + 结算追踪)
// 采用双轨存储:积分(int64) + 人民币等值(float64)
type CommissionRecord struct {
	BaseModel
	TenantID            uint    `gorm:"index;not null" json:"tenantId"`
	UserID              uint    `gorm:"index;not null" json:"userId"`              // 佣金接收者(邀请人)
	SourceUserID        uint    `gorm:"index" json:"sourceUserId"`                 // 消费用户(被邀请人)
	SourceTenantID      uint    `gorm:"index" json:"sourceTenantId"`
	OrderAmount         int64   `gorm:"type:bigint;default:0" json:"orderAmount"`  // 原始消费金额(积分 credits)
	OrderAmountRMB      float64 `gorm:"type:decimal(16,4);default:0" json:"orderAmountRmb"`  // 原始消费金额(人民币)
	CommissionRate      float64 `gorm:"type:decimal(5,4)" json:"commissionRate"`   // 佣金比例(与 EffectiveRate 一致,保留向后兼容)
	CommissionAmount    int64   `gorm:"type:bigint;default:0" json:"commissionAmount"`       // 佣金金额(积分 credits)
	CommissionAmountRMB float64 `gorm:"type:decimal(16,4);default:0" json:"commissionAmountRmb"` // 佣金金额(人民币)
	Type                string  `gorm:"size:20;not null" json:"type"`              // REFERRAL / REFERRAL_OVERRIDE
	Status              string  `gorm:"size:20;default:PENDING;index" json:"status"` // PENDING / SETTLED / WITHDRAWN / REFUNDED
	RelatedID           string  `gorm:"size:64;index" json:"relatedId"`            // 关联订单/请求 ID(用于退款冲销)

	// --- v3.1 新增字段 ---
	AttributionID *uint      `gorm:"index" json:"attributionId"`                  // 关联 ReferralAttribution.ID
	OverrideID    *uint      `gorm:"index" json:"overrideId"`                     // 关联 UserCommissionOverride.ID,NULL=用默认比例
	EffectiveRate float64    `gorm:"type:decimal(5,4);default:0" json:"effectiveRate"` // 实际生效比例(审计用,记录当时的 rate)
	Credited      bool       `gorm:"default:false;index" json:"credited"`         // 是否已入账到用户余额
	SettleAt      *time.Time `json:"settleAt"`                                    // SETTLED 时间
}

// TableName 指定佣金记录表名
func (CommissionRecord) TableName() string {
	return "commission_records"
}
