package permission_test

import (
	"net/http"
	"strings"
	"testing"
)

func requireAdminToken(t *testing.T) {
	t.Helper()
	if env.admin == nil || env.admin.Token == "" {
		t.Skip("admin token not available")
	}
}

func TestCurrentRBAC_AdminCanAccessTenantList(t *testing.T) {
	requireAdminToken(t)

	resp, statusCode, err := doGet(baseURL+"/api/v1/admin/tenants?page=1&page_size=1", env.admin.Token)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if statusCode != http.StatusOK || resp.Code != 0 {
		t.Fatalf("admin tenant list should be allowed, got status=%d code=%d msg=%s", statusCode, resp.Code, resp.Message)
	}
}

func TestCurrentRBAC_AdminEndpointsRequireAuth(t *testing.T) {
	resp, statusCode, err := doGet(baseURL+"/api/v1/admin/tenants?page=1&page_size=1", "")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if statusCode != http.StatusUnauthorized && statusCode != http.StatusForbidden {
		t.Fatalf("anonymous admin access should be denied, got status=%d code=%d msg=%s", statusCode, resp.Code, resp.Message)
	}
}

func TestCurrentRBAC_LegacyAgentTreeAPIIsRemoved(t *testing.T) {
	requireAdminToken(t)

	resp, statusCode, err := doGet(baseURL+"/api/v1/agent/sub-agents?page_size=1", env.admin.Token)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if statusCode != http.StatusNotFound {
		t.Fatalf("legacy agent tree endpoint should remain removed in v4 RBAC mode, got status=%d code=%d msg=%s", statusCode, resp.Code, resp.Message)
	}
	if !strings.Contains(strings.ToLower(resp.Message), "not found") {
		t.Fatalf("expected not found response for legacy agent endpoint, got %q", resp.Message)
	}
}
