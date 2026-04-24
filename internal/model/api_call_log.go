package model

import "time"

// ApiCallLog records one client-facing API request and links it to channel logs by request_id.
type ApiCallLog struct {
	ID        uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	CreatedAt time.Time `gorm:"index" json:"created_at"`

	RequestID string `gorm:"type:varchar(64);uniqueIndex;not null" json:"request_id"`
	ParentID  string `gorm:"type:varchar(64);index" json:"parent_id,omitempty"`

	UserID   uint   `gorm:"index;not null" json:"user_id"`
	TenantID uint   `gorm:"index;not null" json:"tenant_id"`
	ApiKeyID uint   `gorm:"index" json:"api_key_id"`
	ClientIP string `gorm:"type:varchar(45)" json:"client_ip"`

	Endpoint     string `gorm:"type:varchar(100);index" json:"endpoint"`
	RequestModel string `gorm:"type:varchar(100);index" json:"request_model"`
	ActualModel  string `gorm:"type:varchar(100)" json:"actual_model,omitempty"`
	IsStream     bool   `gorm:"default:false" json:"is_stream"`
	RequestBody  string `gorm:"type:text" json:"request_body,omitempty"`
	MessageCount int    `gorm:"default:0" json:"message_count"`
	MaxTokens    int    `gorm:"default:0" json:"max_tokens"`

	ChannelID    uint   `gorm:"index" json:"channel_id"`
	ChannelName  string `gorm:"type:varchar(100)" json:"channel_name,omitempty"`
	SupplierName string `gorm:"type:varchar(50)" json:"supplier_name,omitempty"`
	RetryCount   int    `gorm:"default:0" json:"retry_count"`
	RetryDetail  string `gorm:"type:text" json:"retry_detail,omitempty"`

	UpstreamURL       string `gorm:"type:varchar(500)" json:"upstream_url,omitempty"`
	UpstreamStatus    int    `gorm:"default:0" json:"upstream_status"`
	UpstreamLatencyMs int    `gorm:"default:0" json:"upstream_latency_ms"`

	StatusCode       int    `gorm:"default:200;index" json:"status_code"`
	PromptTokens     int    `gorm:"default:0" json:"prompt_tokens"`
	CompletionTokens int    `gorm:"default:0" json:"completion_tokens"`
	TotalTokens      int    `gorm:"default:0" json:"total_tokens"`
	ResponseBody     string `gorm:"type:text" json:"response_body,omitempty"`
	ErrorMessage     string `gorm:"type:text" json:"error_message,omitempty"`
	ErrorType        string `gorm:"type:varchar(50)" json:"error_type,omitempty"`

	CostCredits           int64   `gorm:"default:0" json:"cost_credits"`
	CostUnits             int64   `gorm:"default:0" json:"cost_units"`
	CostRMB               float64 `gorm:"type:decimal(16,6);default:0" json:"cost_rmb"`
	EstimatedCostCredits  int64   `gorm:"default:0" json:"estimated_cost_credits"`
	EstimatedCostUnits    int64   `gorm:"default:0" json:"estimated_cost_units"`
	FrozenCredits         int64   `gorm:"default:0" json:"frozen_credits"`
	FrozenUnits           int64   `gorm:"default:0" json:"frozen_units"`
	ActualCostCredits     int64   `gorm:"default:0" json:"actual_cost_credits"`
	ActualCostUnits       int64   `gorm:"default:0" json:"actual_cost_units"`
	PlatformCostRMB       float64 `gorm:"type:decimal(16,6);default:0" json:"platform_cost_rmb"`
	PlatformCostUnits     int64   `gorm:"default:0" json:"platform_cost_units"`
	BillingStatus         string  `gorm:"type:varchar(32);default:settled;index" json:"billing_status"`
	UsageSource           string  `gorm:"type:varchar(32);default:provider" json:"usage_source"`
	UsageEstimated        bool    `gorm:"default:false" json:"usage_estimated"`
	UnderCollectedCredits int64   `gorm:"default:0" json:"under_collected_credits"`
	UnderCollectedUnits   int64   `gorm:"default:0" json:"under_collected_units"`
	CacheReadTokens       int     `gorm:"default:0" json:"cache_read_tokens"`
	CacheWriteTokens      int     `gorm:"default:0" json:"cache_write_tokens"`
	CacheSavingsRMB       float64 `gorm:"type:decimal(16,6);default:0" json:"cache_savings_rmb"`
	BillingSnapshot       JSON    `gorm:"type:json" json:"billing_snapshot,omitempty"`
	MatchedPriceTier      string  `gorm:"type:varchar(64);index" json:"matched_price_tier,omitempty"`
	MatchedPriceTierIdx   int     `gorm:"default:-1" json:"matched_price_tier_idx"`
	ThinkingMode          bool    `gorm:"default:false;index" json:"thinking_mode"`

	UserDiscountID   *uint    `gorm:"index" json:"user_discount_id,omitempty"`
	UserDiscountRate *float64 `gorm:"type:decimal(6,4)" json:"user_discount_rate,omitempty"`
	UserDiscountType string   `gorm:"type:varchar(20)" json:"user_discount_type,omitempty"`

	ImageCount  int     `gorm:"default:0" json:"image_count,omitempty"`
	CharCount   int     `gorm:"default:0" json:"char_count,omitempty"`
	DurationSec float64 `gorm:"type:decimal(12,3);default:0" json:"duration_sec,omitempty"`
	CallCount   int     `gorm:"default:0" json:"call_count,omitempty"`

	TotalLatencyMs int `gorm:"default:0;index" json:"total_latency_ms"`
	RouteLatencyMs int `gorm:"default:0" json:"route_latency_ms"`
	QueueLatencyMs int `gorm:"default:0" json:"queue_latency_ms"`
	FirstTokenMs   int `gorm:"default:0" json:"first_token_ms"`

	Status string `gorm:"type:varchar(20);default:success;index" json:"status"`
}

func (ApiCallLog) TableName() string {
	return "api_call_logs"
}

// ApiCallRetryDetail stores one retry attempt for a client API call.
type ApiCallRetryDetail struct {
	Attempt   int    `json:"attempt"`
	ChannelID uint   `json:"channel_id"`
	Error     string `json:"error"`
	LatencyMs int    `json:"latency_ms"`
}
