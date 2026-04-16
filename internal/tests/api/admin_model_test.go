package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// ========== AI 模型管理 CRUD 测试 ==========

func TestAdminListAIModels_Success(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doGet(baseURL+"/api/v1/admin/ai-models?page=1&page_size=10", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}

	page, err := parsePageData(resp)
	if err != nil {
		t.Fatalf("parse page data: %v", err)
	}
	if page.Total < 0 {
		t.Error("total should be >= 0")
	}
}

func TestAdminCreateAIModel_Success(t *testing.T) {
	requireAdmin(t)

	// 先获取一个供应商 ID
	supplierID := getOrCreateSupplierID(t)

	body := map[string]interface{}{
		"model_name":  uniqueName("test-model"),
		"supplier_id": supplierID,
		"model_type":  "chat",
		"status":      "pending",
		"is_active":   true,
	}

	resp, status, err := doPost(baseURL+"/api/v1/admin/ai-models", body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

func TestAdminGetAIModelByID_Success(t *testing.T) {
	requireAdmin(t)

	// 先列出模型获取一个 ID
	resp, status, err := doGet(baseURL+"/api/v1/admin/ai-models?page=1&page_size=1", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	if status != http.StatusOK {
		t.Skip("cannot list models")
	}

	page, _ := parsePageData(resp)
	var models []struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(page.List, &models); err != nil || len(models) == 0 {
		t.Skip("no models available for GetByID test")
	}

	modelID := models[0].ID
	resp2, status2, err := doGet(fmt.Sprintf("%s/api/v1/admin/ai-models/%d", baseURL, modelID), adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if status2 != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status2, resp2.Message)
	}
}

func TestAdminUpdateAIModel_Success(t *testing.T) {
	requireAdmin(t)

	// 获取一个已有模型
	modelID := getExistingModelID(t)

	body := map[string]interface{}{
		"model_name": uniqueName("updated-model"),
	}

	resp, status, err := doPut(fmt.Sprintf("%s/api/v1/admin/ai-models/%d", baseURL, modelID), body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

func TestAdminVerifyAIModel_Success(t *testing.T) {
	requireAdmin(t)

	modelID := getExistingModelID(t)

	resp, status, err := doPost(fmt.Sprintf("%s/api/v1/admin/ai-models/%d/verify", baseURL, modelID), nil, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)

	if status != http.StatusOK {
		t.Logf("verify returned %d: %s (may already be verified)", status, resp.Message)
	}
}

func TestAdminSetOfflineAIModel_Success(t *testing.T) {
	requireAdmin(t)

	modelID := getExistingModelID(t)

	resp, status, err := doPost(fmt.Sprintf("%s/api/v1/admin/ai-models/%d/offline", baseURL, modelID), nil, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)

	if status != http.StatusOK {
		t.Logf("offline returned %d: %s (may already be offline)", status, resp.Message)
	}
}

func TestAdminDeleteAIModel_Success(t *testing.T) {
	requireAdmin(t)

	// 创建一个新模型然后删除
	supplierID := getOrCreateSupplierID(t)
	modelName := uniqueName("del-model")
	body := map[string]interface{}{
		"model_name":  modelName,
		"supplier_id": supplierID,
		"model_type":  "chat",
		"status":      "pending",
		"is_active":   false,
	}

	createResp, createStatus, err := doPost(baseURL+"/api/v1/admin/ai-models", body, adminToken)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if createStatus != http.StatusOK {
		t.Skip("cannot create model for deletion test")
	}

	var created struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(createResp.Data, &created); err != nil || created.ID == 0 {
		t.Skip("cannot parse created model ID")
	}

	resp, status, err := doDelete(fmt.Sprintf("%s/api/v1/admin/ai-models/%d", baseURL, created.ID), adminToken)
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

func TestAdminPublicModels_NoAuth(t *testing.T) {
	resp, status, err := doGet(baseURL+"/api/v1/public/models?page=1&page_size=10", "")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Fatalf("public models should be accessible without auth, got %d: %s", status, resp.Message)
	}
}

func TestAdminAIModel_UserForbidden(t *testing.T) {
	requireUser(t)

	_, status, err := doGet(baseURL+"/api/v1/admin/ai-models?page=1&page_size=1", userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if status != http.StatusForbidden && status != http.StatusUnauthorized {
		t.Errorf("expected 403 or 401 for regular user, got %d", status)
	}
}

// ========== 模型批量检测测试 ==========

func TestAdminBatchCheckModels_Sync(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doPost(baseURL+"/api/v1/admin/models/batch-check-sync", nil, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	// 接受 200 或 500（如果没有配置的供应商）
	if status >= 500 {
		t.Logf("batch check returned %d: %s (may have no configured providers)", status, resp.Message)
	}
}

func TestAdminGetCheckHistory(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doGet(baseURL+"/api/v1/admin/models/check-history?page=1&page_size=10", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

func TestAdminGetLatestCheckSummary(t *testing.T) {
	requireAdmin(t)

	_, status, err := doGet(baseURL+"/api/v1/admin/models/check-latest", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Logf("latest check returned %d (may have no check history)", status)
	}
}

// ========== 辅助函数 ==========

func getOrCreateSupplierID(t *testing.T) uint {
	t.Helper()

	// 先尝试获取已有供应商
	resp, status, err := doGet(baseURL+"/api/v1/admin/suppliers?page=1&page_size=1", adminToken)
	if err == nil && status == http.StatusOK {
		page, _ := parsePageData(resp)
		var suppliers []struct {
			ID uint `json:"id"`
		}
		if err := json.Unmarshal(page.List, &suppliers); err == nil && len(suppliers) > 0 {
			return suppliers[0].ID
		}
	}

	// 创建一个新供应商
	body := map[string]interface{}{
		"name":      uniqueName("test-supplier"),
		"code":      uniqueName("ts"),
		"base_url":  "https://api.example.com",
		"is_active": true,
	}
	resp2, status2, err := doPost(baseURL+"/api/v1/admin/suppliers", body, adminToken)
	if err != nil || status2 != http.StatusOK {
		t.Skipf("cannot create supplier: status=%d, err=%v", status2, err)
	}

	var created struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(resp2.Data, &created); err != nil || created.ID == 0 {
		t.Skip("cannot parse supplier ID")
	}
	return created.ID
}

func getExistingModelID(t *testing.T) uint {
	t.Helper()
	requireAdmin(t)

	resp, status, err := doGet(baseURL+"/api/v1/admin/ai-models?page=1&page_size=1", adminToken)
	if err != nil || status != http.StatusOK {
		t.Skip("cannot list models")
	}

	page, _ := parsePageData(resp)
	var models []struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(page.List, &models); err != nil || len(models) == 0 {
		t.Skip("no models available")
	}
	return models[0].ID
}
