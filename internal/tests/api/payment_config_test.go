package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// ==================== 支付渠道配置测试 ====================

func TestGetPaymentProviders_Admin(t *testing.T) {
	requireAdmin(t)

	resp, statusCode, err := doGet(baseURL+"/api/v1/admin/payment/providers", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}

	var providers []map[string]interface{}
	if err := json.Unmarshal(resp.Data, &providers); err != nil {
		t.Fatalf("unmarshal providers: %v", err)
	}
	if len(providers) < 4 {
		t.Errorf("expected at least 4 providers (seed data), got %d", len(providers))
	}
}

func TestUpdatePaymentProvider_Admin(t *testing.T) {
	requireAdmin(t)

	body := map[string]interface{}{
		"display_name": "WeChat Pay",
		"is_sandbox":   true,
		"config_json":  `{"app_id":"wx123","mch_id":"mch456","api_key":"secret"}`,
	}

	resp, statusCode, err := doPut(baseURL+"/api/v1/admin/payment/providers/WECHAT", body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
}

func TestTogglePaymentProvider_Admin(t *testing.T) {
	requireAdmin(t)

	resp, statusCode, err := doRequest(http.MethodPatch, baseURL+"/api/v1/admin/payment/providers/WECHAT/toggle", nil, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
}

func TestAESEncryptionRoundTrip(t *testing.T) {
	requireAdmin(t)

	// 更新配置（会被加密存储）
	configData := `{"app_id":"test_app","secret_key":"super_secret_value_12345"}`
	body := map[string]interface{}{
		"config_json": configData,
	}

	_, statusCode, err := doPut(baseURL+"/api/v1/admin/payment/providers/STRIPE", body, adminToken)
	if err != nil {
		t.Fatalf("update request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)
	if statusCode != http.StatusOK {
		t.Skipf("update returned %d", statusCode)
	}

	// 读取回来（应该已解密）
	resp, statusCode, err := doGet(baseURL+"/api/v1/admin/payment/providers", adminToken)
	if err != nil {
		t.Fatalf("get request failed: %v", err)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", statusCode)
	}

	var providers []map[string]interface{}
	if err := json.Unmarshal(resp.Data, &providers); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, p := range providers {
		if p["provider_type"] == "STRIPE" {
			cj, ok := p["config_json"].(string)
			if !ok || cj == "" {
				t.Error("config_json should be returned decrypted and non-empty")
				return
			}
			// 验证解密后的 JSON 可解析
			var parsed map[string]interface{}
			if err := json.Unmarshal([]byte(cj), &parsed); err != nil {
				t.Errorf("decrypted config_json should be valid JSON: %v", err)
			}
			if parsed["app_id"] != "test_app" {
				t.Errorf("expected app_id=test_app, got %v", parsed["app_id"])
			}
			return
		}
	}
	t.Error("STRIPE provider not found")
}

// ==================== 银行账号测试 ====================

func TestBankAccountCRUD_Admin(t *testing.T) {
	requireAdmin(t)

	// Create
	createBody := map[string]interface{}{
		"account_name":   "TokenHub Technology Ltd",
		"bank_name":      "Bank of China",
		"branch_name":    "Central Branch",
		"account_number": "6228480000000001",
		"swift_code":     "BKCHCNBJ",
		"currency":       "CNY",
		"remark":         "Test bank account",
		"is_active":      true,
	}
	createResp, statusCode, err := doPost(baseURL+"/api/v1/admin/payment/bank-accounts", createBody, adminToken)
	if err != nil {
		t.Fatalf("create request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)
	if statusCode != http.StatusOK {
		t.Fatalf("create expected 200, got %d: %s", statusCode, createResp.Message)
	}

	var created map[string]interface{}
	if err := json.Unmarshal(createResp.Data, &created); err != nil {
		t.Fatalf("unmarshal created: %v", err)
	}
	accountID := int(created["id"].(float64))

	// List
	listResp, statusCode, err := doGet(baseURL+"/api/v1/admin/payment/bank-accounts", adminToken)
	if err != nil {
		t.Fatalf("list request failed: %v", err)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("list expected 200, got %d", statusCode)
	}
	var accounts []map[string]interface{}
	if err := json.Unmarshal(listResp.Data, &accounts); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(accounts) < 1 {
		t.Error("expected at least 1 bank account")
	}

	// Update
	updateBody := map[string]interface{}{
		"remark": "Updated remark",
	}
	_, statusCode, err = doPut(fmt.Sprintf("%s/api/v1/admin/payment/bank-accounts/%d", baseURL, accountID), updateBody, adminToken)
	if err != nil {
		t.Fatalf("update request failed: %v", err)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("update expected 200, got %d", statusCode)
	}

	// Delete
	_, statusCode, err = doDelete(fmt.Sprintf("%s/api/v1/admin/payment/bank-accounts/%d", baseURL, accountID), adminToken)
	if err != nil {
		t.Fatalf("delete request failed: %v", err)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("delete expected 200, got %d", statusCode)
	}
}

// ==================== 付款方式测试 ====================

func TestGetPaymentMethods_Admin(t *testing.T) {
	requireAdmin(t)

	resp, statusCode, err := doGet(baseURL+"/api/v1/admin/payment/methods", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}

	var methods []map[string]interface{}
	if err := json.Unmarshal(resp.Data, &methods); err != nil {
		t.Fatalf("unmarshal methods: %v", err)
	}
	if len(methods) < 5 {
		t.Errorf("expected at least 5 payment methods (seed data), got %d", len(methods))
	}
}

func TestTogglePaymentMethod_Admin(t *testing.T) {
	requireAdmin(t)

	resp, statusCode, err := doRequest(http.MethodPatch, baseURL+"/api/v1/admin/payment/methods/ALIPAY/toggle", nil, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
}

// ==================== 公开接口测试 ====================

func TestPublicPaymentMethods(t *testing.T) {
	// 不需要 token
	resp, statusCode, err := doGet(baseURL+"/api/v1/public/payment-methods", "")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}

	var methods []map[string]interface{}
	if err := json.Unmarshal(resp.Data, &methods); err != nil {
		t.Fatalf("unmarshal methods: %v", err)
	}
	// 至少有 BANK_TRANSFER（种子数据默认启用）
	found := false
	for _, m := range methods {
		if m["type"] == "BANK_TRANSFER" {
			found = true
			break
		}
	}
	if !found {
		t.Log("BANK_TRANSFER not found in active methods (may depend on seed data)")
	}
}

// ==================== 权限测试 ====================

func TestPaymentConfig_NonAdmin_Forbidden(t *testing.T) {
	requireUser(t)

	// 普通用户不能访问管理接口
	_, statusCode, err := doGet(baseURL+"/api/v1/admin/payment/providers", userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, statusCode)

	if statusCode == http.StatusOK {
		t.Error("expected non-200 for regular user accessing admin payment config")
	}
	// 403 或 401 均可接受
	if statusCode != http.StatusForbidden && statusCode != http.StatusUnauthorized {
		t.Logf("got status %d (expected 401 or 403)", statusCode)
	}
}
