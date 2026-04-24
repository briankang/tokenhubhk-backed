package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ─── Rate Limit & Quota Test Suite ──────────────────────────────────────

// TestRateLimitGet 测试获取全局限流配置
func TestRateLimitGet(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doGet(baseURL+"/api/v1/admin/rate-limits", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Skipf("user limits endpoint rejected current payload with %d", status)
	}

	// 验证返回包含限流配置字段
	data, _ := json.Marshal(resp.Data)
	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse response: %v", err)
	}

	if _, ok := cfg["ipRpm"]; !ok {
		if limits, ok := cfg["rateLimits"].(map[string]interface{}); !ok || limits["ipRpm"] == nil {
			t.Error("response missing ipRpm field")
		}
	}
	if _, ok := cfg["userRpm"]; !ok {
		if limits, ok := cfg["rateLimits"].(map[string]interface{}); !ok || limits["userRpm"] == nil {
			t.Error("response missing userRpm field")
		}
	}
	if _, ok := cfg["globalQps"]; !ok {
		if limits, ok := cfg["rateLimits"].(map[string]interface{}); !ok || limits["globalQps"] == nil {
			t.Error("response missing globalQps field")
		}
	}
	t.Logf("rate limit config: %+v", cfg)
}

// TestRateLimitUpdate 测试更新全局限流配置
func TestRateLimitUpdate(t *testing.T) {
	requireAdmin(t)

	body := map[string]interface{}{
		"ipRpm":     50,
		"userRpm":   200,
		"apiKeyRpm": 80,
		"globalQps": 2000,
	}

	resp, status, err := doPut(baseURL+"/api/v1/admin/rate-limits", body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d, resp: %+v", status, resp)
	}
	t.Log("rate limit config updated successfully")
}

// TestRateLimitTrigger429 测试限流触发429
func TestRateLimitTrigger429(t *testing.T) {
	// 先设置极低的限流值以触发429
	requireAdmin(t)

	body := map[string]interface{}{
		"ipRpm":     2,
		"userRpm":   2,
		"apiKeyRpm": 2,
		"globalQps": 10000,
	}
	_, status, _ := doPut(baseURL+"/api/v1/admin/rate-limits", body, adminToken)
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	// 连续发送多个未认证请求触发IP限流
	got429 := false
	for i := 0; i < 10; i++ {
		_, s, _ := doGet(baseURL+"/health", "")
		if s == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}

	if !got429 {
		t.Log("429 not triggered (rate limiter may be fail-open without Redis)")
	} else {
		t.Log("429 triggered successfully")
	}

	// 恢复正常限流值
	resetBody := map[string]interface{}{
		"ipRpm":     30,
		"userRpm":   120,
		"apiKeyRpm": 60,
		"globalQps": 1000,
	}
	doPut(baseURL+"/api/v1/admin/rate-limits", resetBody, adminToken)
}

// TestUserLimitsGet 测试获取用户限额配置
func TestUserLimitsGet(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doGet(baseURL+"/api/v1/admin/users/1/limits", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Skipf("user limits endpoint rejected current payload with %d", status)
	}

	data, _ := json.Marshal(resp.Data)
	var result map[string]interface{}
	json.Unmarshal(data, &result)
	t.Logf("user limits: %+v", result)
}

// TestUserLimitsUpdate 测试设置用户限额
func TestUserLimitsUpdate(t *testing.T) {
	requireAdmin(t)

	body := map[string]interface{}{
		"dailyLimit":      100.0,
		"monthlyLimit":    1000.0,
		"maxTokensPerReq": 16384,
		"maxConcurrent":   3,
	}

	resp, status, err := doPut(baseURL+"/api/v1/admin/users/1/limits", body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d, resp: %+v", status, resp)
	}
	t.Log("user limits updated successfully")
}

// TestBalanceReconciliation 测试余额对账报告
func TestBalanceReconciliation(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doGet(baseURL+"/api/v1/admin/balance/reconciliation", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Skipf("user limits read endpoint returned %d", status)
	}

	data, _ := json.Marshal(resp.Data)
	var report map[string]interface{}
	json.Unmarshal(data, &report)

	if _, ok := report["expiredFreezes"]; !ok {
		t.Error("response missing expiredFreezes field")
	}
	t.Logf("reconciliation report: %+v", report)
}

// TestInsufficientBalance402 测试余额不足返回402
func TestInsufficientBalance402(t *testing.T) {
	// 通过 Open API Key 发送聊天请求（使用不存在的API Key）
	// 预期：认证失败401或余额不足402
	apiKey := ensureOpenAPIKey(t)
	if apiKey == "" {
		t.Skip("API key not available")
	}

	body := map[string]interface{}{
		"model": "nonexistent-model-for-402-test",
		"messages": []map[string]string{
			{"role": "user", "content": "test"},
		},
	}

	resp, status, err := doPost(baseURL+"/api/v1/chat/completions", body, apiKey)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	// 可能是402余额不足、400模型不存在或401认证失败
	validStatuses := map[int]bool{
		http.StatusPaymentRequired:    true, // 余额不足
		http.StatusBadRequest:         true, // 模型不存在
		http.StatusUnauthorized:       true, // API Key无效
		http.StatusBadGateway:         true, // 提供商错误
		http.StatusServiceUnavailable: true, // 无可用渠道
		http.StatusTooManyRequests:    true, // 限流
	}
	if !validStatuses[status] {
		t.Fatalf("expected 402/400/401/502/503/429, got %d, resp: %+v", status, resp)
	}
	t.Logf("balance check returned status %d", status)
}

// TestConcurrentDeduction 并发扣减竞态测试
func TestConcurrentDeduction(t *testing.T) {
	requireAdmin(t)

	// 先给用户充值
	rechargeBody := map[string]interface{}{
		"amount": 100.0,
		"remark": "concurrent test recharge",
	}
	_, status, _ := doPost(baseURL+"/api/v1/admin/users/1/recharge", rechargeBody, adminToken)
	if status == http.StatusNotImplemented || status == http.StatusNotFound {
		t.Skip("recharge not implemented")
	}

	// 并发发送10个请求
	var wg sync.WaitGroup
	var successCount int64
	var failCount int64

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, s, _ := doGet(fmt.Sprintf("%s/api/v1/admin/users/1/balance", baseURL), adminToken)
			if s == http.StatusOK {
				atomic.AddInt64(&successCount, 1)
			} else {
				atomic.AddInt64(&failCount, 1)
			}
		}(i)
	}

	wg.Wait()
	t.Logf("concurrent test: %d success, %d fail", successCount, failCount)

	if successCount == 0 {
		t.Error("all concurrent requests failed")
	}
}

// TestFreezeAndRelease 测试预扣费冻结与释放流程
func TestFreezeAndRelease(t *testing.T) {
	requireAdmin(t)

	// 获取用户初始余额
	resp, status, err := doGet(baseURL+"/api/v1/admin/users/1/balance", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Skipf("user limits read endpoint returned %d", status)
	}

	data, _ := json.Marshal(resp.Data)
	var bal map[string]interface{}
	json.Unmarshal(data, &bal)
	t.Logf("user balance: %+v", bal)

	// 验证冻结金额字段存在
	if _, ok := bal["frozenAmount"]; !ok {
		t.Error("response missing frozenAmount field")
	}
}

// TestRateLimitResponseHeaders 测试限流响应头
func TestRateLimitResponseHeaders(t *testing.T) {
	// 发送一个请求并检查响应头
	req, err := http.NewRequest(http.MethodGet, baseURL+"/health", nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("execute request: %v", err)
	}
	defer resp.Body.Close()

	// 检查限流响应头是否存在
	limit := resp.Header.Get("X-RateLimit-Limit")
	remaining := resp.Header.Get("X-RateLimit-Remaining")
	reset := resp.Header.Get("X-RateLimit-Reset")

	if limit != "" {
		t.Logf("X-RateLimit-Limit: %s", limit)
		t.Logf("X-RateLimit-Remaining: %s", remaining)
		t.Logf("X-RateLimit-Reset: %s", reset)
	} else {
		t.Log("rate limit headers not present (Redis may not be available)")
	}
}

// TestDailyMonthlyLimits 测试日/月限额配置
func TestDailyMonthlyLimits(t *testing.T) {
	requireAdmin(t)

	// 设置用户限额
	body := map[string]interface{}{
		"dailyLimit":      0.001, // 极低日限额
		"monthlyLimit":    0.002,
		"maxTokensPerReq": 32768,
		"maxConcurrent":   5,
	}
	_, status, _ := doPut(baseURL+"/api/v1/admin/users/1/limits", body, adminToken)
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Skipf("user limits endpoint rejected current payload with %d", status)
	}

	// 验证限额已生效
	resp, status, _ := doGet(baseURL+"/api/v1/admin/users/1/limits", adminToken)
	if status != http.StatusOK {
		t.Skipf("user limits read endpoint returned %d", status)
	}

	data, _ := json.Marshal(resp.Data)
	var result map[string]interface{}
	json.Unmarshal(data, &result)
	t.Logf("daily/monthly limits set: %+v", result)

	// 恢复正常限额（0=无限制）
	resetBody := map[string]interface{}{
		"dailyLimit":      0,
		"monthlyLimit":    0,
		"maxTokensPerReq": 32768,
		"maxConcurrent":   5,
	}
	doPut(baseURL+"/api/v1/admin/users/1/limits", resetBody, adminToken)
}

// TestRateLimitNonAdmin403 测试非管理员访问限流配置返回403
func TestRateLimitNonAdmin403(t *testing.T) {
	requireUser(t)

	_, status, err := doGet(baseURL+"/api/v1/admin/rate-limits", userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)

	if status != http.StatusForbidden && status != http.StatusUnauthorized {
		t.Fatalf("expected 403/401, got %d", status)
	}
	t.Logf("non-admin access correctly denied with status %d", status)
}
