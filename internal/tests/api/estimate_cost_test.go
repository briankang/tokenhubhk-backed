package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestV1EstimateCost(t *testing.T) {
	apiKey := ensureCodingAPIKey(t)

	modelName := "qwen-plus"
	if first := firstV1ModelID(t, apiKey); first != "" {
		modelName = first
	}

	body, status, err := doRawRequest("POST", baseURL+"/v1/estimate/cost", map[string]interface{}{
		"model":         modelName,
		"input_tokens":  100,
		"output_tokens": 50,
	}, apiKey)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if status == http.StatusNotFound {
		t.Skip("no priced model available for estimate")
	}
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, string(body))
	}

	var resp apiResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("parse response wrapper: %v, body=%s", err, string(body))
	}
	var payload struct {
		Model            string  `json:"model"`
		EstimatedCredits int64   `json:"estimated_credits"`
		EstimatedRMB     float64 `json:"estimated_rmb"`
		PricingUnit      string  `json:"pricing_unit"`
	}
	if err := json.Unmarshal(resp.Data, &payload); err != nil {
		t.Fatalf("parse estimate payload: %v, data=%s", err, string(resp.Data))
	}
	if payload.Model == "" {
		t.Fatal("model should not be empty")
	}
	if payload.EstimatedCredits < 0 || payload.EstimatedRMB < 0 {
		t.Fatalf("estimated cost should not be negative: %+v", payload)
	}
}

func firstV1ModelID(t *testing.T, apiKey string) string {
	t.Helper()
	body, status, err := doRawRequest("GET", baseURL+"/v1/models", nil, apiKey)
	if err != nil || status != http.StatusOK {
		return ""
	}
	var resp openAIModelListResp
	if err := json.Unmarshal(body, &resp); err != nil || len(resp.Data) == 0 {
		return ""
	}
	return resp.Data[0].ID
}
