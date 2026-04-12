package model

import (
	"time"
)

// AgentLevel 代理等级配置表 - 基于销售驱动的代理分销等级体系
// 等级从 A0（推广员）到 A4（铂金代理），每个等级拥有不同的佣金比例和权益
// 采用双轨存储：积分门槛(int64) + 人民币等值(float64)，佣金比例为 float64
type AgentLevel struct {
	ID                  uint    `gorm:"primaryKey" json:"id"`
	LevelCode           string  `gorm:"size:10;uniqueIndex;not null" json:"level_code"`                      // 等级编码: A0/A1/A2/A3/A4
	LevelName           string  `gorm:"size:50;not null" json:"level_name"`                                  // 等级名称: 推广员/青铜代理/白银代理/黄金代理/铂金代理
	Rank                int     `gorm:"column:level_rank;not null;default:0" json:"rank"`                    // 排序等级 0-4，数值越大等级越高
	MinMonthlySales     int64   `gorm:"type:bigint;not null;default:0" json:"min_monthly_sales"`             // 团队月销售额门槛（积分 credits）
	MinMonthlySalesRMB  float64 `gorm:"type:decimal(16,4);default:0" json:"min_monthly_sales_rmb"`           // 团队月销售额门槛（人民币）
	MinDirectSubs       int     `gorm:"not null;default:0" json:"min_direct_subs"`                           // 最少直推人数门槛
	DirectCommission    float64 `gorm:"type:decimal(5,4);not null;default:0.05" json:"direct_commission"`    // 直推佣金比例（如 0.05 = 5%）
	L2Commission        float64 `gorm:"type:decimal(5,4);not null;default:0" json:"l2_commission"`           // 二级佣金比例
	L3Commission        float64 `gorm:"type:decimal(5,4);not null;default:0" json:"l3_commission"`           // 三级佣金比例
	Benefits            string  `gorm:"type:text" json:"benefits"`                                           // 等级权益 JSON 字符串（如: 独立子站、API折扣、专属客服等）
	DegradeMonths       int     `gorm:"not null;default:2" json:"degrade_months"`                            // 连续不达标月数触发降级
	IsActive            bool    `gorm:"not null;default:true" json:"is_active"`                              // 是否启用
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// UserAgentProfile 用户代理档案 - 记录用户的代理身份和业绩数据
// 每个代理用户一条记录，存储销售业绩、下线数、收益等核心数据
// 采用双轨存储：销售额积分(int64) + 人民币等值(float64)
type UserAgentProfile struct {
	ID                    uint       `gorm:"primaryKey" json:"id"`
	UserID                uint       `gorm:"uniqueIndex;not null" json:"user_id"`                                  // 关联用户ID，一对一
	AgentLevelID          uint       `gorm:"not null" json:"agent_level_id"`                                       // 当前代理等级ID
	AgentLevel            AgentLevel `gorm:"foreignKey:AgentLevelID" json:"agent_level"`                           // 关联代理等级配置
	Status                string     `gorm:"size:20;not null;default:'PENDING'" json:"status"`                     // 代理状态: PENDING(待审)/ACTIVE(激活)/SUSPENDED(冻结)/DEGRADED(降级)
	AppliedAt             time.Time  `gorm:"not null" json:"applied_at"`                                           // 申请时间
	ApprovedAt            *time.Time `json:"approved_at"`                                                          // 审核通过时间
	ApprovedBy            uint       `gorm:"default:0" json:"approved_by"`                                         // 审核人ID
	CurrentMonthSales     int64      `gorm:"type:bigint;not null;default:0" json:"current_month_sales"`            // 当月团队销售额（积分 credits）
	CurrentMonthSalesRMB  float64    `gorm:"type:decimal(16,4);default:0" json:"current_month_sales_rmb"`          // 当月团队销售额（人民币）
	LastMonthSales        int64      `gorm:"type:bigint;not null;default:0" json:"last_month_sales"`               // 上月团队销售额（积分 credits）
	LastMonthSalesRMB     float64    `gorm:"type:decimal(16,4);default:0" json:"last_month_sales_rmb"`             // 上月团队销售额（人民币）
	DirectSubsCount       int        `gorm:"not null;default:0" json:"direct_subs_count"`                          // 直推下线人数
	TotalEarnings         float64    `gorm:"type:decimal(16,6);not null;default:0" json:"total_earnings"`          // 累计佣金收益（人民币）
	WithdrawnAmount       float64    `gorm:"type:decimal(16,6);not null;default:0" json:"withdrawn_amount"`        // 已提现金额（人民币）
	LastDegradeCheck      *time.Time `json:"last_degrade_check"`                                                   // 上次降级检查时间
	DegradeWarnings       int        `gorm:"not null;default:0" json:"degrade_warnings"`                           // 连续不达标月数计数器
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

// WithdrawalRequest 提现申请记录 - 代理用户发起的佣金提现请求
// 包含审核流程: PENDING → APPROVED → COMPLETED 或 PENDING → REJECTED
type WithdrawalRequest struct {
	ID          uint    `gorm:"primaryKey" json:"id"`
	UserID      uint    `gorm:"index;not null" json:"user_id"`                              // 申请人用户ID
	Amount      float64 `gorm:"type:decimal(16,6);not null" json:"amount"`                  // 提现金额（元）
	Status      string  `gorm:"size:20;not null;default:'PENDING'" json:"status"`           // 状态: PENDING/APPROVED/REJECTED/COMPLETED
	BankInfo    string  `gorm:"size:500" json:"bank_info"`                                   // 收款信息（加密存储，含银行卡号、户名等）
	AdminID     uint    `gorm:"default:0" json:"admin_id"`                                   // 审核人管理员ID
	AdminRemark string  `gorm:"size:500" json:"admin_remark"`                                // 审核备注
	ProcessedAt *time.Time `json:"processed_at"`                                             // 处理完成时间
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}
