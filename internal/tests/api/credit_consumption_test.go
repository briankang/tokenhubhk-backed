package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestCreditConsumptionDaily_Admin(t *testing.T) {
	requireAdmin(t)

	now := time.Now()
	start := now.AddDate(0, 0, -30).Format("2006-01-02")
	end := now.Format("2006-01-02")

	resp, status, err := doGet(baseURL+"/api/v1/admin/consumption/daily?page=1&page_size=10&group_by=date_user&start_date="+start+"&end_date="+end, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
	if _, err := parsePageData(resp); err != nil {
		t.Fatalf("parse page data: %v", err)
	}
}

func TestCreditConsumptionModelBreakdown_DateRange(t *testing.T) {
	requireAdmin(t)

	now := time.Now()
	start := now.AddDate(0, 0, -30).Format("2006-01-02")
	end := now.Format("2006-01-02")

	resp, status, err := doGet(baseURL+"/api/v1/admin/consumption/model-breakdown?user_id=1&start_date="+start+"&end_date="+end, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}

	var body struct {
		List []struct {
			Date         string `json:"date"`
			RequestModel string `json:"request_model"`
		} `json:"list"`
	}
	if err := json.Unmarshal(resp.Data, &body); err != nil {
		t.Fatalf("parse breakdown response: %v", err)
	}
}
