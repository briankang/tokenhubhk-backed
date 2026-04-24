package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestAdminReconciliationIncludesBillingSummary(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doGet(baseURL+"/api/v1/admin/reconciliation", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)
	if status != http.StatusOK || resp.Code != 0 {
		t.Fatalf("expected 200, got status=%d code=%d msg=%s", status, resp.Code, resp.Message)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(resp.Data, &raw); err != nil {
		t.Fatalf("parse reconciliation response: %v", err)
	}
	if _, ok := raw["billingReconciliation"]; !ok {
		t.Fatalf("reconciliation response missing billingReconciliation: %s", string(resp.Data))
	}

	var payload struct {
		BillingReconciliation struct {
			TotalRequests               int64   `json:"total_requests"`
			SettledRequests             int64   `json:"settled_requests"`
			DeductFailedRequests        int64   `json:"deduct_failed_requests"`
			EstimatedRequests           int64   `json:"estimated_requests"`
			ActualRevenueCredits        int64   `json:"actual_revenue_credits"`
			ActualRevenueRMB            float64 `json:"actual_revenue_rmb"`
			EstimatedCostCredits        int64   `json:"estimated_cost_credits"`
			FrozenCredits               int64   `json:"frozen_credits"`
			UnderCollectedCredits       int64   `json:"under_collected_credits"`
			UnderCollectedRMB           float64 `json:"under_collected_rmb"`
			PlatformCostRMB             float64 `json:"platform_cost_rmb"`
			GrossProfitRMB              float64 `json:"gross_profit_rmb"`
			MissingUsageRequests        int64   `json:"missing_usage_requests"`
			MissingPlatformCostRequests int64   `json:"missing_platform_cost_requests"`
		} `json:"billingReconciliation"`
	}
	if err := json.Unmarshal(resp.Data, &payload); err != nil {
		t.Fatalf("parse reconciliation response: %v", err)
	}
	if payload.BillingReconciliation.TotalRequests < 0 ||
		payload.BillingReconciliation.SettledRequests < 0 ||
		payload.BillingReconciliation.DeductFailedRequests < 0 ||
		payload.BillingReconciliation.EstimatedRequests < 0 ||
		payload.BillingReconciliation.ActualRevenueCredits < 0 ||
		payload.BillingReconciliation.EstimatedCostCredits < 0 ||
		payload.BillingReconciliation.FrozenCredits < 0 ||
		payload.BillingReconciliation.UnderCollectedCredits < 0 ||
		payload.BillingReconciliation.MissingUsageRequests < 0 ||
		payload.BillingReconciliation.MissingPlatformCostRequests < 0 {
		t.Fatalf("billing reconciliation counters should not be negative: %+v", payload.BillingReconciliation)
	}
}

func TestAdminBillingReconciliationSnapshotEndpoints(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doPost(baseURL+"/api/v1/admin/reconciliation/snapshots", nil, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)
	if status != http.StatusOK || resp.Code != 0 {
		t.Fatalf("expected snapshot create 200, got status=%d code=%d msg=%s", status, resp.Code, resp.Message)
	}
	var snap struct {
		Date                 string `json:"date"`
		ReconciliationHealth string `json:"reconciliation_health"`
	}
	if err := json.Unmarshal(resp.Data, &snap); err != nil {
		t.Fatalf("parse snapshot: %v", err)
	}
	if snap.Date == "" || snap.ReconciliationHealth == "" {
		t.Fatalf("snapshot should include date and health: %+v", snap)
	}

	listResp, listStatus, listErr := doGet(baseURL+"/api/v1/admin/reconciliation/snapshots?page=1&page_size=5", adminToken)
	if listErr != nil {
		t.Fatalf("list request failed: %v", listErr)
	}
	if listStatus != http.StatusOK || listResp.Code != 0 {
		t.Fatalf("expected snapshot list 200, got status=%d code=%d msg=%s", listStatus, listResp.Code, listResp.Message)
	}
	page, err := parsePageData(listResp)
	if err != nil {
		t.Fatalf("parse snapshot page: %v", err)
	}
	if page.Total < 1 {
		t.Fatalf("expected at least one snapshot after create, got total=%d", page.Total)
	}
}
