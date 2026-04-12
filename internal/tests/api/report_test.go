package api_test

import (
	"net/http"
	"testing"
	"time"
)

func TestOverviewReport_Admin(t *testing.T) {
	requireAdmin(t)

	resp, statusCode, err := doGet(baseURL+"/api/v1/admin/reports/overview?period=month", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
}

func TestUsageReport_Success(t *testing.T) {
	requireAdmin(t)

	now := time.Now()
	start := now.AddDate(0, -1, 0).Format("2006-01-02")
	end := now.AddDate(0, 0, 1).Format("2006-01-02")

	resp, statusCode, err := doGet(baseURL+"/api/v1/admin/reports/usage?start_date="+start+"&end_date="+end+"&group_by=day", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
}

func TestProfitReport_Success(t *testing.T) {
	requireAdmin(t)

	now := time.Now()
	start := now.AddDate(0, -1, 0).Format("2006-01-02")
	end := now.AddDate(0, 0, 1).Format("2006-01-02")

	resp, statusCode, err := doGet(baseURL+"/api/v1/admin/reports/profit?start_date="+start+"&end_date="+end, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
}

func TestProfitTrend_ByDay(t *testing.T) {
	requireAdmin(t)

	now := time.Now()
	start := now.AddDate(0, -1, 0).Format("2006-01-02")
	end := now.AddDate(0, 0, 1).Format("2006-01-02")

	resp, statusCode, err := doGet(baseURL+"/api/v1/admin/reports/profit/trend?start_date="+start+"&end_date="+end+"&group_by=day", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
}

func TestProfitTrend_ByMonth(t *testing.T) {
	requireAdmin(t)

	now := time.Now()
	start := now.AddDate(-1, 0, 0).Format("2006-01-02")
	end := now.AddDate(0, 0, 1).Format("2006-01-02")

	resp, statusCode, err := doGet(baseURL+"/api/v1/admin/reports/profit/trend?start_date="+start+"&end_date="+end+"&group_by=month", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
}

func TestTopAgents_Success(t *testing.T) {
	requireAdmin(t)

	now := time.Now()
	start := now.AddDate(0, -1, 0).Format("2006-01-02")
	end := now.AddDate(0, 0, 1).Format("2006-01-02")

	resp, statusCode, err := doGet(baseURL+"/api/v1/admin/reports/profit/top-agents?start_date="+start+"&end_date="+end+"&limit=10", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
}
