package payment

import (
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
	"net/url"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"tokenhub-server/internal/config"
)

// AlipayGateway 支付宝开放平台支付网关，实现 PaymentGateway 接口
type AlipayGateway struct {
	appID           string
	privateKey      *rsa.PrivateKey
	alipayPublicKey *rsa.PublicKey
	notifyURL       string
	gateway         string
	httpClient      *http.Client
	logger          *zap.Logger
}

// NewAlipayGateway 根据配置创建支付宝网关实例，加载RSA私钥和支付宝公钥
func NewAlipayGateway(logger *zap.Logger) (*AlipayGateway, error) {
	cfg := config.Global.Payment.Alipay
	gw := &AlipayGateway{
		appID:      cfg.AppID,
		notifyURL:  cfg.NotifyURL,
		gateway:    "https://openapi.alipay.com/gateway.do",
		httpClient: &http.Client{Timeout: 30 * time.Second},
		logger:     logger,
	}

	if cfg.PrivateKey != "" {
		pk, err := parseRSAPrivateKeyFromBase64(cfg.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("alipay: parse private key: %w", err)
		}
		gw.privateKey = pk
	}

	if cfg.AlipayPublicKey != "" {
		pub, err := parseRSAPublicKeyFromBase64(cfg.AlipayPublicKey)
		if err != nil {
			return nil, fmt.Errorf("alipay: parse public key: %w", err)
		}
		gw.alipayPublicKey = pub
	}

	return gw, nil
}

func (g *AlipayGateway) Name() string { return "alipay" }

// CreateOrder 创建支付宝电脑网站支付订单（alipay.trade.page.pay），返回跳转支付URL
func (g *AlipayGateway) CreateOrder(ctx context.Context, order *PaymentOrder) (*PaymentResult, error) {
	bizContent := map[string]interface{}{
		"out_trade_no": order.OrderNo,
		"total_amount": fmt.Sprintf("%.2f", order.Amount),
		"subject":      order.Subject,
		"product_code": "FAST_INSTANT_TRADE_PAY",
	}
	if order.Description != "" {
		bizContent["body"] = order.Description
	}

	bizJSON, err := json.Marshal(bizContent)
	if err != nil {
		return nil, fmt.Errorf("alipay: marshal biz_content: %w", err)
	}

	params := map[string]string{
		"app_id":      g.appID,
		"method":      "alipay.trade.page.pay",
		"charset":     "utf-8",
		"sign_type":   "RSA2",
		"timestamp":   time.Now().Format("2006-01-02 15:04:05"),
		"version":     "1.0",
		"notify_url":  g.notifyURL,
		"biz_content": string(bizJSON),
	}
	if order.ReturnURL != "" {
		params["return_url"] = order.ReturnURL
	}

	sign, err := g.signParams(params)
	if err != nil {
		return nil, fmt.Errorf("alipay: sign: %w", err)
	}
	params["sign"] = sign

	// Build the full redirect URL
	values := url.Values{}
	for k, v := range params {
		values.Set(k, v)
	}
	payURL := g.gateway + "?" + values.Encode()

	return &PaymentResult{
		Gateway:  "alipay",
		OrderNo:  order.OrderNo,
		PayURL:   payURL,
		ExpireAt: time.Now().Add(30 * time.Minute).Format(time.RFC3339),
	}, nil
}

// QueryOrder 查询支付宝订单状态（alipay.trade.query），返回交易状态和金额
func (g *AlipayGateway) QueryOrder(ctx context.Context, orderNo string) (*OrderStatus, error) {
	bizContent := map[string]string{"out_trade_no": orderNo}
	bizJSON, _ := json.Marshal(bizContent)

	params := map[string]string{
		"app_id":      g.appID,
		"method":      "alipay.trade.query",
		"charset":     "utf-8",
		"sign_type":   "RSA2",
		"timestamp":   time.Now().Format("2006-01-02 15:04:05"),
		"version":     "1.0",
		"biz_content": string(bizJSON),
	}

	sign, err := g.signParams(params)
	if err != nil {
		return nil, fmt.Errorf("alipay: sign: %w", err)
	}
	params["sign"] = sign

	values := url.Values{}
	for k, v := range params {
		values.Set(k, v)
	}

	resp, err := g.httpClient.Post(g.gateway, "application/x-www-form-urlencoded", strings.NewReader(values.Encode()))
	if err != nil {
		return nil, fmt.Errorf("alipay: http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("alipay: read response: %w", err)
	}

	var result struct {
		Response struct {
			TradeStatus string `json:"trade_status"`
			TradeNo     string `json:"trade_no"`
			TotalAmount string `json:"total_amount"`
			SendPayDate string `json:"send_pay_date"`
		} `json:"alipay_trade_query_response"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("alipay: unmarshal: %w", err)
	}

	status := "pending"
	switch result.Response.TradeStatus {
	case "TRADE_SUCCESS", "TRADE_FINISHED":
		status = "success"
	case "TRADE_CLOSED":
		status = "failed"
	}

	var amount float64
	fmt.Sscanf(result.Response.TotalAmount, "%f", &amount)

	return &OrderStatus{
		OrderNo:      orderNo,
		GatewayTxnID: result.Response.TradeNo,
		Status:       status,
		Amount:       amount,
		PaidAt:       result.Response.SendPayDate,
	}, nil
}

// Refund 发起支付宝退款（alipay.trade.refund），根据订单号和金额进行退款
func (g *AlipayGateway) Refund(ctx context.Context, orderNo string, amount float64, reason string) (*RefundResult, error) {
	refundNo := "R" + orderNo
	bizContent := map[string]interface{}{
		"out_trade_no":   orderNo,
		"refund_amount":  fmt.Sprintf("%.2f", amount),
		"refund_reason":  reason,
		"out_request_no": refundNo,
	}
	bizJSON, _ := json.Marshal(bizContent)

	params := map[string]string{
		"app_id":      g.appID,
		"method":      "alipay.trade.refund",
		"charset":     "utf-8",
		"sign_type":   "RSA2",
		"timestamp":   time.Now().Format("2006-01-02 15:04:05"),
		"version":     "1.0",
		"biz_content": string(bizJSON),
	}

	sign, err := g.signParams(params)
	if err != nil {
		return nil, fmt.Errorf("alipay: sign: %w", err)
	}
	params["sign"] = sign

	values := url.Values{}
	for k, v := range params {
		values.Set(k, v)
	}

	resp, err := g.httpClient.Post(g.gateway, "application/x-www-form-urlencoded", strings.NewReader(values.Encode()))
	if err != nil {
		return nil, fmt.Errorf("alipay: http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("alipay: read response: %w", err)
	}

	var result struct {
		Response struct {
			Code        string `json:"code"`
			TradeNo     string `json:"trade_no"`
			FundChange  string `json:"fund_change"`
		} `json:"alipay_trade_refund_response"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("alipay: unmarshal: %w", err)
	}

	refundStatus := "failed"
	if result.Response.Code == "10000" && result.Response.FundChange == "Y" {
		refundStatus = "success"
	}

	return &RefundResult{
		OrderNo:         orderNo,
		RefundNo:        refundNo,
		Amount:          amount,
		Status:          refundStatus,
		GatewayRefundID: result.Response.TradeNo,
	}, nil
}

// VerifyCallback 验证支付宝异步回调通知的RSA2签名，解析支付结果
func (g *AlipayGateway) VerifyCallback(_ context.Context, data []byte, _ map[string]string) (*CallbackResult, error) {
	// Parse form-encoded notification
	values, err := url.ParseQuery(string(data))
	if err != nil {
		return nil, fmt.Errorf("alipay: parse callback: %w", err)
	}

	sig := values.Get("sign")
	if sig == "" {
		return nil, fmt.Errorf("alipay: missing sign in callback")
	}

	// Build the string to verify (sorted params excluding sign and sign_type)
	signStr := g.buildSignString(values)
	if err := g.verifyRSA2(signStr, sig); err != nil {
		return nil, fmt.Errorf("alipay: verify signature: %w", err)
	}

	status := "failed"
	tradeStatus := values.Get("trade_status")
	if tradeStatus == "TRADE_SUCCESS" || tradeStatus == "TRADE_FINISHED" {
		status = "success"
	}

	var amount float64
	fmt.Sscanf(values.Get("total_amount"), "%f", &amount)

	return &CallbackResult{
		OrderNo:      values.Get("out_trade_no"),
		GatewayTxnID: values.Get("trade_no"),
		Amount:       amount,
		Status:       status,
		PaidAt:       values.Get("gmt_payment"),
	}, nil
}

// signParams 使用SHA256WithRSA（RSA2）算法对参数进行签名
func (g *AlipayGateway) signParams(params map[string]string) (string, error) {
	if g.privateKey == nil {
		return "", fmt.Errorf("alipay: private key not configured")
	}

	// Sort keys and build sign string
	keys := make([]string, 0, len(params))
	for k := range params {
		if k == "sign" || k == "sign_type" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte('&')
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(params[k])
	}

	hash := sha256.Sum256([]byte(sb.String()))
	sig, err := rsa.SignPKCS1v15(rand.Reader, g.privateKey, crypto.SHA256, hash[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// buildSignString 根据回调表单参数构建待验签字符串（排除sign和sign_type，字典序拼接）
func (g *AlipayGateway) buildSignString(values url.Values) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		if k == "sign" || k == "sign_type" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte('&')
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(values.Get(k))
	}
	return sb.String()
}

// verifyRSA2 验证RSA2（SHA256WithRSA）签名
func (g *AlipayGateway) verifyRSA2(content, signature string) error {
	if g.alipayPublicKey == nil {
		return fmt.Errorf("alipay: public key not configured")
	}
	sigBytes, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	hash := sha256.Sum256([]byte(content))
	return rsa.VerifyPKCS1v15(g.alipayPublicKey, crypto.SHA256, hash[:], sigBytes)
}

// parseRSAPrivateKeyFromBase64 从Base64编码的PKCS8字符串解析RSA私钥，兼容PEM格式
func parseRSAPrivateKeyFromBase64(b64 string) (*rsa.PrivateKey, error) {
	der, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		// Try PEM format
		block, _ := pem.Decode([]byte(b64))
		if block != nil {
			der = block.Bytes
		} else {
			return nil, fmt.Errorf("decode base64: %w", err)
		}
	}
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		pk, err2 := x509.ParsePKCS1PrivateKey(der)
		if err2 != nil {
			return nil, fmt.Errorf("parse private key: pkcs8=%v, pkcs1=%v", err, err2)
		}
		return pk, nil
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("not an RSA private key")
	}
	return rsaKey, nil
}

// parseRSAPublicKeyFromBase64 从Base64编码的字符串解析RSA公钥
func parseRSAPublicKeyFromBase64(b64 string) (*rsa.PublicKey, error) {
	der, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		block, _ := pem.Decode([]byte(b64))
		if block != nil {
			der = block.Bytes
		} else {
			return nil, fmt.Errorf("decode base64: %w", err)
		}
	}
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("not an RSA public key")
	}
	return rsaPub, nil
}
