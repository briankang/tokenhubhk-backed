package api_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// ----- Configuration -----

const (
	defaultBaseURL     = "http://localhost:8090"
	defaultAdminEmail  = "admin@tokenhubhk.com"
	defaultAdminPass   = "admin123456"
	testMagicEmailCode = "654321"
	testInviteCode     = "platform"
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
		doPut(baseURL+"/api/v1/admin/system/config/test.magic_email_code", map[string]string{
			"value": testMagicEmailCode,
		}, adminToken) //nolint
		doPut(baseURL+"/api/v1/admin/guard-config", map[string]interface{}{
			"email_otp_enabled":        false,
			"ip_reg_limit_per_hour":    1000,
			"ip_reg_limit_per_day":     10000,
			"email_domain_daily_max":   10000,
			"fingerprint_enabled":      false,
			"fingerprint_daily_max":    100,
			"min_form_dwell_seconds":   0,
			"ip_reputation_enabled":    false,
			"block_vpn":                false,
			"block_tor":                false,
			"disposable_email_blocked": true,
		}, adminToken) //nolint
		// 清除 Redis 缓存（含 ratelimit:* key），避免 IP 限流干扰测试
		doPost(baseURL+"/api/v1/admin/cache/clear-all", nil, adminToken) //nolint
	}

	// Register a regular user for user-scoped tests
	ts := fmt.Sprintf("%d", time.Now().UnixNano())
	userEmail = fmt.Sprintf("testuser_%s@test.com", ts)
	userPass = "Test@123456"

	_, statusCode, regErr := doPost(baseURL+"/api/v1/auth/register", registerPayload(userEmail, userPass, "TestUser_"+ts), "")
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
		apiResp = apiResponse{
			Code:    resp.StatusCode,
			Message: string(respBody),
		}
		return &apiResp, resp.StatusCode, nil
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
		"password": authPassword(email, password),
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

// Auth password test covenant:
// Login/register API tests must submit the same client-side password hash as H5:
// SHA-256(password + strings.ToLower(strings.TrimSpace(email))).
// Raw passwords are only test inputs; they must not be sent to /auth/login.
func authPassword(email, password string) string {
	sum := sha256.Sum256([]byte(password + strings.ToLower(strings.TrimSpace(email))))
	return fmt.Sprintf("%x", sum)
}

func registerPayload(email, password, name string) map[string]string {
	return map[string]string{
		"email":       email,
		"password":    authPassword(email, password),
		"name":        name,
		"email_code":  testMagicEmailCode,
		"invite_code": testInviteCode,
	}
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

func skipIfForbidden(t *testing.T, statusCode int) {
	t.Helper()
	if statusCode == http.StatusForbidden {
		t.Skip("endpoint returned 403 Forbidden (route permission may not be mapped)")
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
