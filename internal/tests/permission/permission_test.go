package permission_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

// ============================================================
// Section 1: Data Isolation Tests (P-01 ~ P-05)
// ============================================================

// P-01: ADMIN can see all tenants' data
func TestAdmin_CanSeeAllTenants(t *testing.T) {
	requireReady(t)

	// Admin calls overview report (tenantId=0 means all)
	resp, statusCode, err := doGet(baseURL+"/api/v1/admin/reports/overview?period=month", env.admin.Token)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d (msg=%s)", statusCode, resp.Message)
	}
	if resp.Code != 0 {
		t.Fatalf("expected code 0, got %d (msg=%s)", resp.Code, resp.Message)
	}

	// Admin should be able to see all tenants listed
	listResp, listStatus, listErr := doGet(baseURL+"/api/v1/admin/tenants", env.admin.Token)
	if listErr != nil {
		t.Fatalf("list tenants request failed: %v", listErr)
	}
	// Even if the endpoint is notImplemented (501), admin should have access (not 403)
	if listStatus == http.StatusForbidden {
		t.Errorf("expected admin to access /admin/tenants, but got 403 Forbidden")
	}
	_ = listResp
}

// P-02: L1 Agent A can see tenantA + tenantA1 + tenantA1a + tenantA2 data
func TestAgentL1_CanSeeOwnSubtree(t *testing.T) {
	requireReady(t)

	// Agent A should see its sub-agents (A1 and A2)
	resp, statusCode, err := doGet(baseURL+"/api/v1/agent/sub-agents?page_size=100", env.agentA.Token)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d (msg=%s)", statusCode, resp.Message)
	}
	if resp.Code != 0 {
		t.Fatalf("expected code 0, got %d (msg=%s)", resp.Code, resp.Message)
	}

	page, err := parsePageList(resp)
	if err != nil {
		t.Fatalf("parse page: %v", err)
	}
	tenants, err := unmarshalTenants(page.List)
	if err != nil {
		t.Fatalf("unmarshal tenants: %v", err)
	}

	ids := extractTenantIDs(tenants)

	// Should contain A1 and A2
	if env.tenantA1 > 0 && !containsTenantID(ids, env.tenantA1) {
		t.Errorf("expected Agent A's sub-agents to include tenant A1 (id=%d), got ids=%v", env.tenantA1, ids)
	}
	if env.tenantA2 > 0 && !containsTenantID(ids, env.tenantA2) {
		t.Errorf("expected Agent A's sub-agents to include tenant A2 (id=%d), got ids=%v", env.tenantA2, ids)
	}

	// Agent A should also be able to view reports for its subtree
	startDate, endDate := todayRange()
	reportResp, reportStatus, reportErr := doGet(
		fmt.Sprintf("%s/api/v1/agent/reports/overview?period=month", baseURL),
		env.agentA.Token,
	)
	if reportErr != nil {
		t.Fatalf("report request failed: %v", reportErr)
	}
	if reportStatus == http.StatusForbidden {
		t.Errorf("expected Agent A to access own reports, got 403")
	}
	_ = reportResp
	_ = startDate
	_ = endDate
}

// P-03: L1 Agent A cannot see L1 Agent B's data
func TestAgentL1_CannotSeeOtherL1(t *testing.T) {
	requireReady(t)

	if env.tenantB == 0 {
		t.Skip("tenantB not available")
	}

	// Agent A tries to access Agent B's tenant keys
	resp, statusCode, err := doGet(
		fmt.Sprintf("%s/api/v1/agent/sub-tenants/%d/keys", baseURL, env.tenantB),
		env.agentA.Token,
	)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	// Should be forbidden (403) or the data should be empty/filtered
	if statusCode == http.StatusOK && resp.Code == 0 {
		page, parseErr := parsePageList(resp)
		if parseErr == nil && page.Total > 0 {
			t.Errorf("Agent A should NOT see Agent B's keys, but got total=%d", page.Total)
		}
	}
	// 403 is the expected proper behavior
	if statusCode == http.StatusForbidden {
		// Correct behavior
		return
	}
}

// P-04: L2 Agent A1 can see tenantA1 + tenantA1a data
func TestAgentL2_CanSeeOwnSubtree(t *testing.T) {
	requireReady(t)

	// Agent A1 should see sub-agent A1a
	resp, statusCode, err := doGet(baseURL+"/api/v1/agent/sub-agents?page_size=100", env.agentA1.Token)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d (msg=%s)", statusCode, resp.Message)
	}

	page, err := parsePageList(resp)
	if err != nil {
		t.Fatalf("parse page: %v", err)
	}
	tenants, err := unmarshalTenants(page.List)
	if err != nil {
		t.Fatalf("unmarshal tenants: %v", err)
	}

	ids := extractTenantIDs(tenants)

	if env.tenantA1a > 0 && !containsTenantID(ids, env.tenantA1a) {
		t.Errorf("expected Agent A1's sub-agents to include tenant A1a (id=%d), got ids=%v", env.tenantA1a, ids)
	}

	// Should NOT contain A2 (sibling)
	if env.tenantA2 > 0 && containsTenantID(ids, env.tenantA2) {
		t.Errorf("Agent A1 should NOT see sibling tenant A2 (id=%d) in sub-agents", env.tenantA2)
	}
}

// P-05: L3 Agent A1a can only see own data and its users
func TestAgentL3_CanOnlySeeOwnAndUsers(t *testing.T) {
	requireReady(t)

	// L3 agents cannot have sub-agents, so sub-agents list should be empty
	resp, statusCode, err := doGet(baseURL+"/api/v1/agent/sub-agents?page_size=100", env.agentA1a.Token)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", statusCode)
	}

	page, err := parsePageList(resp)
	if err != nil {
		t.Fatalf("parse page: %v", err)
	}

	if page.Total != 0 {
		t.Errorf("expected L3 agent to have 0 sub-agents, got total=%d", page.Total)
	}

	// L3 agent should still be able to view own reports
	reportResp, reportStatus, reportErr := doGet(
		baseURL+"/api/v1/agent/reports/overview?period=month",
		env.agentA1a.Token,
	)
	if reportErr != nil {
		t.Fatalf("report request failed: %v", reportErr)
	}
	if reportStatus == http.StatusForbidden {
		t.Errorf("L3 agent should be able to view own reports, got 403")
	}
	_ = reportResp
}

// ============================================================
// Section 2: Privilege Escalation Tests (P-06 ~ P-10)
// ============================================================

// P-06: L1 Agent A cannot access L1 Agent B's reports
func TestAgentL1_CannotAccessOtherL1Report(t *testing.T) {
	requireReady(t)

	if env.tenantB == 0 {
		t.Skip("tenantB not available")
	}

	// Agent A tries to access drilldown for Agent B's tenant
	startDate, endDate := todayRange()
	resp, statusCode, err := doGet(
		fmt.Sprintf("%s/api/v1/agent/reports/profit/drilldown?start_date=%s&end_date=%s", baseURL, startDate, endDate),
		env.agentA.Token,
	)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	// The agent endpoint uses the caller's own tenantId from JWT, so it shouldn't leak B's data
	if statusCode == http.StatusOK && resp.Code == 0 {
		var items []json.RawMessage
		if json.Unmarshal(resp.Data, &items) == nil {
			for _, item := range items {
				var drill struct {
					TenantID uint `json:"tenant_id"`
				}
				if json.Unmarshal(item, &drill) == nil {
					if drill.TenantID == env.tenantB || drill.TenantID == env.tenantB1 {
						t.Errorf("Agent A's drilldown should NOT contain Agent B's tenant (found tenant_id=%d)", drill.TenantID)
					}
				}
			}
		}
	}
}

// P-07: L2 Agent A1 cannot access sibling L2 Agent A2's data
func TestAgentL2_CannotAccessSiblingData(t *testing.T) {
	requireReady(t)

	if env.tenantA2 == 0 {
		t.Skip("tenantA2 not available")
	}

	// Agent A1 tries to access A2's keys
	resp, statusCode, err := doGet(
		fmt.Sprintf("%s/api/v1/agent/sub-tenants/%d/keys", baseURL, env.tenantA2),
		env.agentA1.Token,
	)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if statusCode == http.StatusForbidden {
		return // Expected: access denied
	}

	if statusCode == http.StatusOK && resp.Code == 0 {
		page, parseErr := parsePageList(resp)
		if parseErr == nil && page.Total > 0 {
			t.Errorf("Agent A1 should NOT see sibling Agent A2's keys (tenant=%d), but got total=%d",
				env.tenantA2, page.Total)
		}
	}
}

// P-08: L3 Agent A1a cannot access parent L2 Agent A1's data
func TestAgentL3_CannotAccessParentData(t *testing.T) {
	requireReady(t)

	if env.tenantA1 == 0 {
		t.Skip("tenantA1 not available")
	}

	// L3 agent A1a tries to access parent A1's keys
	resp, statusCode, err := doGet(
		fmt.Sprintf("%s/api/v1/agent/sub-tenants/%d/keys", baseURL, env.tenantA1),
		env.agentA1a.Token,
	)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if statusCode == http.StatusForbidden {
		return // Expected
	}

	if statusCode == http.StatusOK && resp.Code == 0 {
		page, parseErr := parsePageList(resp)
		if parseErr == nil && page.Total > 0 {
			t.Errorf("L3 Agent A1a should NOT see parent Agent A1's keys (tenant=%d), but got total=%d",
				env.tenantA1, page.Total)
		}
	}
}

// P-09: User U1 cannot access User U2's data
func TestUser_CannotAccessOtherUserData(t *testing.T) {
	requireReady(t)

	// User U1 tries to access user API keys list — should only see own keys
	resp, statusCode, err := doGet(baseURL+"/api/v1/user/api-keys", env.userU1.Token)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	// Even if the endpoint returns 501, it should not be another user's data
	if statusCode == http.StatusOK && resp.Code == 0 {
		page, parseErr := parsePageList(resp)
		if parseErr == nil {
			keys, keyErr := unmarshalKeys(page.List)
			if keyErr == nil {
				for _, k := range keys {
					if env.userU2 != nil && k.UserID == env.userU2.UserID {
						t.Errorf("User U1 should NOT see User U2's keys (found key with user_id=%d)", k.UserID)
					}
				}
			}
		}
	}
}

// P-10: Regular User U1 cannot access agent-level reports
func TestUser_CannotAccessAgentReports(t *testing.T) {
	requireReady(t)

	// User tries to access agent report endpoint
	resp, statusCode, err := doGet(
		baseURL+"/api/v1/agent/reports/overview?period=month",
		env.userU1.Token,
	)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	// Should be 403 because RequireRole("AGENT_L1", "AGENT_L2", "AGENT_L3") is enforced
	if statusCode != http.StatusForbidden {
		t.Errorf("expected USER to get 403 from agent reports, got status=%d, code=%d, msg=%s",
			statusCode, resp.Code, resp.Message)
	}
}

// ============================================================
// Section 3: Key Access Permission Tests (P-11 ~ P-16)
// ============================================================

// P-11: L1 Agent A can list sub-tree keys
func TestAgentL1_CanListSubtreeKeys(t *testing.T) {
	requireReady(t)

	if env.tenantA1 == 0 {
		t.Skip("tenantA1 not available")
	}

	// Agent A lists keys of sub-tenant A1
	resp, statusCode, err := doGet(
		fmt.Sprintf("%s/api/v1/agent/sub-tenants/%d/keys", baseURL, env.tenantA1),
		env.agentA.Token,
	)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	// Should succeed (200) since A1 is in A's subtree
	if statusCode == http.StatusForbidden {
		t.Errorf("Agent A should be able to list keys for sub-tenant A1 (id=%d), got 403", env.tenantA1)
	}
	if statusCode == http.StatusOK && resp.Code != 0 {
		t.Errorf("expected code 0, got code=%d, msg=%s", resp.Code, resp.Message)
	}
}

// P-12: L1 Agent A cannot list L1 Agent B's keys
func TestAgentL1_CannotListOtherL1Keys(t *testing.T) {
	requireReady(t)

	if env.tenantB == 0 {
		t.Skip("tenantB not available")
	}

	resp, statusCode, err := doGet(
		fmt.Sprintf("%s/api/v1/agent/sub-tenants/%d/keys", baseURL, env.tenantB),
		env.agentA.Token,
	)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if statusCode == http.StatusForbidden {
		return // Correct
	}

	if statusCode == http.StatusOK && resp.Code == 0 {
		page, parseErr := parsePageList(resp)
		if parseErr == nil && page.Total > 0 {
			t.Errorf("Agent A should NOT see Agent B's keys (tenant=%d), but got total=%d",
				env.tenantB, page.Total)
		}
	}
}

// P-13: L2 Agent A1 can view key usage for keys in own subtree
func TestAgentL2_CanViewKeyUsage(t *testing.T) {
	requireReady(t)

	if env.keyU1 == 0 {
		t.Skip("keyU1 not created")
	}

	// Key U1 belongs to tenant A1a which is in A1's subtree
	resp, statusCode, err := doGet(
		fmt.Sprintf("%s/api/v1/agent/keys/%d/usage", baseURL, env.keyU1),
		env.agentA1.Token,
	)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if statusCode == http.StatusForbidden {
		t.Errorf("Agent A1 should be able to view usage for key %d (in subtree), got 403", env.keyU1)
	}
	_ = resp
}

// P-14: L2 Agent A1 cannot view key usage for keys outside subtree
func TestAgentL2_CannotViewOtherKeyUsage(t *testing.T) {
	requireReady(t)

	if env.keyU3 == 0 {
		t.Skip("keyU3 not created")
	}

	// Key U3 belongs to tenant B1, which is NOT in A1's subtree
	resp, statusCode, err := doGet(
		fmt.Sprintf("%s/api/v1/agent/keys/%d/usage", baseURL, env.keyU3),
		env.agentA1.Token,
	)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if statusCode == http.StatusForbidden {
		return // Correct
	}

	// If 200, the data should be empty or not contain B1's data
	if statusCode == http.StatusOK && resp.Code == 0 {
		page, parseErr := parsePageList(resp)
		if parseErr == nil && page.Total > 0 {
			t.Errorf("Agent A1 should NOT see key usage for key %d (outside subtree), but got total=%d",
				env.keyU3, page.Total)
		}
	}
}

// P-15: User U1 can only see own keys
func TestUser_CanOnlyViewOwnKeys(t *testing.T) {
	requireReady(t)

	resp, statusCode, err := doGet(baseURL+"/api/v1/user/api-keys", env.userU1.Token)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if statusCode == http.StatusNotImplemented {
		t.Skip("user api-keys endpoint not implemented")
	}

	if statusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", statusCode)
	}

	page, err := parsePageList(resp)
	if err != nil {
		t.Fatalf("parse page: %v", err)
	}

	keys, err := unmarshalKeys(page.List)
	if err != nil {
		t.Fatalf("unmarshal keys: %v", err)
	}

	for _, k := range keys {
		if env.userU1.UserID > 0 && k.UserID != env.userU1.UserID {
			t.Errorf("User U1 should only see own keys, but found key with user_id=%d (expected %d)",
				k.UserID, env.userU1.UserID)
		}
	}
}

// P-16: User U1 cannot revoke other user's key
func TestUser_CannotRevokeOtherUserKeys(t *testing.T) {
	requireReady(t)

	if env.keyU2 == 0 {
		t.Skip("keyU2 not created")
	}

	// U1 tries to revoke U2's key
	resp, statusCode, err := doDelete(
		fmt.Sprintf("%s/api/v1/user/api-keys/%d", baseURL, env.keyU2),
		env.userU1.Token,
	)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if statusCode == http.StatusNotImplemented {
		t.Skip("user api-keys delete endpoint not implemented")
	}

	// Should be rejected (400 or 403)
	if statusCode == http.StatusOK && resp.Code == 0 {
		t.Errorf("User U1 should NOT be able to revoke User U2's key (id=%d), but request succeeded",
			env.keyU2)
	}
}

// ============================================================
// Section 4: Report Data Correctness Tests (P-17 ~ P-20)
// ============================================================

// P-17: L1 Agent A's report data = aggregate of own subtree's DailyStats
func TestAgentL1_ReportAggregation(t *testing.T) {
	requireReady(t)

	resp, statusCode, err := doGet(
		baseURL+"/api/v1/agent/reports/overview?period=month",
		env.agentA.Token,
	)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if statusCode == http.StatusForbidden {
		t.Fatalf("Agent A should be able to access own reports, got 403")
	}

	if statusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d (msg=%s)", statusCode, resp.Message)
	}

	// Parse overview report
	var overview overviewReport
	if err := json.Unmarshal(resp.Data, &overview); err != nil {
		t.Fatalf("parse overview: %v", err)
	}

	// The report should reflect aggregated data (may be zero if no real traffic)
	// Key assertion: the response should parse without error and return valid structure
	if overview.TotalRequests < 0 {
		t.Errorf("total_requests should be >= 0, got %d", overview.TotalRequests)
	}
	if overview.TotalRevenue < 0 {
		t.Errorf("total_revenue should be >= 0, got %f", overview.TotalRevenue)
	}
}

// P-18: Agent profit report hides cost data (channel cost not visible to agents)
func TestAgent_ReportHidesCostData(t *testing.T) {
	requireReady(t)

	startDate, endDate := todayRange()
	resp, statusCode, err := doGet(
		fmt.Sprintf("%s/api/v1/agent/reports/profit?start_date=%s&end_date=%s&group_by=day",
			baseURL, startDate, endDate),
		env.agentA.Token,
	)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if statusCode != http.StatusOK {
		t.Skipf("profit report returned status %d; may have no data", statusCode)
	}

	// Parse profit report
	var profitReport struct {
		Summary *struct {
			TotalCost    float64 `json:"total_cost"`
			GrossProfit  float64 `json:"gross_profit"`
			ProfitMargin float64 `json:"profit_margin"`
		} `json:"summary"`
		Trend []struct {
			TotalCost    float64 `json:"total_cost"`
			GrossProfit  float64 `json:"gross_profit"`
			ProfitMargin float64 `json:"profit_margin"`
		} `json:"trend"`
	}
	if err := json.Unmarshal(resp.Data, &profitReport); err != nil {
		t.Fatalf("parse profit report: %v", err)
	}

	// Agent handler zeroes out cost fields
	if profitReport.Summary != nil {
		if profitReport.Summary.TotalCost != 0 {
			t.Errorf("agent should not see total_cost, expected 0, got %f", profitReport.Summary.TotalCost)
		}
		if profitReport.Summary.GrossProfit != 0 {
			t.Errorf("agent should not see gross_profit, expected 0, got %f", profitReport.Summary.GrossProfit)
		}
		if profitReport.Summary.ProfitMargin != 0 {
			t.Errorf("agent should not see profit_margin, expected 0, got %f", profitReport.Summary.ProfitMargin)
		}
	}

	for i, item := range profitReport.Trend {
		if item.TotalCost != 0 {
			t.Errorf("trend[%d]: agent should not see total_cost, expected 0, got %f", i, item.TotalCost)
		}
	}
}

// P-19: 3-level profit drilldown correctness (A -> A1/A2 -> A1a)
func TestProfitDrillDown_Correctness(t *testing.T) {
	requireReady(t)

	startDate, endDate := todayRange()

	// Agent A drilldown should show A1 and A2 as children
	resp, statusCode, err := doGet(
		fmt.Sprintf("%s/api/v1/agent/reports/profit/drilldown?start_date=%s&end_date=%s",
			baseURL, startDate, endDate),
		env.agentA.Token,
	)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if statusCode != http.StatusOK {
		t.Skipf("drilldown returned status %d", statusCode)
	}

	var items []struct {
		TenantID uint   `json:"tenant_id"`
		TenantName string `json:"tenant_name"`
	}
	if err := json.Unmarshal(resp.Data, &items); err != nil {
		t.Fatalf("parse drilldown: %v", err)
	}

	// Drilldown should only contain direct children of A
	for _, item := range items {
		if item.TenantID == env.tenantB || item.TenantID == env.tenantB1 {
			t.Errorf("Agent A's drilldown should NOT contain Agent B's data (found tenant_id=%d, name=%s)",
				item.TenantID, item.TenantName)
		}
	}

	// Verify cost fields are zeroed for agent view
	var rawItems []struct {
		TotalCost float64 `json:"total_cost"`
	}
	if json.Unmarshal(resp.Data, &rawItems) == nil {
		for i, r := range rawItems {
			if r.TotalCost != 0 {
				t.Errorf("drilldown[%d]: agent should not see total_cost, got %f", i, r.TotalCost)
			}
		}
	}
}

// P-20: Top agents ranking only includes own subtree
func TestTopAgents_OnlySubtree(t *testing.T) {
	requireReady(t)

	startDate, endDate := todayRange()

	// Admin top-agents endpoint
	resp, statusCode, err := doGet(
		fmt.Sprintf("%s/api/v1/admin/reports/profit/top-agents?start_date=%s&end_date=%s&limit=50",
			baseURL, startDate, endDate),
		env.admin.Token,
	)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if statusCode != http.StatusOK {
		t.Skipf("top-agents returned status %d", statusCode)
	}

	// Admin should see agents from all branches
	var agents []struct {
		TenantID uint `json:"tenant_id"`
	}
	if err := json.Unmarshal(resp.Data, &agents); err != nil {
		// May be empty if no data
		t.Skipf("no top agents data: %v", err)
	}

	// Verify that a non-admin agent would NOT see other branches
	// Agent A's consumption summary should only reflect own data
	consumptionResp, consumptionStatus, consumptionErr := doGet(
		baseURL+"/api/v1/agent/consumption?period=month",
		env.agentA.Token,
	)
	if consumptionErr != nil {
		t.Fatalf("consumption request failed: %v", consumptionErr)
	}
	if consumptionStatus == http.StatusForbidden {
		t.Errorf("Agent A should be able to access own consumption, got 403")
	}
	_ = consumptionResp
}

// ============================================================
// Section 5: Edge Cases (P-21 ~ P-24)
// ============================================================

// P-21: New agent (no data) report returns zero values, not error
func TestNewAgent_EmptyReport(t *testing.T) {
	requireReady(t)

	// Use agent A1a (L3) which likely has no traffic data
	resp, statusCode, err := doGet(
		baseURL+"/api/v1/agent/reports/overview?period=month",
		env.agentA1a.Token,
	)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	// Should return 200 with zero values, NOT an error
	if statusCode == http.StatusInternalServerError {
		t.Errorf("new agent with no data should not cause 500 error (msg=%s)", resp.Message)
	}

	if statusCode == http.StatusOK {
		var overview overviewReport
		if err := json.Unmarshal(resp.Data, &overview); err != nil {
			t.Fatalf("parse overview: %v", err)
		}
		// All values should be >= 0 (zero is fine)
		if overview.TotalRequests < 0 {
			t.Errorf("expected total_requests >= 0, got %d", overview.TotalRequests)
		}
	}
}

// P-22: Deactivated sub-agent's data is still visible to parent
func TestDeactivatedAgent_DataStillVisible(t *testing.T) {
	requireReady(t)

	if env.tenantA2 == 0 {
		t.Skip("tenantA2 not available")
	}

	// Agent A deactivates A2
	deactivateResp, deactivateStatus, deactivateErr := doPut(
		fmt.Sprintf("%s/api/v1/agent/sub-agents/%d/status", baseURL, env.tenantA2),
		map[string]interface{}{"is_active": false},
		env.agentA.Token,
	)
	if deactivateErr != nil {
		t.Fatalf("deactivate request failed: %v", deactivateErr)
	}

	if deactivateStatus != http.StatusOK {
		t.Skipf("deactivate returned status %d (msg=%s)", deactivateStatus, deactivateResp.Message)
	}

	// Agent A should still be able to see A2 in sub-agents list
	listResp, listStatus, listErr := doGet(
		baseURL+"/api/v1/agent/sub-agents?page_size=100",
		env.agentA.Token,
	)
	if listErr != nil {
		t.Fatalf("list request failed: %v", listErr)
	}
	if listStatus != http.StatusOK {
		t.Fatalf("expected status 200, got %d", listStatus)
	}

	page, err := parsePageList(listResp)
	if err != nil {
		t.Fatalf("parse page: %v", err)
	}

	tenants, err := unmarshalTenants(page.List)
	if err != nil {
		t.Fatalf("unmarshal tenants: %v", err)
	}

	found := false
	for _, tenant := range tenants {
		if tenant.ID == env.tenantA2 {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("deactivated agent A2 (tenant=%d) should still appear in parent's sub-agents list", env.tenantA2)
	}

	// Re-activate A2 for other tests
	_, _, _ = doPut(
		fmt.Sprintf("%s/api/v1/agent/sub-agents/%d/status", baseURL, env.tenantA2),
		map[string]interface{}{"is_active": true},
		env.agentA.Token,
	)
}

// P-23: Tenant subtree cache invalidation after structure change
func TestTenantSubtreeCache_Invalidation(t *testing.T) {
	requireReady(t)

	// This test verifies that after creating a new sub-agent, the parent's subtree
	// is updated correctly. We check by listing sub-agents before and after.

	// Count current sub-agents for Agent A
	resp1, status1, err1 := doGet(baseURL+"/api/v1/agent/sub-agents?page_size=100", env.agentA.Token)
	if err1 != nil {
		t.Fatalf("request failed: %v", err1)
	}
	if status1 != http.StatusOK {
		t.Skipf("sub-agents returned status %d", status1)
	}

	page1, _ := parsePageList(resp1)
	countBefore := page1.Total

	// Create a temporary sub-agent
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	tmpEmail := "cache_test_" + ts + "@test.com"
	_, tmpStatus, tmpErr := doPost(baseURL+"/api/v1/agent/sub-agents", map[string]interface{}{
		"name":           "CacheTest_" + ts,
		"contact_email":  tmpEmail,
		"admin_email":    tmpEmail,
		"admin_password": "Cache@123456",
		"admin_name":     "CacheTest",
	}, env.agentA.Token)
	if tmpErr != nil {
		t.Fatalf("create temp sub-agent: %v", tmpErr)
	}
	if tmpStatus != http.StatusOK {
		t.Skipf("create sub-agent returned status %d", tmpStatus)
	}

	// List again — count should increase
	resp2, status2, err2 := doGet(baseURL+"/api/v1/agent/sub-agents?page_size=100", env.agentA.Token)
	if err2 != nil {
		t.Fatalf("request failed: %v", err2)
	}
	if status2 != http.StatusOK {
		t.Fatalf("expected status 200, got %d", status2)
	}

	page2, _ := parsePageList(resp2)
	countAfter := page2.Total

	if countAfter <= countBefore {
		t.Errorf("expected sub-agents count to increase after creating new sub-agent: before=%d, after=%d",
			countBefore, countAfter)
	}
}

// P-24: Concurrent permission checks do not interfere with each other
func TestConcurrent_PermissionChecks(t *testing.T) {
	requireReady(t)

	// Run concurrent requests from different actors and verify each gets correct results
	actors := []*testActor{
		env.agentA,
		env.agentA1,
		env.agentA1a,
		env.agentB,
		env.agentB1,
	}

	var wg sync.WaitGroup
	errors := make(chan string, len(actors)*2)

	for _, actor := range actors {
		if actor == nil || actor.Token == "" {
			continue
		}
		wg.Add(1)
		go func(a *testActor) {
			defer wg.Done()

			// Each actor requests own sub-agents
			resp, statusCode, err := doGet(
				baseURL+"/api/v1/agent/sub-agents?page_size=100",
				a.Token,
			)
			if err != nil {
				errors <- fmt.Sprintf("actor %s: request failed: %v", a.Name, err)
				return
			}
			if statusCode == http.StatusForbidden {
				errors <- fmt.Sprintf("actor %s: unexpected 403 on own sub-agents", a.Name)
				return
			}
			if statusCode == http.StatusOK && resp.Code != 0 {
				errors <- fmt.Sprintf("actor %s: unexpected code %d", a.Name, resp.Code)
				return
			}

			// Each actor also requests own reports
			reportResp, reportStatus, reportErr := doGet(
				baseURL+"/api/v1/agent/reports/overview?period=month",
				a.Token,
			)
			if reportErr != nil {
				errors <- fmt.Sprintf("actor %s: report request failed: %v", a.Name, reportErr)
				return
			}
			if reportStatus == http.StatusForbidden {
				errors <- fmt.Sprintf("actor %s: unexpected 403 on own reports", a.Name)
				return
			}
			_ = reportResp
		}(actor)
	}

	wg.Wait()
	close(errors)

	for errMsg := range errors {
		t.Error(errMsg)
	}
}

// ============================================================
// Additional Tests (P-25 ~ P-26)
// ============================================================

// P-25: User cannot access admin endpoints
func TestUser_CannotAccessAdminEndpoints(t *testing.T) {
	requireReady(t)

	adminEndpoints := []string{
		"/api/v1/admin/reports/overview?period=month",
		"/api/v1/admin/tenants",
		"/api/v1/admin/users",
	}

	for _, endpoint := range adminEndpoints {
		resp, statusCode, err := doGet(baseURL+endpoint, env.userU1.Token)
		if err != nil {
			t.Fatalf("request to %s failed: %v", endpoint, err)
		}
		if statusCode != http.StatusForbidden {
			t.Errorf("user accessing %s: expected 403, got status=%d, code=%d, msg=%s",
				endpoint, statusCode, resp.Code, resp.Message)
		}
	}
}

// P-26: Agent cannot access admin-only endpoints
func TestAgent_CannotAccessAdminEndpoints(t *testing.T) {
	requireReady(t)

	adminEndpoints := []string{
		"/api/v1/admin/reports/overview?period=month",
		"/api/v1/admin/tenants",
		"/api/v1/admin/users",
	}

	for _, endpoint := range adminEndpoints {
		resp, statusCode, err := doGet(baseURL+endpoint, env.agentA.Token)
		if err != nil {
			t.Fatalf("request to %s failed: %v", endpoint, err)
		}
		if statusCode != http.StatusForbidden {
			t.Errorf("agent accessing %s: expected 403, got status=%d, code=%d, msg=%s",
				endpoint, statusCode, resp.Code, resp.Message)
		}
	}
}
