package errcode

import "fmt"

// AppError 结构化应用错误，包含错误码和消息键
type AppError struct {
	Code   int    `json:"code"`
	MsgKey string `json:"msg_key"`
}

// Error 实现error接口
func (e *AppError) Error() string {
	return fmt.Sprintf("errcode %d: %s", e.Code, e.MsgKey)
}

// New 创建新的AppError实例
func New(code int, msgKey string) *AppError {
	if msgKey == "" {
		msgKey = "error.unknown"
	}
	return &AppError{Code: code, MsgKey: msgKey}
}

// --- 认证错误 (10000-19999) ---
var (
	ErrUnauthorized     = New(10001, "error.unauthorized")
	ErrTokenExpired     = New(10002, "error.token_expired")
	ErrTokenInvalid     = New(10003, "error.token_invalid")
	ErrPermissionDenied = New(10004, "error.permission_denied")
	ErrApiKeyInvalid    = New(10005, "error.api_key_invalid")
	ErrApiKeyExpired    = New(10006, "error.api_key_expired")
	ErrLoginFailed      = New(10007, "error.login_failed")
)

// --- 业务错误 (20000-29999) ---
var (
	ErrBadRequest       = New(20001, "error.bad_request")
	ErrNotFound         = New(20002, "error.not_found")
	ErrDuplicate        = New(20003, "error.duplicate")
	ErrValidation       = New(20004, "error.validation")
	ErrTenantNotFound   = New(20005, "error.tenant_not_found")
	ErrUserNotFound     = New(20006, "error.user_not_found")
	ErrModelNotFound    = New(20007, "error.model_not_found")
	ErrQuotaExceeded    = New(20008, "error.quota_exceeded")
	ErrIdempotentRepeat = New(20009, "error.idempotent_repeat")
)

// --- 渠道错误 (30000-39999) ---
var (
	ErrChannelUnavailable = New(30001, "error.channel_unavailable")
	ErrChannelTimeout     = New(30002, "error.channel_timeout")
	ErrChannelRateLimit   = New(30003, "error.channel_rate_limit")
	ErrNoAvailableChannel = New(30004, "error.no_available_channel")
	ErrChannelAllFailed   = New(30005, "error.channel_all_failed")
)

// --- 支付错误 (40000-49999) ---
var (
	ErrPaymentFailed      = New(40001, "error.payment_failed")
	ErrPaymentDuplicate   = New(40002, "error.payment_duplicate")
	ErrRefundFailed       = New(40003, "error.refund_failed")
	ErrInsufficientFund   = New(40004, "error.insufficient_fund")
	ErrInsufficientBalance = New(40005, "error.insufficient_balance") // 余额/积分不足
)

// --- 系统错误 (50000-59999) ---
var (
	ErrInternal   = New(50001, "error.internal")
	ErrDatabase   = New(50002, "error.database")
	ErrRedis      = New(50003, "error.redis")
	ErrRateLimit  = New(50004, "error.rate_limit")
	ErrTimeout    = New(50005, "error.timeout")
	ErrMarshal    = New(50006, "error.marshal")
	ErrThirdParty = New(50007, "error.third_party")
)
