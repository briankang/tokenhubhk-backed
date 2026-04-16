package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// ==================== 渠道统计 API 测试 ====================

// TestGetChannelStats_Success 测试获取渠道统计（默认24小时）
func TestGetChannelStats_Success(t *testing.T) {
	requireAdmin(t)

	resp, statusCode, err := doGet(baseURL+"/api/v1/admin/channel-stats?hours=24", adminToken)
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

	// 解析为数组，验证结构
	var stats []json.RawMessage
	if err := json.Unmarshal(resp.Data, &stats); err != nil {
		t.Fatalf("expected array of channel stat items, parse failed: %v", err)
	}

	t.Logf("渠道统计返回 %d 条记录", len(stats))
}

// TestGetChannelStats_CustomHours 测试自定义小时参数
func TestGetChannelStats_CustomHours(t *testing.T) {
	requireAdmin(t)

	resp, statusCode, err := doGet(baseURL+"/api/v1/admin/channel-stats?hours=1", adminToken)
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

	var stats []json.RawMessage
	if err := json.Unmarshal(resp.Data, &stats); err != nil {
		t.Fatalf("expected array response, parse failed: %v", err)
	}

	t.Logf("渠道统计(1小时)返回 %d 条记录", len(stats))
}

// TestGetChannelStats_Unauthorized 测试未认证访问渠道统计
func TestGetChannelStats_Unauthorized(t *testing.T) {
	resp, statusCode, err := doGet(baseURL+"/api/v1/admin/channel-stats?hours=24", "")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, statusCode)

	if statusCode == http.StatusOK && resp.Code == 0 {
		t.Fatal("expected unauthorized error, but got 200 with code 0")
	}
	if statusCode != http.StatusUnauthorized {
		t.Logf("注意: 期望 401, 实际返回 %d (可能返回其他错误码)", statusCode)
	}
}

// TestGetChannelStats_UserForbidden 测试普通用户访问渠道统计被拒绝
func TestGetChannelStats_UserForbidden(t *testing.T) {
	requireUser(t)

	resp, statusCode, err := doGet(baseURL+"/api/v1/admin/channel-stats?hours=24", userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, statusCode)

	if statusCode == http.StatusOK && resp.Code == 0 {
		t.Fatal("expected forbidden error for regular user, but got 200 with code 0")
	}
	if statusCode != http.StatusForbidden {
		t.Logf("注意: 期望 403, 实际返回 %d (可能返回其他错误码)", statusCode)
	}
}

// ==================== 定时任务管理 API 测试 ====================

// TestListCronTasks_Success 测试获取定时任务列表
func TestListCronTasks_Success(t *testing.T) {
	requireAdmin(t)

	resp, statusCode, err := doGet(baseURL+"/api/v1/admin/cron-tasks", adminToken)
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

	var tasks []struct {
		Name     string `json:"name"`
		Schedule string `json:"schedule"`
		Enabled  bool   `json:"enabled"`
	}
	if err := json.Unmarshal(resp.Data, &tasks); err != nil {
		t.Fatalf("parse cron tasks failed: %v", err)
	}

	if len(tasks) == 0 {
		t.Fatal("expected non-empty array of cron tasks")
	}

	// 验证每个任务都有必要字段
	for i, task := range tasks {
		if task.Name == "" {
			t.Errorf("task[%d] has empty name", i)
		}
		if task.Schedule == "" {
			t.Errorf("task[%d] (%s) has empty schedule", i, task.Name)
		}
	}

	t.Logf("定时任务列表返回 %d 个任务", len(tasks))
}

// TestToggleCronTask_DisableAndEnable 测试禁用和启用单个定时任务
func TestToggleCronTask_DisableAndEnable(t *testing.T) {
	requireAdmin(t)

	taskName := "frozen_release"

	// 确保测试结束后重新启用任务，避免破坏系统
	t.Cleanup(func() {
		doPut(
			fmt.Sprintf("%s/api/v1/admin/cron-tasks/%s/toggle", baseURL, taskName),
			map[string]interface{}{"enabled": true},
			adminToken,
		)
	})

	// 步骤1: 禁用任务
	resp, statusCode, err := doPut(
		fmt.Sprintf("%s/api/v1/admin/cron-tasks/%s/toggle", baseURL, taskName),
		map[string]interface{}{"enabled": false},
		adminToken,
	)
	if err != nil {
		t.Fatalf("disable request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200 for disable, got %d: %s", statusCode, resp.Message)
	}

	// 步骤2: 验证任务已禁用
	listResp, listStatus, err := doGet(baseURL+"/api/v1/admin/cron-tasks", adminToken)
	if err != nil {
		t.Fatalf("list request failed: %v", err)
	}
	if listStatus != http.StatusOK {
		t.Fatalf("list expected 200, got %d", listStatus)
	}

	var tasks []struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal(listResp.Data, &tasks); err != nil {
		t.Fatalf("parse tasks list failed: %v", err)
	}

	found := false
	for _, task := range tasks {
		if task.Name == taskName {
			found = true
			if task.Enabled {
				t.Errorf("expected task %s to be disabled, but it is enabled", taskName)
			}
			break
		}
	}
	if !found {
		t.Logf("注意: 任务 %s 未在列表中找到，可能名称不同", taskName)
	}

	// 步骤3: 重新启用任务
	resp, statusCode, err = doPut(
		fmt.Sprintf("%s/api/v1/admin/cron-tasks/%s/toggle", baseURL, taskName),
		map[string]interface{}{"enabled": true},
		adminToken,
	)
	if err != nil {
		t.Fatalf("enable request failed: %v", err)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("expected 200 for enable, got %d: %s", statusCode, resp.Message)
	}

	t.Logf("定时任务 %s 禁用/启用切换测试通过", taskName)
}

// TestToggleCronTask_NonExistent 测试切换不存在的定时任务
func TestToggleCronTask_NonExistent(t *testing.T) {
	requireAdmin(t)

	resp, statusCode, err := doPut(
		baseURL+"/api/v1/admin/cron-tasks/nonexistent_task/toggle",
		map[string]interface{}{"enabled": false},
		adminToken,
	)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	// 不存在的任务应返回错误
	if statusCode == http.StatusOK && resp.Code == 0 {
		t.Error("expected error for non-existent task, but got success")
	}

	t.Logf("不存在的任务返回: status=%d, code=%d, message=%s", statusCode, resp.Code, resp.Message)
}

// TestBatchToggleCronTasks 测试批量切换定时任务状态
func TestBatchToggleCronTasks(t *testing.T) {
	requireAdmin(t)

	taskNames := []string{"frozen_release", "balance_reconcile"}

	// 确保测试结束后重新启用所有任务
	t.Cleanup(func() {
		doPut(
			baseURL+"/api/v1/admin/cron-tasks/batch-toggle",
			map[string]interface{}{
				"names":   taskNames,
				"enabled": true,
			},
			adminToken,
		)
	})

	// 步骤1: 批量禁用
	resp, statusCode, err := doPut(
		baseURL+"/api/v1/admin/cron-tasks/batch-toggle",
		map[string]interface{}{
			"names":   taskNames,
			"enabled": false,
		},
		adminToken,
	)
	if err != nil {
		t.Fatalf("batch disable request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("expected 200 for batch disable, got %d: %s", statusCode, resp.Message)
	}

	// 步骤2: 验证任务已禁用
	listResp, listStatus, err := doGet(baseURL+"/api/v1/admin/cron-tasks", adminToken)
	if err != nil {
		t.Fatalf("list request failed: %v", err)
	}
	if listStatus != http.StatusOK {
		t.Fatalf("list expected 200, got %d", listStatus)
	}

	var tasks []struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal(listResp.Data, &tasks); err != nil {
		t.Fatalf("parse tasks list failed: %v", err)
	}

	for _, targetName := range taskNames {
		for _, task := range tasks {
			if task.Name == targetName && task.Enabled {
				t.Errorf("expected task %s to be disabled after batch toggle", targetName)
			}
		}
	}

	// 步骤3: 批量重新启用
	resp, statusCode, err = doPut(
		baseURL+"/api/v1/admin/cron-tasks/batch-toggle",
		map[string]interface{}{
			"names":   taskNames,
			"enabled": true,
		},
		adminToken,
	)
	if err != nil {
		t.Fatalf("batch enable request failed: %v", err)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("expected 200 for batch enable, got %d: %s", statusCode, resp.Message)
	}

	t.Log("批量切换定时任务测试通过")
}

// ==================== 缓存管理 API 测试 ====================

// TestClearChannelRouteCache 测试清除渠道路由缓存
func TestClearChannelRouteCache(t *testing.T) {
	requireAdmin(t)

	resp, statusCode, err := doPost(baseURL+"/api/v1/admin/cache/clear-channel-routes", nil, adminToken)
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

	var data struct {
		Deleted int64  `json:"deleted"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatalf("parse response data failed: %v", err)
	}

	t.Logf("清除渠道路由缓存: deleted=%d, message=%s", data.Deleted, data.Message)
}

// TestClearAllCache 测试清除所有缓存
func TestClearAllCache(t *testing.T) {
	requireAdmin(t)

	resp, statusCode, err := doPost(baseURL+"/api/v1/admin/cache/clear-all", nil, adminToken)
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

	var data struct {
		Deleted int64  `json:"deleted"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatalf("parse response data failed: %v", err)
	}

	t.Logf("清除所有缓存: deleted=%d, message=%s", data.Deleted, data.Message)
}

// TestCacheStats 测试获取缓存统计信息
func TestCacheStats(t *testing.T) {
	requireAdmin(t)

	resp, statusCode, err := doGet(baseURL+"/api/v1/admin/cache/stats", adminToken)
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

	var stats struct {
		KeyCount   int64  `json:"key_count"`
		MemoryUsed string `json:"memory_used"`
	}
	if err := json.Unmarshal(resp.Data, &stats); err != nil {
		t.Fatalf("parse cache stats failed: %v", err)
	}

	t.Logf("缓存统计: key_count=%d, memory_used=%s", stats.KeyCount, stats.MemoryUsed)
}

// ==================== 审计日志 API 测试 ====================

// TestGetAuditLogs_Success 测试获取审计日志列表
func TestGetAuditLogs_Success(t *testing.T) {
	requireAdmin(t)

	resp, statusCode, err := doGet(baseURL+"/api/v1/admin/audit-logs", adminToken)
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

	// 尝试解析为分页格式或数组格式
	var pageData pageResponse
	if err := json.Unmarshal(resp.Data, &pageData); err == nil {
		t.Logf("审计日志返回分页格式: total=%d, page=%d", pageData.Total, pageData.Page)
		return
	}

	// 也可能直接返回数组
	var logs []json.RawMessage
	if err := json.Unmarshal(resp.Data, &logs); err == nil {
		t.Logf("审计日志返回 %d 条记录", len(logs))
		return
	}

	t.Log("审计日志返回成功，数据格式非标准分页或数组")
}

// TestGetAuditLogs_WithFilters 测试带过滤条件获取审计日志
func TestGetAuditLogs_WithFilters(t *testing.T) {
	requireAdmin(t)

	resp, statusCode, err := doGet(
		baseURL+"/api/v1/admin/audit-logs?action=LOGIN&start_date=2026-01-01",
		adminToken,
	)
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

	t.Logf("审计日志(带过滤)返回成功: message=%s", resp.Message)
}

// ==================== 自定义渠道管理 API 测试（含缓存失效验证） ====================

// TestCustomChannel_CRUD_WithCacheInvalidation 测试自定义渠道的完整 CRUD 流程
func TestCustomChannel_CRUD_WithCacheInvalidation(t *testing.T) {
	requireAdmin(t)

	// ---- 步骤1: 创建自定义渠道 ----
	channelName := uniqueName("custom_ch")
	createResp, statusCode, err := doPost(baseURL+"/api/v1/admin/channels", map[string]interface{}{
		"name":        channelName,
		"supplier_id": 1,
		"type":        "openai",
		"endpoint":    "https://api.example.com/v1",
		"api_key":     "sk-custom-test-" + channelName,
		"weight":      10,
		"priority":    1,
		"status":      "active",
	}, adminToken)
	if err != nil {
		t.Fatalf("create request failed: %v", err)
	}
	skipIfNotImplemented(t, statusCode)
	skipIfNotFound(t, statusCode)

	if statusCode != http.StatusOK {
		t.Fatalf("create expected 200, got %d: %s", statusCode, createResp.Message)
	}
	if createResp.Code != 0 {
		t.Fatalf("create expected code 0, got %d: %s", createResp.Code, createResp.Message)
	}

	var created struct {
		ID   uint   `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(createResp.Data, &created); err != nil {
		t.Fatalf("parse created channel failed: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("expected non-zero channel ID after create")
	}

	t.Logf("创建自定义渠道成功: id=%d, name=%s", created.ID, created.Name)

	// ---- 步骤2: 验证渠道出现在列表中 ----
	listResp, listStatus, err := doGet(
		fmt.Sprintf("%s/api/v1/admin/channels?page=1&page_size=100", baseURL),
		adminToken,
	)
	if err != nil {
		t.Fatalf("list request failed: %v", err)
	}
	if listStatus != http.StatusOK {
		t.Fatalf("list expected 200, got %d", listStatus)
	}

	// 尝试在返回数据中找到刚创建的渠道
	foundInList := false
	var pageData pageResponse
	if err := json.Unmarshal(listResp.Data, &pageData); err == nil && pageData.List != nil {
		var channels []struct {
			ID   uint   `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(pageData.List, &channels); err == nil {
			for _, ch := range channels {
				if ch.ID == created.ID {
					foundInList = true
					break
				}
			}
		}
	}

	if foundInList {
		t.Logf("已验证渠道 %d 出现在列表中", created.ID)
	} else {
		t.Logf("注意: 未在列表中找到渠道 %d (可能需要不同的分页或列表格式)", created.ID)
	}

	// ---- 步骤3: 更新渠道 ----
	updateResp, updateStatus, err := doPut(
		fmt.Sprintf("%s/api/v1/admin/channels/%d", baseURL, created.ID),
		map[string]interface{}{
			"weight":   20,
			"priority": 2,
		},
		adminToken,
	)
	if err != nil {
		t.Fatalf("update request failed: %v", err)
	}
	skipIfNotImplemented(t, updateStatus)

	if updateStatus != http.StatusOK {
		t.Fatalf("update expected 200, got %d: %s", updateStatus, updateResp.Message)
	}

	t.Logf("更新渠道 %d 成功", created.ID)

	// ---- 步骤4: 删除渠道 ----
	deleteResp, deleteStatus, err := doDelete(
		fmt.Sprintf("%s/api/v1/admin/channels/%d", baseURL, created.ID),
		adminToken,
	)
	if err != nil {
		t.Fatalf("delete request failed: %v", err)
	}
	skipIfNotImplemented(t, deleteStatus)

	if deleteStatus != http.StatusOK {
		t.Fatalf("delete expected 200, got %d: %s", deleteStatus, deleteResp.Message)
	}

	t.Logf("删除渠道 %d 成功，CRUD 全流程通过", created.ID)

	// ---- 缓存失效验证: 删除后不应在列表中出现 ----
	listResp2, listStatus2, err := doGet(
		fmt.Sprintf("%s/api/v1/admin/channels?page=1&page_size=100", baseURL),
		adminToken,
	)
	if err != nil {
		t.Fatalf("post-delete list request failed: %v", err)
	}
	if listStatus2 != http.StatusOK {
		t.Fatalf("post-delete list expected 200, got %d", listStatus2)
	}

	stillExists := false
	var pageData2 pageResponse
	if err := json.Unmarshal(listResp2.Data, &pageData2); err == nil && pageData2.List != nil {
		var channels []struct {
			ID uint `json:"id"`
		}
		if err := json.Unmarshal(pageData2.List, &channels); err == nil {
			for _, ch := range channels {
				if ch.ID == created.ID {
					stillExists = true
					break
				}
			}
		}
	}

	if stillExists {
		t.Error("渠道删除后仍出现在列表中，缓存可能未正确失效")
	} else {
		t.Log("缓存失效验证通过: 删除后渠道已从列表中移除")
	}
}
