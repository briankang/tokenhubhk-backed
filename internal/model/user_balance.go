package model

// UserBalance 用户余额/额度模型
// 采用双轨存储：积分(credits/int64) + 人民币等值(float64/decimal)
// 1 RMB = 10,000 credits，积分用于精确计算，人民币用于展示
type UserBalance struct {
	BaseModel
	TenantID         uint    `gorm:"index;not null" json:"tenantId"`
	UserID           uint    `gorm:"uniqueIndex;not null" json:"userId"`
	Balance          int64   `gorm:"type:bigint;default:0" json:"balance"`           // 当前可用余额（积分 credits）
	BalanceRMB       float64 `gorm:"type:decimal(16,4);default:0" json:"balanceRmb"` // 余额等值人民币
	FreeQuota        int64   `gorm:"type:bigint;default:0" json:"freeQuota"`         // 赠送体验额度（积分 credits）
	FreeQuotaRMB     float64 `gorm:"type:decimal(16,4);default:0" json:"freeQuotaRmb"` // 赠送额度等值人民币
	TotalConsumed    int64   `gorm:"type:bigint;default:0" json:"totalConsumed"`     // 累计消费（积分 credits）
	TotalConsumedRMB float64 `gorm:"type:decimal(16,4);default:0" json:"totalConsumedRmb"` // 累计消费等值人民币
	FrozenAmount     int64   `gorm:"type:bigint;default:0" json:"frozenAmount"`      // 冻结金额（积分 credits）
	Currency         string  `gorm:"size:10;default:CREDIT" json:"currency"`         // 币种统一为 CREDIT

	User   User   `gorm:"foreignKey:UserID" json:"-"`
	Tenant Tenant `gorm:"foreignKey:TenantID" json:"-"`
}

// TableName 指定用户余额表名
func (UserBalance) TableName() string {
	return "user_balances"
}
