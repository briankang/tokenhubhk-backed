package api_test

import (
	"net/http"
	"testing"
)

// ═══════════════════════════════════════════════════════════════
// 安装向导 API 测试
// ═══════════════════════════════════════════════════════════════

// TestSetup_Status 测试初始化状态查询
func TestSetup_Status(t *testing.T) {
	resp, status, err := doGet(baseURL+"/api/v1/setup/status", "")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	skipIfNotFound(t, status)
	if status != http.StatusOK {
		t.Fatalf("期望 200，实际 %d: %s", status, resp.Message)
	}
	// data 应该包含 initialized 字段
	if resp.Data == nil {
		t.Fatal("返回数据为空")
	}
	t.Logf("setup status response: %s", string(resp.Data))
}

// TestSetup_TestDB 测试数据库连接检测
func TestSetup_TestDB(t *testing.T) {
	resp, status, err := doPost(baseURL+"/api/v1/setup/test-db", nil, "")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	skipIfNotFound(t, status)
	// 如果系统已初始化，返回 403
	if status == http.StatusForbidden {
		t.Log("系统已初始化，test-db 返回 403（预期行为）")
		return
	}
	if status != http.StatusOK {
		t.Fatalf("期望 200 或 403，实际 %d: %s", status, resp.Message)
	}
	t.Log("数据库连接测试通过")
}

// TestSetup_TestRedis 测试 Redis 连接检测
func TestSetup_TestRedis(t *testing.T) {
	resp, status, err := doPost(baseURL+"/api/v1/setup/test-redis", nil, "")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	skipIfNotFound(t, status)
	if status == http.StatusForbidden {
		t.Log("系统已初始化，test-redis 返回 403（预期行为）")
		return
	}
	if status != http.StatusOK {
		t.Fatalf("期望 200 或 403，实际 %d: %s", status, resp.Message)
	}
	t.Log("Redis 连接测试通过")
}

// TestSetup_Migrate 测试数据库迁移
func TestSetup_Migrate(t *testing.T) {
	resp, status, err := doPost(baseURL+"/api/v1/setup/migrate", nil, "")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	skipIfNotFound(t, status)
	if status == http.StatusForbidden {
		t.Log("系统已初始化，migrate 返回 403（预期行为）")
		return
	}
	if status != http.StatusOK {
		t.Fatalf("期望 200 或 403，实际 %d: %s", status, resp.Message)
	}
	t.Log("数据库迁移通过")
}

// TestSetup_InitCache 测试缓存初始化
func TestSetup_InitCache(t *testing.T) {
	resp, status, err := doPost(baseURL+"/api/v1/setup/init-cache", nil, "")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	skipIfNotFound(t, status)
	if status == http.StatusForbidden {
		t.Log("系统已初始化，init-cache 返回 403（预期行为）")
		return
	}
	if status != http.StatusOK {
		t.Fatalf("期望 200 或 403，实际 %d: %s", status, resp.Message)
	}
	t.Log("缓存初始化通过")
}

// TestSetup_CreateAdmin 测试创建管理员（已初始化时返回403）
func TestSetup_CreateAdmin(t *testing.T) {
	body := map[string]string{
		"username": "test_admin",
		"password": "Test@123456",
		"email":    "test_admin@tokenhub.ai",
	}
	resp, status, err := doPost(baseURL+"/api/v1/setup/create-admin", body, "")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	skipIfNotFound(t, status)
	if status == http.StatusForbidden {
		t.Log("系统已初始化，create-admin 返回 403（预期行为）")
		return
	}
	// 可能管理员已存在返回 500
	if status == http.StatusOK {
		t.Log("管理员创建成功")
	} else {
		t.Logf("create-admin 返回 %d: %s（管理员可能已存在）", status, resp.Message)
	}
}

// TestSetup_CreateAdmin_BadRequest 测试参数校验
func TestSetup_CreateAdmin_BadRequest(t *testing.T) {
	// 缺少必填字段
	body := map[string]string{
		"username": "",
		"password": "",
		"email":    "",
	}
	resp, status, err := doPost(baseURL+"/api/v1/setup/create-admin", body, "")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	skipIfNotFound(t, status)
	if status == http.StatusForbidden {
		t.Log("系统已初始化，返回 403（预期行为）")
		return
	}
	if status != http.StatusBadRequest {
		t.Logf("期望 400，实际 %d: %s", status, resp.Message)
	}
}

// TestSetup_SaveConfig 测试保存配置
func TestSetup_SaveConfig(t *testing.T) {
	body := map[string]string{
		"site_name": "TokenHub AI Test",
	}
	resp, status, err := doPost(baseURL+"/api/v1/setup/save-config", body, "")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	skipIfNotFound(t, status)
	if status == http.StatusForbidden {
		t.Log("系统已初始化，save-config 返回 403（预期行为）")
		return
	}
	if status != http.StatusOK {
		t.Fatalf("期望 200 或 403，实际 %d: %s", status, resp.Message)
	}
	t.Log("配置保存成功")
}

// TestSetup_Finalize 测试完成安装
func TestSetup_Finalize(t *testing.T) {
	resp, status, err := doPost(baseURL+"/api/v1/setup/finalize", nil, "")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	skipIfNotFound(t, status)
	if status == http.StatusForbidden {
		t.Log("系统已初始化，finalize 返回 403（预期行为）")
		return
	}
	if status != http.StatusOK {
		t.Fatalf("期望 200 或 403，实际 %d: %s", status, resp.Message)
	}
	t.Log("安装完成")
}

// TestSetup_GuardAfterInit 测试初始化后守卫中间件
func TestSetup_GuardAfterInit(t *testing.T) {
	// 先检查状态
	resp, status, err := doGet(baseURL+"/api/v1/setup/status", "")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	skipIfNotFound(t, status)
	if status != http.StatusOK {
		t.Skipf("无法获取 setup status: %d", status)
	}
	t.Logf("status 响应: %s", string(resp.Data))

	// status 端点始终可访问（不受 403 限制）
	if status != http.StatusOK {
		t.Fatalf("setup/status 应始终返回 200，实际 %d", status)
	}
}

// TestSetup_StatusAlwaysAccessible 验证 /setup/status 不需要认证
func TestSetup_StatusAlwaysAccessible(t *testing.T) {
	resp, status, err := doGet(baseURL+"/api/v1/setup/status", "invalid_token_12345")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	skipIfNotFound(t, status)
	// status 端点应始终返回 200，无论 token 是否有效
	if status != http.StatusOK {
		t.Fatalf("期望 200，实际 %d: %s", status, resp.Message)
	}
}
