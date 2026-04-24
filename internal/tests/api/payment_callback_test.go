package api_test

import (
	"net/http"
	"testing"
)

// ========== 支付回调测试（模拟各网关回调） ==========

func TestPaymentCallback_Wechat_InvalidSignature(t *testing.T) {
	// 微信支付回调 — 无有效签名应返回错误
	body := map[string]interface{}{
		"id":            "test-notification-id",
		"create_time":   "2026-04-13T12:00:00+08:00",
		"resource_type": "encrypt-resource",
		"event_type":    "TRANSACTION.SUCCESS",
		"resource": map[string]string{
			"algorithm":       "AEAD_AES_256_GCM",
			"ciphertext":      "fake-ciphertext",
			"nonce":           "fake-nonce",
			"associated_data": "transaction",
		},
	}

	resp, status, err := doPost(baseURL+"/api/v1/payment/callback/wechat", body, "")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)

	// 无效签名应该被拒绝，不应返回 500
	if status == http.StatusInternalServerError {
		t.Fatalf("server error on invalid wechat callback: %s", resp.Message)
	}
	t.Logf("wechat callback returned %d (expected rejection)", status)
}

func TestPaymentCallback_Alipay_InvalidSignature(t *testing.T) {
	body := map[string]interface{}{
		"trade_status": "TRADE_SUCCESS",
		"out_trade_no": "test-order-001",
		"total_amount": "100.00",
		"sign":         "invalid-signature",
		"sign_type":    "RSA2",
	}

	resp, status, err := doPost(baseURL+"/api/v1/payment/callback/alipay", body, "")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)

	if status == http.StatusInternalServerError {
		t.Fatalf("server error on invalid alipay callback: %s", resp.Message)
	}
	t.Logf("alipay callback returned %d (expected rejection)", status)
}

func TestPaymentCallback_Stripe_NoSignature(t *testing.T) {
	body := map[string]interface{}{
		"type": "checkout.session.completed",
		"data": map[string]interface{}{
			"object": map[string]string{
				"id":     "cs_test_fake",
				"status": "complete",
			},
		},
	}

	resp, status, err := doPost(baseURL+"/api/v1/payment/callback/stripe", body, "")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)

	if status == http.StatusInternalServerError {
		t.Fatalf("server error on stripe callback without signature: %s", resp.Message)
	}
	t.Logf("stripe callback returned %d (expected rejection)", status)
}

func TestPaymentCallback_PayPal_InvalidWebhook(t *testing.T) {
	body := map[string]interface{}{
		"event_type": "CHECKOUT.ORDER.COMPLETED",
		"resource": map[string]string{
			"id":     "fake-paypal-order-id",
			"status": "COMPLETED",
		},
	}

	resp, status, err := doPost(baseURL+"/api/v1/payment/callback/paypal", body, "")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)

	if status == http.StatusInternalServerError {
		t.Fatalf("server error on invalid paypal callback: %s", resp.Message)
	}
	t.Logf("paypal callback returned %d (expected rejection)", status)
}

// ========== 支付退款管理员测试 ==========

func TestAdminPaymentRefund_NonExistentOrder(t *testing.T) {
	requireAdmin(t)

	body := map[string]interface{}{
		"amount": 10.0,
		"reason": "test refund",
	}

	resp, status, err := doPost(baseURL+"/api/v1/admin/payment/refund/non-existent-order", body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	// 应返回 404 或 400，不应 500
	if status == http.StatusInternalServerError {
		t.Fatalf("server error on non-existent order refund: %s", resp.Message)
	}
	t.Logf("refund non-existent order returned %d", status)
}

func TestAdminPaymentRefund_UserForbidden(t *testing.T) {
	requireUser(t)

	body := map[string]interface{}{
		"amount": 10.0,
		"reason": "test refund",
	}

	_, status, err := doPost(baseURL+"/api/v1/admin/payment/refund/some-order", body, userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if status != http.StatusForbidden && status != http.StatusUnauthorized {
		t.Errorf("expected 403/401, got %d", status)
	}
}

// ========== Chat API 测试（/api/v1/chat/*） ==========

func TestChatCompletions_NoAuth(t *testing.T) {
	body := map[string]interface{}{
		"model": "gpt-4o",
		"messages": []map[string]string{
			{"role": "user", "content": "hi"},
		},
	}

	_, status, err := doPost(baseURL+"/api/v1/chat/completions", body, "")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)

	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		t.Errorf("chat completions without auth should return 401/403, got %d", status)
	}
}

func TestChatListModels_NoAuth(t *testing.T) {
	_, status, err := doGet(baseURL+"/api/v1/chat/models", "")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)

	// Chat models 可能需要 API key 或者允许公开访问
	if status == http.StatusInternalServerError {
		t.Fatalf("chat models should not return 500")
	}
}

func TestChatEmbeddings_NotImplemented(t *testing.T) {
	body := map[string]interface{}{
		"model": "text-embedding-3-small",
		"input": "hello world",
	}

	resp, status, err := doPost(baseURL+"/api/v1/chat/embeddings", body, "")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)

	// Embeddings 可能未实现（501）或需要认证（401）
	if status == http.StatusInternalServerError {
		t.Fatalf("embeddings should not return 500: %s", resp.Message)
	}
}

func TestChatOrchestrated_NoAuth(t *testing.T) {
	body := map[string]interface{}{
		"orchestration_id": 1,
		"messages": []map[string]string{
			{"role": "user", "content": "hi"},
		},
	}

	_, status, err := doPost(baseURL+"/api/v1/chat/orchestrated", body, "")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)

	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		t.Errorf("orchestrated chat without auth should return 401/403, got %d", status)
	}
}

// ========== 健康检查增强测试 ==========

func TestHealthCheck_Success(t *testing.T) {
	resp, status, err := doGet(baseURL+"/health", "")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("health check should return 200, got %d: %s", status, resp.Message)
	}
}

// ========== 公开端点测试 ==========

func TestPublicLocale_Success(t *testing.T) {
	resp, status, err := doGet(baseURL+"/api/v1/public/detect-locale", "")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Logf("detect-locale returned %d: %s", status, resp.Message)
	}
}

func TestPublicPaymentMethods_Success(t *testing.T) {
	resp, status, err := doGet(baseURL+"/api/v1/public/payment-methods", "")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Fatalf("public payment methods should return 200, got %d: %s", status, resp.Message)
	}
}

// ========== 边界情况测试 ==========

func TestAllPublicEndpoints_NoAuth_No500(t *testing.T) {
	endpoints := []struct {
		method string
		path   string
	}{
		{"GET", "/health"},
		{"GET", "/api/v1/public/models"},
		{"GET", "/api/v1/public/payment-methods"},
		{"GET", "/api/v1/public/agent-levels"},
		{"GET", "/api/v1/public/detect-locale"},
		{"GET", "/api/v1/public/whitelabel"},
	}

	for _, ep := range endpoints {
		resp, status, err := doGet(baseURL+ep.path, "")
		if err != nil {
			continue
		}
		if status == http.StatusInternalServerError {
			t.Errorf("public endpoint %s %s returned 500: %s", ep.method, ep.path, resp.Message)
		}
	}
}

func TestAllProtectedEndpoints_NoAuth_Returns401(t *testing.T) {
	endpoints := []string{
		"/api/v1/user/profile",
		"/api/v1/user/balance",
		"/api/v1/user/api-keys",
		"/api/v1/user/available-models",
		"/api/v1/admin/users",
		"/api/v1/agent/dashboard",
	}

	for _, ep := range endpoints {
		_, status, err := doGet(baseURL+ep, "")
		if err != nil {
			continue
		}
		if status != http.StatusUnauthorized && status != http.StatusForbidden && status != http.StatusNotFound {
			t.Errorf("protected endpoint %s without auth should return 401/403, got %d", ep, status)
		}
	}
}

func TestInvalidToken_Returns401(t *testing.T) {
	_, status, err := doGet(baseURL+"/api/v1/user/profile", "invalid-jwt-token-here")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if status != http.StatusUnauthorized {
		t.Errorf("expected 401 with invalid token, got %d", status)
	}
}
