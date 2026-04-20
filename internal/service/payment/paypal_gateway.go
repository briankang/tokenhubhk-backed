package payment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"tokenhub-server/internal/config"
)

// PayPalGateway PayPal REST API v2支付网关，实现 PaymentGateway 接口，支持OAuth2令牌自动续期
type PayPalGateway struct {
	clientID     string
	clientSecret string
	webhookID    string // v3.2: webhook 签名校验必填
	sandbox      bool
	baseURL      string
	httpClient   *http.Client
	logger       *zap.Logger

	mu          sync.RWMutex
	accessToken string
	tokenExpiry time.Time
}

// NewPayPalGateway 根据配置创建PayPal支付网关实例，支持沙箱/生产环境切换
func NewPayPalGateway(logger *zap.Logger) *PayPalGateway {
	cfg := config.Global.Payment.PayPal
	baseURL := "https://api-m.paypal.com"
	if cfg.Sandbox {
		baseURL = "https://api-m.sandbox.paypal.com"
	}
	return &PayPalGateway{
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		webhookID:    cfg.WebhookID,
		sandbox:      cfg.Sandbox,
		baseURL:      baseURL,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
		logger:       logger,
	}
}

func (g *PayPalGateway) Name() string { return "paypal" }

// getAccessToken 获取有效的OAuth2访问令牌，过期时自动刷新（双重检查锁机制）
func (g *PayPalGateway) getAccessToken(ctx context.Context) (string, error) {
	g.mu.RLock()
	if g.accessToken != "" && time.Now().Before(g.tokenExpiry) {
		token := g.accessToken
		g.mu.RUnlock()
		return token, nil
	}
	g.mu.RUnlock()

	g.mu.Lock()
	defer g.mu.Unlock()

	// Double-check after acquiring write lock
	if g.accessToken != "" && time.Now().Before(g.tokenExpiry) {
		return g.accessToken, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		g.baseURL+"/v1/oauth2/token",
		strings.NewReader("grant_type=client_credentials"))
	if err != nil {
		return "", fmt.Errorf("paypal: new token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(g.clientID, g.clientSecret)

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("paypal: token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("paypal: read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("paypal: token request failed, status=%d, body=%s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("paypal: unmarshal token: %w", err)
	}

	g.accessToken = tokenResp.AccessToken
	// Expire 60 seconds early to avoid edge cases
	g.tokenExpiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn-60) * time.Second)

	return g.accessToken, nil
}

// CreateOrder 创建PayPal订单并返回用户授权支付的跳转URL
func (g *PayPalGateway) CreateOrder(ctx context.Context, order *PaymentOrder) (*PaymentResult, error) {
	token, err := g.getAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	currency := strings.ToUpper(order.Currency)
	if currency == "" {
		currency = "USD"
	}

	orderBody := map[string]interface{}{
		"intent": "CAPTURE",
		"purchase_units": []map[string]interface{}{
			{
				"reference_id": order.OrderNo,
				"description":  order.Subject,
				"amount": map[string]interface{}{
					"currency_code": currency,
					"value":         fmt.Sprintf("%.2f", order.Amount),
				},
			},
		},
		"application_context": map[string]interface{}{
			"return_url": order.ReturnURL + "?status=success",
			"cancel_url": order.ReturnURL + "?status=cancel",
		},
	}

	payload, err := json.Marshal(orderBody)
	if err != nil {
		return nil, fmt.Errorf("paypal: marshal order: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		g.baseURL+"/v2/checkout/orders",
		bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("paypal: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("paypal: http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("paypal: read response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		g.logger.Error("paypal create order failed", zap.Int("status", resp.StatusCode), zap.String("body", string(body)))
		return nil, fmt.Errorf("paypal: create order failed, status=%d", resp.StatusCode)
	}

	var ppOrder struct {
		ID    string `json:"id"`
		Links []struct {
			Href string `json:"href"`
			Rel  string `json:"rel"`
		} `json:"links"`
	}
	if err := json.Unmarshal(body, &ppOrder); err != nil {
		return nil, fmt.Errorf("paypal: unmarshal: %w", err)
	}

	var approveURL string
	for _, link := range ppOrder.Links {
		if link.Rel == "approve" {
			approveURL = link.Href
			break
		}
	}

	return &PaymentResult{
		Gateway:      "paypal",
		OrderNo:      order.OrderNo,
		GatewayTxnID: ppOrder.ID,
		PayURL:       approveURL,
		ExpireAt:     time.Now().Add(3 * time.Hour).Format(time.RFC3339),
	}, nil
}

// QueryOrder 查询PayPal订单状态，通过PayPal订单ID获取交易信息
func (g *PayPalGateway) QueryOrder(ctx context.Context, orderNo string) (*OrderStatus, error) {
	token, err := g.getAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	// PayPal uses its own order ID, not ours. We store the gateway txn id.
	// For query, the caller should pass the PayPal order ID if available.
	// Fallback: return pending status since we cannot search by merchant reference directly.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		g.baseURL+"/v2/checkout/orders/"+orderNo, nil)
	if err != nil {
		return nil, fmt.Errorf("paypal: new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("paypal: http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("paypal: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("paypal: query order failed, status=%d", resp.StatusCode)
	}

	var ppOrder struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		PurchaseUnits []struct {
			ReferenceID string `json:"reference_id"`
			Amount      struct {
				Value string `json:"value"`
			} `json:"amount"`
		} `json:"purchase_units"`
	}
	if err := json.Unmarshal(body, &ppOrder); err != nil {
		return nil, fmt.Errorf("paypal: unmarshal: %w", err)
	}

	status := "pending"
	switch ppOrder.Status {
	case "COMPLETED", "APPROVED":
		status = "success"
	case "VOIDED":
		status = "failed"
	}

	var amount float64
	refID := orderNo
	if len(ppOrder.PurchaseUnits) > 0 {
		fmt.Sscanf(ppOrder.PurchaseUnits[0].Amount.Value, "%f", &amount)
		if ppOrder.PurchaseUnits[0].ReferenceID != "" {
			refID = ppOrder.PurchaseUnits[0].ReferenceID
		}
	}

	return &OrderStatus{
		OrderNo:      refID,
		GatewayTxnID: ppOrder.ID,
		Status:       status,
		Amount:       amount,
	}, nil
}

// Refund 对已捕获的PayPal订单发起退款，先查询捕获ID再执行退款
func (g *PayPalGateway) Refund(ctx context.Context, orderNo string, amount float64, reason string) (*RefundResult, error) {
	token, err := g.getAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	// First, get the capture ID from the order
	orderStatus, err := g.QueryOrder(ctx, orderNo)
	if err != nil {
		return nil, fmt.Errorf("paypal: query order for refund: %w", err)
	}

	captureID := orderStatus.GatewayTxnID
	if captureID == "" {
		return nil, fmt.Errorf("paypal: no capture found for order %s", orderNo)
	}

	refundBody := map[string]interface{}{
		"amount": map[string]interface{}{
			"currency_code": "USD",
			"value":         fmt.Sprintf("%.2f", amount),
		},
		"note_to_payer": reason,
	}
	payload, _ := json.Marshal(refundBody)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		g.baseURL+"/v2/payments/captures/"+captureID+"/refund",
		bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("paypal: new refund request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("paypal: http refund request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("paypal: read refund response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("paypal: refund failed, status=%d, body=%s", resp.StatusCode, string(body))
	}

	var refundResp struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &refundResp); err != nil {
		return nil, fmt.Errorf("paypal: unmarshal refund: %w", err)
	}

	refundStatus := "processing"
	if refundResp.Status == "COMPLETED" {
		refundStatus = "success"
	} else if refundResp.Status == "FAILED" || refundResp.Status == "CANCELLED" {
		refundStatus = "failed"
	}

	return &RefundResult{
		OrderNo:         orderNo,
		RefundNo:        "R" + orderNo,
		Amount:          amount,
		Status:          refundStatus,
		GatewayRefundID: refundResp.ID,
	}, nil
}

// VerifyCallback 验证PayPal Webhook签名（通过PayPal验签API）并解析支付事件
func (g *PayPalGateway) VerifyCallback(ctx context.Context, data []byte, headers map[string]string) (*CallbackResult, error) {
	// Extract PayPal webhook headers
	transmissionID := headers["Paypal-Transmission-Id"]
	transmissionTime := headers["Paypal-Transmission-Time"]
	transmissionSig := headers["Paypal-Transmission-Sig"]
	certURL := headers["Paypal-Cert-Url"]
	authAlgo := headers["Paypal-Auth-Algo"]

	if transmissionID == "" || transmissionSig == "" {
		return nil, fmt.Errorf("paypal: missing webhook signature headers")
	}

	// Verify signature via PayPal API
	token, err := g.getAccessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("paypal: get token for verification: %w", err)
	}

	verifyBody := map[string]interface{}{
		"transmission_id":   transmissionID,
		"transmission_time": transmissionTime,
		"cert_url":          certURL,
		"auth_algo":         authAlgo,
		"transmission_sig":  transmissionSig,
		"webhook_id":        g.webhookID,
		"webhook_event":     json.RawMessage(data),
	}
	verifyPayload, _ := json.Marshal(verifyBody)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		g.baseURL+"/v1/notifications/verify-webhook-signature",
		bytes.NewReader(verifyPayload))
	if err != nil {
		return nil, fmt.Errorf("paypal: new verify request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("paypal: verify request: %w", err)
	}
	defer resp.Body.Close()

	verifyRespBody, _ := io.ReadAll(resp.Body)
	var verifyResult struct {
		VerificationStatus string `json:"verification_status"`
	}
	if err := json.Unmarshal(verifyRespBody, &verifyResult); err != nil {
		return nil, fmt.Errorf("paypal: unmarshal verify response: %w", err)
	}

	if verifyResult.VerificationStatus != "SUCCESS" {
		return nil, fmt.Errorf("paypal: webhook verification failed: %s", verifyResult.VerificationStatus)
	}

	return parsePayPalEvent(data)
}

// parsePayPalEvent 解析 PayPal webhook 事件并适配多种事件/资源结构
// 支持 2024+ 最新格式：
//   - CHECKOUT.ORDER.APPROVED (V2 Order API, resource.purchase_units[0])
//   - PAYMENT.CAPTURE.COMPLETED (V2 Capture, resource.custom_id + resource.amount.value)
//   - PAYMENT.SALE.COMPLETED (Classic Sale, resource.invoice_number + resource.amount.total)
//   - CHECKOUT.ORDER.COMPLETED (V2 新命名)
//   - PAYMENT.CAPTURE.REFUNDED / PAYMENT.CAPTURE.DENIED (退款/失败)
func parsePayPalEvent(data []byte) (*CallbackResult, error) {
	var base struct {
		EventType string          `json:"event_type"`
		Resource  json.RawMessage `json:"resource"`
	}
	if err := json.Unmarshal(data, &base); err != nil {
		return nil, fmt.Errorf("paypal: unmarshal event: %w", err)
	}

	// 解析 resource 时兼容三种 shape
	var resource struct {
		ID            string `json:"id"`
		Status        string `json:"status"`
		CustomID      string `json:"custom_id"`        // V2 Capture: 业务订单号透传
		InvoiceNumber string `json:"invoice_number"`   // Classic Sale: 业务订单号
		ParentPayment string `json:"parent_payment"`
		Amount        struct {
			Value    string `json:"value"`    // V2 format
			Total    string `json:"total"`    // Classic format
			Currency string `json:"currency_code"`
		} `json:"amount"`
		PurchaseUnits []struct {
			ReferenceID string `json:"reference_id"`
			CustomID    string `json:"custom_id"`
			Amount      struct {
				Value string `json:"value"`
			} `json:"amount"`
		} `json:"purchase_units"`
	}
	if err := json.Unmarshal(base.Resource, &resource); err != nil {
		return nil, fmt.Errorf("paypal: unmarshal resource: %w", err)
	}

	status := "failed"
	switch base.EventType {
	case "CHECKOUT.ORDER.APPROVED",
		"PAYMENT.CAPTURE.COMPLETED",
		"PAYMENT.SALE.COMPLETED",
		"CHECKOUT.ORDER.COMPLETED":
		status = "success"
	case "PAYMENT.CAPTURE.REFUNDED":
		status = "refunded"
	case "PAYMENT.CAPTURE.DENIED",
		"PAYMENT.SALE.DENIED":
		status = "failed"
	default:
		// 未识别事件，仍返回错误让 handler 拒绝
		return nil, fmt.Errorf("paypal: unhandled event type: %s", base.EventType)
	}

	// 优先级：CustomID (Capture) → InvoiceNumber (Sale) → purchase_units[0].ReferenceID/CustomID (Order)
	orderNo := resource.CustomID
	if orderNo == "" {
		orderNo = resource.InvoiceNumber
	}
	if orderNo == "" && len(resource.PurchaseUnits) > 0 {
		if resource.PurchaseUnits[0].CustomID != "" {
			orderNo = resource.PurchaseUnits[0].CustomID
		} else {
			orderNo = resource.PurchaseUnits[0].ReferenceID
		}
	}

	// 金额：优先直接 resource.amount.value，其次 amount.total（Classic），最后 purchase_units[0].amount.value
	var amount float64
	valueStr := resource.Amount.Value
	if valueStr == "" {
		valueStr = resource.Amount.Total
	}
	if valueStr == "" && len(resource.PurchaseUnits) > 0 {
		valueStr = resource.PurchaseUnits[0].Amount.Value
	}
	if valueStr != "" {
		fmt.Sscanf(valueStr, "%f", &amount)
	}

	return &CallbackResult{
		OrderNo:      orderNo,
		GatewayTxnID: resource.ID,
		Amount:       amount,
		Status:       status,
		PaidAt:       time.Now().Format(time.RFC3339),
	}, nil
}
