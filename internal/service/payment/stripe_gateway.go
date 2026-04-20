package payment

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"tokenhub-server/internal/config"
)

// StripeGateway Stripe支付网关，基于Checkout Session API实现 PaymentGateway 接口
type StripeGateway struct {
	secretKey     string
	webhookSecret string
	httpClient    *http.Client
	logger        *zap.Logger
}

// NewStripeGateway 根据配置创建Stripe支付网关实例
func NewStripeGateway(logger *zap.Logger) *StripeGateway {
	cfg := config.Global.Payment.Stripe
	return &StripeGateway{
		secretKey:     cfg.SecretKey,
		webhookSecret: cfg.WebhookSecret,
		httpClient:    &http.Client{Timeout: 30 * time.Second},
		logger:        logger,
	}
}

func (g *StripeGateway) Name() string { return "stripe" }

// CreateOrder 创建Stripe Checkout Session，返回支付页面URL供用户跳转付款
func (g *StripeGateway) CreateOrder(ctx context.Context, order *PaymentOrder) (*PaymentResult, error) {
	amountCents := AmountToCents(order.Amount)
	currency := strings.ToLower(order.Currency)
	if currency == "" {
		currency = "usd"
	}

	formData := fmt.Sprintf(
		"mode=payment&success_url=%s&cancel_url=%s&line_items[0][price_data][currency]=%s&line_items[0][price_data][unit_amount]=%d&line_items[0][price_data][product_data][name]=%s&line_items[0][quantity]=1&metadata[order_no]=%s",
		urlEncode(order.ReturnURL+"?status=success"),
		urlEncode(order.ReturnURL+"?status=cancel"),
		currency,
		amountCents,
		urlEncode(order.Subject),
		order.OrderNo,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.stripe.com/v1/checkout/sessions",
		strings.NewReader(formData))
	if err != nil {
		return nil, fmt.Errorf("stripe: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(g.secretKey, "")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("stripe: http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("stripe: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		g.logger.Error("stripe create session failed", zap.Int("status", resp.StatusCode), zap.String("body", string(body)))
		return nil, fmt.Errorf("stripe: create session failed, status=%d", resp.StatusCode)
	}

	var session struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &session); err != nil {
		return nil, fmt.Errorf("stripe: unmarshal: %w", err)
	}

	return &PaymentResult{
		Gateway:      "stripe",
		OrderNo:      order.OrderNo,
		GatewayTxnID: session.ID,
		PayURL:       session.URL,
		ExpireAt:     time.Now().Add(24 * time.Hour).Format(time.RFC3339),
	}, nil
}

// QueryOrder 查询Stripe Checkout Session状态，通过metadata中的order_no匹配订单
func (g *StripeGateway) QueryOrder(ctx context.Context, orderNo string) (*OrderStatus, error) {
	// Search sessions by metadata.order_no
	searchURL := fmt.Sprintf("https://api.stripe.com/v1/checkout/sessions?limit=1")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("stripe: new request: %w", err)
	}
	req.SetBasicAuth(g.secretKey, "")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("stripe: http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("stripe: read response: %w", err)
	}

	var result struct {
		Data []struct {
			ID            string `json:"id"`
			PaymentStatus string `json:"payment_status"`
			AmountTotal   int64  `json:"amount_total"`
			Metadata      map[string]string `json:"metadata"`
			PaymentIntent string `json:"payment_intent"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("stripe: unmarshal: %w", err)
	}

	// Find our order
	for _, s := range result.Data {
		if s.Metadata["order_no"] == orderNo {
			status := "pending"
			if s.PaymentStatus == "paid" {
				status = "success"
			} else if s.PaymentStatus == "unpaid" {
				status = "pending"
			}
			return &OrderStatus{
				OrderNo:      orderNo,
				GatewayTxnID: s.PaymentIntent,
				Status:       status,
				Amount:       float64(s.AmountTotal) / 100.0,
			}, nil
		}
	}

	return &OrderStatus{
		OrderNo: orderNo,
		Status:  "pending",
	}, nil
}

// Refund 通过Stripe Refunds API创建退款，先查询payment_intent再发起退款
func (g *StripeGateway) Refund(ctx context.Context, orderNo string, amount float64, reason string) (*RefundResult, error) {
	// First query to get the payment_intent
	orderStatus, err := g.QueryOrder(ctx, orderNo)
	if err != nil {
		return nil, fmt.Errorf("stripe: query order for refund: %w", err)
	}
	if orderStatus.GatewayTxnID == "" {
		return nil, fmt.Errorf("stripe: no payment_intent found for order %s", orderNo)
	}

	amountCents := AmountToCents(amount)
	formData := fmt.Sprintf("payment_intent=%s&amount=%d&reason=requested_by_customer",
		orderStatus.GatewayTxnID, amountCents)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.stripe.com/v1/refunds",
		strings.NewReader(formData))
	if err != nil {
		return nil, fmt.Errorf("stripe: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(g.secretKey, "")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("stripe: http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("stripe: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("stripe: refund failed, status=%d, body=%s", resp.StatusCode, string(body))
	}

	var refund struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &refund); err != nil {
		return nil, fmt.Errorf("stripe: unmarshal: %w", err)
	}

	refundStatus := "processing"
	if refund.Status == "succeeded" {
		refundStatus = "success"
	} else if refund.Status == "failed" {
		refundStatus = "failed"
	}

	return &RefundResult{
		OrderNo:         orderNo,
		RefundNo:        "R" + orderNo,
		Amount:          amount,
		Status:          refundStatus,
		GatewayRefundID: refund.ID,
	}, nil
}

// VerifyCallback 验证Stripe Webhook签名（HMAC-SHA256）并解析支付事件
func (g *StripeGateway) VerifyCallback(_ context.Context, data []byte, headers map[string]string) (*CallbackResult, error) {
	sigHeader := headers["Stripe-Signature"]
	if sigHeader == "" {
		// Also check lowercase
		sigHeader = headers["stripe-signature"]
	}
	if sigHeader == "" {
		return nil, fmt.Errorf("stripe: missing Stripe-Signature header")
	}

	// Parse the signature header: t=timestamp,v1=signature
	var timestamp string
	var signatures []string
	parts := strings.Split(sigHeader, ",")
	for _, part := range parts {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			timestamp = kv[1]
		case "v1":
			signatures = append(signatures, kv[1])
		}
	}

	if timestamp == "" || len(signatures) == 0 {
		return nil, fmt.Errorf("stripe: invalid signature header format")
	}

	// Check timestamp tolerance (5 minutes)
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("stripe: invalid timestamp: %w", err)
	}
	if time.Now().Unix()-ts > 300 {
		return nil, fmt.Errorf("stripe: webhook timestamp too old")
	}

	// Compute expected signature: HMAC-SHA256(webhook_secret, "timestamp.payload")
	signedPayload := timestamp + "." + string(data)
	mac := hmac.New(sha256.New, []byte(g.webhookSecret))
	mac.Write([]byte(signedPayload))
	expected := hex.EncodeToString(mac.Sum(nil))

	verified := false
	for _, sig := range signatures {
		if hmac.Equal([]byte(expected), []byte(sig)) {
			verified = true
			break
		}
	}
	if !verified {
		return nil, fmt.Errorf("stripe: signature verification failed")
	}

	return parseStripeEvent(data)
}

// parseStripeEvent 解析 Stripe 事件并映射到 CallbackResult
// 支持 2024+ 最新事件格式：
//   - checkout.session.completed（推荐，包含 metadata.order_no）
//   - payment_intent.succeeded（低层事件，兼容旧版集成）
//   - charge.refunded（退款回调）
//   - charge.succeeded（V1 兼容）
func parseStripeEvent(data []byte) (*CallbackResult, error) {
	var event struct {
		Type string `json:"type"`
		Data struct {
			Object json.RawMessage `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return nil, fmt.Errorf("stripe: unmarshal event: %w", err)
	}

	switch event.Type {
	case "checkout.session.completed":
		var session struct {
			PaymentIntent string            `json:"payment_intent"`
			PaymentStatus string            `json:"payment_status"`
			AmountTotal   int64             `json:"amount_total"`
			Metadata      map[string]string `json:"metadata"`
		}
		if err := json.Unmarshal(event.Data.Object, &session); err != nil {
			return nil, fmt.Errorf("stripe: unmarshal session: %w", err)
		}
		status := "failed"
		if session.PaymentStatus == "paid" {
			status = "success"
		}
		return &CallbackResult{
			OrderNo:      session.Metadata["order_no"],
			GatewayTxnID: session.PaymentIntent,
			Amount:       float64(session.AmountTotal) / 100.0,
			Status:       status,
			PaidAt:       time.Now().Format(time.RFC3339),
		}, nil

	case "payment_intent.succeeded", "charge.succeeded":
		var pi struct {
			ID       string            `json:"id"`
			Amount   int64             `json:"amount"`
			Status   string            `json:"status"`
			Metadata map[string]string `json:"metadata"`
		}
		if err := json.Unmarshal(event.Data.Object, &pi); err != nil {
			return nil, fmt.Errorf("stripe: unmarshal payment_intent: %w", err)
		}
		status := "failed"
		if pi.Status == "succeeded" {
			status = "success"
		}
		return &CallbackResult{
			OrderNo:      pi.Metadata["order_no"],
			GatewayTxnID: pi.ID,
			Amount:       float64(pi.Amount) / 100.0,
			Status:       status,
			PaidAt:       time.Now().Format(time.RFC3339),
		}, nil

	case "charge.refunded":
		// 退款回调：业务侧可忽略（RefundService 已同步跟踪），但仍返回结构
		var charge struct {
			ID       string            `json:"id"`
			Amount   int64             `json:"amount_refunded"`
			Metadata map[string]string `json:"metadata"`
		}
		if err := json.Unmarshal(event.Data.Object, &charge); err != nil {
			return nil, fmt.Errorf("stripe: unmarshal refund: %w", err)
		}
		return &CallbackResult{
			OrderNo:      charge.Metadata["order_no"],
			GatewayTxnID: charge.ID,
			Amount:       float64(charge.Amount) / 100.0,
			Status:       "refunded",
			PaidAt:       time.Now().Format(time.RFC3339),
		}, nil

	default:
		return nil, fmt.Errorf("stripe: unhandled event type: %s", event.Type)
	}
}

// urlEncode 简单的URL编码工具函数，对特殊字符进行百分号编码
func urlEncode(s string) string {
	var buf bytes.Buffer
	for _, b := range []byte(s) {
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '-' || b == '_' || b == '.' || b == '~' {
			buf.WriteByte(b)
		} else {
			buf.WriteString(fmt.Sprintf("%%%02X", b))
		}
	}
	return buf.String()
}
