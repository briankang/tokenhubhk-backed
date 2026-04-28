package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

type modelOpsCalculatorDTO struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	IsActive bool   `json:"is_active"`
	Version  string `json:"version"`
	Source   string `json:"source"`
	Fields   []struct {
		Key     string      `json:"key"`
		Label   string      `json:"label"`
		Type    string      `json:"type"`
		Default interface{} `json:"default"`
	} `json:"fields"`
	Formula []string `json:"formula"`
}

type modelOpsProfilesDTO struct {
	List []struct {
		ID             uint   `json:"id"`
		ModelName      string `json:"model_name"`
		CalculatorType string `json:"calculator_type"`
		Price          struct {
			PricingUnit  string  `json:"pricing_unit"`
			SellingInput float64 `json:"selling_input"`
		} `json:"price"`
	} `json:"list"`
	Total int64 `json:"total"`
	Stats struct {
		Total          int `json:"total"`
		NeedCalculator int `json:"need_calculator"`
	} `json:"stats"`
}

func TestModelOpsCalculators_ListUpdateReset(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doGet(baseURL+"/api/v1/admin/model-ops/calculators", adminToken)
	if err != nil {
		t.Fatalf("list calculators failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}

	var calculators []modelOpsCalculatorDTO
	if err := json.Unmarshal(resp.Data, &calculators); err != nil {
		t.Fatalf("parse calculators: %v", err)
	}
	if len(calculators) == 0 {
		t.Fatal("expected default calculators")
	}
	first := calculators[0]
	if first.Type == "" || first.Name == "" || len(first.Fields) == 0 || len(first.Formula) == 0 {
		t.Fatalf("calculator missing required config fields: %#v", first)
	}

	updatedName := uniqueName("calc-api")
	updateResp, updateStatus, err := doPut(baseURL+"/api/v1/admin/model-ops/calculators/"+first.Type, map[string]interface{}{
		"name":          updatedName,
		"description":   "API test update",
		"model_types":   []string{"LLM", "VLM"},
		"pricing_units": []string{"per_million_tokens"},
		"fields": []map[string]interface{}{
			{"key": "input_tokens", "label": "Input Tokens", "type": "number", "default": 1000},
			{"key": "output_tokens", "label": "Output Tokens", "type": "number", "default": 500},
		},
		"formula":        []string{"input_cost = input_tokens / 1000000", "total = input_cost"},
		"accuracy_level": "api-test",
		"is_active":      false,
		"version":        "api-test-v1",
		"source":         "api-test",
	}, adminToken)
	if err != nil {
		t.Fatalf("update calculator failed: %v", err)
	}
	if updateStatus != http.StatusOK {
		t.Fatalf("expected update 200, got %d: %s", updateStatus, updateResp.Message)
	}
	var updated modelOpsCalculatorDTO
	if err := json.Unmarshal(updateResp.Data, &updated); err != nil {
		t.Fatalf("parse updated calculator: %v", err)
	}
	if updated.Name != updatedName || updated.IsActive {
		t.Fatalf("update did not persist name/status: %#v", updated)
	}

	resetResp, resetStatus, err := doPost(baseURL+"/api/v1/admin/model-ops/calculators/reset-defaults", nil, adminToken)
	if err != nil {
		t.Fatalf("reset calculators failed: %v", err)
	}
	if resetStatus != http.StatusOK {
		t.Fatalf("expected reset 200, got %d: %s", resetStatus, resetResp.Message)
	}
	var resetData struct {
		Updated int `json:"updated"`
	}
	if err := json.Unmarshal(resetResp.Data, &resetData); err != nil {
		t.Fatalf("parse reset response: %v", err)
	}
	if resetData.Updated <= 0 {
		t.Fatalf("expected positive reset count, got %d", resetData.Updated)
	}
}

func TestModelOpsCalculators_UpdateUnknownCode(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doPut(baseURL+"/api/v1/admin/model-ops/calculators/not-a-calculator", map[string]interface{}{
		"name": "missing",
	}, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if status != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", status, resp.Message)
	}
}

func TestModelOpsCalculators_CreateCustom(t *testing.T) {
	requireAdmin(t)

	code := "custom_" + uniqueName("calc")
	resp, status, err := doPost(baseURL+"/api/v1/admin/model-ops/calculators", map[string]interface{}{
		"type":           code,
		"name":           "Custom Calculator",
		"description":    "created by api test",
		"model_types":    []string{"LLM"},
		"pricing_units":  []string{"per_million_tokens"},
		"fields":         []map[string]interface{}{{"key": "input_tokens", "label": "Input Tokens", "type": "number", "default": 1000}},
		"formula":        []string{"total = input_tokens / 1000000 * input_unit_price"},
		"accuracy_level": "custom",
		"is_active":      true,
		"version":        "v1",
		"source":         "api-test",
	}, adminToken)
	if err != nil {
		t.Fatalf("create calculator failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected create 200, got %d: %s", status, resp.Message)
	}
	var created modelOpsCalculatorDTO
	if err := json.Unmarshal(resp.Data, &created); err != nil {
		t.Fatalf("parse created calculator: %v", err)
	}
	if created.Type == "" || created.Name != "Custom Calculator" || !created.IsActive {
		t.Fatalf("create did not persist calculator: %#v", created)
	}
}

func TestModelOpsProfilesAndPreviewAPI(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doGet(baseURL+"/api/v1/admin/model-ops/profiles?page=1&page_size=5", adminToken)
	if err != nil {
		t.Fatalf("profiles request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
	var profiles modelOpsProfilesDTO
	if err := json.Unmarshal(resp.Data, &profiles); err != nil {
		t.Fatalf("parse profiles: %v", err)
	}
	if profiles.Total < 0 || profiles.Stats.Total < 0 || profiles.Stats.NeedCalculator < 0 {
		t.Fatalf("invalid profile stats: %#v", profiles.Stats)
	}
	if len(profiles.List) == 0 {
		t.Skip("no model ops profiles available for calculate-preview")
	}

	previewResp, previewStatus, err := doPost(baseURL+"/api/v1/admin/model-ops/calculate-preview", map[string]interface{}{
		"model_id": profiles.List[0].ID,
		"params": map[string]interface{}{
			"input_tokens":  1000,
			"output_tokens": 500,
			"image_count":   1,
		},
	}, adminToken)
	if err != nil {
		t.Fatalf("preview request failed: %v", err)
	}
	if previewStatus != http.StatusOK {
		t.Fatalf("expected preview 200, got %d: %s", previewStatus, previewResp.Message)
	}
	var preview struct {
		ModelID        uint     `json:"model_id"`
		CalculatorType string   `json:"calculator_type"`
		OfficialAmount float64  `json:"official_amount"`
		SellingAmount  float64  `json:"selling_amount"`
		Formula        []string `json:"formula"`
	}
	if err := json.Unmarshal(previewResp.Data, &preview); err != nil {
		t.Fatalf("parse preview: %v", err)
	}
	if preview.ModelID != profiles.List[0].ID || preview.CalculatorType == "" {
		t.Fatalf("invalid preview response: %#v", preview)
	}
}

func TestModelOpsCalculatorsRequireAdminAuth(t *testing.T) {
	resp, status, err := doGet(baseURL+"/api/v1/admin/model-ops/calculators", "")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		t.Fatalf("expected 401/403 without admin auth, got %d: %s", status, resp.Message)
	}
}
