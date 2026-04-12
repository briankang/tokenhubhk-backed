package payment

import "context"

// PaymentGateway 统一支付网关接口（策略模式）
type PaymentGateway interface {
	// Name 返回网关标识符（如 "wechat"/"alipay"/"stripe"/"paypal"）
	Name() string
	// CreateOrder 创建支付订单并返回支付参数（二维码链接/跳转链接/SDK 参数）
	CreateOrder(ctx context.Context, order *PaymentOrder) (*PaymentResult, error)
	// QueryOrder 从远端网关查询订单状态
	QueryOrder(ctx context.Context, orderNo string) (*OrderStatus, error)
	// Refund 对指定订单发起退款
	Refund(ctx context.Context, orderNo string, amount float64, reason string) (*RefundResult, error)
	// VerifyCallback 验证回调签名并解析通知数据
	VerifyCallback(ctx context.Context, data []byte, headers map[string]string) (*CallbackResult, error)
}

// PaymentOrder 创建支付订单所需的参数
type PaymentOrder struct {
	OrderNo     string            `json:"order_no"`
	Amount      float64           `json:"amount"`
	Currency    string            `json:"currency"`
	Subject     string            `json:"subject"`
	Description string            `json:"description"`
	ReturnURL   string            `json:"return_url"`
	NotifyURL   string            `json:"notify_url"`
	ClientIP    string            `json:"client_ip"`
	Extra       map[string]string `json:"extra"`
}

// PaymentResult CreateOrder 的响应结果
type PaymentResult struct {
	Gateway      string `json:"gateway"`
	OrderNo      string `json:"order_no"`
	GatewayTxnID string `json:"gateway_txn_id,omitempty"`
	PayURL       string `json:"pay_url,omitempty"`
	QRCode       string `json:"qr_code,omitempty"`
	SDKParams    string `json:"sdk_params,omitempty"`
	ExpireAt     string `json:"expire_at,omitempty"`
}

// OrderStatus 从网关查询到的订单状态
type OrderStatus struct {
	OrderNo      string  `json:"order_no"`
	GatewayTxnID string  `json:"gateway_txn_id"`
	Status       string  `json:"status"` // "pending" / "success" / "failed" / "closed"
	Amount       float64 `json:"amount"`
	PaidAt       string  `json:"paid_at,omitempty"`
}

// RefundResult 退款请求的响应结果
type RefundResult struct {
	OrderNo        string  `json:"order_no"`
	RefundNo       string  `json:"refund_no"`
	Amount         float64 `json:"amount"`
	Status         string  `json:"status"` // "processing" / "success" / "failed"
	GatewayRefundID string `json:"gateway_refund_id,omitempty"`
}

// CallbackResult 解析并验证后的回调通知
type CallbackResult struct {
	OrderNo      string  `json:"order_no"`
	GatewayTxnID string  `json:"gateway_txn_id"`
	Amount       float64 `json:"amount"`
	Status       string  `json:"status"` // "success" / "failed"
	PaidAt       string  `json:"paid_at"`
}

// AmountToCents 将浮点金额转换为整数分，用于安全金额比较
func AmountToCents(amount float64) int64 {
	return int64(amount*100 + 0.5)
}
