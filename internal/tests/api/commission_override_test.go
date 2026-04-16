package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// ---- 佣金覆盖相关数据结构 ----

type commissionOverrideItem struct {
	ID             uint    `json:"id"`
	UserID         uint    `json:"userId"`
	UserEmail      string  `json:"userEmail"`
	IsActive       bool    `json:"isActive"`
	CommissionRate float64 `json:"commissionRate"`
	Note           string  `json:"note"`
}

// ============================================================
// TestAdminCreateCommissionOverride — 创建加佣配置
// ============================================================
func TestAdminCreateCommissionOverride(t *testing.T) {
	requireAdmin(t)

	// 先注册一个目标用户
	email := uniqueEmail("override_create")
	pass := "Test@123456"
	_, uid := registerAndLogin(t, email, pass)
	if uid == 0 {
		t.Skip("could not obtain target user ID")
	}

	resp, status, err := doPost(baseURL+"/api/v1/admin/commission-overrides", map[string]interface{}{
		"userId":         uid,
		"commissionRate": 0.25,
		"note":           "KOL 合作",
	}, adminToken)
	if err != nil {
		t.Fatalf("create override request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
	if resp.Code != 0 {
		t.Fatalf("expected code 0, got %d: %s", resp.Code, resp.Message)
	}

	var item commissionOverrideItem
	if err := json.Unmarshal(resp.Data, &item); err != nil {
		t.Fatalf("parse override response: %v", err)
	}
	if item.ID == 0 {
		t.Fatal("expected non-zero override ID")
	}
	if item.CommissionRate != 0.25 {
		t.Errorf("expected rate=0.25, got %.4f", item.CommissionRate)
	}
	if !item.IsActive {
		t.Error("expected isActive=true for newly created override")
	}
	t.Logf("TestAdminCreateCommissionOverride: id=%d, rate=%.2f", item.ID, item.CommissionRate)
}

// ============================================================
// TestAdminCreateOverride_RateExceedsCap — rate > 0.80 应返回 400
// ============================================================
func TestAdminCreateOverride_RateExceedsCap(t *testing.T) {
	requireAdmin(t)

	email := uniqueEmail("override_cap")
	pass := "Test@123456"
	_, uid := registerAndLogin(t, email, pass)
	if uid == 0 {
		t.Skip("could not obtain target user ID")
	}

	resp, status, err := doPost(baseURL+"/api/v1/admin/commission-overrides", map[string]interface{}{
		"userId":         uid,
		"commissionRate": 0.85, // 超出 0.80 上限
		"note":           "too high",
	}, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)

	if status == http.StatusOK && resp.Code == 0 {
		t.Fatal("expected validation error for rate>0.80, but got success")
	}
	if status != http.StatusBadRequest {
		t.Logf("注意: 期望 400, 实际返回 %d (仍为预期的错误)", status)
	}
	t.Logf("TestAdminCreateOverride_RateExceedsCap: status=%d, msg=%s", status, resp.Message)
}

// ============================================================
// TestAdminListCommissionOverrides — 创建 2 条后列表 >= 2
// ============================================================
func TestAdminListCommissionOverrides(t *testing.T) {
	requireAdmin(t)

	var createdIDs []uint
	for i := 0; i < 2; i++ {
		email := uniqueEmail(fmt.Sprintf("override_list%d", i))
		pass := "Test@123456"
		_, uid := registerAndLogin(t, email, pass)
		if uid == 0 {
			continue
		}
		resp, status, _ := doPost(baseURL+"/api/v1/admin/commission-overrides", map[string]interface{}{
			"userId":         uid,
			"commissionRate": 0.20,
			"note":           fmt.Sprintf("list test %d", i),
		}, adminToken)
		if status == http.StatusOK && resp.Code == 0 {
			var item commissionOverrideItem
			json.Unmarshal(resp.Data, &item)
			if item.ID > 0 {
				createdIDs = append(createdIDs, item.ID)
			}
		}
	}

	listResp, status, err := doGet(baseURL+"/api/v1/admin/commission-overrides?page=1&page_size=50&active=true", adminToken)
	if err != nil {
		t.Fatalf("list request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, listResp.Message)
	}
	if listResp.Code != 0 {
		t.Fatalf("expected code 0, got %d: %s", listResp.Code, listResp.Message)
	}

	page, err := parsePageData(listResp)
	if err != nil {
		t.Fatalf("parse page data: %v", err)
	}
	if page.Total < 2 {
		t.Errorf("expected total >= 2, got %d", page.Total)
	}
	t.Logf("TestAdminListCommissionOverrides: total=%d, created=%v", page.Total, createdIDs)
}

// ============================================================
// TestAdminUpdateCommissionOverride — 创建后 PUT 修改 rate
// ============================================================
func TestAdminUpdateCommissionOverride(t *testing.T) {
	requireAdmin(t)

	email := uniqueEmail("override_update")
	pass := "Test@123456"
	_, uid := registerAndLogin(t, email, pass)
	if uid == 0 {
		t.Skip("could not obtain target user ID")
	}

	// 创建
	createResp, status, err := doPost(baseURL+"/api/v1/admin/commission-overrides", map[string]interface{}{
		"userId":         uid,
		"commissionRate": 0.20,
		"note":           "original",
	}, adminToken)
	if err != nil {
		t.Fatalf("create request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)
	if status != http.StatusOK {
		t.Fatalf("create expected 200, got %d: %s", status, createResp.Message)
	}

	var created commissionOverrideItem
	json.Unmarshal(createResp.Data, &created)
	if created.ID == 0 {
		t.Fatal("expected non-zero ID after create")
	}

	// 更新
	updateResp, updateStatus, err := doPut(
		fmt.Sprintf("%s/api/v1/admin/commission-overrides/%d", baseURL, created.ID),
		map[string]interface{}{
			"commissionRate": 0.30,
			"note":           "updated note",
		},
		adminToken,
	)
	if err != nil {
		t.Fatalf("update request failed: %v", err)
	}
	skipIfNotFound(t, updateStatus)

	if updateStatus != http.StatusOK {
		t.Fatalf("update expected 200, got %d: %s", updateStatus, updateResp.Message)
	}
	if updateResp.Code != 0 {
		t.Fatalf("update expected code 0, got %d: %s", updateResp.Code, updateResp.Message)
	}

	var updated commissionOverrideItem
	json.Unmarshal(updateResp.Data, &updated)
	if updated.CommissionRate != 0.30 {
		t.Errorf("expected rate=0.30 after update, got %.4f", updated.CommissionRate)
	}
	t.Logf("TestAdminUpdateCommissionOverride: id=%d, new_rate=%.2f", updated.ID, updated.CommissionRate)
}

// ============================================================
// TestAdminDeleteCommissionOverride — 删除后从列表消失
// ============================================================
func TestAdminDeleteCommissionOverride(t *testing.T) {
	requireAdmin(t)

	email := uniqueEmail("override_delete")
	pass := "Test@123456"
	_, uid := registerAndLogin(t, email, pass)
	if uid == 0 {
		t.Skip("could not obtain target user ID")
	}

	// 创建
	createResp, status, _ := doPost(baseURL+"/api/v1/admin/commission-overrides", map[string]interface{}{
		"userId":         uid,
		"commissionRate": 0.15,
		"note":           "to be deleted",
	}, adminToken)
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)
	if status != http.StatusOK {
		t.Fatalf("create expected 200, got %d", status)
	}

	var created commissionOverrideItem
	json.Unmarshal(createResp.Data, &created)
	if created.ID == 0 {
		t.Fatal("expected non-zero ID")
	}

	// 删除
	deleteResp, deleteStatus, err := doDelete(
		fmt.Sprintf("%s/api/v1/admin/commission-overrides/%d", baseURL, created.ID),
		adminToken,
	)
	if err != nil {
		t.Fatalf("delete request failed: %v", err)
	}
	if deleteStatus != http.StatusOK {
		t.Fatalf("delete expected 200, got %d: %s", deleteStatus, deleteResp.Message)
	}

	// 验证 is_active 为 nil/false（列表中不应出现）
	listResp, _, _ := doGet(
		fmt.Sprintf("%s/api/v1/admin/commission-overrides?active=true&page=1&page_size=100", baseURL),
		adminToken,
	)
	if listResp != nil && listResp.Data != nil {
		page, _ := parsePageData(listResp)
		if page != nil && page.List != nil {
			var items []commissionOverrideItem
			json.Unmarshal(page.List, &items)
			for _, item := range items {
				if item.ID == created.ID {
					t.Errorf("expected deleted override id=%d to not appear in active list", created.ID)
				}
			}
		}
	}
	t.Logf("TestAdminDeleteCommissionOverride: deleted id=%d, no longer in active list", created.ID)
}

// ============================================================
// TestAdminGetOverridesByUser — GET /commission-overrides/user/:user_id
// ============================================================
func TestAdminGetOverridesByUser(t *testing.T) {
	requireAdmin(t)

	email := uniqueEmail("override_byuser")
	pass := "Test@123456"
	_, uid := registerAndLogin(t, email, pass)
	if uid == 0 {
		t.Skip("could not obtain target user ID")
	}

	// 创建一条覆盖
	doPost(baseURL+"/api/v1/admin/commission-overrides", map[string]interface{}{
		"userId":         uid,
		"commissionRate": 0.20,
		"note":           "by user test",
	}, adminToken)

	// 按用户查询
	listResp, status, err := doGet(
		fmt.Sprintf("%s/api/v1/admin/commission-overrides/user/%d", baseURL, uid),
		adminToken,
	)
	if err != nil {
		t.Fatalf("get by user request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, listResp.Message)
	}
	if listResp.Code != 0 {
		t.Fatalf("expected code 0, got %d: %s", listResp.Code, listResp.Message)
	}

	// 结果应为数组且至少含 1 条
	var items []commissionOverrideItem
	if err := json.Unmarshal(listResp.Data, &items); err != nil {
		t.Fatalf("parse override list by user: %v", err)
	}
	if len(items) == 0 {
		t.Error("expected at least 1 override for target user, got 0")
	}
	for _, item := range items {
		if item.UserID != uid {
			t.Errorf("all items should have userID=%d, got %d", uid, item.UserID)
		}
	}
	t.Logf("TestAdminGetOverridesByUser: found %d overrides for uid=%d", len(items), uid)
}
