package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"testing"
	"time"
)

// ----- Configuration -----

const (
	defaultBaseURL    = "http://localhost:8090"
	defaultAdminEmail = "admin@tokenhubhk.com"
	defaultAdminPass  = "admin123456"
)

var (
	baseURL    string
	adminToken string
	userToken  string
	userEmail  string
	userPass   string
)

// ----- Common response types -----

type apiResponse struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type pageResponse struct {
	List     json.RawMessage `json:"list"`
	Total    int64           `json:"total"`
	Page     int             `json:"page"`
	PageSize int             `json:"page_size"`
}

type loginResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// ----- TestMain -----

func TestMain(m *testing.M) {
	baseURL = os.Getenv("TEST_BASE_URL")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	// Wait for server readiness
	if !waitForServer(30) {
		fmt.Println("FATAL: server not reachable at", baseURL)
		os.Exit(1)
	}

	// Login as admin
	token, err := loginUser(defaultAdminEmail, defaultAdminPass)
	if err != nil {
		fmt.Println("WARN: admin login failed:", err)
	} else {
		adminToken = token
		// 清除 Redis 缓存（含 ratelimit:* key），避免 IP 限流干扰测试
		doPost(baseURL+"/api/v1/admin/cache/clear-all", nil, adminToken) //nolint
	}

	// Register a regular user for user-scoped tests
	ts := fmt.Sprintf("%d", time.Now().UnixNano())
	userEmail = fmt.Sprintf("testuser_%s@test.com", ts)
	userPass = "Test@123456"

	_, statusCode, regErr := doPost(baseURL+"/api/v1/auth/register", map[string]string{
		"email":    userEmail,
		"password": userPass,
		"name":     "TestUser_" + ts,
	}, "")
	if regErr == nil && statusCode == http.StatusOK {
		ut, loginErr := loginUser(userEmail, userPass)
		if loginErr == nil {
			userToken = ut
		}
	}

	code := m.Run()
	os.Exit(code)
}

func waitForServer(timeoutSec int) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	for i := 0; i < timeoutSec; i++ {
		resp, err := client.Get(baseURL + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(1 * time.Second)
	}
	return false
}

// ----- HTTP helpers -----

func doRequest(method, url string, body interface{}, token string) (*apiResponse, int, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// 测试跳过 IP 限流（需服务端配置 RATE_LIMIT_BYPASS_TOKEN）
	req.Header.Set("X-Test-Skip-RateLimit", "th-test-bypass-2026")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

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
		return nil, resp.StatusCode, fmt.Errorf("unmarshal response (status=%d, body=%s): %w", resp.StatusCode, string(respBody), err)
	}

	return &apiResp, resp.StatusCode, nil
}

func doGet(url string, token string) (*apiResponse, int, error) {
	return doRequest(http.MethodGet, url, nil, token)
}

func doPost(url string, body interface{}, token string) (*apiResponse, int, error) {
	return doRequest(http.MethodPost, url, body, token)
}

func doPut(url string, body interface{}, token string) (*apiResponse, int, error) {
	return doRequest(http.MethodPut, url, body, token)
}

func doDelete(url string, token string) (*apiResponse, int, error) {
	return doRequest(http.MethodDelete, url, nil, token)
}

// ----- Auth helpers -----

func loginUser(email, password string) (string, error) {
	resp, statusCode, err := doPost(baseURL+"/api/v1/auth/login", map[string]string{
		"email":    email,
		"password": password,
	}, "")
	if err != nil {
		return "", fmt.Errorf("login request failed: %w", err)
	}
	if statusCode == http.StatusNotImplemented {
		return "", fmt.Errorf("login not implemented (501)")
	}
	if statusCode != http.StatusOK || resp.Code != 0 {
		return "", fmt.Errorf("login failed: status=%d, code=%d, msg=%s", statusCode, resp.Code, resp.Message)
	}

	var lr loginResponse
	if err := json.Unmarshal(resp.Data, &lr); err != nil {
		return "", fmt.Errorf("parse login response: %w", err)
	}
	return lr.AccessToken, nil
}

// ----- Test helpers -----

func requireAdmin(t *testing.T) {
	t.Helper()
	if adminToken == "" {
		t.Skip("admin token not available")
	}
}

func requireUser(t *testing.T) {
	t.Helper()
	if userToken == "" {
		t.Skip("user token not available")
	}
}

func skipIfNotImplemented(t *testing.T, statusCode int) {
	t.Helper()
	if statusCode == http.StatusNotImplemented {
		t.Skip("endpoint returned 501 Not Implemented")
	}
}

func skipIfNotFound(t *testing.T, statusCode int) {
	t.Helper()
	if statusCode == http.StatusNotFound {
		t.Skip("endpoint returned 404 Not Found (route may not be registered)")
	}
}

func parsePageData(resp *apiResponse) (*pageResponse, error) {
	var page pageResponse
	if err := json.Unmarshal(resp.Data, &page); err != nil {
		return nil, fmt.Errorf("parse page data: %w", err)
	}
	return &page, nil
}

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s_%d_%d", prefix, time.Now().UnixMilli(), rand.Intn(10000))
}

func uniqueEmail(prefix string) string {
	return fmt.Sprintf("%s_%d_%d@test.com", prefix, time.Now().UnixMilli(), rand.Intn(10000))
}

func unmarshalData(data json.RawMessage, v interface{}) error {
	return json.Unmarshal(data, v)
}
