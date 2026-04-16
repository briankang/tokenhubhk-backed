package model

import "time"

// ApiCallLog API 调用全链路日志，记录每次 API 请求的完整生命周期
// 与 channel_logs 通过 request_id 关联，支持全链路追踪
type ApiCallLog struct {
	ID        uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	CreatedAt time.Time `gorm:"index" json:"created_at"`

	// ── 请求标识 ──
	RequestID string `gorm:"type:varchar(64);uniqueIndex;not null" json:"request_id"` // 全局唯一请求ID（贯穿全链路）
	ParentID  string `gorm:"type:varchar(64);index" json:"parent_id,omitempty"`       // 父请求ID（重试时关联原始请求）

	// ── 调用方信息 ──
	UserID    uint   `gorm:"index;not null" json:"user_id"`
	TenantID  uint   `gorm:"index;not null" json:"tenant_id"`
	ApiKeyID  uint   `gorm:"index" json:"api_key_id"`          // 使用的 API Key ID
	ClientIP  string `gorm:"type:varchar(45)" json:"client_ip"` // 客户端 IP

	// ── 请求信息 ──
	Endpoint      string `gorm:"type:varchar(100);index" json:"endpoint"`         // 请求端点：/v1/chat/completions, /v1/completions
	RequestModel  string `gorm:"type:varchar(100);index" json:"request_model"`    // 用户请求的模型名
	ActualModel   string `gorm:"type:varchar(100)" json:"actual_model,omitempty"` // 实际路由到的模型名（别名映射后）
	IsStream      bool   `gorm:"default:false" json:"is_stream"`                  // 是否流式请求
	RequestBody   string `gorm:"type:text" json:"request_body,omitempty"`         // 完整请求 JSON（Authorization 头已移除；text 类型上限约 65KB）
	MessageCount  int    `gorm:"default:0" json:"message_count"`                  // messages 数组长度
	MaxTokens     int    `gorm:"default:0" json:"max_tokens"`                     // 请求的 max_tokens

	// ── 路由信息 ──
	ChannelID    uint   `gorm:"index" json:"channel_id"`                            // 最终使用的渠道 ID
	ChannelName  string `gorm:"type:varchar(100)" json:"channel_name,omitempty"`    // 渠道名称
	SupplierName string `gorm:"type:varchar(50)" json:"supplier_name,omitempty"`    // 供应商名称（OpenAI/Anthropic等）
	RetryCount   int    `gorm:"default:0" json:"retry_count"`                       // 重试次数
	RetryDetail  string `gorm:"type:text" json:"retry_detail,omitempty"`            // 重试详情 JSON: [{channel_id, error, latency_ms}]

	// ── 上游调用信息 ──
	UpstreamURL      string `gorm:"type:varchar(500)" json:"upstream_url,omitempty"`     // 上游请求 URL
	UpstreamStatus   int    `gorm:"default:0" json:"upstream_status"`                    // 上游 HTTP 状态码
	UpstreamLatencyMs int   `gorm:"default:0" json:"upstream_latency_ms"`                // 上游响应延迟（ms）

	// ── 响应信息 ──
	StatusCode     int    `gorm:"default:200;index" json:"status_code"`         // 返回给客户端的状态码
	PromptTokens   int    `gorm:"default:0" json:"prompt_tokens"`               // 输入 Token 数
	CompletionTokens int  `gorm:"default:0" json:"completion_tokens"`           // 输出 Token 数
	TotalTokens    int    `gorm:"default:0" json:"total_tokens"`                // 总 Token 数
	ResponseBody   string `gorm:"type:text" json:"response_body,omitempty"`     // 响应体摘要（截取前2000字符）
	ErrorMessage   string `gorm:"type:text" json:"error_message,omitempty"`     // 错误信息
	ErrorType      string `gorm:"type:varchar(50)" json:"error_type,omitempty"` // 错误类型：auth_error, rate_limit, upstream_error, timeout 等

	// ── 计费信息 ──
	CostCredits     int64   `gorm:"default:0" json:"cost_credits"`                // 消费积分
	CostRMB         float64 `gorm:"type:decimal(16,6);default:0" json:"cost_rmb"` // 消费人民币
	CacheReadTokens  int     `gorm:"default:0" json:"cache_read_tokens"`           // 缓存命中Token数（从供应商响应中解析）
	CacheWriteTokens int     `gorm:"default:0" json:"cache_write_tokens"`          // 缓存写入Token数（Anthropic cache_creation_input_tokens）
	CacheSavingsRMB  float64 `gorm:"type:decimal(16,6);default:0" json:"cache_savings_rmb"` // 因缓存节省的费用（相比全量输入价的差值）

	// ── 多计费单位：非 Token 量化字段（与 AIModel.PricingUnit 对应）──
	ImageCount  int     `gorm:"default:0" json:"image_count,omitempty"`                   // 图片生成张数（per_image）
	CharCount   int     `gorm:"default:0" json:"char_count,omitempty"`                    // 字符数（per_10k_characters / per_million_characters）
	DurationSec float64 `gorm:"type:decimal(12,3);default:0" json:"duration_sec,omitempty"` // 时长(秒)（per_second / per_minute / per_hour）
	CallCount   int     `gorm:"default:0" json:"call_count,omitempty"`                    // 调用次数（per_call）

	// ── 耗时分解 ──
	TotalLatencyMs  int `gorm:"default:0;index" json:"total_latency_ms"` // 总耗时（ms）
	RouteLatencyMs  int `gorm:"default:0" json:"route_latency_ms"`       // 路由选择耗时（ms）
	QueueLatencyMs  int `gorm:"default:0" json:"queue_latency_ms"`       // 排队等待耗时（ms）
	FirstTokenMs    int `gorm:"default:0" json:"first_token_ms"`         // 首 Token 延迟（流式）

	// ── 状态 ──
	Status string `gorm:"type:varchar(20);default:success;index" json:"status"` // success / error / timeout / rate_limited
}

// TableName 指定表名
func (ApiCallLog) TableName() string {
	return "api_call_logs"
}

// ApiCallRetryDetail 重试详情条目
type ApiCallRetryDetail struct {
	Attempt   int    `json:"attempt"`
	ChannelID uint   `json:"channel_id"`
	Error     string `json:"error"`
	LatencyMs int    `json:"latency_ms"`
}
