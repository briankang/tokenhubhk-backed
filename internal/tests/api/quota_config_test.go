package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// ---- 配额配置数据结构 ----

type quotaConfigData struct {
	ID                   uint  `json:"id"`
	DefaultFreeQuota     int64 `json:"defaultFreeQuota"`
	RegistrationBonus    int64 `json:"registrationBonus"`
	InviteeBonus         int64 `json:"inviteeBonus"`
	InviteeUnlockCredits int64 `json:"inviteeUnlockCredits"`
	InviterBonus         int64 `json:"inviterBonus"`
	InviterUnlockPaidRMB int64 `json:"inviterUnlockPaidRmb"`
	InviterMonthlyCap    int   `json:"inviterMonthlyCap"`
	IsActive             bool  `json:"isActive"`
}

// ---- 用户余额数据结构 ----

type userBalanceData struct {
	Balance    int64   `json:"balance"`
	BalanceRMB float64 `json:"balanceRmb"`
	FreeQuota  int64   `json:"freeQuota"`
	TotalConsumed int64 `json:"totalConsumed"`
}

// ============================================================
// TestAdminGetQuotaConfig — GET /admin/quota-config
// ============================================================
func TestAdminGetQuotaConfig(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doGet(baseURL+"/api/v1/admin/quota-config", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
	if resp.Code != 0 {
		t.Fatalf("expected code 0, got %d: %s", resp.Code, resp.Message)
	}

	var cfg quotaConfigData
	if err := json.Unmarshal(resp.Data, &cfg); err != nil {
		t.Fatalf("parse quota config: %v", err)
	}

	// 配置表应已存在（通过种子数据初始化）
	t.Logf("TestAdminGetQuotaConfig: defaultFreeQuota=%d, inviteeBonus=%d", cfg.DefaultFreeQuota, cfg.InviteeBonus)
}

// ============================================================
// TestAdminUpdateQuotaConfig — 更新 inviteeBonus 后持久化
// ============================================================
func TestAdminUpdateQuotaConfig(t *testing.T) {
	requireAdmin(t)

	// 获取当前值用于恢复
	origResp, _, _ := doGet(baseURL+"/api/v1/admin/quota-config", adminToken)
	var origCfg quotaConfigData
	if origResp != nil {
		json.Unmarshal(origResp.Data, &origCfg)
	}
	t.Cleanup(func() {
		if origCfg.InviteeBonus >= 0 {
			doPut(baseURL+"/api/v1/admin/quota-config", map[string]interface{}{
				"inviteeBonus": origCfg.InviteeBonus,
			}, adminToken)
		}
	})

	const newInviteeBonus int64 = 8000
	updateResp, status, err := doPut(baseURL+"/api/v1/admin/quota-config", map[string]interface{}{
		"inviteeBonus": newInviteeBonus,
	}, adminToken)
	if err != nil {
		t.Fatalf("update request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, updateResp.Message)
	}
	if updateResp.Code != 0 {
		t.Fatalf("expected code 0, got %d: %s", updateResp.Code, updateResp.Message)
	}

	var updated quotaConfigData
	if err := json.Unmarshal(updateResp.Data, &updated); err != nil {
		t.Fatalf("parse updated config: %v", err)
	}
	if updated.InviteeBonus != newInviteeBonus {
		t.Errorf("expected inviteeBonus=%d, got %d", newInviteeBonus, updated.InviteeBonus)
	}

	// 二次 GET 验证持久化
	getResp, getStatus, _ := doGet(baseURL+"/api/v1/admin/quota-config", adminToken)
	if getStatus == http.StatusOK && getResp != nil {
		var persisted quotaConfigData
		json.Unmarshal(getResp.Data, &persisted)
		if persisted.InviteeBonus != newInviteeBonus {
			t.Errorf("persistence check: expected %d, got %d", newInviteeBonus, persisted.InviteeBonus)
		}
	}
	t.Logf("TestAdminUpdateQuotaConfig: inviteeBonus=%d persisted", newInviteeBonus)
}

// ============================================================
// TestAdminRechargeUserCredits — 管理员充值，余额增加
// ============================================================
func TestAdminRechargeUserCredits(t *testing.T) {
	requireAdmin(t)

	email := uniqueEmail("recharge_test")
	pass := "Test@123456"
	_, uid := registerAndLogin(t, email, pass)
	if uid == 0 {
		t.Skip("could not obtain user ID from profile")
	}

	// 获取充值前余额
	balBefore, balStatus, _ := doGet(fmt.Sprintf("%s/api/v1/admin/users/%d/balance", baseURL, uid), adminToken)
	skipIfNotFound(t, balStatus)
	skipIfNotImplemented(t, balStatus)

	var before userBalanceData
	if balBefore != nil && balBefore.Data != nil {
		json.Unmarshal(balBefore.Data, &before)
	}

	const rechargeAmt int64 = 100_000

	rechargeResp, status, err := doPost(
		fmt.Sprintf("%s/api/v1/admin/users/%d/recharge-credits", baseURL, uid),
		map[string]interface{}{
			"credits": rechargeAmt,
			"remark":  "test top-up",
		},
		adminToken,
	)
	if err != nil {
		t.Fatalf("recharge request failed: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, rechargeResp.Message)
	}
	if rechargeResp.Code != 0 {
		t.Fatalf("expected code 0, got %d: %s", rechargeResp.Code, rechargeResp.Message)
	}

	var after userBalanceData
	if err := json.Unmarshal(rechargeResp.Data, &after); err != nil {
		t.Fatalf("parse recharge response: %v", err)
	}

	expectedBalance := before.Balance + rechargeAmt
	if after.Balance != expectedBalance {
		t.Errorf("expected balance=%d after recharge, got %d", expectedBalance, after.Balance)
	}
	t.Logf("TestAdminRechargeUserCredits: before=%d, after=%d, +%d", before.Balance, after.Balance, rechargeAmt)
}

// ============================================================
// TestAdminSetUserCredits — 设置为精确值
// ============================================================
func TestAdminSetUserCredits(t *testing.T) {
	requireAdmin(t)

	email := uniqueEmail("setcredits_test")
	pass := "Test@123456"
	_, uid := registerAndLogin(t, email, pass)
	if uid == 0 {
		t.Skip("could not obtain user ID from profile")
	}

	// 先充一些余额
	doPost(fmt.Sprintf("%s/api/v1/admin/users/%d/recharge-credits", baseURL, uid),
		map[string]interface{}{"credits": 50_000, "remark": "setup"},
		adminToken,
	)

	const targetCredits int64 = 200_000

	setResp, status, err := doPut(
		fmt.Sprintf("%s/api/v1/admin/users/%d/set-credits", baseURL, uid),
		map[string]interface{}{
			"credits": targetCredits,
			"remark":  "set to exact value",
		},
		adminToken,
	)
	if err != nil {
		t.Fatalf("set-credits request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, setResp.Message)
	}
	if setResp.Code != 0 {
		t.Fatalf("expected code 0, got %d: %s", setResp.Code, setResp.Message)
	}

	var result userBalanceData
	if err := json.Unmarshal(setResp.Data, &result); err != nil {
		t.Fatalf("parse set-credits response: %v", err)
	}
	if result.Balance != targetCredits {
		t.Errorf("expected exact balance=%d, got %d", targetCredits, result.Balance)
	}
	t.Logf("TestAdminSetUserCredits: balance set to %d credits", result.Balance)
}
