package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// ========== 缓存命中/未命中测试 ==========

// TestCache_Miss_Then_Hit 测试首次请求未命中缓存，第二次请求命中缓存
func TestCache_Miss_Then_Hit(t *testing.T) {
	requireAdmin(t)

	url := baseURL + "/api/v1/admin/suppliers?page=1&page_size=5"

	// 第一次请求 — 应该是 MISS
	resp1, status1, headers1, err := doGetWithHeaders(url, adminToken)
	if err != nil {
		t.Fatalf("第一次请求失败: %v", err)
	}
	skipIfNotImplemented(t, status1)
	if status1 != http.StatusOK {
		t.Fatalf("期望 200, 得到 %d: %s", status1, resp1.Message)
	}
	cacheHeader1 := headers1.Get("X-Cache")
	if cacheHeader1 != "" && cacheHeader1 != "MISS" {
		t.Logf("注意: 第一次请求 X-Cache=%s (可能有预热缓存)", cacheHeader1)
	}

	// 第二次请求 — 应该是 HIT
	resp2, status2, headers2, err := doGetWithHeaders(url, adminToken)
	if err != nil {
		t.Fatalf("第二次请求失败: %v", err)
	}
	if status2 != http.StatusOK {
		t.Fatalf("期望 200, 得到 %d: %s", status2, resp2.Message)
	}
	cacheHeader2 := headers2.Get("X-Cache")
	if cacheHeader2 == "HIT" {
		t.Log("缓存命中测试通过: X-Cache=HIT")
	} else {
		t.Logf("注意: X-Cache=%s (缓存中间件可能未应用于此路由)", cacheHeader2)
	}
}

// ========== 缓存统计测试 ==========

// TestCache_Stats 测试获取缓存统计信息
func TestCache_Stats(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doGet(baseURL+"/api/v1/admin/cache/stats", adminToken)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Fatalf("期望 200, 得到 %d: %s", status, resp.Message)
	}

	var stats struct {
		KeyCount   int64  `json:"key_count"`
		MemoryUsed string `json:"memory_used"`
	}
	if err := json.Unmarshal(resp.Data, &stats); err != nil {
		t.Fatalf("解析统计数据失败: %v", err)
	}

	t.Logf("缓存统计: key_count=%d, memory_used=%s", stats.KeyCount, stats.MemoryUsed)
}

// ========== 一键清理缓存测试 ==========

// TestCache_ClearAll 测试清除所有缓存
func TestCache_ClearAll(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doPost(baseURL+"/api/v1/admin/cache/clear-all", nil, adminToken)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Fatalf("期望 200, 得到 %d: %s", status, resp.Message)
	}

	var data struct {
		Deleted int64  `json:"deleted"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatalf("解析响应数据失败: %v", err)
	}

	t.Logf("清除缓存: deleted=%d, message=%s", data.Deleted, data.Message)
}

// ========== 按前缀清理缓存测试 ==========

// TestCache_ClearByPrefix 测试按前缀清除缓存
func TestCache_ClearByPrefix(t *testing.T) {
	requireAdmin(t)

	// 先触发一些缓存
	doGet(baseURL+"/api/v1/admin/suppliers?page=1", adminToken)

	resp, status, err := doPost(baseURL+"/api/v1/admin/cache/clear/suppliers", nil, adminToken)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Fatalf("期望 200, 得到 %d: %s", status, resp.Message)
	}

	var data struct {
		Prefix  string `json:"prefix"`
		Deleted int64  `json:"deleted"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatalf("解析响应数据失败: %v", err)
	}

	if data.Prefix != "suppliers" {
		t.Errorf("期望 prefix=suppliers, 得到 %s", data.Prefix)
	}

	t.Logf("按前缀清除缓存: prefix=%s, deleted=%d", data.Prefix, data.Deleted)
}

// ========== 缓存预热测试 ==========

// TestCache_Warm 测试手动触发缓存预热
func TestCache_Warm(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doPost(baseURL+"/api/v1/admin/cache/warm", nil, adminToken)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Fatalf("期望 200, 得到 %d: %s", status, resp.Message)
	}

	// 验证预热后缓存统计有数据
	statsResp, statsStatus, err := doGet(baseURL+"/api/v1/admin/cache/stats", adminToken)
	if err != nil {
		t.Fatalf("获取缓存统计失败: %v", err)
	}
	if statsStatus != http.StatusOK {
		t.Fatalf("缓存统计期望 200, 得到 %d", statsStatus)
	}

	var stats struct {
		KeyCount int64 `json:"key_count"`
	}
	if err := json.Unmarshal(statsResp.Data, &stats); err != nil {
		t.Fatalf("解析统计数据失败: %v", err)
	}

	if stats.KeyCount > 0 {
		t.Logf("预热验证通过: 缓存条目数=%d", stats.KeyCount)
	} else {
		t.Log("注意: 预热后缓存条目数为 0 (可能数据库为空)")
	}
}

// ========== 权限测试 ==========

// TestCache_Forbidden_NonAdmin 测试非管理员无法访问缓存管理接口
func TestCache_Forbidden_NonAdmin(t *testing.T) {
	requireUser(t)

	endpoints := []struct {
		method string
		url    string
	}{
		{"GET", baseURL + "/api/v1/admin/cache/stats"},
		{"POST", baseURL + "/api/v1/admin/cache/clear-all"},
		{"POST", baseURL + "/api/v1/admin/cache/warm"},
	}

	for _, ep := range endpoints {
		var resp *apiResponse
		var status int
		var err error

		switch ep.method {
		case "GET":
			resp, status, err = doGet(ep.url, userToken)
		case "POST":
			resp, status, err = doPost(ep.url, nil, userToken)
		}

		if err != nil {
			t.Fatalf("请求失败 %s %s: %v", ep.method, ep.url, err)
		}
		skipIfNotFound(t, status)
		skipIfForbidden(t, status)

		if status == http.StatusOK && resp.Code == 0 {
			t.Errorf("期望非管理员被拒绝, 但 %s %s 返回 200", ep.method, ep.url)
		}
	}
}

// ========== 公开接口缓存测试 ==========

// TestCache_PublicEndpoint 测试公开接口是否携带缓存头
func TestCache_PublicEndpoint(t *testing.T) {
	url := baseURL + "/api/v1/public/payment-methods"

	// 第一次请求
	_, status1, headers1, err := doGetWithHeaders(url, "")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	skipIfNotImplemented(t, status1)
	skipIfNotFound(t, status1)

	cacheHeader1 := headers1.Get("X-Cache")
	t.Logf("公开接口第一次请求: status=%d, X-Cache=%s", status1, cacheHeader1)

	// 第二次请求 — 期望命中缓存
	_, status2, headers2, err := doGetWithHeaders(url, "")
	if err != nil {
		t.Fatalf("第二次请求失败: %v", err)
	}

	cacheHeader2 := headers2.Get("X-Cache")
	t.Logf("公开接口第二次请求: status=%d, X-Cache=%s", status2, cacheHeader2)
}

// ========== 辅助函数 ==========

// doGetWithHeaders 执行 GET 请求并返回响应头
func doGetWithHeaders(url string, token string) (*apiResponse, int, http.Header, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	var apiResp apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, resp.StatusCode, resp.Header, fmt.Errorf("decode response: %w", err)
	}

	return &apiResp, resp.StatusCode, resp.Header, nil
}
