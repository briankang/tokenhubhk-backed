package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// ========== 用户 API Key 完整生命周期测试 ==========

func TestUserApiKey_Generate_Success(t *testing.T) {
	requireUser(t)

	body := map[string]interface{}{
		"name": uniqueName("test-key"),
	}

	resp, status, err := doPost(baseURL+"/api/v1/user/api-keys", body, userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}

	// 验证返回数据包含 key
	var data struct {
		ID  uint   `json:"id"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if data.Key == "" {
		t.Error("generated key should not be empty")
	}
}

func TestUserApiKey_List_Success(t *testing.T) {
	requireUser(t)

	resp, status, err := doGet(baseURL+"/api/v1/user/api-keys", userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

func TestUserApiKey_Reveal_Success(t *testing.T) {
	requireUser(t)

	// 先生成一个 key
	genBody := map[string]interface{}{
		"name": uniqueName("reveal-key"),
	}
	genResp, genStatus, err := doPost(baseURL+"/api/v1/user/api-keys", genBody, userToken)
	if err != nil || genStatus != http.StatusOK {
		t.Skip("cannot generate key for reveal test")
	}

	var genData struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(genResp.Data, &genData); err != nil || genData.ID == 0 {
		t.Skip("cannot parse generated key ID")
	}

	resp, status, err := doGet(fmt.Sprintf("%s/api/v1/user/api-keys/%d/reveal", baseURL, genData.ID), userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)

	if status != http.StatusOK {
		t.Logf("reveal returned %d: %s (may not have encrypted key)", status, resp.Message)
	}
}

func TestUserApiKey_Update_Success(t *testing.T) {
	requireUser(t)

	keyID := getExistingApiKeyID(t)

	body := map[string]interface{}{
		"name": uniqueName("renamed-key"),
	}

	resp, status, err := doPut(fmt.Sprintf("%s/api/v1/user/api-keys/%d", baseURL, keyID), body, userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

func TestUserApiKey_Revoke_Success(t *testing.T) {
	requireUser(t)

	// 创建一个新 key 然后吊销
	genBody := map[string]interface{}{
		"name": uniqueName("revoke-key"),
	}
	genResp, genStatus, err := doPost(baseURL+"/api/v1/user/api-keys", genBody, userToken)
	if err != nil || genStatus != http.StatusOK {
		t.Skip("cannot generate key for revoke test")
	}

	var genData struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(genResp.Data, &genData); err != nil || genData.ID == 0 {
		t.Skip("cannot parse generated key ID")
	}

	resp, status, err := doDelete(fmt.Sprintf("%s/api/v1/user/api-keys/%d", baseURL, genData.ID), userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}

	// 验证已吊销（再次获取应该不在列表中或标记为 revoked）
}

func TestUserApiKey_CrossUser_Forbidden(t *testing.T) {
	requireUser(t)
	requireAdmin(t)

	// 生成一个 admin 的 key
	genBody := map[string]interface{}{
		"name": uniqueName("admin-key"),
	}
	genResp, genStatus, err := doPost(baseURL+"/api/v1/user/api-keys", genBody, adminToken)
	if err != nil || genStatus != http.StatusOK {
		t.Skip("cannot generate admin key")
	}

	var genData struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(genResp.Data, &genData); err != nil || genData.ID == 0 {
		t.Skip("cannot parse admin key ID")
	}

	// 普通用户尝试删除 admin 的 key，应该被拒绝
	_, status, err := doDelete(fmt.Sprintf("%s/api/v1/user/api-keys/%d", baseURL, genData.ID), userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if status == http.StatusOK {
		t.Error("regular user should not be able to revoke admin's API key")
	}
}

// ========== 用户可用模型列表测试 ==========

func TestUserAvailableModels_Success(t *testing.T) {
	requireUser(t)

	resp, status, err := doGet(baseURL+"/api/v1/user/available-models", userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

func TestUserAvailableModels_NoAuth(t *testing.T) {
	_, status, err := doGet(baseURL+"/api/v1/user/available-models", "")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if status != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", status)
	}
}

// ========== 用户可用渠道列表测试 ==========

func TestUserAvailableChannels_Success(t *testing.T) {
	requireUser(t)

	resp, status, err := doGet(baseURL+"/api/v1/user/available-channels", userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

// ========== 会员等级相关测试 ==========

func TestUserMemberProfile_Success(t *testing.T) {
	requireUser(t)

	resp, status, err := doGet(baseURL+"/api/v1/user/member/profile", userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

func TestUserMemberLevels_Success(t *testing.T) {
	requireUser(t)

	resp, status, err := doGet(baseURL+"/api/v1/user/member/levels", userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

func TestUserMemberProgress_Success(t *testing.T) {
	requireUser(t)

	resp, status, err := doGet(baseURL+"/api/v1/user/member/progress", userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

func TestUserMember_NoAuth(t *testing.T) {
	endpoints := []string{
		"/api/v1/user/member/profile",
		"/api/v1/user/member/levels",
		"/api/v1/user/member/progress",
	}

	for _, ep := range endpoints {
		_, status, err := doGet(baseURL+ep, "")
		if err != nil {
			continue
		}
		if status != http.StatusUnauthorized {
			t.Errorf("expected 401 for %s without auth, got %d", ep, status)
		}
	}
}

// ========== 用户使用量/账单增强测试 ==========

func TestUserUsage_Success(t *testing.T) {
	requireUser(t)

	resp, status, err := doGet(baseURL+"/api/v1/user/usage?start_date=2025-01-01&end_date=2026-12-31", userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
}

// ========== 辅助函数 ==========

func getExistingApiKeyID(t *testing.T) uint {
	t.Helper()
	requireUser(t)

	// 先尝试列出已有 keys
	resp, status, err := doGet(baseURL+"/api/v1/user/api-keys", userToken)
	if err == nil && status == http.StatusOK {
		var keys []struct {
			ID uint `json:"id"`
		}
		if err := json.Unmarshal(resp.Data, &keys); err == nil && len(keys) > 0 {
			return keys[0].ID
		}
	}

	// 如果没有，创建一个
	genBody := map[string]interface{}{
		"name": uniqueName("helper-key"),
	}
	genResp, genStatus, err := doPost(baseURL+"/api/v1/user/api-keys", genBody, userToken)
	if err != nil || genStatus != http.StatusOK {
		t.Skip("cannot generate API key")
	}

	var data struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(genResp.Data, &data); err != nil || data.ID == 0 {
		t.Skip("cannot parse API key ID")
	}
	return data.ID
}
