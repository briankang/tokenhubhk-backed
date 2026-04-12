package model

// Payment 支付订单模型，记录一笔财务交易
// 支持多币种支付和汇率换算，最终转换为积分充值
type Payment struct {
	BaseModel
	TenantID         uint    `gorm:"index;not null" json:"tenant_id"`                          // 租户 ID
	UserID           uint    `gorm:"index;not null" json:"user_id"`                            // 用户 ID
	Amount           float64 `gorm:"type:decimal(12,2);not null" json:"amount"`                 // 原始支付金额
	OriginalCurrency string  `gorm:"type:varchar(10);default:'CNY'" json:"original_currency"`   // 原始支付币种
	ExchangeRate     float64 `gorm:"type:decimal(16,8);default:1" json:"exchange_rate"`         // 当时汇率（外币→CNY）
	FeeRate          float64 `gorm:"type:decimal(8,4);default:0" json:"fee_rate"`               // 手续费比例
	FeeAmount        float64 `gorm:"type:decimal(16,4);default:0" json:"fee_amount"`            // 手续费金额(RMB)
	RMBAmount        float64 `gorm:"type:decimal(16,4);default:0" json:"rmb_amount"`            // 换汇后人民币净额
	CreditAmount     int64   `gorm:"type:bigint;default:0" json:"credit_amount"`               // 兑换积分数量
	Currency         string  `gorm:"type:varchar(10);default:'CNY'" json:"currency"`            // 显示币种（兼容字段）
	Gateway          string  `gorm:"type:varchar(20);not null;index" json:"gateway"`            // 支付网关: wechat/alipay/stripe/paypal
	GatewayTxnID     string  `gorm:"type:varchar(200);index" json:"gateway_txn_id,omitempty"`   // 网关交易号
	Status           string  `gorm:"type:varchar(20);default:'pending';index" json:"status"`    // 状态: pending/completed/failed/refunded
	Metadata         JSON    `gorm:"type:json" json:"metadata,omitempty"`                      // 元数据 (JSON)

	Tenant Tenant `gorm:"foreignKey:TenantID" json:"tenant,omitempty"` // 关联租户
	User   User   `gorm:"foreignKey:UserID" json:"user,omitempty"`     // 关联用户
}

// TableName 指定支付订单表名
func (Payment) TableName() string {
	return "payments"
}
