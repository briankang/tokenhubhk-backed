package model

// QuotaConfig 全局额度配置，控制用户注册赠送额度
// 采用积分(int64)存储，1 RMB = 10,000 credits
type QuotaConfig struct {
	BaseModel
	DefaultFreeQuota  int64  `gorm:"type:bigint;default:10000" json:"defaultFreeQuota"`  // 注册赠送额度(积分 credits)，默认1元=10000积分
	RegistrationBonus int64  `gorm:"type:bigint;default:0" json:"registrationBonus"`     // 额外注册奖励(积分)
	IsActive          bool   `gorm:"default:true" json:"isActive"`
	Description       string `gorm:"size:200" json:"description"`
}

// TableName 指定额度配置表名
func (QuotaConfig) TableName() string {
	return "quota_configs"
}
