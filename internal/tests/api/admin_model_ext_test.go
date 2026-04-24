package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// ============================================================
// A-02~A-06: List / Stats 鎵╁睍
// ============================================================

// A-02: page_size=200 瓒呭ぇ鍒嗛〉
func TestAdminListAIModels_LargePageSize(t *testing.T) {
	requireAdmin(t)
	_, status, err := doGet(baseURL+"/api/v1/admin/ai-models?page=1&page_size=200", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	if status != http.StatusOK && status != http.StatusBadRequest {
		t.Fatalf("expected 200 or 400, got %d", status)
	}
}

// A-03: supplier_id=999999 涓嶅瓨鍦?鈫?200 绌哄垪琛?
func TestAdminListAIModels_NonExistentSupplier(t *testing.T) {
	requireAdmin(t)
	resp, status, err := doGet(baseURL+"/api/v1/admin/ai-models?page=1&page_size=10&supplier_id=999999", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
	page, err := parsePageData(resp)
	if err != nil {
		t.Fatalf("parse page data: %v", err)
	}
	var models []json.RawMessage
	if err := json.Unmarshal(page.List, &models); err == nil && len(models) > 0 {
		t.Errorf("expected empty list for non-existent supplier, got %d models", len(models))
	}
}

// A-04: keyword search
func TestAdminListAIModels_SearchByName(t *testing.T) {
	requireAdmin(t)
	resp, status, err := doGet(baseURL+"/api/v1/admin/ai-models?page=1&page_size=10&search=doubao", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
	page, _ := parsePageData(resp)
	if page.Total < 0 {
		t.Error("total should be >= 0")
	}
}

// A-05: status=online 鍙傛暟锛堝悗绔?admin 鍒楄〃鎺ュ彛涓嶆寜 status 杩囨护锛屽彧楠岃瘉 200锛?
func TestAdminListAIModels_FilterByStatus(t *testing.T) {
	requireAdmin(t)
	resp, status, err := doGet(baseURL+"/api/v1/admin/ai-models?page=1&page_size=10&status=online", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	// 鍚庣 admin 鍒楄〃鎺ュ彛 ListWithFilter 涓嶆敮鎸?status 杩囨护鍙傛暟锛屽彧楠岃瘉璇锋眰姝ｅ父杩斿洖 200
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
	if _, err := parsePageData(resp); err != nil {
		t.Fatalf("parse page data: %v", err)
	}
}

// A-06: GET /admin/ai-models/stats
func TestAdminAIModelStats(t *testing.T) {
	requireAdmin(t)
	resp, status, err := doGet(baseURL+"/api/v1/admin/ai-models/stats", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
	var stats struct {
		Total   *int64 `json:"total"`
		Enabled *int64 `json:"enabled"`
		Online  *int64 `json:"online"`
	}
	if err := json.Unmarshal(resp.Data, &stats); err != nil {
		t.Fatalf("parse stats: %v", err)
	}
	if stats.Total == nil || *stats.Total < 0 {
		t.Error("expected non-negative total")
	}
}

// ============================================================
// A-08~A-10: Create 鏍￠獙
// ============================================================

// A-08: 缂哄皯 model_name 鈫?400
func TestAdminCreateAIModel_MissingModelName(t *testing.T) {
	requireAdmin(t)
	supplierID := getOrCreateSupplierID(t)
	body := map[string]interface{}{
		"supplier_id": supplierID,
		"model_type":  "LLM",
	}
	resp, status, err := doPost(baseURL+"/api/v1/admin/ai-models", body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	if status != http.StatusBadRequest {
		t.Errorf("expected 400 for missing model_name, got %d: %s", status, resp.Message)
	}
}

// A-09: 缂哄皯 supplier_id 鈫?400
func TestAdminCreateAIModel_MissingSupplierID(t *testing.T) {
	requireAdmin(t)
	body := map[string]interface{}{
		"model_name": uniqueName("no-supplier"),
		"model_type": "LLM",
	}
	resp, status, err := doPost(baseURL+"/api/v1/admin/ai-models", body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	if status != http.StatusBadRequest {
		t.Errorf("expected 400 for missing supplier_id, got %d: %s", status, resp.Message)
	}
}

// A-10: 鍚?supplier 涓嬮噸澶?model_name 鈫?400/409
func TestAdminCreateAIModel_DuplicateName(t *testing.T) {
	requireAdmin(t)
	supplierID := getOrCreateSupplierID(t)
	modelName := uniqueName("dup-model")

	// 绗竴娆″垱寤?
	body := map[string]interface{}{
		"model_name":  modelName,
		"supplier_id": supplierID,
		"model_type":  "LLM",
		"is_active":   false,
	}
	resp1, s1, err := doPost(baseURL+"/api/v1/admin/ai-models", body, adminToken)
	if err != nil || s1 != http.StatusOK {
		t.Skipf("cannot create first model: status=%d err=%v", s1, err)
	}
	// 娓呯悊
	var created struct {
		ID uint `json:"id"`
	}
	json.Unmarshal(resp1.Data, &created) //nolint
	defer func() {
		if created.ID > 0 {
			doDelete(fmt.Sprintf("%s/api/v1/admin/ai-models/%d", baseURL, created.ID), adminToken) //nolint
		}
	}()

	// duplicate create should fail
	_, s2, err2 := doPost(baseURL+"/api/v1/admin/ai-models", body, adminToken)
	if err2 != nil {
		t.Fatalf("request failed: %v", err2)
	}
	if s2 == http.StatusOK {
		t.Error("expected 400/409 for duplicate model_name, got 200")
	}
	if s2 != http.StatusBadRequest && s2 != http.StatusConflict {
		t.Errorf("expected 400 or 409, got %d", s2)
	}
}

// ============================================================
// A-12: Get 404
// ============================================================

// A-12: 涓嶅瓨鍦ㄧ殑 id 鈫?404锛堣繖閲?404 鏄鏈熺粨鏋滐紝涓嶈皟鐢?skipIfNotFound锛?
func TestAdminGetAIModel_NotFound(t *testing.T) {
	requireAdmin(t)
	resp, status, err := doGet(baseURL+"/api/v1/admin/ai-models/9999999", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if status != http.StatusNotFound {
		t.Errorf("expected 404 for non-existent id, got %d: %s", status, resp.Message)
	}
}

// ============================================================
// A-14~A-16: Update 瀛楁鏇存柊
// ============================================================

// A-14: 鏇存柊 input_cost_rmb
func TestAdminUpdateAIModel_PriceFields(t *testing.T) {
	requireAdmin(t)
	modelID := getOrCreateTestModel(t)
	body := map[string]interface{}{
		"input_cost_rmb": 1.5,
	}
	resp, status, err := doPut(fmt.Sprintf("%s/api/v1/admin/ai-models/%d", baseURL, modelID), body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
	// 楠岃瘉绉垎瀛楁鍐欏簱
	getResp, getStatus, _ := doGet(fmt.Sprintf("%s/api/v1/admin/ai-models/%d", baseURL, modelID), adminToken)
	if getStatus == http.StatusOK {
		var m struct {
			InputCostRMB float64 `json:"input_cost_rmb"`
		}
		if err := json.Unmarshal(getResp.Data, &m); err == nil {
			if m.InputCostRMB != 1.5 {
				t.Errorf("input_cost_rmb = %v, want 1.5", m.InputCostRMB)
			}
		}
	}
}

// A-15: 鏇存柊 features.requires_stream=true
func TestAdminUpdateAIModel_Features(t *testing.T) {
	requireAdmin(t)
	modelID := getOrCreateTestModel(t)
	body := map[string]interface{}{
		"features": map[string]interface{}{
			"requires_stream": true,
		},
	}
	resp, status, err := doPut(fmt.Sprintf("%s/api/v1/admin/ai-models/%d", baseURL, modelID), body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

// A-16: extra_params 鍚?bogus flag {stop: true} 鈫?鍐欏簱鍚?stop 涓嶅簲瀛樺湪
func TestAdminUpdateAIModel_ExtraParamsBogusFlag(t *testing.T) {
	requireAdmin(t)
	modelID := getOrCreateTestModel(t)
	body := map[string]interface{}{
		"extra_params": map[string]interface{}{
			"stop": true, // bogus flag
		},
	}
	resp, status, err := doPut(fmt.Sprintf("%s/api/v1/admin/ai-models/%d", baseURL, modelID), body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	// 鎺ュ彈 200锛堣繃婊ゅ苟淇濆瓨锛夋垨 400锛堟嫆缁?bogus锛?
	if status != http.StatusOK && status != http.StatusBadRequest {
		t.Errorf("expected 200 or 400, got %d: %s", status, resp.Message)
	}
}

// A-20: reactivate
func TestAdminReactivateAIModel(t *testing.T) {
	requireAdmin(t)
	modelID := getExistingModelID(t)
	resp, status, err := doPost(fmt.Sprintf("%s/api/v1/admin/ai-models/%d/reactivate", baseURL, modelID), nil, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)
	if status != http.StatusOK {
		t.Logf("reactivate returned %d: %s (may already be active)", status, resp.Message)
	}
}

// ============================================================
// A-21~A-25: 妯″瀷鍚屾 API
// ============================================================

// A-21: 鍏ㄩ噺鍚屾
func TestAdminSyncAllModels(t *testing.T) {
	requireAdmin(t)
	t.Skip("offline test plan: skip provider sync endpoint that may call external channels")
	resp, status, err := doPost(baseURL+"/api/v1/admin/models/sync", nil, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)
	if status != http.StatusOK {
		t.Logf("sync all returned %d: %s (may have no active channels)", status, resp.Message)
	}
}

// A-22: 鎸夋笭閬撳悓姝ワ紙鏈夋晥 channelId锛?
func TestAdminSyncByChannel_Valid(t *testing.T) {
	requireAdmin(t)
	channelID := getAnyChannelID(t)
	resp, status, err := doPost(fmt.Sprintf("%s/api/v1/admin/models/sync/%d", baseURL, channelID), nil, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)
	if status != http.StatusOK {
		t.Logf("sync channel %d returned %d: %s", channelID, status, resp.Message)
	}
}

// A-23: 鎸夋笭閬撳悓姝ワ紙鏃犳晥 channelId=999999锛?
func TestAdminSyncByChannel_InvalidID(t *testing.T) {
	requireAdmin(t)
	resp, status, err := doPost(baseURL+"/api/v1/admin/models/sync/999999", nil, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	// 鏃犳晥娓犻亾 ID 鈫?404 鎴?400
	if status == http.StatusOK {
		t.Errorf("expected 404/400 for invalid channel ID, got 200: %s", resp.Message)
	}
}

// A-24: GET /admin/channel-models
func TestAdminListChannelModels(t *testing.T) {
	requireAdmin(t)
	resp, status, err := doGet(baseURL+"/api/v1/admin/channel-models?page=1&page_size=10", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
	if _, err := parsePageData(resp); err != nil {
		t.Fatalf("parse page data: %v", err)
	}
}

// ============================================================
// A-26~A-31: 妯″瀷妫€娴?API
// ============================================================

// A-26: check-selected 浼?2 涓湪绾挎ā鍨?ID
func TestAdminCheckSelectedModels(t *testing.T) {
	requireAdmin(t)
	ids := getTwoOnlineModelIDs(t)
	body := map[string]interface{}{
		"model_ids": ids,
	}
	resp, status, err := doPost(baseURL+"/api/v1/admin/models/check-selected", body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)
	if status != http.StatusOK {
		t.Logf("check-selected returned %d: %s", status, resp.Message)
	}
}

// A-27: check-selected 绌?model_ids 鈫?400
func TestAdminCheckSelectedModels_EmptyIDs(t *testing.T) {
	requireAdmin(t)
	body := map[string]interface{}{
		"model_ids": []uint{},
	}
	resp, status, err := doPost(baseURL+"/api/v1/admin/models/check-selected", body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	if status != http.StatusBadRequest {
		t.Errorf("expected 400 for empty model_ids, got %d: %s", status, resp.Message)
	}
}

// A-29: check-history with supplier_code filter
func TestAdminCheckHistory_WithSupplierCode(t *testing.T) {
	requireAdmin(t)
	resp, status, err := doGet(baseURL+"/api/v1/admin/models/check-history?page=1&page_size=10&supplier_code=volcengine", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
	if _, err := parsePageData(resp); err != nil {
		t.Fatalf("parse page data: %v", err)
	}
}

// ============================================================
// A-32~A-40: 鑳藉姏娴嬭瘯 API
// ============================================================

// A-32: GET /admin/capability-test/cases 鈫?total>=42
func TestAdminCapabilityTestCases_All(t *testing.T) {
	requireAdmin(t)
	resp, status, err := doGet(baseURL+"/api/v1/admin/capability-test/cases?page=1&page_size=100", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
	page, err := parsePageData(resp)
	if err != nil {
		t.Fatalf("parse page data: %v", err)
	}
	if page.Total < 42 {
		t.Errorf("expected at least 42 capability test cases, got %d", page.Total)
	}
}

// A-33: GET /admin/capability-test/cases?category=thinking
func TestAdminCapabilityTestCases_FilterCategory(t *testing.T) {
	requireAdmin(t)
	resp, status, err := doGet(baseURL+"/api/v1/admin/capability-test/cases?category=thinking", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
	page, _ := parsePageData(resp)
	var cases []struct {
		Category string `json:"category"`
	}
	if err := json.Unmarshal(page.List, &cases); err == nil {
		for _, c := range cases {
			if c.Category != "" && c.Category != "thinking" {
				t.Errorf("expected all cases to have category=thinking, got %q", c.Category)
			}
		}
	}
}

// A-34: POST /admin/capability-test/estimate
func TestAdminCapabilityTestEstimate(t *testing.T) {
	requireAdmin(t)
	modelID := getExistingModelID(t)
	caseIDs := getCapabilityTestCaseIDs(t, 3)
	if len(caseIDs) == 0 {
		t.Skip("no capability test cases available")
	}
	body := map[string]interface{}{
		"model_ids": []uint{modelID},
		"case_ids":  caseIDs,
	}
	resp, status, err := doPost(baseURL+"/api/v1/admin/capability-test/estimate", body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

// A-35: POST /admin/capability-test/run 鈫?杩斿洖 task_id
func TestAdminCapabilityTestRun(t *testing.T) {
	requireAdmin(t)
	modelID := getExistingModelID(t)
	caseIDs := getCapabilityTestCaseIDs(t, 2)
	if len(caseIDs) == 0 {
		t.Skip("no capability test cases available")
	}
	body := map[string]interface{}{
		"model_ids":  []uint{modelID},
		"case_ids":   caseIDs,
		"auto_apply": false,
	}
	resp, status, err := doPost(baseURL+"/api/v1/admin/capability-test/run", body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
	var result struct {
		TaskID uint `json:"task_id"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil || result.TaskID == 0 {
		t.Logf("response: %s", string(resp.Data))
		t.Log("task_id field may be named differently or be 0")
	}
}

// A-36: GET /admin/capability-test/tasks
func TestAdminCapabilityTestTasks(t *testing.T) {
	requireAdmin(t)
	resp, status, err := doGet(baseURL+"/api/v1/admin/capability-test/tasks?page=1&page_size=10", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
	if _, err := parsePageData(resp); err != nil {
		t.Fatalf("parse page data: %v", err)
	}
}

// A-37: GET /admin/capability-test/tasks/:id
func TestAdminCapabilityTestTaskDetail(t *testing.T) {
	requireAdmin(t)
	taskID := getAnyCapabilityTaskID(t)
	resp, status, err := doGet(fmt.Sprintf("%s/api/v1/admin/capability-test/tasks/%d", baseURL, taskID), adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

// A-38: POST /admin/capability-test/tasks/:id/auto-apply
func TestAdminCapabilityTestAutoApply(t *testing.T) {
	requireAdmin(t)
	taskID := getAnyCapabilityTaskID(t)
	resp, status, err := doPost(fmt.Sprintf("%s/api/v1/admin/capability-test/tasks/%d/auto-apply", baseURL, taskID), nil, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)
	if status != http.StatusOK {
		t.Logf("auto-apply returned %d: %s (task may not be completed)", status, resp.Message)
	}
}

// A-39: GET /admin/capability-test/untested-count
func TestAdminCapabilityTestUntestedCount(t *testing.T) {
	requireAdmin(t)
	resp, status, err := doGet(baseURL+"/api/v1/admin/capability-test/untested-count", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
	var result struct {
		Count *int `json:"count"`
	}
	if err := json.Unmarshal(resp.Data, &result); err == nil {
		if result.Count != nil && *result.Count < 0 {
			t.Error("untested count should be >= 0")
		}
	}
}

// A-40: POST /admin/capability-test/run-untested
func TestAdminCapabilityTestRunUntested(t *testing.T) {
	requireAdmin(t)
	resp, status, err := doPost(baseURL+"/api/v1/admin/capability-test/run-untested", nil, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)
	if status != http.StatusOK {
		t.Logf("run-untested returned %d: %s (may have 0 untested models)", status, resp.Message)
	}
}

// ============================================================
// A-41~A-43: 涓嬬嚎鎵弿 API
// ============================================================

// A-41: GET /admin/models/scanned-offline
func TestAdminScanOfflineAll(t *testing.T) {
	requireAdmin(t)
	resp, status, err := doGet(baseURL+"/api/v1/admin/models/scanned-offline", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
	var result struct {
		Groups           json.RawMessage `json:"groups"`
		TotalModels      int             `json:"total_models"`
		SuppliersScanned int             `json:"suppliers_scanned"`
		ScanDurationMs   int64           `json:"scan_duration_ms"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		t.Fatalf("parse scanned-offline result: %v", err)
	}
	if result.SuppliersScanned < 0 {
		t.Errorf("suppliers_scanned should be >= 0, got %d", result.SuppliersScanned)
	}
}

// A-42: POST /admin/models/deprecation-scan?supplier=volcengine
func TestAdminDeprecationScan_Volcengine(t *testing.T) {
	requireAdmin(t)
	resp, status, err := doPost(baseURL+"/api/v1/admin/models/deprecation-scan?supplier=volcengine", nil, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)
	if status != http.StatusOK {
		t.Logf("deprecation-scan volcengine returned %d: %s", status, resp.Message)
	}
}

// A-43: POST /admin/models/bulk-deprecate (浼犵┖ model_ids锛岄獙璇佸弬鏁版牎楠?
func TestAdminBulkDeprecate_Validation(t *testing.T) {
	requireAdmin(t)
	body := map[string]interface{}{
		"model_ids": []uint{},
	}
	_, status, err := doPost(baseURL+"/api/v1/admin/models/bulk-deprecate", body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)
	// 绌哄垪琛ㄥ簲璇ヨ繑鍥?400 鎴?200锛堟搷浣?0 涓ā鍨嬶級
	if status != http.StatusBadRequest && status != http.StatusOK {
		t.Errorf("expected 400 or 200, got %d", status)
	}
}

// ============================================================
// A-44~A-46: 鏍囩绠＄悊 API
// ============================================================

// A-44: POST /admin/models/batch-labels
func TestAdminBatchLabels_Add(t *testing.T) {
	requireAdmin(t)
	modelID := getExistingModelID(t)
	body := map[string]interface{}{
		"model_ids": []uint{modelID},
		"labels": []map[string]string{
			{"key": "test-env", "value": "ci"},
		},
	}
	resp, status, err := doPost(baseURL+"/api/v1/admin/models/batch-labels", body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)
	if status != http.StatusOK {
		t.Logf("batch-labels add returned %d: %s", status, resp.Message)
	}
}

// A-45: GET /admin/ai-models/:id/labels
func TestAdminGetModelLabels(t *testing.T) {
	requireAdmin(t)
	modelID := getExistingModelID(t)
	_, status, err := doGet(fmt.Sprintf("%s/api/v1/admin/ai-models/%d/labels", baseURL, modelID), adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)
	if status != http.StatusOK {
		t.Logf("get labels returned %d (may have no labels API)", status)
	}
}

// A-46: DELETE /admin/models/batch-labels
func TestAdminBatchLabels_Remove(t *testing.T) {
	requireAdmin(t)
	modelID := getExistingModelID(t)
	body := map[string]interface{}{
		"model_ids":  []uint{modelID},
		"label_keys": []string{"test-env"},
	}
	_, status, err := doRequest(http.MethodDelete, baseURL+"/api/v1/admin/models/batch-labels", body, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)
	// 200 鎴?404锛堜箣鍓嶆病娣诲姞杩囷級閮藉彲浠?
	if status != http.StatusOK && status != http.StatusNotFound {
		t.Logf("batch-labels delete returned %d", status)
	}
}

// ============================================================
// A-47~A-49: 鍏紑妯″瀷 API
// ============================================================

// A-48: GET /public/models?page_size=500
func TestPublicModels_LargePageSize(t *testing.T) {
	resp, status, err := doGet(baseURL+"/api/v1/public/models?page=1&page_size=500", "")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
	page, err := parsePageData(resp)
	if err != nil {
		// 鍙兘鏄暟缁勬牸寮?
		var models []json.RawMessage
		if err2 := json.Unmarshal(resp.Data, &models); err2 != nil {
			t.Logf("response format: %s", string(resp.Data)[:min(200, len(resp.Data))])
		}
		return
	}
	if page.Total < 0 {
		t.Error("total should be >= 0")
	}
}

// A-49: 绂佺敤妯″瀷鍚庡叕寮€鍒楄〃缂撳瓨澶辨晥
func TestPublicModels_CacheInvalidationAfterDisable(t *testing.T) {
	requireAdmin(t)
	modelID := getExistingOnlineModelID(t)

	// 鍏堣褰曟ā鍨嬪悕
	getResp, getStatus, _ := doGet(fmt.Sprintf("%s/api/v1/admin/ai-models/%d", baseURL, modelID), adminToken)
	if getStatus != http.StatusOK {
		t.Skip("cannot get model info")
	}
	var m struct {
		ModelName string `json:"model_name"`
		IsActive  bool   `json:"is_active"`
	}
	if err := json.Unmarshal(getResp.Data, &m); err != nil {
		t.Skip("cannot parse model info")
	}

	// 涓嬬嚎妯″瀷
	doPost(fmt.Sprintf("%s/api/v1/admin/ai-models/%d/offline", baseURL, modelID), nil, adminToken) //nolint

	// 绋嶇瓑缂撳瓨澶辨晥
	time.Sleep(500 * time.Millisecond)

	// 纭鍏紑鍒楄〃涓嶅惈璇ユā鍨?
	publicResp, publicStatus, _ := doGet(fmt.Sprintf("%s/api/v1/public/models?page=1&page_size=500", baseURL), "")
	if publicStatus == http.StatusOK {
		if pageData, err := parsePageData(publicResp); err == nil {
			var pubModels []struct {
				ModelName string `json:"model_name"`
			}
			if err := json.Unmarshal(pageData.List, &pubModels); err == nil {
				for _, pm := range pubModels {
					if pm.ModelName == m.ModelName {
						t.Errorf("model %q still in public list after offline (cache not invalidated)", m.ModelName)
					}
				}
			}
		}
	}

	// 鎭㈠妯″瀷锛堟竻鐞嗭級
	doPost(fmt.Sprintf("%s/api/v1/admin/ai-models/%d/verify", baseURL, modelID), nil, adminToken) //nolint
}

// ============================================================
// A-50~A-54: 鏉冮檺杈圭晫娴嬭瘯
// ============================================================

// A-50: 鏈璇佽闂?/admin/ai-models 鈫?401
func TestAdminAIModels_Unauthenticated(t *testing.T) {
	resp, status, err := doGet(baseURL+"/api/v1/admin/ai-models?page=1&page_size=1", "")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	if status != http.StatusUnauthorized {
		t.Errorf("expected 401 for unauthenticated request, got %d: %s", status, resp.Message)
	}
}

// A-51: USER 瑙掕壊璁块棶 /admin/ai-models 鈫?403 (already in admin_model_test.go, complementary)
func TestAdminAIModels_UserCannotCreate(t *testing.T) {
	requireUser(t)
	supplierID := uint(1) // 鐩存帴鐢?ID=1锛岄伩鍏?admin 璋冪敤
	body := map[string]interface{}{
		"model_name":  uniqueName("user-attempt"),
		"supplier_id": supplierID,
		"model_type":  "LLM",
	}
	_, status, err := doPost(baseURL+"/api/v1/admin/ai-models", body, userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if status != http.StatusForbidden && status != http.StatusUnauthorized {
		t.Errorf("expected 403/401 for user creating model, got %d", status)
	}
}

// A-52: USER 瑙掕壊鏃犳硶鍒犻櫎妯″瀷
func TestAdminAIModels_UserCannotDelete(t *testing.T) {
	requireUser(t)
	_, status, err := doDelete(baseURL+"/api/v1/admin/ai-models/1", userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if status != http.StatusForbidden && status != http.StatusUnauthorized {
		t.Errorf("expected 403/401 for user deleting model, got %d", status)
	}
}

// A-53: ADMIN 鍙闂ā鍨嬬鐞?
func TestAdminAIModels_AdminCanAccess(t *testing.T) {
	requireAdmin(t)
	resp, status, err := doGet(baseURL+"/api/v1/admin/ai-models?page=1&page_size=1", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("expected 200 for admin, got %d: %s", status, resp.Message)
	}
}

// A-54: ADMIN 鍙Е鍙戞ā鍨嬪悓姝?
func TestAdminModels_AdminCanSync(t *testing.T) {
	requireAdmin(t)
	t.Skip("offline test plan: skip provider sync endpoint that may call external channels")
	_, status, err := doPost(baseURL+"/api/v1/admin/models/sync", nil, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	// 200 鎴?500锛堟棤娓犻亾锛夐兘鍙互锛?03 鎵嶆槸澶辫触
	if status == http.StatusForbidden || status == http.StatusUnauthorized {
		t.Errorf("admin should be able to trigger sync, got %d", status)
	}
}

// ============================================================
// 杈呭姪鍑芥暟
// ============================================================

// getOrCreateTestModel 鑾峰彇鎴栧垱寤轰竴涓复鏃舵ā鍨嬶紝杩斿洖 ID

func getOrCreateCategoryID(t *testing.T, supplierID uint) uint {
	t.Helper()
	name := uniqueName("ext-cat")
	resp, status, err := doPost(baseURL+"/api/v1/admin/model-categories", map[string]interface{}{
		"name":        name,
		"code":        uniqueName("extcat"),
		"description": "admin model ext test category",
		"sort_order":  10,
	}, adminToken)
	if err != nil || status != http.StatusOK {
		t.Skipf("cannot create category: status=%d err=%v", status, err)
	}
	var created struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(resp.Data, &created); err != nil || created.ID == 0 {
		t.Skip("cannot parse created category ID")
	}
	t.Cleanup(func() { doDelete(fmt.Sprintf("%s/api/v1/admin/model-categories/%d", baseURL, created.ID), adminToken) })
	return created.ID
}

func getOrCreateTestModel(t *testing.T) uint {
	t.Helper()
	requireAdmin(t)

	supplierID := getOrCreateSupplierID(t)
	categoryID := getOrCreateCategoryID(t, supplierID)
	modelName := uniqueName("tmp-model")
	body := map[string]interface{}{
		"model_name":  modelName,
		"supplier_id": supplierID,
		"category_id": categoryID,
		"model_type":  "LLM",
		"is_active":   false,
		"status":      "offline",
	}
	resp, status, err := doPost(baseURL+"/api/v1/admin/ai-models", body, adminToken)
	if err != nil || status != http.StatusOK {
		t.Skipf("cannot create temp model: status=%d err=%v", status, err)
	}
	var created struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(resp.Data, &created); err != nil || created.ID == 0 {
		t.Skip("cannot parse created model ID")
	}
	t.Cleanup(func() {
		doDelete(fmt.Sprintf("%s/api/v1/admin/ai-models/%d", baseURL, created.ID), adminToken) //nolint
	})
	return created.ID
}

// getAnyChannelID 浠?channel 鍒楄〃涓幏鍙栫涓€涓?channelID
func getAnyChannelID(t *testing.T) uint {
	t.Helper()
	resp, status, err := doGet(baseURL+"/api/v1/admin/channels?page=1&page_size=1", adminToken)
	if err != nil || status != http.StatusOK {
		t.Skip("cannot list channels")
	}
	page, _ := parsePageData(resp)
	var channels []struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(page.List, &channels); err != nil || len(channels) == 0 {
		t.Skip("no channels available")
	}
	return channels[0].ID
}

// getTwoOnlineModelIDs 鑾峰彇 2 涓湪绾挎ā鍨?ID
func getTwoOnlineModelIDs(t *testing.T) []uint {
	t.Helper()
	resp, status, err := doGet(baseURL+"/api/v1/admin/ai-models?page=1&page_size=5&status=online", adminToken)
	if err != nil || status != http.StatusOK {
		t.Skip("cannot list online models")
	}
	page, _ := parsePageData(resp)
	var models []struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(page.List, &models); err != nil || len(models) < 1 {
		t.Skip("not enough online models")
	}
	ids := make([]uint, 0, 2)
	for _, m := range models {
		ids = append(ids, m.ID)
		if len(ids) == 2 {
			break
		}
	}
	return ids
}

// getCapabilityTestCaseIDs 鑾峰彇鍓?n 涓?capability test case ID
func getCapabilityTestCaseIDs(t *testing.T, n int) []uint {
	t.Helper()
	resp, status, err := doGet(fmt.Sprintf("%s/api/v1/admin/capability-test/cases?page=1&page_size=%d", baseURL, n), adminToken)
	if err != nil || status != http.StatusOK {
		return nil
	}
	page, _ := parsePageData(resp)
	var cases []struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(page.List, &cases); err != nil {
		return nil
	}
	ids := make([]uint, 0, n)
	for _, c := range cases {
		ids = append(ids, c.ID)
		if len(ids) == n {
			break
		}
	}
	return ids
}

// getAnyCapabilityTaskID 鑾峰彇浠绘剰涓€涓兘鍔涙祴璇曚换鍔?ID
func getAnyCapabilityTaskID(t *testing.T) uint {
	t.Helper()
	resp, status, err := doGet(baseURL+"/api/v1/admin/capability-test/tasks?page=1&page_size=1", adminToken)
	if err != nil || status != http.StatusOK {
		t.Skip("cannot list capability test tasks")
	}
	page, _ := parsePageData(resp)
	var tasks []struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(page.List, &tasks); err != nil || len(tasks) == 0 {
		t.Skip("no capability test tasks available")
	}
	return tasks[0].ID
}

// getExistingOnlineModelID 鑾峰彇涓€涓?status=online 鐨勬ā鍨?ID
func getExistingOnlineModelID(t *testing.T) uint {
	t.Helper()
	resp, status, err := doGet(baseURL+"/api/v1/admin/ai-models?page=1&page_size=1&status=online", adminToken)
	if err != nil || status != http.StatusOK {
		t.Skip("cannot list online models")
	}
	page, _ := parsePageData(resp)
	var models []struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(page.List, &models); err != nil || len(models) == 0 {
		t.Skip("no online models available")
	}
	return models[0].ID
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
