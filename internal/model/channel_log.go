package model

import "time"

// ChannelLog 渠道请求日志，记录每次通过渠道路由的 API 请求
type ChannelLog struct {
	BaseModel
	ChannelID      uint      `gorm:"index;not null" json:"channel_id"`        // 渠道 ID
	ModelName      string    `gorm:"type:varchar(100);index" json:"model_name"` // 模型名称
	TenantID       uint      `gorm:"index;not null" json:"tenant_id"`         // 租户 ID
	UserID         uint      `gorm:"index" json:"user_id"`                    // 用户 ID
	ApiKeyID       uint      `gorm:"index" json:"api_key_id"`                 // API Key ID
	AgentLevel     int       `gorm:"default:0" json:"agent_level"`            // 代理层级
	AgentTenantID  uint      `gorm:"index" json:"agent_tenant_id"`            // 代理租户 ID
	RequestTokens    int `gorm:"default:0" json:"request_tokens"`    // 请求 Token 数
	ResponseTokens   int `gorm:"default:0" json:"response_tokens"`   // 响应 Token 数
	CacheReadTokens  int `gorm:"default:0" json:"cache_read_tokens"`  // 缓存命中 Token 数
	CacheWriteTokens int `gorm:"default:0" json:"cache_write_tokens"` // 缓存写入 Token 数
	LatencyMs      int       `gorm:"default:0" json:"latency_ms"`             // 耗时 (ms)
	StatusCode     int       `gorm:"default:200" json:"status_code"`           // HTTP 状态码
	ErrorMessage   string    `gorm:"type:text" json:"error_message,omitempty"` // 错误信息
	RequestID      string    `gorm:"type:varchar(64);index" json:"request_id"` // 请求 ID（全链路追踪）
	CreatedAt      time.Time `gorm:"index" json:"created_at"`                  // 创建时间
}

// TableName 指定渠道日志表名
func (ChannelLog) TableName() string {
	return "channel_logs"
}
