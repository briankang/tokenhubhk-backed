package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// ─── 参数映射 API 测试 ───

// TestParamMapping_ListAll 列出所有平台参数
func TestParamMapping_ListAll(t *testing.T) {
	if adminToken == "" {
		t.Skip("admin token not available")
	}

	resp, statusCode, err := doGet(baseURL+"/api/v1/admin/param-mappings", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", statusCode, resp.Message)
	}

	var params []struct {
		ID          uint   `json:"id"`
		ParamName   string `json:"param_name"`
		ParamType   string `json:"param_type"`
		DisplayName string `json:"display_name"`
		Category    string `json:"category"`
		IsActive    bool   `json:"is_active"`
		Mappings    []struct {
			ID              uint   `json:"id"`
			SupplierCode    string `json:"supplier_code"`
			VendorParamName string `json:"vendor_param_name"`
			TransformType   string `json:"transform_type"`
			Supported       bool   `json:"supported"`
		} `json:"mappings"`
	}
	if err := json.Unmarshal(resp.Data, &params); err != nil {
		t.Fatalf("parse data: %v", err)
	}

	// 验证种子数据：至少10个平台参数
	if len(params) < 10 {
		t.Errorf("expected at least 10 params from seed, got %d", len(params))
	}

	// 验证 enable_thinking 参数存在且有映射
	found := false
	for _, p := range params {
		if p.ParamName == "enable_thinking" {
			found = true
			if p.ParamType != "bool" {
				t.Errorf("enable_thinking: expected type=bool, got=%s", p.ParamType)
			}
			if !p.IsActive {
				t.Error("enable_thinking: expected is_active=true")
			}
			if len(p.Mappings) < 5 {
				t.Errorf("enable_thinking: expected at least 5 supplier mappings, got %d", len(p.Mappings))
			}
			// 验证 Anthropic 映射为 nested 类型
			for _, m := range p.Mappings {
				if m.SupplierCode == "anthropic" {
					if m.TransformType != "nested" {
						t.Errorf("anthropic enable_thinking: expected transform=nested, got=%s", m.TransformType)
					}
					if m.VendorParamName != "thinking" {
						t.Errorf("anthropic enable_thinking: expected vendor_param=thinking, got=%s", m.VendorParamName)
					}
				}
			}
			break
		}
	}
	if !found {
		t.Error("expected enable_thinking param in seed data")
	}
}

// TestParamMapping_GetSingle 获取单个参数详情
func TestParamMapping_GetSingle(t *testing.T) {
	if adminToken == "" {
		t.Skip("admin token not available")
	}

	// 先获取列表拿到第一个 ID
	resp, _, err := doGet(baseURL+"/api/v1/admin/param-mappings", adminToken)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	var params []struct {
		ID uint `json:"id"`
	}
	json.Unmarshal(resp.Data, &params)
	if len(params) == 0 {
		t.Skip("no params found")
	}

	// 获取详情
	resp2, statusCode, err := doGet(baseURL+"/api/v1/admin/param-mappings/"+itoa(params[0].ID), adminToken)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", statusCode)
	}

	var detail struct {
		ID        uint   `json:"id"`
		ParamName string `json:"param_name"`
		Mappings  []struct {
			ID uint `json:"id"`
		} `json:"mappings"`
	}
	if err := json.Unmarshal(resp2.Data, &detail); err != nil {
		t.Fatalf("parse detail: %v", err)
	}
	if detail.ID != params[0].ID {
		t.Errorf("expected id=%d, got=%d", params[0].ID, detail.ID)
	}
}

// TestParamMapping_CRUD 创建、更新、删除参数
func TestParamMapping_CRUD(t *testing.T) {
	if adminToken == "" {
		t.Skip("admin token not available")
	}

	// 1. 创建参数（唯一名避免冲突）
	uniqueName := fmt.Sprintf("test_api_param_%d", time.Now().UnixNano())
	createResp, statusCode, err := doPost(baseURL+"/api/v1/admin/param-mappings", map[string]interface{}{
		"param_name":    uniqueName,
		"param_type":    "string",
		"display_name":  "API测试参数",
		"description":   "用于API集成测试的临时参数",
		"default_value": "\"test\"",
		"category":      "safety",
		"is_active":     true,
	}, adminToken)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if statusCode == http.StatusForbidden {
		t.Skip("param mapping route permission not mapped")
	}
	if statusCode != http.StatusOK {
		t.Fatalf("create: expected 200, got %d: %s", statusCode, createResp.Message)
	}

	var created struct {
		ID uint `json:"id"`
	}
	json.Unmarshal(createResp.Data, &created)
	if created.ID == 0 {
		t.Fatal("expected created.id > 0")
	}
	paramID := itoa(created.ID)

	// 2. 更新参数
	updateResp, statusCode, err := doPut(baseURL+"/api/v1/admin/param-mappings/"+paramID, map[string]interface{}{
		"display_name": "已更新的测试参数",
		"is_active":    false,
	}, adminToken)
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("update: expected 200, got %d: %s", statusCode, updateResp.Message)
	}

	// 3. 验证更新
	getResp, _, _ := doGet(baseURL+"/api/v1/admin/param-mappings/"+paramID, adminToken)
	var updated struct {
		DisplayName string `json:"display_name"`
		IsActive    bool   `json:"is_active"`
	}
	json.Unmarshal(getResp.Data, &updated)
	if updated.DisplayName != "已更新的测试参数" {
		t.Errorf("expected updated display_name, got=%s", updated.DisplayName)
	}
	if updated.IsActive {
		t.Error("expected is_active=false after update")
	}

	// 4. 删除参数
	deleteResp, statusCode, err := doDelete(baseURL+"/api/v1/admin/param-mappings/"+paramID, adminToken)
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d: %s", statusCode, deleteResp.Message)
	}

	// 5. 验证删除（应404或空）
	getResp2, statusCode2, _ := doGet(baseURL+"/api/v1/admin/param-mappings/"+paramID, adminToken)
	if statusCode2 == http.StatusOK && getResp2.Code == 0 {
		t.Error("expected param to be deleted, but still accessible")
	}
}

// TestParamMapping_MappingCRUD 映射的创建、更新、删除
func TestParamMapping_MappingCRUD(t *testing.T) {
	if adminToken == "" {
		t.Skip("admin token not available")
	}

	// 创建临时参数（唯一名避免冲突）
	mappingParamName := fmt.Sprintf("test_mapping_%d", time.Now().UnixNano())
	createResp, _, _ := doPost(baseURL+"/api/v1/admin/param-mappings", map[string]interface{}{
		"param_name": mappingParamName,
		"param_type": "bool",
		"category":   "thinking",
		"is_active":  true,
	}, adminToken)
	var param struct {
		ID uint `json:"id"`
	}
	json.Unmarshal(createResp.Data, &param)
	defer doDelete(baseURL+"/api/v1/admin/param-mappings/"+itoa(param.ID), adminToken)

	// 1. 创建映射
	mappingResp, statusCode, err := doPost(baseURL+"/api/v1/admin/param-mappings/"+itoa(param.ID)+"/mappings", map[string]interface{}{
		"supplier_code":     "openai",
		"vendor_param_name": "reasoning_effort",
		"transform_type":    "rename",
		"supported":         true,
		"notes":             "测试映射",
	}, adminToken)
	if err != nil {
		t.Fatalf("create mapping failed: %v", err)
	}
	if statusCode == http.StatusForbidden {
		t.Skip("param mapping route permission not mapped")
	}
	if statusCode != http.StatusOK {
		t.Fatalf("create mapping: expected 200, got %d: %s", statusCode, mappingResp.Message)
	}

	var mapping struct {
		ID uint `json:"id"`
	}
	json.Unmarshal(mappingResp.Data, &mapping)
	if mapping.ID == 0 {
		t.Fatal("expected mapping.id > 0")
	}

	// 2. Upsert 更新映射（同一 param + supplier 组合）
	updateResp, statusCode, _ := doPost(baseURL+"/api/v1/admin/param-mappings/"+itoa(param.ID)+"/mappings", map[string]interface{}{
		"supplier_code":     "openai",
		"vendor_param_name": "reasoning_effort_v2",
		"transform_type":    "direct",
		"supported":         false,
		"notes":             "更新后的映射",
	}, adminToken)
	if statusCode != http.StatusOK {
		t.Fatalf("upsert mapping: expected 200, got %d: %s", statusCode, updateResp.Message)
	}

	// 3. 验证更新（通过获取供应商映射）
	supplierResp, _, _ := doGet(baseURL+"/api/v1/admin/param-mappings/supplier/openai", adminToken)
	var supplierMappings []struct {
		PlatformParamID uint   `json:"platform_param_id"`
		VendorParamName string `json:"vendor_param_name"`
		TransformType   string `json:"transform_type"`
		Supported       bool   `json:"supported"`
	}
	json.Unmarshal(supplierResp.Data, &supplierMappings)

	found := false
	for _, m := range supplierMappings {
		if m.PlatformParamID == param.ID {
			found = true
			if m.VendorParamName != "reasoning_effort_v2" {
				t.Errorf("expected vendor_param=reasoning_effort_v2, got=%s", m.VendorParamName)
			}
			if m.TransformType != "direct" {
				t.Errorf("expected transform_type=direct, got=%s", m.TransformType)
			}
			if m.Supported {
				t.Error("expected supported=false after upsert")
			}
		}
	}
	if !found {
		t.Error("expected mapping for test param in openai supplier")
	}

	// 4. 删除映射
	delResp, statusCode, _ := doDelete(baseURL+"/api/v1/admin/param-mappings/mappings/"+itoa(mapping.ID), adminToken)
	if statusCode != http.StatusOK {
		t.Fatalf("delete mapping: expected 200, got %d: %s", statusCode, delResp.Message)
	}
}

// TestParamMapping_SupplierBatchUpdate 批量更新供应商映射
func TestParamMapping_SupplierBatchUpdate(t *testing.T) {
	if adminToken == "" {
		t.Skip("admin token not available")
	}

	// 获取 enable_thinking 的 ID
	resp, _, _ := doGet(baseURL+"/api/v1/admin/param-mappings", adminToken)
	var params []struct {
		ID        uint   `json:"id"`
		ParamName string `json:"param_name"`
	}
	json.Unmarshal(resp.Data, &params)

	var thinkingID, searchID uint
	for _, p := range params {
		if p.ParamName == "enable_thinking" {
			thinkingID = p.ID
		}
		if p.ParamName == "enable_search" {
			searchID = p.ID
		}
	}
	if thinkingID == 0 || searchID == 0 {
		t.Skip("seed params not found")
	}

	// 获取当前 baidu_wenxin 映射（保存用于恢复）
	origResp, _, _ := doGet(baseURL+"/api/v1/admin/param-mappings/supplier/baidu_wenxin", adminToken)
	var origMappings json.RawMessage
	origMappings = origResp.Data

	// 批量更新
	batchResp, statusCode, err := doPut(baseURL+"/api/v1/admin/param-mappings/supplier/baidu_wenxin", []map[string]interface{}{
		{
			"platform_param_id": thinkingID,
			"vendor_param_name": "enable_deep_think",
			"transform_type":    "rename",
			"supported":         true,
			"notes":             "批量更新测试",
		},
		{
			"platform_param_id": searchID,
			"vendor_param_name": "enable_search",
			"transform_type":    "direct",
			"supported":         true,
		},
	}, adminToken)
	if err != nil {
		t.Fatalf("batch update failed: %v", err)
	}
	if statusCode == http.StatusForbidden {
		t.Skip("param mapping route permission not mapped")
	}
	if statusCode != http.StatusOK {
		t.Fatalf("batch update: expected 200, got %d: %s", statusCode, batchResp.Message)
	}

	// 验证
	verifyResp, _, _ := doGet(baseURL+"/api/v1/admin/param-mappings/supplier/baidu_wenxin", adminToken)
	var updated []struct {
		PlatformParamID uint   `json:"platform_param_id"`
		VendorParamName string `json:"vendor_param_name"`
	}
	json.Unmarshal(verifyResp.Data, &updated)

	if len(updated) != 2 {
		t.Errorf("expected 2 mappings after batch update, got %d", len(updated))
	}

	// 恢复原始数据
	var restoreData []map[string]interface{}
	json.Unmarshal(origMappings, &restoreData)
	if len(restoreData) > 0 {
		doPut(baseURL+"/api/v1/admin/param-mappings/supplier/baidu_wenxin", restoreData, adminToken)
	}
}

// TestParamMapping_UnauthorizedAccess 未认证请求应失败
func TestParamMapping_UnauthorizedAccess(t *testing.T) {
	_, statusCode, _ := doGet(baseURL+"/api/v1/admin/param-mappings", "")
	if statusCode == http.StatusOK {
		t.Error("expected unauthorized access to be rejected")
	}
}

// TestParamMapping_UserCannotAccess 普通用户不能访问管理接口
func TestParamMapping_UserCannotAccess(t *testing.T) {
	if userToken == "" {
		t.Skip("user token not available")
	}

	_, statusCode, _ := doGet(baseURL+"/api/v1/admin/param-mappings", userToken)
	if statusCode == http.StatusOK {
		t.Error("expected regular user to be rejected from admin endpoint")
	}
}

// TestParamMapping_SeedDataIntegrity 验证种子数据完整性
func TestParamMapping_SeedDataIntegrity(t *testing.T) {
	if adminToken == "" {
		t.Skip("admin token not available")
	}

	resp, _, _ := doGet(baseURL+"/api/v1/admin/param-mappings", adminToken)
	var params []struct {
		ID        uint   `json:"id"`
		ParamName string `json:"param_name"`
		ParamType string `json:"param_type"`
		Category  string `json:"category"`
		IsActive  bool   `json:"is_active"`
		Mappings  []struct {
			SupplierCode string `json:"supplier_code"`
			Supported    bool   `json:"supported"`
		} `json:"mappings"`
	}
	json.Unmarshal(resp.Data, &params)

	// 预期的参数列表
	expectedParams := map[string]string{
		"enable_thinking":   "bool",
		"thinking_budget":   "int",
		"reasoning_effort":  "string",
		"enable_search":     "bool",
		"response_format":   "json",
		"frequency_penalty": "float",
		"presence_penalty":  "float",
		"seed":              "int",
		"top_k":             "int",
		"safe_mode":         "bool",
	}

	paramMap := make(map[string]struct {
		ParamType string
		Mappings  int
	})
	for _, p := range params {
		paramMap[p.ParamName] = struct {
			ParamType string
			Mappings  int
		}{p.ParamType, len(p.Mappings)}
	}

	// safe_mode 是平台概念，大多数供应商不直接支持，允许无映射
	noMappingAllowed := map[string]bool{"safe_mode": true}

	for name, expectedType := range expectedParams {
		p, ok := paramMap[name]
		if !ok {
			t.Errorf("missing expected param: %s", name)
			continue
		}
		if p.ParamType != expectedType {
			t.Errorf("param %s: expected type=%s, got=%s", name, expectedType, p.ParamType)
		}
		if p.Mappings == 0 && !noMappingAllowed[name] {
			t.Errorf("param %s: expected at least 1 mapping, got 0", name)
		}
	}

	// 验证分类覆盖
	categories := make(map[string]bool)
	for _, p := range params {
		categories[p.Category] = true
	}
	for _, cat := range []string{"thinking", "search", "format", "penalty"} {
		if !categories[cat] {
			t.Errorf("missing expected category: %s", cat)
		}
	}
}

// TestParamMapping_NestedTransformRule 验证嵌套转换规则格式正确
func TestParamMapping_NestedTransformRule(t *testing.T) {
	if adminToken == "" {
		t.Skip("admin token not available")
	}

	resp, _, _ := doGet(baseURL+"/api/v1/admin/param-mappings", adminToken)
	var params []struct {
		ParamName string `json:"param_name"`
		Mappings  []struct {
			SupplierCode  string `json:"supplier_code"`
			TransformType string `json:"transform_type"`
			TransformRule string `json:"transform_rule"`
		} `json:"mappings"`
	}
	json.Unmarshal(resp.Data, &params)

	for _, p := range params {
		for _, m := range p.Mappings {
			if m.TransformType == "nested" && m.TransformRule != "" {
				// 验证 transform_rule 是合法 JSON
				var rule interface{}
				if err := json.Unmarshal([]byte(m.TransformRule), &rule); err != nil {
					t.Errorf("param=%s supplier=%s: transform_rule is invalid JSON: %s", p.ParamName, m.SupplierCode, m.TransformRule)
				}
			}
		}
	}
}

// ----- 辅助函数 -----

func itoa(n uint) string {
	return fmt.Sprintf("%d", n)
}
