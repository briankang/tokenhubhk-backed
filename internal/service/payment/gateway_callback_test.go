package payment

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// ====================================================================
// 网关回调解析集成测试
// 使用 ../../tests/testdata/ 下的真实 mock payload
// 验证 4 个网关的 parseXxxEvent + Bypass 解析链路正确
// ====================================================================

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	// 从 backend/internal/service/payment 回退到 backend/ 再进入 internal/tests/testdata
	paths := []string{
		filepath.Join("..", "..", "tests", "testdata", name),
		filepath.Join("..", "..", "..", "tests", "testdata", name),
	}
	for _, p := range paths {
		if b, err := os.ReadFile(p); err == nil {
			return b
		}
	}
	t.Fatalf("fixture not found: %s", name)
	return nil
}

// ==================== Stripe ====================

func TestStripe_CheckoutSessionCompleted(t *testing.T) {
	data := loadFixture(t, "stripe_webhook_payment_succeeded.json")
	r, err := parseStripeEvent(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if r.OrderNo != "ORD-2026041703170004" {
		t.Errorf("order_no = %q, want ORD-2026041703170004", r.OrderNo)
	}
	if r.Amount != 100 {
		t.Errorf("amount = %f, want 100", r.Amount)
	}
	if r.Status != "success" {
		t.Errorf("status = %q, want success", r.Status)
	}
	if r.GatewayTxnID != "pi_3Pqr3y4HxAbCdEf" {
		t.Errorf("txn_id = %q", r.GatewayTxnID)
	}
}

func TestStripe_PaymentIntentSucceeded(t *testing.T) {
	data := loadFixture(t, "stripe_webhook_payment_intent_succeeded.json")
	r, err := parseStripeEvent(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if r.OrderNo != "ORD-2026041703170010" {
		t.Errorf("order_no = %q", r.OrderNo)
	}
	if r.Amount != 100 {
		t.Errorf("amount = %f", r.Amount)
	}
	if r.Status != "success" {
		t.Errorf("status = %q", r.Status)
	}
}

func TestStripe_UnhandledEvent(t *testing.T) {
	data := []byte(`{"type":"customer.created","data":{"object":{}}}`)
	_, err := parseStripeEvent(data)
	if err == nil {
		t.Errorf("expected error for unhandled type")
	}
}

func TestStripe_InvalidJSON(t *testing.T) {
	_, err := parseStripeEvent([]byte(`not json`))
	if err == nil {
		t.Errorf("expected error for invalid json")
	}
}

// ==================== PayPal ====================

func TestPayPal_CaptureCompleted(t *testing.T) {
	data := loadFixture(t, "paypal_capture_completed.json")
	r, err := parsePayPalEvent(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if r.OrderNo != "ORD-2026041703170006" {
		t.Errorf("order_no = %q, want ORD-2026041703170006 (custom_id path)", r.OrderNo)
	}
	if r.Amount != 100 {
		t.Errorf("amount = %f", r.Amount)
	}
	if r.Status != "success" {
		t.Errorf("status = %q", r.Status)
	}
	if r.GatewayTxnID != "5XE89203BZ563460W" {
		t.Errorf("txn_id = %q", r.GatewayTxnID)
	}
}

func TestPayPal_OrderApproved(t *testing.T) {
	data := loadFixture(t, "paypal_order_approved.json")
	r, err := parsePayPalEvent(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if r.OrderNo != "ORD-2026041703170007" {
		t.Errorf("order_no = %q, want ORD-2026041703170007 (purchase_units.custom_id path)", r.OrderNo)
	}
	if r.Amount != 50 {
		t.Errorf("amount = %f", r.Amount)
	}
	if r.Status != "success" {
		t.Errorf("status = %q", r.Status)
	}
}

func TestPayPal_SaleCompleted(t *testing.T) {
	data := loadFixture(t, "paypal_ipn_completed.json")
	r, err := parsePayPalEvent(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Classic Sale 使用 invoice_number
	if r.OrderNo != "ORD-2026041703170005" {
		t.Errorf("order_no = %q, want ORD-2026041703170005 (invoice_number path)", r.OrderNo)
	}
	if r.Amount != 100 {
		t.Errorf("amount = %f", r.Amount)
	}
	if r.Status != "success" {
		t.Errorf("status = %q", r.Status)
	}
}

func TestPayPal_UnhandledEvent(t *testing.T) {
	data := []byte(`{"event_type":"CUSTOMER.DISPUTE.CREATED","resource":{}}`)
	_, err := parsePayPalEvent(data)
	if err == nil {
		t.Errorf("expected error for unhandled event type")
	}
}

// ==================== Alipay (bypass) ====================

func TestAlipay_BypassTradeSuccess(t *testing.T) {
	t.Setenv("PAYMENT_CALLBACK_DEV_BYPASS", "true")
	data := loadFixture(t, "alipay_callback_success.json")
	// Alipay 使用 form-encoded，但我们的 mock 是 JSON；需要转换
	// 实际回调是 form-data，这里测试 bypass 不验签直接解析 JSON 结构
	// 对 JSON mock 文件，使用 parseBypassAlipay 会失败（非 urlencoded）
	// 所以先用 urlencoded 字符串验证
	formData := []byte("out_trade_no=ORD-2026041703170001&total_amount=100.00&trade_status=TRADE_SUCCESS&trade_no=2026041722001417980510000001&gmt_payment=2026-04-17+03%3A17%3A05")
	_ = data
	r, err := parseBypassAlipay(formData)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if r.OrderNo != "ORD-2026041703170001" {
		t.Errorf("order_no = %q", r.OrderNo)
	}
	if r.Amount != 100 {
		t.Errorf("amount = %f", r.Amount)
	}
	if r.Status != "success" {
		t.Errorf("status = %q", r.Status)
	}
}

func TestAlipay_BypassTradeFinished(t *testing.T) {
	t.Setenv("PAYMENT_CALLBACK_DEV_BYPASS", "true")
	formData := []byte("out_trade_no=ORD-FIN-001&total_amount=50.00&trade_status=TRADE_FINISHED&trade_no=9999")
	r, err := parseBypassAlipay(formData)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if r.Status != "success" {
		t.Errorf("status = %q, TRADE_FINISHED should map to success", r.Status)
	}
}

func TestAlipay_BypassTradeClosed(t *testing.T) {
	formData := []byte("out_trade_no=ORD-CLS-001&trade_status=TRADE_CLOSED&total_amount=50.00")
	r, err := parseBypassAlipay(formData)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if r.Status != "failed" {
		t.Errorf("status = %q", r.Status)
	}
}

func TestAlipay_BypassMissingOrderNo(t *testing.T) {
	formData := []byte("trade_status=TRADE_SUCCESS&total_amount=10")
	_, err := parseBypassAlipay(formData)
	if err == nil {
		t.Errorf("expected error for missing out_trade_no")
	}
}

// ==================== WeChat (bypass, decrypted plaintext) ====================

func TestWechat_BypassDecryptedSuccess(t *testing.T) {
	data := loadFixture(t, "wechat_callback_decrypted.json")
	r, err := parseBypassWechat(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if r.OrderNo != "ORD-2026041703170003" {
		t.Errorf("order_no = %q", r.OrderNo)
	}
	if r.Amount != 100 {
		t.Errorf("amount = %f (expected 100 from total=10000 cents)", r.Amount)
	}
	if r.Status != "success" {
		t.Errorf("status = %q", r.Status)
	}
}

func TestWechat_BypassNonSuccess(t *testing.T) {
	data := []byte(`{"out_trade_no":"ORD-X","trade_state":"NOTPAY","amount":{"total":500}}`)
	r, err := parseBypassWechat(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if r.Status != "failed" {
		t.Errorf("NOTPAY should map to failed, got %q", r.Status)
	}
}

func TestWechat_BypassMissingOrderNo(t *testing.T) {
	_, err := parseBypassWechat([]byte(`{"trade_state":"SUCCESS"}`))
	if err == nil {
		t.Errorf("expected error for missing out_trade_no")
	}
}

// ==================== Bypass 环境变量开关 ====================

func TestCallbackDevBypass_Disabled(t *testing.T) {
	t.Setenv("PAYMENT_CALLBACK_DEV_BYPASS", "false")
	if IsCallbackDevBypassEnabled() {
		t.Errorf("should be disabled when env=false")
	}
}

func TestCallbackDevBypass_Enabled(t *testing.T) {
	t.Setenv("PAYMENT_CALLBACK_DEV_BYPASS", "true")
	if !IsCallbackDevBypassEnabled() {
		t.Errorf("should be enabled when env=true")
	}
}

func TestCallbackDevBypass_EnabledYes(t *testing.T) {
	t.Setenv("PAYMENT_CALLBACK_DEV_BYPASS", "yes")
	if !IsCallbackDevBypassEnabled() {
		t.Errorf("should be enabled when env=yes")
	}
}

func TestBypassVerifyCallback_Dispatch(t *testing.T) {
	t.Setenv("PAYMENT_CALLBACK_DEV_BYPASS", "true")
	ctx := context.Background()

	// Stripe
	stripeData := loadFixture(t, "stripe_webhook_payment_succeeded.json")
	r, err := BypassVerifyCallback(ctx, "stripe", stripeData)
	if err != nil || r.OrderNo == "" {
		t.Errorf("stripe dispatch: err=%v r=%v", err, r)
	}

	// PayPal
	paypalData := loadFixture(t, "paypal_capture_completed.json")
	r, err = BypassVerifyCallback(ctx, "paypal", paypalData)
	if err != nil || r.OrderNo == "" {
		t.Errorf("paypal dispatch: err=%v r=%v", err, r)
	}

	// WeChat
	wechatData := loadFixture(t, "wechat_callback_decrypted.json")
	r, err = BypassVerifyCallback(ctx, "wechat", wechatData)
	if err != nil || r.OrderNo == "" {
		t.Errorf("wechat dispatch: err=%v r=%v", err, r)
	}

	// Alipay
	alipayForm := []byte("out_trade_no=ORD-AP&total_amount=10&trade_status=TRADE_SUCCESS&trade_no=1")
	r, err = BypassVerifyCallback(ctx, "alipay", alipayForm)
	if err != nil || r.OrderNo != "ORD-AP" {
		t.Errorf("alipay dispatch: err=%v r=%v", err, r)
	}

	// Unsupported
	_, err = BypassVerifyCallback(ctx, "unknown", nil)
	if err == nil {
		t.Errorf("expected error for unsupported gateway")
	}
}

func TestBypassVerifyCallback_WhenDisabled(t *testing.T) {
	t.Setenv("PAYMENT_CALLBACK_DEV_BYPASS", "false")
	ctx := context.Background()
	_, err := BypassVerifyCallback(ctx, "stripe", []byte(`{}`))
	if err == nil {
		t.Errorf("expected error when bypass disabled")
	}
}
