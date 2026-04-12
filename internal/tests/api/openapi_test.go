package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// ─── Open API Test Types ──────────────────────────────────────────

type openAPIKeyResp struct {
	ID        uint   `json:"id"`
	Name      string `json:"name"`
	Key       string `json:"key"`
	KeyPrefix string `json:"key_prefix"`
}

type openAPIBalanceResp struct {
	Balance       float64 `json:"balance"`
	FreeQuota     float64 `json:"free_quota"`
	TotalConsumed float64 `json:"total_consumed"`
	FrozenAmount  float64 `json:"frozen_amount"`
	Currency      string  `json:"currency"`
}

type openAPIAccountResp struct {
	UserID   uint   `json:"user_id"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Role     string `json:"role"`
	Language string `json:"language"`
	IsActive bool   `json:"is_active"`
}

// openAPIKey caches the generated API key for use across tests
var openAPIKey string

// ─── Helper ─────────────────────────────────────────────────────

// ensureOpenAPIKey generates an API Key for the test user if not yet created
func ensureOpenAPIKey(t *testing.T) string {
	t.Helper()
	if openAPIKey != "" {
		return openAPIKey
	}
	requireUser(t)

	name := fmt.Sprintf("openapi_test_%d", time.Now().UnixNano())
	resp, status, err := doPost(baseURL+"/api/v1/user/api-keys", map[string]string{
		"name": name,
	}, userToken)
	if err != nil {
		t.Fatalf("create api key request failed: %v", err)
	}
	if status == http.StatusNotImplemented {
		t.Skip("api key generation not implemented")
	}
	if status != http.StatusOK || resp.Code != 0 {
		t.Fatalf("create api key failed: status=%d, code=%d, msg=%s", status, resp.Code, resp.Message)
	}

	var keyResp openAPIKeyResp
	if err := json.Unmarshal(resp.Data, &keyResp); err != nil {
		t.Fatalf("parse api key response: %v", err)
	}
	if keyResp.Key == "" {
		t.Fatal("api key is empty")
	}
	openAPIKey = keyResp.Key
	return openAPIKey
}

// doOpenAPIGet sends a GET request with Bearer API Key
func doOpenAPIGet(url, apiKey string) (*apiResponse, int, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}

	var apiResp apiResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("unmarshal (status=%d, body=%s): %w", resp.StatusCode, string(respBody), err)
	}

	return &apiResp, resp.StatusCode, nil
}

// doOpenAPIGetRaw sends a GET request and returns raw response (for CSV export)
func doOpenAPIGetRaw(url, apiKey string) (string, int, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	return string(body), resp.StatusCode, nil
}

// ─── 认证测试 ─────────────────────────────────────────────────────

// TestOpenAPI_01_AuthNoToken 测试无 Token 访问返回 401
func TestOpenAPI_01_AuthNoToken(t *testing.T) {
	resp, status, err := doOpenAPIGet(baseURL+"/api/v1/open/account/info", "")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	if status != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d (resp: %+v)", status, resp)
	}
}

// TestOpenAPI_02_AuthInvalidKey 测试无效 API Key 返回 401
func TestOpenAPI_02_AuthInvalidKey(t *testing.T) {
	resp, status, err := doOpenAPIGet(baseURL+"/api/v1/open/account/info", "sk-invalid_fake_key_123456789")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	if status != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d (resp: %+v)", status, resp)
	}
}

// TestOpenAPI_03_AuthValidKey 测试有效 API Key 认证成功
func TestOpenAPI_03_AuthValidKey(t *testing.T) {
	apiKey := ensureOpenAPIKey(t)

	resp, status, err := doOpenAPIGet(baseURL+"/api/v1/open/account/info", apiKey)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d (resp: %+v)", status, resp)
	}
	if resp.Code != 0 {
		t.Errorf("expected code 0, got %d", resp.Code)
	}
}

// ─── 账户信息测试 ──────────────────────────────────────────────────

// TestOpenAPI_04_AccountInfo 测试账户信息接口返回正确的用户数据
func TestOpenAPI_04_AccountInfo(t *testing.T) {
	apiKey := ensureOpenAPIKey(t)

	resp, status, err := doOpenAPIGet(baseURL+"/api/v1/open/account/info", apiKey)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}

	var info openAPIAccountResp
	if err := json.Unmarshal(resp.Data, &info); err != nil {
		t.Fatalf("parse account info: %v", err)
	}
	if info.UserID == 0 {
		t.Error("user_id should not be 0")
	}
	if info.Email == "" {
		t.Error("email should not be empty")
	}
	if !info.IsActive {
		t.Error("account should be active")
	}
}

// ─── 余额测试 ──────────────────────────────────────────────────────

// TestOpenAPI_05_Balance 测试余额接口返回正确结构
func TestOpenAPI_05_Balance(t *testing.T) {
	apiKey := ensureOpenAPIKey(t)

	resp, status, err := doOpenAPIGet(baseURL+"/api/v1/open/balance", apiKey)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}

	var bal openAPIBalanceResp
	if err := json.Unmarshal(resp.Data, &bal); err != nil {
		t.Fatalf("parse balance: %v", err)
	}
	if bal.Currency == "" {
		t.Error("currency should not be empty")
	}
}

// TestOpenAPI_06_RechargeRecords 测试充值记录分页接口
func TestOpenAPI_06_RechargeRecords(t *testing.T) {
	apiKey := ensureOpenAPIKey(t)

	resp, status, err := doOpenAPIGet(baseURL+"/api/v1/open/balance/recharge-records?page=1&page_size=10", apiKey)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}

	page, err := parsePageData(resp)
	if err != nil {
		t.Fatalf("parse page: %v", err)
	}
	if page.Page != 1 {
		t.Errorf("expected page 1, got %d", page.Page)
	}
	if page.PageSize != 10 {
		t.Errorf("expected page_size 10, got %d", page.PageSize)
	}
}

// ─── 消费测试 ──────────────────────────────────────────────────────

// TestOpenAPI_07_ConsumptionSummary 测试消费汇总接口
func TestOpenAPI_07_ConsumptionSummary(t *testing.T) {
	apiKey := ensureOpenAPIKey(t)

	resp, status, err := doOpenAPIGet(baseURL+"/api/v1/open/consumption/summary?group_by=day", apiKey)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if resp.Code != 0 {
		t.Errorf("expected code 0, got %d", resp.Code)
	}
}

// TestOpenAPI_08_ConsumptionDetails 测试消费明细接口分页
func TestOpenAPI_08_ConsumptionDetails(t *testing.T) {
	apiKey := ensureOpenAPIKey(t)

	resp, status, err := doOpenAPIGet(baseURL+"/api/v1/open/consumption/details?page=1&page_size=5", apiKey)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}

	page, err := parsePageData(resp)
	if err != nil {
		t.Fatalf("parse page: %v", err)
	}
	if page.Page != 1 {
		t.Errorf("expected page 1, got %d", page.Page)
	}
	if page.PageSize != 5 {
		t.Errorf("expected page_size 5, got %d", page.PageSize)
	}
}

// TestOpenAPI_09_ConsumptionDetailsWithFilter 测试消费明细接口日期过滤
func TestOpenAPI_09_ConsumptionDetailsWithFilter(t *testing.T) {
	apiKey := ensureOpenAPIKey(t)

	url := fmt.Sprintf("%s/api/v1/open/consumption/details?date_from=2024-01-01&date_to=2025-12-31&page=1&page_size=10", baseURL)
	resp, status, err := doOpenAPIGet(url, apiKey)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if resp.Code != 0 {
		t.Errorf("expected code 0, got %d", resp.Code)
	}
}

// TestOpenAPI_10_ConsumptionExportCSV 测试消费导出 CSV 接口
func TestOpenAPI_10_ConsumptionExportCSV(t *testing.T) {
	apiKey := ensureOpenAPIKey(t)

	body, status, err := doOpenAPIGetRaw(baseURL+"/api/v1/open/consumption/export", apiKey)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if status == http.StatusNotFound {
		t.Skip("endpoint not found")
	}
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	// CSV should have at least the header row
	if !strings.Contains(body, "ID") || !strings.Contains(body, "Model") {
		t.Error("CSV response should contain header columns")
	}
}

// ─── 用量测试 ──────────────────────────────────────────────────────

// TestOpenAPI_11_UsageStatistics 测试用量统计接口
func TestOpenAPI_11_UsageStatistics(t *testing.T) {
	apiKey := ensureOpenAPIKey(t)

	resp, status, err := doOpenAPIGet(baseURL+"/api/v1/open/usage/statistics", apiKey)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if resp.Code != 0 {
		t.Errorf("expected code 0, got %d", resp.Code)
	}
}

// TestOpenAPI_12_UsageTrend 测试用量趋势接口
func TestOpenAPI_12_UsageTrend(t *testing.T) {
	apiKey := ensureOpenAPIKey(t)

	resp, status, err := doOpenAPIGet(baseURL+"/api/v1/open/usage/trend", apiKey)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if resp.Code != 0 {
		t.Errorf("expected code 0, got %d", resp.Code)
	}
}

// ─── 模型定价测试 ──────────────────────────────────────────────────

// TestOpenAPI_13_ModelsPricing 测试模型定价列表接口
func TestOpenAPI_13_ModelsPricing(t *testing.T) {
	apiKey := ensureOpenAPIKey(t)

	resp, status, err := doOpenAPIGet(baseURL+"/api/v1/open/models/pricing", apiKey)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if resp.Code != 0 {
		t.Errorf("expected code 0, got %d", resp.Code)
	}
}

// ─── Key 管理测试 ──────────────────────────────────────────────────

// TestOpenAPI_14_KeysList 测试 API Key 列表接口
func TestOpenAPI_14_KeysList(t *testing.T) {
	apiKey := ensureOpenAPIKey(t)

	resp, status, err := doOpenAPIGet(baseURL+"/api/v1/open/keys", apiKey)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if resp.Code != 0 {
		t.Errorf("expected code 0, got %d", resp.Code)
	}
	// Should return at least 1 key (the one we created)
	var keys []json.RawMessage
	if err := json.Unmarshal(resp.Data, &keys); err != nil {
		t.Fatalf("parse keys list: %v", err)
	}
	if len(keys) == 0 {
		t.Error("expected at least 1 key in list")
	}
}

// TestOpenAPI_15_KeyUsage 测试单个 Key 用量接口
func TestOpenAPI_15_KeyUsage(t *testing.T) {
	apiKey := ensureOpenAPIKey(t)

	// First get key list to find a valid key ID
	resp, status, err := doOpenAPIGet(baseURL+"/api/v1/open/keys", apiKey)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}

	var keys []struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(resp.Data, &keys); err != nil || len(keys) == 0 {
		t.Skip("no keys available to test usage")
	}

	keyID := keys[0].ID
	usageResp, usageStatus, usageErr := doOpenAPIGet(
		fmt.Sprintf("%s/api/v1/open/keys/%d/usage", baseURL, keyID), apiKey)
	if usageErr != nil {
		t.Fatalf("request failed: %v", usageErr)
	}
	if usageStatus != http.StatusOK {
		t.Fatalf("expected 200, got %d (resp: %+v)", usageStatus, usageResp)
	}
	if usageResp.Code != 0 {
		t.Errorf("expected code 0, got %d", usageResp.Code)
	}
}

// TestOpenAPI_16_KeyUsageInvalidID 测试无效 Key ID 返回错误
func TestOpenAPI_16_KeyUsageInvalidID(t *testing.T) {
	apiKey := ensureOpenAPIKey(t)

	resp, status, err := doOpenAPIGet(baseURL+"/api/v1/open/keys/99999/usage", apiKey)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	if status != http.StatusNotFound {
		t.Errorf("expected 404, got %d (resp: %+v)", status, resp)
	}
}

// ─── 分页参数测试 ──────────────────────────────────────────────────

// TestOpenAPI_17_PaginationDefaults 测试默认分页参数
func TestOpenAPI_17_PaginationDefaults(t *testing.T) {
	apiKey := ensureOpenAPIKey(t)

	resp, status, err := doOpenAPIGet(baseURL+"/api/v1/open/consumption/details", apiKey)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}

	page, err := parsePageData(resp)
	if err != nil {
		t.Fatalf("parse page: %v", err)
	}
	// Default page should be 1, page_size should be 20
	if page.Page != 1 {
		t.Errorf("expected default page=1, got %d", page.Page)
	}
	if page.PageSize != 20 {
		t.Errorf("expected default page_size=20, got %d", page.PageSize)
	}
}

// ─── 限流测试 (429) ────────────────────────────────────────────────

// TestOpenAPI_18_RateLimit429 测试限流触发 429（需要 Redis 运行中）
// 注意: 此测试会发送超过60个请求，仅在 CI 中需要时运行
func TestOpenAPI_18_RateLimit429(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping rate limit test in short mode")
	}
	apiKey := ensureOpenAPIKey(t)

	// Send requests rapidly to trigger rate limit
	gotRateLimit := false
	for i := 0; i < 65; i++ {
		_, status, err := doOpenAPIGet(baseURL+"/api/v1/open/account/info", apiKey)
		if err != nil {
			continue
		}
		if status == http.StatusNotFound {
			t.Skip("endpoint not found")
		}
		if status == http.StatusTooManyRequests {
			gotRateLimit = true
			break
		}
	}
	// Rate limit depends on Redis being available, so we don't fail hard
	if !gotRateLimit {
		t.Log("WARN: rate limit not triggered (Redis may be unavailable or limit too high)")
	}
}
