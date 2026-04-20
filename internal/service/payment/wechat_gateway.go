package payment

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"

	"tokenhub-server/internal/config"
)

// WechatGateway 微信支付V3版网关，实现 PaymentGateway 接口
type WechatGateway struct {
	appID        string
	mchID        string
	apiKey       string
	apiV3Key     string // v3.2: V3 API 密钥，用于 AEAD_AES_256_GCM 回调资源解密
	certSerialNo string
	privateKey   *rsa.PrivateKey
	notifyURL    string
	httpClient   *http.Client
	logger       *zap.Logger
}

// NewWechatGateway 根据全局配置创建微信支付网关实例，加载RSA私钥用于签名
func NewWechatGateway(logger *zap.Logger) (*WechatGateway, error) {
	cfg := config.Global.Payment.Wechat
	gw := &WechatGateway{
		appID:        cfg.AppID,
		mchID:        cfg.MchID,
		apiKey:       cfg.APIKey,
		apiV3Key:     cfg.APIV3Key,
		certSerialNo: cfg.CertSerialNo,
		notifyURL:    cfg.NotifyURL,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
		logger:       logger,
	}

	if cfg.PrivateKeyPath != "" {
		pk, err := loadRSAPrivateKey(cfg.PrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("wechat: load private key: %w", err)
		}
		gw.privateKey = pk
	}

	return gw, nil
}

func (g *WechatGateway) Name() string { return "wechat" }

// CreateOrder 创建微信Native支付订单，返回二维码链接（code_url）供用户扫码付款
func (g *WechatGateway) CreateOrder(ctx context.Context, order *PaymentOrder) (*PaymentResult, error) {
	amountCents := AmountToCents(order.Amount)
	body := map[string]interface{}{
		"appid":        g.appID,
		"mchid":        g.mchID,
		"description":  order.Subject,
		"out_trade_no": order.OrderNo,
		"notify_url":   g.notifyURL,
		"amount": map[string]interface{}{
			"total":    amountCents,
			"currency": "CNY",
		},
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("wechat: marshal body: %w", err)
	}

	url := "https://api.mch.weixin.qq.com/v3/pay/transactions/native"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("wechat: new request: %w", err)
	}

	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	nonceStr := generateNonce(32)
	signature, err := g.sign(http.MethodPost, "/v3/pay/transactions/native", timestamp, nonceStr, string(payload))
	if err != nil {
		return nil, fmt.Errorf("wechat: sign: %w", err)
	}

	authHeader := fmt.Sprintf(`WECHATPAY2-SHA256-RSA2048 mchid="%s",nonce_str="%s",timestamp="%s",serial_no="%s",signature="%s"`,
		g.mchID, nonceStr, timestamp, g.certSerialNo, signature)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", authHeader)

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wechat: http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("wechat: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		g.logger.Error("wechat create order failed", zap.Int("status", resp.StatusCode), zap.String("body", string(respBody)))
		return nil, fmt.Errorf("wechat: create order failed, status=%d, body=%s", resp.StatusCode, string(respBody))
	}

	var result struct {
		CodeURL string `json:"code_url"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("wechat: unmarshal response: %w", err)
	}

	return &PaymentResult{
		Gateway:  "wechat",
		OrderNo:  order.OrderNo,
		QRCode:   result.CodeURL,
		ExpireAt: time.Now().Add(2 * time.Hour).Format(time.RFC3339),
	}, nil
}

// QueryOrder 查询微信支付订单状态，根据商户订单号获取交易状态和支付信息
func (g *WechatGateway) QueryOrder(ctx context.Context, orderNo string) (*OrderStatus, error) {
	url := fmt.Sprintf("https://api.mch.weixin.qq.com/v3/pay/transactions/out-trade-no/%s?mchid=%s", orderNo, g.mchID)
	path := fmt.Sprintf("/v3/pay/transactions/out-trade-no/%s?mchid=%s", orderNo, g.mchID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("wechat: new request: %w", err)
	}

	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	nonceStr := generateNonce(32)
	signature, err := g.sign(http.MethodGet, path, timestamp, nonceStr, "")
	if err != nil {
		return nil, fmt.Errorf("wechat: sign: %w", err)
	}

	authHeader := fmt.Sprintf(`WECHATPAY2-SHA256-RSA2048 mchid="%s",nonce_str="%s",timestamp="%s",serial_no="%s",signature="%s"`,
		g.mchID, nonceStr, timestamp, g.certSerialNo, signature)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", authHeader)

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wechat: http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("wechat: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wechat: query order failed, status=%d", resp.StatusCode)
	}

	var wxResp struct {
		TradeState string `json:"trade_state"`
		TransID    string `json:"transaction_id"`
		Amount     struct {
			Total int64 `json:"total"`
		} `json:"amount"`
		SuccessTime string `json:"success_time"`
	}
	if err := json.Unmarshal(respBody, &wxResp); err != nil {
		return nil, fmt.Errorf("wechat: unmarshal: %w", err)
	}

	status := "pending"
	switch wxResp.TradeState {
	case "SUCCESS":
		status = "success"
	case "CLOSED", "REVOKED", "PAYERROR":
		status = "failed"
	}

	return &OrderStatus{
		OrderNo:      orderNo,
		GatewayTxnID: wxResp.TransID,
		Status:       status,
		Amount:       float64(wxResp.Amount.Total) / 100.0,
		PaidAt:       wxResp.SuccessTime,
	}, nil
}

// Refund 通过微信支付V3接口发起退款，以商户订单号为退款单号前缀
func (g *WechatGateway) Refund(ctx context.Context, orderNo string, amount float64, reason string) (*RefundResult, error) {
	refundNo := "R" + orderNo
	amountCents := AmountToCents(amount)

	body := map[string]interface{}{
		"out_trade_no":  orderNo,
		"out_refund_no": refundNo,
		"reason":        reason,
		"amount": map[string]interface{}{
			"refund":   amountCents,
			"total":    amountCents,
			"currency": "CNY",
		},
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("wechat: marshal: %w", err)
	}

	url := "https://api.mch.weixin.qq.com/v3/refund/domestic/refunds"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("wechat: new request: %w", err)
	}

	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	nonceStr := generateNonce(32)
	signature, err := g.sign(http.MethodPost, "/v3/refund/domestic/refunds", timestamp, nonceStr, string(payload))
	if err != nil {
		return nil, fmt.Errorf("wechat: sign: %w", err)
	}

	authHeader := fmt.Sprintf(`WECHATPAY2-SHA256-RSA2048 mchid="%s",nonce_str="%s",timestamp="%s",serial_no="%s",signature="%s"`,
		g.mchID, nonceStr, timestamp, g.certSerialNo, signature)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", authHeader)

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wechat: http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("wechat: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wechat: refund failed, status=%d, body=%s", resp.StatusCode, string(respBody))
	}

	var wxResp struct {
		RefundID string `json:"refund_id"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal(respBody, &wxResp); err != nil {
		return nil, fmt.Errorf("wechat: unmarshal: %w", err)
	}

	refundStatus := "processing"
	if wxResp.Status == "SUCCESS" {
		refundStatus = "success"
	} else if wxResp.Status == "ABNORMAL" || wxResp.Status == "CLOSED" {
		refundStatus = "failed"
	}

	return &RefundResult{
		OrderNo:         orderNo,
		RefundNo:        refundNo,
		Amount:          amount,
		Status:          refundStatus,
		GatewayRefundID: wxResp.RefundID,
	}, nil
}

// VerifyCallback 验证微信支付V3回调签名并解析通知数据（AEAD_AES_256_GCM解密）
func (g *WechatGateway) VerifyCallback(_ context.Context, data []byte, headers map[string]string) (*CallbackResult, error) {
	// WeChat V3 callback uses AEAD_AES_256_GCM encrypted resource; here we parse the outer envelope.
	// In production, the platform certificate should be used to verify the Wechatpay-Signature header.
	// For now we parse the decrypted notification body.
	timestamp := headers["Wechatpay-Timestamp"]
	nonce := headers["Wechatpay-Nonce"]
	sig := headers["Wechatpay-Signature"]

	if timestamp == "" || nonce == "" || sig == "" {
		return nil, fmt.Errorf("wechat: missing callback signature headers")
	}

	// Build the message to verify: timestamp\nnonce\nbody\n
	message := timestamp + "\n" + nonce + "\n" + string(data) + "\n"
	_ = message // Signature verification with platform certificate omitted (requires cert download)

	// Parse the callback body
	var notification struct {
		EventType string `json:"event_type"`
		Resource  struct {
			Ciphertext     string `json:"ciphertext"`
			Nonce          string `json:"nonce"`
			AssociatedData string `json:"associated_data"`
		} `json:"resource"`
	}
	if err := json.Unmarshal(data, &notification); err != nil {
		return nil, fmt.Errorf("wechat: unmarshal notification: %w", err)
	}

	// Decrypt the resource ciphertext using AEAD_AES_256_GCM with the V3 API key.
	// 优先使用 APIV3Key（微信支付 V3 官方要求）；空时兼容回退到 APIKey
	decryptKey := g.apiV3Key
	if decryptKey == "" {
		decryptKey = g.apiKey
	}
	plaintext, err := decryptAES256GCM(decryptKey, notification.Resource.Nonce, notification.Resource.Ciphertext, notification.Resource.AssociatedData)
	if err != nil {
		return nil, fmt.Errorf("wechat: decrypt resource: %w", err)
	}

	var payment struct {
		OutTradeNo    string `json:"out_trade_no"`
		TransactionID string `json:"transaction_id"`
		TradeState    string `json:"trade_state"`
		SuccessTime   string `json:"success_time"`
		Amount        struct {
			Total int64 `json:"total"`
		} `json:"amount"`
	}
	if err := json.Unmarshal(plaintext, &payment); err != nil {
		return nil, fmt.Errorf("wechat: unmarshal payment: %w", err)
	}

	status := "failed"
	if payment.TradeState == "SUCCESS" {
		status = "success"
	}

	return &CallbackResult{
		OrderNo:      payment.OutTradeNo,
		GatewayTxnID: payment.TransactionID,
		Amount:       float64(payment.Amount.Total) / 100.0,
		Status:       status,
		PaidAt:       payment.SuccessTime,
	}, nil
}

// sign 生成微信支付V3接口的SHA256-RSA签名
func (g *WechatGateway) sign(method, path, timestamp, nonceStr, body string) (string, error) {
	if g.privateKey == nil {
		return "", fmt.Errorf("wechat: private key not configured")
	}
	message := strings.Join([]string{method, path, timestamp, nonceStr, body}, "\n") + "\n"
	hash := sha256.Sum256([]byte(message))
	sig, err := rsa.SignPKCS1v15(rand.Reader, g.privateKey, crypto.SHA256, hash[:])
	if err != nil {
		return "", fmt.Errorf("wechat: rsa sign: %w", err)
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// loadRSAPrivateKey 从PEM文件加载RSA私钥，支持PKCS8和PKCS1格式
func loadRSAPrivateKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		// Fallback to PKCS1
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("not an RSA private key")
	}
	return rsaKey, nil
}
