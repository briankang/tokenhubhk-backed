package model

// DailyStats 每日统计聚合数据，按租户/模型/渠道维度汇总
type DailyStats struct {
	BaseModel
	Date              string  `gorm:"type:varchar(10);index;not null" json:"date"`       // 日期 (2024-01-01)
	TenantID          uint    `gorm:"index;not null" json:"tenant_id"`                  // 租户 ID
	ModelID           uint    `gorm:"index" json:"model_id"`                            // 模型 ID
	ChannelID         uint    `gorm:"index" json:"channel_id"`                          // 渠道 ID
	AgentLevel        int     `gorm:"default:0" json:"agent_level"`                     // 代理层级
	TotalRequests     int64   `gorm:"default:0" json:"total_requests"`                  // 总请求数
	TotalInputTokens  int64   `gorm:"default:0" json:"total_input_tokens"`              // 总输入 Token
	TotalOutputTokens int64   `gorm:"default:0" json:"total_output_tokens"`             // 总输出 Token
	TotalCost         float64 `gorm:"type:decimal(20,6);default:0" json:"total_cost"`    // 总成本
	TotalRevenue      float64 `gorm:"type:decimal(20,6);default:0" json:"total_revenue"` // 总收入
	AvgLatencyMs      float64 `gorm:"type:decimal(10,2);default:0" json:"avg_latency_ms"` // 平均耗时 (ms)
	ErrorCount        int64   `gorm:"default:0" json:"error_count"`                     // 错误数
}

// TableName 指定每日统计表名
func (DailyStats) TableName() string {
	return "daily_stats"
}
