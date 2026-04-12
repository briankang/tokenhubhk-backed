package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// ----- Channel CRUD -----

func TestCreateChannel_Success(t *testing.T) {
	requireAdmin(t)

	name := uniqueName("channel")
	resp, statusCode, err := doPost(baseURL+"/api/v1/admin/channels", map[string]interface{}{
		"name":        name,
		"supplier_id": 1,
		"type":        "openai",
		"endpoint":    "https://api.openai.com/v1",
		"api_key":     "sk-test-key-" + name,
		"weight":      10,
		"priority":    1,
		"status":      "active",
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

	var ch struct {
		ID   uint   `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(resp.Data, &ch); err != nil {
		t.Fatalf("parse channel: %v", err)
	}
	if ch.ID == 0 {
		t.Error("expected non-zero channel ID")
	}
}

func TestListChannels_Success(t *testing.T) {
	requireAdmin(t)

	resp, statusCode, err := doGet(baseURL+"/api/v1/admin/channels?page=1&page_size=10", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
}

func TestUpdateChannel_Success(t *testing.T) {
	requireAdmin(t)

	// Create a channel first
	name := uniqueName("ch_upd")
	createResp, statusCode, err := doPost(baseURL+"/api/v1/admin/channels", map[string]interface{}{
		"name":        name,
		"supplier_id": 1,
		"type":        "openai",
		"endpoint":    "https://api.openai.com/v1",
		"api_key":     "sk-test-" + name,
		"weight":      5,
		"priority":    1,
		"status":      "active",
	}, adminToken)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)
	if statusCode != http.StatusOK {
		t.Skipf("create channel returned %d, skipping update test", statusCode)
	}

	var ch struct{ ID uint `json:"id"` }
	json.Unmarshal(createResp.Data, &ch)
	if ch.ID == 0 {
		t.Skip("no channel ID returned")
	}

	// Update
	resp, statusCode, err := doPut(fmt.Sprintf("%s/api/v1/admin/channels/%d", baseURL, ch.ID), map[string]interface{}{
		"weight": 20,
	}, adminToken)
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
}

func TestDeleteChannel_Success(t *testing.T) {
	requireAdmin(t)

	// Create
	name := uniqueName("ch_del")
	createResp, statusCode, err := doPost(baseURL+"/api/v1/admin/channels", map[string]interface{}{
		"name":        name,
		"supplier_id": 1,
		"type":        "openai",
		"endpoint":    "https://api.openai.com/v1",
		"api_key":     "sk-test-" + name,
		"weight":      5,
		"priority":    1,
		"status":      "active",
	}, adminToken)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)
	if statusCode != http.StatusOK {
		t.Skipf("create returned %d", statusCode)
	}

	var ch struct{ ID uint `json:"id"` }
	json.Unmarshal(createResp.Data, &ch)
	if ch.ID == 0 {
		t.Skip("no channel ID")
	}

	// Delete
	resp, statusCode, err := doDelete(fmt.Sprintf("%s/api/v1/admin/channels/%d", baseURL, ch.ID), adminToken)
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
}

// ----- Channel Tags -----

func TestCreateChannelTag_Success(t *testing.T) {
	requireAdmin(t)

	name := uniqueName("tag")
	resp, statusCode, err := doPost(baseURL+"/api/v1/admin/channel-tags", map[string]interface{}{
		"name":  name,
		"color": "#FF0000",
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

func TestSetChannelTags_Success(t *testing.T) {
	requireAdmin(t)

	// Create a tag
	tagName := uniqueName("settag")
	tagResp, statusCode, err := doPost(baseURL+"/api/v1/admin/channel-tags", map[string]interface{}{
		"name":  tagName,
		"color": "#00FF00",
	}, adminToken)
	if err != nil || statusCode != http.StatusOK {
		t.Skip("cannot create tag for SetTags test")
	}
	var tag struct{ ID uint `json:"id"` }
	json.Unmarshal(tagResp.Data, &tag)

	// Create a channel
	chName := uniqueName("ch_settag")
	chResp, statusCode, err := doPost(baseURL+"/api/v1/admin/channels", map[string]interface{}{
		"name":        chName,
		"supplier_id": 1,
		"type":        "openai",
		"endpoint":    "https://api.openai.com/v1",
		"api_key":     "sk-test-" + chName,
		"weight":      5,
		"priority":    1,
		"status":      "active",
	}, adminToken)
	if err != nil || statusCode != http.StatusOK {
		t.Skip("cannot create channel for SetTags test")
	}
	var ch struct{ ID uint `json:"id"` }
	json.Unmarshal(chResp.Data, &ch)

	if tag.ID == 0 || ch.ID == 0 {
		t.Skip("no tag or channel ID")
	}

	// Set tags
	resp, statusCode, err := doPut(fmt.Sprintf("%s/api/v1/admin/channels/%d/tags", baseURL, ch.ID), map[string]interface{}{
		"tag_ids": []uint{tag.ID},
	}, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
}

// ----- Channel Groups -----

func TestCreateChannelGroup_Success(t *testing.T) {
	requireAdmin(t)

	name := uniqueName("grp")
	resp, statusCode, err := doPost(baseURL+"/api/v1/admin/channel-groups", map[string]interface{}{
		"name":     name,
		"code":     "grp_" + name,
		"strategy": "RoundRobin",
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

// ----- Backup Rules -----

func TestCreateBackupRule_Success(t *testing.T) {
	requireAdmin(t)

	// First create a channel group for the backup rule
	grpName := uniqueName("bkp_grp")
	grpResp, statusCode, _ := doPost(baseURL+"/api/v1/admin/channel-groups", map[string]interface{}{
		"name":     grpName,
		"code":     grpName,
		"strategy": "RoundRobin",
	}, adminToken)
	if statusCode != http.StatusOK {
		t.Skip("cannot create channel group for backup rule test")
	}
	var grp struct{ ID uint `json:"id"` }
	json.Unmarshal(grpResp.Data, &grp)
	if grp.ID == 0 {
		t.Skip("no group ID")
	}

	name := uniqueName("bkp_rule")
	resp, statusCode, err := doPost(baseURL+"/api/v1/admin/backup-rules", map[string]interface{}{
		"name":             name,
		"model_pattern":    "gpt-*",
		"primary_group_id": grp.ID,
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

func TestBackupRuleStatus_Success(t *testing.T) {
	requireAdmin(t)

	// Create group + rule
	grpName := uniqueName("bkps_grp")
	grpResp, statusCode, _ := doPost(baseURL+"/api/v1/admin/channel-groups", map[string]interface{}{
		"name":     grpName,
		"code":     grpName,
		"strategy": "RoundRobin",
	}, adminToken)
	if statusCode != http.StatusOK {
		t.Skip("cannot create group")
	}
	var grp struct{ ID uint `json:"id"` }
	json.Unmarshal(grpResp.Data, &grp)

	ruleName := uniqueName("bkps_rule")
	ruleResp, statusCode, _ := doPost(baseURL+"/api/v1/admin/backup-rules", map[string]interface{}{
		"name":             ruleName,
		"model_pattern":    "gpt-*",
		"primary_group_id": grp.ID,
	}, adminToken)
	if statusCode != http.StatusOK {
		t.Skip("cannot create backup rule")
	}
	var rule struct{ ID uint `json:"id"` }
	json.Unmarshal(ruleResp.Data, &rule)
	if rule.ID == 0 {
		t.Skip("no rule ID")
	}

	// Get status
	resp, statusCode, err := doGet(fmt.Sprintf("%s/api/v1/admin/backup-rules/%d/status", baseURL, rule.ID), adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}
}
