package integration_test

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
	defaultAdminEmail = "admin@tokenhub.ai"
	defaultAdminPass  = "Admin@123456"
)

var (
	baseURL    string
	adminToken string
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

	if !waitForServer(30) {
		fmt.Println("FATAL: server not reachable at", baseURL)
		os.Exit(1)
	}

	token, err := loginUser(defaultAdminEmail, defaultAdminPass)
	if err != nil {
		fmt.Println("WARN: admin login failed:", err)
	} else {
		adminToken = token
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
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 30 * time.Second}
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

func doGet(url, token string) (*apiResponse, int, error) {
	return doRequest(http.MethodGet, url, nil, token)
}

func doPost(url string, body interface{}, token string) (*apiResponse, int, error) {
	return doRequest(http.MethodPost, url, body, token)
}

func doPut(url string, body interface{}, token string) (*apiResponse, int, error) {
	return doRequest(http.MethodPut, url, body, token)
}

func doDelete(url, token string) (*apiResponse, int, error) {
	return doRequest(http.MethodDelete, url, nil, token)
}

func loginUser(email, password string) (string, error) {
	resp, statusCode, err := doPost(baseURL+"/api/v1/auth/login", map[string]string{
		"email":    email,
		"password": password,
	}, "")
	if err != nil {
		return "", fmt.Errorf("login request failed: %w", err)
	}
	if statusCode == http.StatusNotImplemented {
		return "", fmt.Errorf("login not implemented")
	}
	if statusCode != http.StatusOK || resp.Code != 0 {
		return "", fmt.Errorf("login failed: status=%d, code=%d, msg=%s", statusCode, resp.Code, resp.Message)
	}
	var lr loginResponse
	if err := json.Unmarshal(resp.Data, &lr); err != nil {
		return "", fmt.Errorf("parse login: %w", err)
	}
	return lr.AccessToken, nil
}

func registerUser(email, password, name string) error {
	resp, statusCode, err := doPost(baseURL+"/api/v1/auth/register", map[string]string{
		"email":    email,
		"password": password,
		"name":     name,
	}, "")
	if err != nil {
		return fmt.Errorf("register request: %w", err)
	}
	if statusCode == http.StatusNotImplemented {
		return fmt.Errorf("register not implemented")
	}
	if statusCode != http.StatusOK || resp.Code != 0 {
		return fmt.Errorf("register failed: status=%d, code=%d, msg=%s", statusCode, resp.Code, resp.Message)
	}
	return nil
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
		t.Skip("endpoint returned 404 Not Found")
	}
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
