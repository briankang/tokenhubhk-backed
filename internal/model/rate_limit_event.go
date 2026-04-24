package model

import "time"

// RateLimitEvent 限流 429 触发事件审计
// 字段说明：
//
//	SubjectType — ip / user / apikey / member / strict / tpm / global
//	SubjectID   — 对应维度的标识（IP 字符串 / userID / apiKeyID）
//	Rule        — 触发的规则名（如 sliding_60s / strict_login / apikey_anomaly）
//	Limit       — 阈值（每窗口允许请求数）
//	WindowSec   — 窗口长度（秒）
//	Path/Method — 触发的 HTTP 路径和方法
type RateLimitEvent struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	SubjectType string    `gorm:"size:16;index:idx_subject" json:"subject_type"`
	SubjectID   string    `gorm:"size:128;index:idx_subject" json:"subject_id"`
	Rule        string    `gorm:"size:64" json:"rule"`
	LimitVal    int       `gorm:"column:limit_val" json:"limit"`
	WindowSec   int       `json:"window_seconds"`
	Path        string    `gorm:"size:256;index" json:"path"`
	Method      string    `gorm:"size:10" json:"method"`
	UserAgent   string    `gorm:"size:512" json:"user_agent"`
	RequestID   string    `gorm:"size:64;index" json:"request_id"`
	CreatedAt   time.Time `gorm:"index" json:"created_at"`
}

// TableName 返回表名
func (RateLimitEvent) TableName() string {
	return "rate_limit_events"
}

// RateLimitEventQuery 分页查询条件
type RateLimitEventQuery struct {
	SubjectType string
	SubjectID   string
	Path        string
	StartDate   time.Time
	EndDate     time.Time
	Page        int
	PageSize    int
}
