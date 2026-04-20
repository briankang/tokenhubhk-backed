package model

import "time"

// PaymentEventLog 支付事件全链路日志
// 记录支付/退款/提现/汇率全流程的所有外部交互、状态变更、用户操作
// 用途：审计、问题排查、对账、合规
type PaymentEventLog struct {
	ID          uint64    `gorm:"primaryKey" json:"id"`
	PaymentID   *uint64   `gorm:"index" json:"payment_id,omitempty"`
	RefundID    *uint64   `gorm:"index" json:"refund_id,omitempty"`
	WithdrawID  *uint64   `gorm:"index" json:"withdraw_id,omitempty"`
	OrderNo     string    `gorm:"type:varchar(64);index" json:"order_no,omitempty"`
	EventType   string    `gorm:"type:varchar(40);not null;index" json:"event_type"` // 见 EventType* 常量
	ActorType   string    `gorm:"type:varchar(20);not null" json:"actor_type"`       // user/admin/gateway/system/cron
	ActorID     *uint64   `json:"actor_id,omitempty"`
	Gateway     string    `gorm:"type:varchar(20);index" json:"gateway,omitempty"`
	AccountID   *uint64   `json:"account_id,omitempty"` // 关联 PaymentProviderAccount.ID
	IP          string    `gorm:"type:varchar(64)" json:"ip,omitempty"`
	UserAgent   string    `gorm:"type:varchar(500)" json:"user_agent,omitempty"`
	PayloadJSON string    `gorm:"type:text" json:"payload_json,omitempty"` // 请求/输入
	ResultJSON  string    `gorm:"type:text" json:"result_json,omitempty"`  // 响应/结果
	Success     bool      `gorm:"index" json:"success"`
	ErrorMsg    string    `gorm:"type:varchar(1000)" json:"error_msg,omitempty"`
	DurationMs  int64     `json:"duration_ms"` // 耗时（毫秒）
	CreatedAt   time.Time `gorm:"index" json:"created_at"`
}

// TableName 指定事件日志表名
func (PaymentEventLog) TableName() string {
	return "payment_event_logs"
}

// 事件类型常量
const (
	// 支付事件
	EventPaymentCreated         = "payment.created"
	EventPaymentGatewayRequest  = "payment.gateway_request"
	EventPaymentGatewayResponse = "payment.gateway_response"
	EventPaymentCallbackRecv    = "payment.callback_received"
	EventPaymentCallbackVerify  = "payment.callback_verified"
	EventPaymentCallbackFailed  = "payment.callback_failed"
	EventPaymentCredited        = "payment.credited"
	EventPaymentCreditFailed    = "payment.credit_failed"

	// 退款事件
	EventRefundRequested     = "refund.requested"
	EventRefundApproved      = "refund.approved"
	EventRefundRejected      = "refund.rejected"
	EventRefundGatewayCalled = "refund.gateway_called"
	EventRefundCompleted     = "refund.completed"
	EventRefundFailed        = "refund.failed"

	// 提现事件
	EventWithdrawalRequested = "withdrawal.requested"
	EventWithdrawalApproved  = "withdrawal.approved"
	EventWithdrawalRejected  = "withdrawal.rejected"
	EventWithdrawalPaid      = "withdrawal.paid"
	EventWithdrawalCancelled = "withdrawal.cancelled"

	// 汇率事件
	EventExchangeRateFetched  = "exchange_rate.fetched"
	EventExchangeRateFallback = "exchange_rate.fallback"
	EventExchangeRateFailed   = "exchange_rate.failed"
	EventExchangeRateOverride = "exchange_rate.override"

	// 操作者类型
	ActorUser    = "user"
	ActorAdmin   = "admin"
	ActorGateway = "gateway"
	ActorSystem  = "system"
	ActorCron    = "cron"
)
