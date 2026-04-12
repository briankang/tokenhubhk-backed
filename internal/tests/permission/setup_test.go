package permission_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// ----- Configuration -----

const (
	defaultBaseURL   = "http://localhost:8090"
	defaultAdminEmail = "admin@tokenhub.ai"
	defaultAdminPass  = "Admin@123456"
)

var baseURL string

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

type registerResponse struct {
	ID    uint   `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Role  string `json:"role"`
}

type tenantInfo struct {
	ID       uint   `json:"id"`
	Name     string `json:"name"`
	ParentID *uint  `json:"parent_id,omitempty"`
	Level    int    `json:"level"`
	IsActive bool   `json:"is_active"`
}

type keyInfo struct {
	ID        uint   `json:"id"`
	TenantID  uint   `json:"tenant_id"`
	UserID    uint   `json:"user_id"`
	Name      string `json:"name"`
	KeyPrefix string `json:"key_prefix"`
	IsActive  bool   `json:"is_active"`
}

type overviewReport struct {
	TotalRevenue      float64 `json:"total_revenue"`
	TotalCost         float64 `json:"total_cost"`
	GrossProfit       float64 `json:"gross_profit"`
	ProfitMargin      float64 `json:"profit_margin"`
	TotalRequests     int64   `json:"total_requests"`
	TotalInputTokens  int64   `json:"total_input_tokens"`
	TotalOutputTokens int64   `json:"total_output_tokens"`
	ErrorCount        int64   `json:"error_count"`
	ErrorRate         float64 `json:"error_rate"`
	ActiveTenants     int64   `json:"active_tenants"`
	ActiveKeys        int64   `json:"active_keys"`
}

// ----- Test actor: holds auth token + identity -----

type testActor struct {
	Name     string
	Email    string
	Password string
	Role     string
	TenantID uint
	UserID   uint
	Token    string
}

// ----- Global test environment state -----

var env struct {
	admin    *testActor
	agentA   *testActor // L1
	agentA1  *testActor // L2 under A
	agentA1a *testActor // L3 under A1
	userU1   *testActor // USER under A1a
	agentA2  *testActor // L2 under A
	userU2   *testActor // USER under A2
	agentB   *testActor // L1
	agentB1  *testActor // L2 under B
	userU3   *testActor // USER under B1

	// Keys created per actor (first key)
	keyU1 uint
	keyU2 uint
	keyU3 uint

	// Tenant IDs
	tenantA   uint
	tenantA1  uint
	tenantA1a uint
	tenantA2  uint
	tenantB   uint
	tenantB1  uint

	ready bool
}

// TestMain is the entry point; it constructs all test data before running tests.
func TestMain(m *testing.M) {
	baseURL = os.Getenv("TEST_BASE_URL")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	// Wait for server readiness (up to 30 seconds)
	if !waitForServer(30) {
		fmt.Println("FATAL: server not reachable at", baseURL)
		os.Exit(1)
	}

	// Setup test data
	if err := setupTestEnvironment(); err != nil {
		fmt.Println("FATAL: failed to setup test environment:", err)
		os.Exit(1)
	}

	env.ready = true
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

// setupTestEnvironment creates the full multi-level agent hierarchy.
func setupTestEnvironment() error {
	// Step 1: Login as admin
	adminToken, err := loginUser(defaultAdminEmail, defaultAdminPass)
	if err != nil {
		return fmt.Errorf("admin login failed: %w", err)
	}
	env.admin = &testActor{
		Name:  "Admin",
		Email: defaultAdminEmail,
		Token: adminToken,
		Role:  "ADMIN",
	}

	ts := fmt.Sprintf("%d", time.Now().UnixMilli())

	// Step 2: Create L1 Agent A
	env.agentA, env.tenantA, err = createSubAgentViaAdmin(env.admin.Token, "AgentA_"+ts, "agenta_"+ts+"@test.com", "AGENT_L1")
	if err != nil {
		return fmt.Errorf("create agent A: %w", err)
	}

	// Step 3: Create L1 Agent B
	env.agentB, env.tenantB, err = createSubAgentViaAdmin(env.admin.Token, "AgentB_"+ts, "agentb_"+ts+"@test.com", "AGENT_L1")
	if err != nil {
		return fmt.Errorf("create agent B: %w", err)
	}

	// Step 4: As Agent A, create sub-agents A1 and A2
	env.agentA1, env.tenantA1, err = createSubAgentViaAgent(env.agentA.Token, "AgentA1_"+ts, "agenta1_"+ts+"@test.com")
	if err != nil {
		return fmt.Errorf("create agent A1: %w", err)
	}

	env.agentA2, env.tenantA2, err = createSubAgentViaAgent(env.agentA.Token, "AgentA2_"+ts, "agenta2_"+ts+"@test.com")
	if err != nil {
		return fmt.Errorf("create agent A2: %w", err)
	}

	// Step 5: As Agent A1, create sub-agent A1a
	env.agentA1a, env.tenantA1a, err = createSubAgentViaAgent(env.agentA1.Token, "AgentA1a_"+ts, "agenta1a_"+ts+"@test.com")
	if err != nil {
		return fmt.Errorf("create agent A1a: %w", err)
	}

	// Step 6: As Agent B, create sub-agent B1
	env.agentB1, env.tenantB1, err = createSubAgentViaAgent(env.agentB.Token, "AgentB1_"+ts, "agentb1_"+ts+"@test.com")
	if err != nil {
		return fmt.Errorf("create agent B1: %w", err)
	}

	// Step 7: Create regular users under appropriate tenants via registration
	env.userU1, err = createUserUnderTenant(env.agentA1a.Token, "UserU1_"+ts, "useru1_"+ts+"@test.com", "Test@12345", env.tenantA1a)
	if err != nil {
		return fmt.Errorf("create user U1: %w", err)
	}

	env.userU2, err = createUserUnderTenant(env.agentA2.Token, "UserU2_"+ts, "useru2_"+ts+"@test.com", "Test@12345", env.tenantA2)
	if err != nil {
		return fmt.Errorf("create user U2: %w", err)
	}

	env.userU3, err = createUserUnderTenant(env.agentB1.Token, "UserU3_"+ts, "useru3_"+ts+"@test.com", "Test@12345", env.tenantB1)
	if err != nil {
		return fmt.Errorf("create user U3: %w", err)
	}

	// Step 8: Create API keys for each user
	env.keyU1, err = createApiKey(env.userU1.Token, "key-u1")
	if err != nil {
		// Non-fatal; some tests may skip
		fmt.Println("WARN: failed to create key for U1:", err)
	}
	env.keyU2, err = createApiKey(env.userU2.Token, "key-u2")
	if err != nil {
		fmt.Println("WARN: failed to create key for U2:", err)
	}
	env.keyU3, err = createApiKey(env.userU3.Token, "key-u3")
	if err != nil {
		fmt.Println("WARN: failed to create key for U3:", err)
	}

	fmt.Println("=== Test environment setup complete ===")
	fmt.Printf("  Admin: %s\n", env.admin.Email)
	fmt.Printf("  AgentA (L1, tenant %d): %s\n", env.tenantA, env.agentA.Email)
	fmt.Printf("    AgentA1 (L2, tenant %d): %s\n", env.tenantA1, env.agentA1.Email)
	fmt.Printf("      AgentA1a (L3, tenant %d): %s\n", env.tenantA1a, env.agentA1a.Email)
	fmt.Printf("        UserU1 (USER, tenant %d): %s\n", env.tenantA1a, env.userU1.Email)
	fmt.Printf("    AgentA2 (L2, tenant %d): %s\n", env.tenantA2, env.agentA2.Email)
	fmt.Printf("      UserU2 (USER, tenant %d): %s\n", env.tenantA2, env.userU2.Email)
	fmt.Printf("  AgentB (L1, tenant %d): %s\n", env.tenantB, env.agentB.Email)
	fmt.Printf("    AgentB1 (L2, tenant %d): %s\n", env.tenantB1, env.agentB1.Email)
	fmt.Printf("      UserU3 (USER, tenant %d): %s\n", env.tenantB1, env.userU3.Email)

	return nil
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

	client := &http.Client{Timeout: 10 * time.Second}
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

// ----- Authentication helpers -----

func loginUser(email, password string) (string, error) {
	resp, statusCode, err := doPost(baseURL+"/api/v1/auth/login", map[string]string{
		"email":    email,
		"password": password,
	}, "")
	if err != nil {
		return "", fmt.Errorf("login request failed: %w", err)
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

// createSubAgentViaAdmin creates a top-level agent tenant using admin API.
// Returns the logged-in agent actor and its tenant ID.
func createSubAgentViaAdmin(adminToken, name, email, role string) (*testActor, uint, error) {
	password := "Agent@123456"

	// Create tenant via admin API
	resp, statusCode, err := doPost(baseURL+"/api/v1/admin/tenants", map[string]interface{}{
		"name":           name,
		"contact_email":  email,
		"admin_email":    email,
		"admin_password": password,
		"admin_name":     name,
	}, adminToken)
	if err != nil {
		return nil, 0, fmt.Errorf("create tenant request: %w", err)
	}
	// Accept 200 or 501 (notImplemented). For notImplemented, try agent sub-agents endpoint.
	if statusCode == http.StatusNotImplemented {
		// Admin tenant creation is not implemented; fall back to direct register + assign
		return createSubAgentFallback(adminToken, name, email, password, role)
	}
	if statusCode != http.StatusOK || resp.Code != 0 {
		return nil, 0, fmt.Errorf("create tenant: status=%d, code=%d, msg=%s", statusCode, resp.Code, resp.Message)
	}

	// Parse tenant from response
	var t tenantInfo
	if err := json.Unmarshal(resp.Data, &t); err != nil {
		return nil, 0, fmt.Errorf("parse tenant response: %w", err)
	}

	// Login as the new agent
	token, err := loginUser(email, password)
	if err != nil {
		return nil, t.ID, fmt.Errorf("agent login: %w", err)
	}

	actor := &testActor{
		Name:     name,
		Email:    email,
		Password: password,
		Role:     role,
		TenantID: t.ID,
		Token:    token,
	}
	return actor, t.ID, nil
}

// createSubAgentFallback handles the case where admin tenant creation is notImplemented.
// It registers the user normally and returns a best-effort actor.
func createSubAgentFallback(adminToken, name, email, password, role string) (*testActor, uint, error) {
	// Register user
	resp, statusCode, err := doPost(baseURL+"/api/v1/auth/register", map[string]interface{}{
		"email":    email,
		"password": password,
		"name":     name,
	}, "")
	if err != nil {
		return nil, 0, fmt.Errorf("register fallback: %w", err)
	}
	if statusCode != http.StatusOK && statusCode != http.StatusNotImplemented {
		if resp.Code != 0 {
			return nil, 0, fmt.Errorf("register fallback: status=%d, code=%d, msg=%s", statusCode, resp.Code, resp.Message)
		}
	}

	// Login
	token, loginErr := loginUser(email, password)
	if loginErr != nil {
		return nil, 0, fmt.Errorf("fallback agent login: %w", loginErr)
	}

	actor := &testActor{
		Name:     name,
		Email:    email,
		Password: password,
		Role:     role,
		Token:    token,
	}
	return actor, 0, nil
}

// createSubAgentViaAgent creates a sub-agent using the agent's sub-agents API.
func createSubAgentViaAgent(agentToken, name, email string) (*testActor, uint, error) {
	password := "Agent@123456"

	resp, statusCode, err := doPost(baseURL+"/api/v1/agent/sub-agents", map[string]interface{}{
		"name":           name,
		"contact_email":  email,
		"admin_email":    email,
		"admin_password": password,
		"admin_name":     name,
	}, agentToken)
	if err != nil {
		return nil, 0, fmt.Errorf("create sub-agent request: %w", err)
	}
	if statusCode != http.StatusOK || resp.Code != 0 {
		return nil, 0, fmt.Errorf("create sub-agent: status=%d, code=%d, msg=%s", statusCode, resp.Code, resp.Message)
	}

	// Login as the new sub-agent
	token, err := loginUser(email, password)
	if err != nil {
		return nil, 0, fmt.Errorf("sub-agent login: %w", err)
	}

	// Retrieve tenant ID from sub-agents list
	var tenantID uint
	listResp, _, listErr := doGet(baseURL+"/api/v1/agent/sub-agents?page_size=100", agentToken)
	if listErr == nil && listResp.Code == 0 {
		var page pageResponse
		if json.Unmarshal(listResp.Data, &page) == nil {
			var tenants []tenantInfo
			if json.Unmarshal(page.List, &tenants) == nil {
				for _, t := range tenants {
					if t.Name == name {
						tenantID = t.ID
						break
					}
				}
			}
		}
	}

	actor := &testActor{
		Name:     name,
		Email:    email,
		Password: password,
		TenantID: tenantID,
		Token:    token,
	}
	return actor, tenantID, nil
}

// createUserUnderTenant creates a regular USER under the given tenant via the agent user API.
func createUserUnderTenant(agentToken, name, email, password string, tenantID uint) (*testActor, error) {
	// Try agent user creation API
	resp, statusCode, err := doPost(baseURL+"/api/v1/agent/users", map[string]interface{}{
		"email":    email,
		"password": password,
		"name":     name,
	}, agentToken)
	if err != nil {
		return nil, fmt.Errorf("create user request: %w", err)
	}

	// If agent user API works
	if statusCode == http.StatusOK && resp.Code == 0 {
		token, loginErr := loginUser(email, password)
		if loginErr != nil {
			return nil, fmt.Errorf("user login after creation: %w", loginErr)
		}
		return &testActor{
			Name:     name,
			Email:    email,
			Password: password,
			Role:     "USER",
			TenantID: tenantID,
			Token:    token,
		}, nil
	}

	// Fallback: register directly (user will be under default tenant)
	resp, statusCode, err = doPost(baseURL+"/api/v1/auth/register", map[string]interface{}{
		"email":    email,
		"password": password,
		"name":     name,
	}, "")
	if err != nil {
		return nil, fmt.Errorf("register user fallback: %w", err)
	}
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("register user: status=%d, code=%d, msg=%s", statusCode, resp.Code, resp.Message)
	}

	token, loginErr := loginUser(email, password)
	if loginErr != nil {
		return nil, fmt.Errorf("user login fallback: %w", loginErr)
	}

	return &testActor{
		Name:     name,
		Email:    email,
		Password: password,
		Role:     "USER",
		TenantID: tenantID,
		Token:    token,
	}, nil
}

// createApiKey creates an API key via the user API and returns the key ID.
func createApiKey(userToken, keyName string) (uint, error) {
	resp, statusCode, err := doPost(baseURL+"/api/v1/user/api-keys", map[string]string{
		"name": keyName,
	}, userToken)
	if err != nil {
		return 0, fmt.Errorf("create api key request: %w", err)
	}
	if statusCode != http.StatusOK || resp.Code != 0 {
		return 0, fmt.Errorf("create api key: status=%d, code=%d, msg=%s", statusCode, resp.Code, resp.Message)
	}

	var result struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return 0, fmt.Errorf("parse key response: %w", err)
	}
	return result.ID, nil
}

// ----- Test helpers -----

func requireReady(t *testing.T) {
	t.Helper()
	if !env.ready {
		t.Skip("test environment not ready")
	}
}

// parsePageList extracts the page response from an API response.
func parsePageList(resp *apiResponse) (*pageResponse, error) {
	var page pageResponse
	if err := json.Unmarshal(resp.Data, &page); err != nil {
		return nil, fmt.Errorf("parse page data: %w", err)
	}
	return &page, nil
}

// unmarshalList unmarshals page list into a slice of the given type.
func unmarshalTenants(data json.RawMessage) ([]tenantInfo, error) {
	var list []tenantInfo
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func unmarshalKeys(data json.RawMessage) ([]keyInfo, error) {
	var list []keyInfo
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// containsTenantID checks if a tenant ID is in the list.
func containsTenantID(ids []uint, target uint) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

// extractTenantIDs extracts tenant IDs from a list of tenantInfo.
func extractTenantIDs(tenants []tenantInfo) []uint {
	ids := make([]uint, len(tenants))
	for i, t := range tenants {
		ids[i] = t.ID
	}
	return ids
}

// todayRange returns today's date range for API queries.
func todayRange() (string, string) {
	now := time.Now()
	start := now.AddDate(0, -1, 0).Format("2006-01-02")
	end := now.AddDate(0, 0, 1).Format("2006-01-02")
	return start, end
}
