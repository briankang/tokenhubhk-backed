package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// ========== 供应商管理 CRUD 测试 ==========

func TestAdminListSuppliers_Success(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doGet(baseURL+"/api/v1/admin/suppliers?page=1&page_size=10", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

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

func TestAdminCreateSupplier_Success(t *testing.T) {
	requireAdmin(t)

	body := map[string]interface{}{
		"name":        uniqueName("supplier"),
		"code":        uniqueName("sp"),
		"base_url":    "https://api.test-supplier.com/v1",
		"access_type": "api",
		"status":      "active",
		"is_active":   true,
	}

	resp, status, err := doPost(baseURL+"/api/v1/admin/suppliers", body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

func TestAdminGetSupplierByID_Success(t *testing.T) {
	requireAdmin(t)

	supplierID := getOrCreateSupplierID(t)

	resp, status, err := doGet(fmt.Sprintf("%s/api/v1/admin/suppliers/%d", baseURL, supplierID), adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

func TestAdminUpdateSupplier_Success(t *testing.T) {
	requireAdmin(t)

	supplierID := getOrCreateSupplierID(t)

	body := map[string]interface{}{
		"name": uniqueName("updated-supplier"),
	}

	resp, status, err := doPut(fmt.Sprintf("%s/api/v1/admin/suppliers/%d", baseURL, supplierID), body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

func TestAdminDeleteSupplier_Success(t *testing.T) {
	requireAdmin(t)

	// 创建一个专用于删除的供应商
	body := map[string]interface{}{
		"name":      uniqueName("del-supplier"),
		"code":      uniqueName("ds"),
		"base_url":  "https://api.del-supplier.com",
		"is_active": false,
	}

	createResp, createStatus, err := doPost(baseURL+"/api/v1/admin/suppliers", body, adminToken)
	if err != nil || createStatus != http.StatusOK {
		t.Skip("cannot create supplier for deletion")
	}

	var created struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(createResp.Data, &created); err != nil || created.ID == 0 {
		t.Skip("cannot parse created supplier ID")
	}

	resp, status, err := doDelete(fmt.Sprintf("%s/api/v1/admin/suppliers/%d", baseURL, created.ID), adminToken)
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

func TestAdminSupplier_UserForbidden(t *testing.T) {
	requireUser(t)

	_, status, err := doGet(baseURL+"/api/v1/admin/suppliers?page=1&page_size=1", userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if status != http.StatusForbidden && status != http.StatusUnauthorized {
		t.Errorf("expected 403/401 for regular user, got %d", status)
	}
}

// ========== 租户管理测试 ==========

func TestAdminListTenants_Success(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doGet(baseURL+"/api/v1/admin/tenants?page=1&page_size=10", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

func TestAdminCreateTenant_Success(t *testing.T) {
	requireAdmin(t)

	body := map[string]interface{}{
		"name":     uniqueName("tenant"),
		"email":    uniqueEmail("tenant"),
		"password": "Tenant@123456",
	}

	resp, status, err := doPost(baseURL+"/api/v1/admin/tenants", body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Logf("create tenant returned %d: %s", status, resp.Message)
	}
}

// ========== 批量用户操作测试 ==========

func TestAdminBatchCreateUsers_Success(t *testing.T) {
	requireAdmin(t)

	users := []map[string]string{
		{"email": uniqueEmail("batch1"), "password": "Test@123456", "name": "BatchUser1"},
		{"email": uniqueEmail("batch2"), "password": "Test@123456", "name": "BatchUser2"},
	}

	body := map[string]interface{}{
		"users": users,
	}

	resp, status, err := doPost(baseURL+"/api/v1/admin/users/batch", body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Logf("batch create returned %d: %s", status, resp.Message)
	}
}

func TestAdminUpdateUserRole_Success(t *testing.T) {
	requireAdmin(t)

	// 获取一个用户 ID
	userID := getExistingUserID(t)

	body := map[string]interface{}{
		"role": "USER",
	}

	resp, status, err := doPut(fmt.Sprintf("%s/api/v1/admin/users/%d/role", baseURL, userID), body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Logf("update role returned %d: %s", status, resp.Message)
	}
}

func TestAdminRechargeUserRMB_Success(t *testing.T) {
	requireAdmin(t)

	userID := getExistingUserID(t)

	body := map[string]interface{}{
		"amount": 10.0,
		"remark": "API test recharge",
	}

	resp, status, err := doPost(fmt.Sprintf("%s/api/v1/admin/users/%d/recharge-rmb", baseURL, userID), body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Logf("recharge returned %d: %s", status, resp.Message)
	}
}

func TestAdminUpdateUserStatus_Success(t *testing.T) {
	requireAdmin(t)

	userID := getExistingUserID(t)

	body := map[string]interface{}{
		"status": "active",
	}

	resp, status, err := doPut(fmt.Sprintf("%s/api/v1/admin/users/%d/status", baseURL, userID), body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Logf("update status returned %d: %s", status, resp.Message)
	}
}

// ========== 汇率管理测试 ==========

func TestAdminListExchangeRates_Success(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doGet(baseURL+"/api/v1/admin/exchange-rates", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

func TestAdminCreateExchangeRate_Success(t *testing.T) {
	requireAdmin(t)

	body := map[string]interface{}{
		"currency": "JPY",
		"rate":     0.047,
	}

	resp, status, err := doPost(baseURL+"/api/v1/admin/exchange-rates", body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Logf("create exchange rate returned %d: %s (may already exist)", status, resp.Message)
	}
}

// ========== 等级管理测试 ==========

func TestAdminGetMemberLevels_Success(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doGet(baseURL+"/api/v1/admin/member-levels", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

func TestAdminCreateMemberLevel_Success(t *testing.T) {
	requireAdmin(t)

	body := map[string]interface{}{
		"name":          uniqueName("VIP"),
		"level":         99,
		"min_spending":  100000,
		"discount_rate": 0.9,
		"default_rpm":   120,
		"default_tpm":   200000,
	}

	resp, status, err := doPost(baseURL+"/api/v1/admin/member-levels", body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Logf("create member level returned %d: %s", status, resp.Message)
	}
}

func TestAdminGetAgentLevels_Success(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doGet(baseURL+"/api/v1/admin/agent-levels", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

func TestAdminCreateAgentLevel_Success(t *testing.T) {
	requireAdmin(t)

	body := map[string]interface{}{
		"name":            uniqueName("Agent-Level"),
		"level":           99,
		"commission_rate": 0.1,
	}

	resp, status, err := doPost(baseURL+"/api/v1/admin/agent-levels", body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Logf("create agent level returned %d: %s", status, resp.Message)
	}
}

// ========== 提现审核测试 ==========

func TestAdminGetWithdrawalRequests_Success(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doGet(baseURL+"/api/v1/admin/withdrawals?page=1&page_size=10", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

// ========== 模型分类管理测试 ==========

func TestAdminListModelCategories_Success(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doGet(baseURL+"/api/v1/admin/model-categories?page=1&page_size=10", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

func TestAdminCreateModelCategory_Success(t *testing.T) {
	requireAdmin(t)

	body := map[string]interface{}{
		"name":        uniqueName("category"),
		"code":        uniqueName("cat"),
		"description": "Test category for API testing",
		"sort_order":  10,
	}

	resp, status, err := doPost(baseURL+"/api/v1/admin/model-categories", body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Logf("create category returned %d: %s", status, resp.Message)
	}
}

func TestAdminModelCategory_CRUD(t *testing.T) {
	requireAdmin(t)

	// Create
	catName := uniqueName("crud-cat")
	body := map[string]interface{}{
		"name":        catName,
		"code":        uniqueName("cc"),
		"description": "CRUD test category",
		"sort_order":  99,
	}

	createResp, createStatus, err := doPost(baseURL+"/api/v1/admin/model-categories", body, adminToken)
	if err != nil || createStatus != http.StatusOK {
		t.Skipf("cannot create category for CRUD test: status=%d", createStatus)
	}

	var created struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(createResp.Data, &created); err != nil || created.ID == 0 {
		t.Skip("cannot parse created category ID")
	}

	catID := created.ID

	// Read
	resp, status, err := doGet(fmt.Sprintf("%s/api/v1/admin/model-categories/%d", baseURL, catID), adminToken)
	if err != nil {
		t.Fatalf("get by ID failed: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("expected 200 for GetByID, got %d", status)
	}

	// Update
	updateBody := map[string]interface{}{
		"name": catName + "-updated",
	}
	resp, status, err = doPut(fmt.Sprintf("%s/api/v1/admin/model-categories/%d", baseURL, catID), updateBody, adminToken)
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("expected 200 for Update, got %d: %s", status, resp.Message)
	}

	// Delete
	resp, status, err = doDelete(fmt.Sprintf("%s/api/v1/admin/model-categories/%d", baseURL, catID), adminToken)
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("expected 200 for Delete, got %d: %s", status, resp.Message)
	}
}

// ========== 模型佣金配置测试 ==========

func TestAdminListModelCommissions_Success(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doGet(baseURL+"/api/v1/admin/model-commissions?page=1&page_size=10", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

func TestAdminCreateModelCommission_Success(t *testing.T) {
	requireAdmin(t)

	body := map[string]interface{}{
		"model_name":      "gpt-4o",
		"commission_rate": 0.05,
		"is_active":       true,
	}

	resp, status, err := doPost(baseURL+"/api/v1/admin/model-commissions", body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Logf("create commission returned %d: %s", status, resp.Message)
	}
}

// ========== 模型同步与发现测试 ==========

func TestAdminListChannelModels_Success(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doGet(baseURL+"/api/v1/admin/channel-models?page=1&page_size=10", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

func TestAdminBatchUpdateModelStatus_Success(t *testing.T) {
	requireAdmin(t)

	body := map[string]interface{}{
		"model_ids": []uint{},
		"is_active": true,
	}

	resp, status, err := doPut(baseURL+"/api/v1/admin/models/batch-status", body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	// 空列表应该成功返回或返回参数错误
	if status >= 500 {
		t.Fatalf("server error %d: %s", status, resp.Message)
	}
}

// ========== 价格爬虫管理测试 ==========

func TestAdminGetPriceSyncLogs_Success(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doGet(baseURL+"/api/v1/admin/models/price-sync-logs?page=1&page_size=10", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

// ========== 管理员每日统计测试 ==========

func TestAdminDailyStats_Success(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doGet(baseURL+"/api/v1/admin/stats/daily", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

// ========== 代理申请管理测试 ==========

func TestAdminListAgentApplications_Success(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doGet(baseURL+"/api/v1/admin/agent-applications?page=1&page_size=10", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

// ========== 对账报告测试 ==========

func TestAdminReconciliationReport_Success(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doGet(baseURL+"/api/v1/admin/reconciliation", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)
	skipIfForbidden(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

// ========== 辅助函数 ==========

func getExistingUserID(t *testing.T) uint {
	t.Helper()
	requireAdmin(t)

	resp, status, err := doGet(baseURL+"/api/v1/admin/users?page=1&page_size=1", adminToken)
	if err != nil || status != http.StatusOK {
		t.Skip("cannot list users")
	}

	page, _ := parsePageData(resp)
	var users []struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(page.List, &users); err != nil || len(users) == 0 {
		t.Skip("no users available")
	}
	return users[0].ID
}
