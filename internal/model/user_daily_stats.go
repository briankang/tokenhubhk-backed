package model

import "time"

// UserDailyStat 用户调用日表聚合
// 来源：每日01:00从 api_call_logs 按 (user_id × request_model × date) 聚合写入
// 用途：提供用户维度的用量报表查询，解决 daily_stats 不含 user_id 维度的缺口
// 生命周期：永久保留（api_call_logs 仅保留7天，此表是持久化聚合）
type UserDailyStat struct {
	ID           uint      `gorm:"primarykey;autoIncrement"`
	Date         string    `gorm:"type:varchar(10);not null;uniqueIndex:uk_date_user_model,priority:1;index:idx_date"`
	UserID       uint      `gorm:"not null;uniqueIndex:uk_date_user_model,priority:2;index:idx_user_date,priority:1"`
	RequestModel string    `gorm:"type:varchar(255);not null;uniqueIndex:uk_date_user_model,priority:3"`
	RequestCount int64     `gorm:"default:0;not null"`
	SuccessCount int64     `gorm:"default:0;not null"`
	ErrorCount   int64     `gorm:"default:0;not null"`
	InputTokens  int64     `gorm:"default:0;not null"`
	OutputTokens int64     `gorm:"default:0;not null"`
	TotalTokens  int64     `gorm:"default:0;not null"`
	CostCredits  int64     `gorm:"default:0;not null"`
	AvgLatencyMs float64   `gorm:"default:0;not null"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (UserDailyStat) TableName() string { return "user_daily_stats" }
