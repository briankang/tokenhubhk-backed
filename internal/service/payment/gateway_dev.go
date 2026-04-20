package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// ====================================================================
// 网关回调开发旁路（PAYMENT_CALLBACK_DEV_BYPASS）
//
// 仅在以下两个条件同时满足时启用：
//  1. 环境变量 PAYMENT_CALLBACK_DEV_BYPASS=true
//  2. 网关 Provider 配置中 is_sandbox=true
//
// 启用后：
//  - 跳过所有签名/证书/webhook 验证
//  - 直接解析 payload 结构并映射到 CallbackResult
//  - 订单号/金额/状态从明文 payload 提取
//
// 生产环境必须保持 PAYMENT_CALLBACK_DEV_BYPASS=false（默认）
// ====================================================================

// IsCallbackDevBypassEnabled 判断是否启用开发旁路
func IsCallbackDevBypassEnabled() bool {
	v := strings.ToLower(os.Getenv("PAYMENT_CALLBACK_DEV_BYPASS"))
	return v == "true" || v == "1" || v == "yes"
}

// parseBypassWechat 旁路解析微信支付明文回调（用于开发测试，对应 wechat_callback_decrypted.json）
func parseBypassWechat(data []byte) (*CallbackResult, error) {
	var payment struct {
		OutTradeNo    string `json:"out_trade_no"`
		TransactionID string `json:"transaction_id"`
		TradeState    string `json:"trade_state"`
		SuccessTime   string `json:"success_time"`
		Amount        struct {
			Total int64 `json:"total"`
		} `json:"amount"`
	}
	if err := json.Unmarshal(data, &payment); err != nil {
		return nil, fmt.Errorf("wechat bypass: unmarshal: %w", err)
	}
	if payment.OutTradeNo == "" {
		return nil, fmt.Errorf("wechat bypass: missing out_trade_no")
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

// parseBypassAlipay 旁路解析支付宝表单回调（跳过签名）
func parseBypassAlipay(data []byte) (*CallbackResult, error) {
	values, err := url.ParseQuery(string(data))
	if err != nil {
		return nil, fmt.Errorf("alipay bypass: parse form: %w", err)
	}
	status := "failed"
	switch values.Get("trade_status") {
	case "TRADE_SUCCESS", "TRADE_FINISHED":
		status = "success"
	case "TRADE_CLOSED":
		status = "failed"
	}
	var amount float64
	fmt.Sscanf(values.Get("total_amount"), "%f", &amount)

	orderNo := values.Get("out_trade_no")
	if orderNo == "" {
		return nil, fmt.Errorf("alipay bypass: missing out_trade_no")
	}

	return &CallbackResult{
		OrderNo:      orderNo,
		GatewayTxnID: values.Get("trade_no"),
		Amount:       amount,
		Status:       status,
		PaidAt:       values.Get("gmt_payment"),
	}, nil
}

// BypassVerifyCallback 为开发旁路模式提供的无验签解析入口
// 按 gateway 类型分发到对应解析器
func BypassVerifyCallback(_ context.Context, gateway string, data []byte) (*CallbackResult, error) {
	if !IsCallbackDevBypassEnabled() {
		return nil, fmt.Errorf("dev bypass not enabled")
	}
	switch strings.ToLower(gateway) {
	case "wechat":
		return parseBypassWechat(data)
	case "alipay":
		return parseBypassAlipay(data)
	case "stripe":
		return parseStripeEvent(data)
	case "paypal":
		return parsePayPalEvent(data)
	default:
		return nil, fmt.Errorf("unsupported gateway: %s", gateway)
	}
}

// noopGuardForTime 避免 time import 被 lint 干掉（部分调用链可能移除）
var _ = time.Now
