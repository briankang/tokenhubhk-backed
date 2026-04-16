// Package taskqueue 实现基于 Redis Streams 的服务间异步任务通信。
// Backend 通过 Publisher 发布任务到 Worker，Worker 通过 Consumer 消费任务。
// 所有消息使用 HMAC-SHA256 签名，防止伪造任务注入。
package taskqueue

import "time"

// 任务类型常量
const (
	TaskBatchCheck    = "batch_check"    // 模型批量检测
	TaskModelSync     = "model_sync"     // 模型按渠道同步
	TaskModelSyncAll  = "model_sync_all" // 模型全量同步
	TaskPriceScrape   = "price_scrape"   // 价格爬虫预览/应用
	TaskRouteRefresh  = "route_refresh"  // 默认路由刷新
	TaskScanOffline   = "scan_offline"   // 全量下线模型扫描
)

// 任务状态常量
const (
	StatusPending    = "pending"
	StatusRunning    = "running"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
)

// Redis Stream 配置
const (
	StreamKey        = "tasks:worker"           // 任务 Stream key
	ConsumerGroup    = "worker-group"           // 消费者组名
	ProgressPrefix   = "task:progress:"         // 进度 Pub/Sub 前缀
	DeduplicationTTL = 1 * time.Hour            // 任务去重 TTL
	SignatureWindow  = 5 * time.Minute          // 签名时间窗口（防重放）
)

// TaskRequest 任务请求消息（Backend → Worker）
type TaskRequest struct {
	TaskID       string `json:"task_id"`        // 唯一任务 ID (UUID)
	TaskType     string `json:"task_type"`      // 任务类型（见上方常量）
	Payload      string `json:"payload"`        // JSON 序列化的参数
	Timestamp    int64  `json:"timestamp"`      // Unix 时间戳
	Signature    string `json:"signature"`      // HMAC-SHA256 签名
	ReplyChannel string `json:"reply_channel"`  // 进度推送 Pub/Sub 频道
}

// TaskProgress 任务进度消息（Worker → Backend SSE）
type TaskProgress struct {
	TaskID    string `json:"task_id"`
	Status    string `json:"status"`     // pending/running/completed/failed
	Progress  int    `json:"progress"`   // 0-100 百分比
	Message   string `json:"message"`    // 进度文本
	Data      string `json:"data"`       // JSON 结果数据（完成时）
	Error     string `json:"error"`      // 错误信息（失败时）
	Timestamp int64  `json:"timestamp"`
}

// BatchCheckPayload 模型批量检测参数
type BatchCheckPayload struct {
	SupplierID uint `json:"supplier_id,omitempty"` // 0=全部供应商
}

// ModelSyncPayload 模型同步参数
type ModelSyncPayload struct {
	ChannelID  uint `json:"channel_id,omitempty"`  // 0=全部渠道
	SupplierID uint `json:"supplier_id,omitempty"`
}

// PriceScrapePayload 价格爬虫参数
type PriceScrapePayload struct {
	SupplierCode string `json:"supplier_code"`
	AutoApply    bool   `json:"auto_apply"` // 是否自动应用价格
}
