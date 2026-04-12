package model

// RateLimitConfig 限流限额配置模型，存储全局和用户级别的限流限额参数
// ConfigValue 使用 int64 存储数值配置（如请求数/秒）
type RateLimitConfig struct {
	BaseModel
	ConfigKey   string `gorm:"size:50;uniqueIndex;not null" json:"configKey"` // 配置键名，如 ip_rpm, user_rpm, global_qps
	ConfigValue int64  `gorm:"type:bigint;default:0" json:"configValue"`      // 配置数值（整数）
	Description string `gorm:"size:200" json:"description"`
	IsActive    bool   `gorm:"default:true" json:"isActive"`
}

// TableName 指定限流配置表名
func (RateLimitConfig) TableName() string {
	return "rate_limit_configs"
}

// UserQuotaConfig 用户级限额配置模型，存储单个用户的额度限制参数
// 采用双轨存储：积分(int64) + 人民币等值(float64)
type UserQuotaConfig struct {
	BaseModel
	UserID           uint    `gorm:"uniqueIndex;not null" json:"userId"`
	DailyLimit       int64   `gorm:"type:bigint;default:0" json:"dailyLimit"`          // 日限额（积分 credits），0=无限制
	DailyLimitRMB    float64 `gorm:"type:decimal(16,4);default:0" json:"dailyLimitRmb"` // 日限额（人民币）
	MonthlyLimit     int64   `gorm:"type:bigint;default:0" json:"monthlyLimit"`        // 月限额（积分 credits），0=无限制
	MonthlyLimitRMB  float64 `gorm:"type:decimal(16,4);default:0" json:"monthlyLimitRmb"` // 月限额（人民币）
	MaxTokensPerReq  int     `gorm:"default:32768" json:"maxTokensPerReq"`             // 单次请求最大Token数
	MaxConcurrent    int     `gorm:"default:5" json:"maxConcurrent"`                   // 最大并发请求数
	CustomRPM        int     `gorm:"default:0" json:"customRpm"`                       // 自定义每分钟请求数，0=使用全局默认
}

// TableName 指定用户限额配置表名
func (UserQuotaConfig) TableName() string {
	return "user_quota_configs"
}

// FreezeRecord 预扣费冻结记录模型，记录每次预扣费的冻结与结算状态
// 采用双轨存储：积分(int64) + 人民币等值(float64)
type FreezeRecord struct {
	BaseModel
	FreezeID       string  `gorm:"size:50;uniqueIndex;not null" json:"freezeId"`      // 冻结唯一标识（UUID）
	UserID         uint    `gorm:"index;not null" json:"userId"`
	TenantID       uint    `gorm:"index;not null" json:"tenantId"`
	FrozenAmount   int64   `gorm:"type:bigint;not null" json:"frozenAmount"`          // 预扣冻结金额（积分 credits）
	FrozenAmountRMB float64 `gorm:"type:decimal(16,4);default:0" json:"frozenAmountRmb"` // 预扣冻结金额（人民币）
	ActualCost     int64   `gorm:"type:bigint;default:0" json:"actualCost"`           // 实际消费金额（积分 credits）
	ActualCostRMB  float64 `gorm:"type:decimal(16,4);default:0" json:"actualCostRmb"` // 实际消费金额（人民币）
	Status         string  `gorm:"size:20;default:FROZEN;index" json:"status"`        // FROZEN / SETTLED / RELEASED
	ModelName      string  `gorm:"size:100" json:"modelName"`
	RequestID      string  `gorm:"size:50" json:"requestId"`
	Remark         string  `gorm:"size:200" json:"remark"`
}

// TableName 指定冻结记录表名
func (FreezeRecord) TableName() string {
	return "freeze_records"
}
