package model

import "time"

// ModelCheckLog 模型可用性检测记录
// 每次批量检测时为每个模型写入一条记录
type ModelCheckLog struct {
	BaseModel
	ModelID      uint      `gorm:"index;not null" json:"model_id"`             // 关联 ai_models.id
	ModelName    string    `gorm:"type:varchar(100);index" json:"model_name"`  // 模型名称（冗余，方便查询）
	ChannelID    uint      `gorm:"index" json:"channel_id"`                    // 使用的渠道 ID
	Available    bool      `gorm:"not null" json:"available"`                  // 是否可用
	LatencyMs    int64     `json:"latency_ms"`                                 // 响应延迟(ms)
	StatusCode   int       `json:"status_code"`                                // HTTP 状态码
	Error        string    `gorm:"type:text" json:"error,omitempty"`           // 错误信息
	CheckedAt    time.Time `gorm:"index;not null" json:"checked_at"`           // 检测时间
	AutoDisabled bool      `gorm:"default:false" json:"auto_disabled"`         // 是否被自动停用

	// --- 2026-04-15 新增：错误分类 + 上游对照 + 观察窗口 ---
	ErrorCategory       string `gorm:"type:varchar(30);index" json:"error_category,omitempty"`
	// 错误分类: model_not_found/auth_error/permission_denied/rate_limited/quota_exhausted/
	//          timeout/connection_error/invalid_request/image_not_supported/no_route/
	//          server_error/skipped/unknown
	UpstreamStatus      string `gorm:"type:varchar(30);index" json:"upstream_status,omitempty"`
	// 上游清单对照: deprecated_upstream(官网已下架) / upstream_active(官网仍存在) /
	//              unknown(上游API不返回该类型或清单拉取失败) / manual_override(管理员手动重新上线)
	ConsecutiveFailures int    `gorm:"default:0" json:"consecutive_failures"`
	// 本次失败时的连续失败次数快照（含本次）。若本次 available=true 则为 0
}

func (ModelCheckLog) TableName() string {
	return "model_check_logs"
}
