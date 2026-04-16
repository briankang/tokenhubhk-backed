package model

import (
	"time"
)

// MemberLevel 会员等级配置表 - 基于消费驱动的自动升降级体系
// 等级从 V0（普通用户）到 V4（钻石会员），每个等级拥有不同的折扣率和限流配置
// 采用双轨存储：积分(int64) + 人民币等值(float64)，折扣率为 float64
type MemberLevel struct {
	ID                  uint    `gorm:"primaryKey" json:"id"`
	LevelCode           string  `gorm:"size:10;uniqueIndex;not null" json:"level_code"`                    // 等级编码: V0/V1/V2/V3/V4
	LevelName           string  `gorm:"size:50;not null" json:"level_name"`                                // 等级名称: 普通用户/银牌会员/金牌会员/铂金会员/钻石会员
	Rank                int     `gorm:"column:level_rank;not null;default:0" json:"rank"`                  // 排序等级 0-4，数值越大等级越高
	MinTotalConsume     int64   `gorm:"type:bigint;not null;default:0" json:"min_total_consume"`           // 累计消费门槛（积分 credits）
	MinTotalConsumeRMB  float64 `gorm:"type:decimal(16,4);default:0" json:"min_total_consume_rmb"`         // 累计消费门槛（人民币）
	ModelDiscount       float64 `gorm:"type:decimal(5,2);not null;default:1.00" json:"model_discount"`     // 模型调用折扣率（0.80=8折，1.00=无折扣）
	DefaultRPM          int     `gorm:"not null;default:60" json:"default_rpm"`                            // 每分钟请求数默认值（Requests Per Minute）
	DefaultTPM          int     `gorm:"not null;default:100000" json:"default_tpm"`                        // 每分钟最大Token数默认值（Tokens Per Minute）
	DegradeMonths       int     `gorm:"not null;default:3" json:"degrade_months"`                          // 连续不达标月数触发降级
	IsActive            bool    `gorm:"not null;default:true" json:"is_active"`                            // 是否启用
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// UserMemberProfile 用户会员档案 - 记录用户的会员等级和消费统计
// 每个用户一条记录，通过 UserID 唯一关联，存储滚动消费数据用于升降级判断
type UserMemberProfile struct {
	ID               uint        `gorm:"primaryKey" json:"id"`
	UserID           uint        `gorm:"uniqueIndex;not null" json:"user_id"`                              // 关联用户ID，一对一
	MemberLevelID    uint        `gorm:"not null" json:"member_level_id"`                                  // 当前会员等级ID
	MemberLevel      MemberLevel `gorm:"foreignKey:MemberLevelID" json:"member_level"`                    // 关联会员等级配置
	TotalConsume     float64     `gorm:"type:decimal(16,6);not null;default:0" json:"total_consume"`      // 累计消费总额（冗余字段，加速查询）
	MonthConsume1    float64     `gorm:"type:decimal(16,6);not null;default:0" json:"month_consume_1"`    // 最近第1个月消费额
	MonthConsume2    float64     `gorm:"type:decimal(16,6);not null;default:0" json:"month_consume_2"`    // 最近第2个月消费额
	MonthConsume3    float64     `gorm:"type:decimal(16,6);not null;default:0" json:"month_consume_3"`    // 最近第3个月消费额
	LastGiftAt       *time.Time  `json:"last_gift_at"`                                                     // 上次发放月度赠送额度时间
	LastDegradeCheck *time.Time  `json:"last_degrade_check"`                                               // 上次降级检查时间
	DegradeWarnings  int         `gorm:"not null;default:0" json:"degrade_warnings"`                      // 连续不达标月数计数器
	CreatedAt        time.Time   `json:"created_at"`
	UpdatedAt        time.Time   `json:"updated_at"`
}
