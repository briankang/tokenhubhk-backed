package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// ---- 推荐配置数据结构 ----

type referralConfigData struct {
	ID                   uint    `json:"id"`
	CommissionRate       float64 `json:"commissionRate"`
	AttributionDays      int     `json:"attributionDays"`
	LifetimeCapCredits   int64   `json:"lifetimeCapCredits"`
	MinPaidCreditsUnlock int64   `json:"minPaidCreditsUnlock"`
	MinWithdrawAmount    int64   `json:"minWithdrawAmount"`
	SettleDays           int     `json:"settleDays"`
	IsActive             bool    `json:"isActive"`
}

// ============================================================
// TestAdminGetReferralConfig — 获取配置，断言 commission_rate 存在
// ============================================================
func TestAdminGetReferralConfig(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doGet(baseURL+"/api/v1/admin/referral-config", adminToken)
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

	var cfg referralConfigData
	if err := json.Unmarshal(resp.Data, &cfg); err != nil {
		t.Fatalf("parse referral config: %v", err)
	}

	// 默认 CommissionRate 应为 0.10（10%）
	if cfg.CommissionRate <= 0 {
		t.Errorf("expected commissionRate > 0 (default 0.10), got %.4f", cfg.CommissionRate)
	}
	t.Logf("TestAdminGetReferralConfig: rate=%.4f, attributionDays=%d, isActive=%v",
		cfg.CommissionRate, cfg.AttributionDays, cfg.IsActive)
}

// ============================================================
// TestAdminUpdateReferralConfig_Valid — 更新为合法值
// ============================================================
func TestAdminUpdateReferralConfig_Valid(t *testing.T) {
	requireAdmin(t)

	// 先获取当前值作为恢复基准
	origResp, _, _ := doGet(baseURL+"/api/v1/admin/referral-config", adminToken)
	var origCfg referralConfigData
	if origResp != nil {
		json.Unmarshal(origResp.Data, &origCfg)
	}

	t.Cleanup(func() {
		// 测试结束后恢复原值
		if origCfg.CommissionRate > 0 {
			doPut(baseURL+"/api/v1/admin/referral-config", map[string]interface{}{
				"commissionRate":  origCfg.CommissionRate,
				"attributionDays": origCfg.AttributionDays,
			}, adminToken)
		}
	})

	resp, status, err := doPut(baseURL+"/api/v1/admin/referral-config", map[string]interface{}{
		"commissionRate":  0.15,
		"attributionDays": 60,
	}, adminToken)
	if err != nil {
		t.Fatalf("update request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
	if resp.Code != 0 {
		t.Fatalf("expected code 0, got %d: %s", resp.Code, resp.Message)
	}

	var updated referralConfigData
	if err := json.Unmarshal(resp.Data, &updated); err != nil {
		t.Fatalf("parse updated config: %v", err)
	}
	if updated.CommissionRate != 0.15 {
		t.Errorf("expected commissionRate=0.15, got %.4f", updated.CommissionRate)
	}
	if updated.AttributionDays != 60 {
		t.Errorf("expected attributionDays=60, got %d", updated.AttributionDays)
	}
	t.Logf("TestAdminUpdateReferralConfig_Valid: rate=%.4f, days=%d", updated.CommissionRate, updated.AttributionDays)
}

// ============================================================
// TestAdminUpdateReferralConfig_RateTooHigh — commission_rate > 0.80 应拒绝
// ============================================================
func TestAdminUpdateReferralConfig_RateTooHigh(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doPut(baseURL+"/api/v1/admin/referral-config", map[string]interface{}{
		"commissionRate": 0.95, // 超出 0.80 上限
	}, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)

	if status == http.StatusOK && resp.Code == 0 {
		t.Fatal("expected validation error for commissionRate=0.95, got success")
	}
	if status != http.StatusBadRequest {
		t.Logf("注意: 期望 400, 实际返回 %d", status)
	}
	t.Logf("TestAdminUpdateReferralConfig_RateTooHigh: status=%d, msg=%s", status, resp.Message)
}

// ============================================================
// TestAdminUpdateReferralConfig_NegativeAttributionDays — 负值应被拒绝
// ============================================================
func TestAdminUpdateReferralConfig_NegativeAttributionDays(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doPut(baseURL+"/api/v1/admin/referral-config", map[string]interface{}{
		"attributionDays": -1,
	}, adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)

	if status == http.StatusOK && resp.Code == 0 {
		t.Fatal("expected validation error for negative attributionDays, got success")
	}
	t.Logf("TestAdminUpdateReferralConfig_NegativeAttributionDays: status=%d, msg=%s", status, resp.Message)
}

// ============================================================
// TestAdminUpdateReferralConfig_Persistence — 更新后 re-GET 能看到持久化的值
// ============================================================
func TestAdminUpdateReferralConfig_Persistence(t *testing.T) {
	requireAdmin(t)

	// 获取当前值
	origResp, _, _ := doGet(baseURL+"/api/v1/admin/referral-config", adminToken)
	var origCfg referralConfigData
	if origResp != nil {
		json.Unmarshal(origResp.Data, &origCfg)
	}
	t.Cleanup(func() {
		if origCfg.CommissionRate > 0 {
			doPut(baseURL+"/api/v1/admin/referral-config", map[string]interface{}{
				"commissionRate":  origCfg.CommissionRate,
				"attributionDays": origCfg.AttributionDays,
				"settleDays":      origCfg.SettleDays,
			}, adminToken)
		}
	})

	const newRate = 0.12
	const newDays = 45
	const newSettleDays = 14

	updateResp, updateStatus, err := doPut(baseURL+"/api/v1/admin/referral-config", map[string]interface{}{
		"commissionRate":  newRate,
		"attributionDays": newDays,
		"settleDays":      newSettleDays,
	}, adminToken)
	if err != nil {
		t.Fatalf("update request failed: %v", err)
	}
	skipIfNotFound(t, updateStatus)
	skipIfNotImplemented(t, updateStatus)
	if updateStatus != http.StatusOK {
		t.Fatalf("update expected 200, got %d: %s", updateStatus, updateResp.Message)
	}

	// re-GET
	getResp, getStatus, err := doGet(baseURL+"/api/v1/admin/referral-config", adminToken)
	if err != nil {
		t.Fatalf("re-get request failed: %v", err)
	}
	if getStatus != http.StatusOK {
		t.Fatalf("re-get expected 200, got %d", getStatus)
	}

	var persisted referralConfigData
	if err := json.Unmarshal(getResp.Data, &persisted); err != nil {
		t.Fatalf("parse persisted config: %v", err)
	}

	if persisted.CommissionRate != newRate {
		t.Errorf("persistence check: expected rate=%.4f, got %.4f", newRate, persisted.CommissionRate)
	}
	if persisted.AttributionDays != newDays {
		t.Errorf("persistence check: expected attributionDays=%d, got %d", newDays, persisted.AttributionDays)
	}
	if persisted.SettleDays != newSettleDays {
		t.Errorf("persistence check: expected settleDays=%d, got %d", newSettleDays, persisted.SettleDays)
	}
	t.Logf("TestAdminUpdateReferralConfig_Persistence: all values persisted correctly")
}
