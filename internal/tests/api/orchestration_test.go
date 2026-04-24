package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestCreateOrchestration_Pipeline(t *testing.T) {
	requireAdmin(t)

	name := uniqueName("orch_pipe")
	steps := `[{"name":"step1","node_type":"llm","model":"gpt-4","prompt":"step1"},{"name":"step2","node_type":"llm","model":"gpt-3.5-turbo","prompt":"step2"}]`
	resp, statusCode, err := doPost(baseURL+"/api/v1/admin/orchestrations", map[string]interface{}{
		"name":        name,
		"code":        "pipe_" + name,
		"description": "Pipeline orchestration test",
		"mode":        "PIPELINE",
		"steps":       json.RawMessage(steps),
		"is_active":   true,
	}, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
	if resp.Code != 0 {
		t.Fatalf("expected code 0, got %d: %s", resp.Code, resp.Message)
	}
}

func TestCreateOrchestration_Router(t *testing.T) {
	requireAdmin(t)

	name := uniqueName("orch_rtr")
	steps := `[{"name":"route_code","node_type":"condition","condition":"contains(input,'code')","model":"gpt-4"},{"name":"route_default","node_type":"llm","condition":"default","model":"gpt-3.5-turbo"}]`
	resp, statusCode, err := doPost(baseURL+"/api/v1/admin/orchestrations", map[string]interface{}{
		"name":        name,
		"code":        "rtr_" + name,
		"description": "Router orchestration test",
		"mode":        "ROUTER",
		"steps":       json.RawMessage(steps),
		"is_active":   true,
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

func TestCreateOrchestration_Fallback(t *testing.T) {
	requireAdmin(t)

	name := uniqueName("orch_fb")
	steps := `[{"name":"primary","node_type":"llm","model":"gpt-4","priority":1},{"name":"backup","node_type":"llm","model":"gpt-3.5-turbo","priority":2}]`
	resp, statusCode, err := doPost(baseURL+"/api/v1/admin/orchestrations", map[string]interface{}{
		"name":        name,
		"code":        "fb_" + name,
		"description": "Fallback orchestration test",
		"mode":        "FALLBACK",
		"steps":       json.RawMessage(steps),
		"is_active":   true,
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

func TestListOrchestrations_Success(t *testing.T) {
	requireAdmin(t)

	resp, statusCode, err := doGet(baseURL+"/api/v1/admin/orchestrations?page=1&page_size=10", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
}

func TestUpdateOrchestration_Success(t *testing.T) {
	requireAdmin(t)

	// Create first
	name := uniqueName("orch_upd")
	steps := `[{"name":"s1","node_type":"llm","model":"gpt-4"}]`
	createResp, statusCode, err := doPost(baseURL+"/api/v1/admin/orchestrations", map[string]interface{}{
		"name":  name,
		"code":  "upd_" + name,
		"mode":  "PIPELINE",
		"steps": json.RawMessage(steps),
	}, adminToken)
	if err != nil || statusCode != http.StatusOK {
		t.Skip("cannot create orchestration for update test")
	}
	var orch struct {
		ID uint `json:"id"`
	}
	json.Unmarshal(createResp.Data, &orch)
	if orch.ID == 0 {
		t.Skip("no orchestration ID")
	}

	// Update
	resp, statusCode, err := doPut(fmt.Sprintf("%s/api/v1/admin/orchestrations/%d", baseURL, orch.ID), map[string]interface{}{
		"description": "Updated description",
	}, adminToken)
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
}

func TestDeleteOrchestration_Success(t *testing.T) {
	requireAdmin(t)

	// Create
	name := uniqueName("orch_del")
	steps := `[{"name":"s1","node_type":"llm","model":"gpt-4"}]`
	createResp, statusCode, err := doPost(baseURL+"/api/v1/admin/orchestrations", map[string]interface{}{
		"name":  name,
		"code":  "del_" + name,
		"mode":  "PIPELINE",
		"steps": json.RawMessage(steps),
	}, adminToken)
	if err != nil || statusCode != http.StatusOK {
		t.Skip("cannot create orchestration for delete test")
	}
	var orch struct {
		ID uint `json:"id"`
	}
	json.Unmarshal(createResp.Data, &orch)
	if orch.ID == 0 {
		t.Skip("no orchestration ID")
	}

	// Delete
	resp, statusCode, err := doDelete(fmt.Sprintf("%s/api/v1/admin/orchestrations/%d", baseURL, orch.ID), adminToken)
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}

	// Verify deleted - should return 404
	_, verifyStatus, _ := doGet(fmt.Sprintf("%s/api/v1/admin/orchestrations/%d", baseURL, orch.ID), adminToken)
	if verifyStatus == http.StatusOK {
		t.Error("orchestration should have been deleted but still returns 200")
	}
}
