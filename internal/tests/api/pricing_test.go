package api_test

import (
	"net/http"
	"testing"
)

func TestSetModelPricing_Success(t *testing.T) {
	requireAdmin(t)

	resp, statusCode, err := doPost(baseURL+"/api/v1/admin/model-pricings", map[string]interface{}{
		"model_id":               1,
		"input_price_per_token":  0.00003,
		"output_price_per_token": 0.00006,
		"currency":               "USD",
	}, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
}

func TestGetPriceMatrix_Success(t *testing.T) {
	requireAdmin(t)

	resp, statusCode, err := doGet(baseURL+"/api/v1/admin/price-matrix", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
}

func TestSetLevelDiscount_Success(t *testing.T) {
	requireAdmin(t)

	resp, statusCode, err := doPost(baseURL+"/api/v1/admin/level-discounts", map[string]interface{}{
		"level":           1,
		"input_discount":  0.9,
		"output_discount": 0.9,
	}, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
}

func TestPriceCalculation_LevelDiscount(t *testing.T) {
	requireAdmin(t)

	resp, statusCode, err := doPost(baseURL+"/api/v1/admin/price-calculate", map[string]interface{}{
		"model_id":      1,
		"agent_level":   1,
		"input_tokens":  1000,
		"output_tokens": 500,
	}, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
}

func TestPriceCalculation_AgentCustom(t *testing.T) {
	requireAdmin(t)

	// First set an agent pricing
	_, statusCode, _ := doPost(baseURL+"/api/v1/admin/agent-pricings", map[string]interface{}{
		"tenant_id":    1,
		"model_id":     1,
		"pricing_type": "FIXED",
		"input_price":  floatPtr(0.00002),
		"output_price": floatPtr(0.00004),
	}, adminToken)
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	// Calculate with agent custom pricing
	resp, statusCode, err := doPost(baseURL+"/api/v1/admin/price-calculate", map[string]interface{}{
		"model_id":      1,
		"tenant_id":     1,
		"input_tokens":  1000,
		"output_tokens": 500,
	}, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
}

func TestPriceCalculation_Inheritance(t *testing.T) {
	requireAdmin(t)

	// Calculate for a tenant with no custom pricing (should inherit)
	resp, statusCode, err := doPost(baseURL+"/api/v1/admin/price-calculate", map[string]interface{}{
		"model_id":      1,
		"tenant_id":     9999,
		"agent_level":   2,
		"input_tokens":  1000,
		"output_tokens": 500,
	}, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
}

func floatPtr(f float64) *float64 {
	return &f
}
