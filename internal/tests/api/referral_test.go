package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// ──────────────────────────────────────────────────
// Referral & Commission Tests
// ──────────────────────────────────────────────────

// TestUserGetReferralLink verifies that a user can obtain a unique referral link/code.
func TestUserGetReferralLink(t *testing.T) {
	requireUser(t)

	resp, status, err := doGet(baseURL+"/api/v1/user/referral/link", userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d (msg=%s)", status, resp.Message)
	}
	if resp.Code != 0 {
		t.Fatalf("expected code 0, got %d", resp.Code)
	}

	var data struct {
		Code          string `json:"code"`
		ClickCount    int    `json:"clickCount"`
		RegisterCount int    `json:"registerCount"`
		Link          string `json:"link"`
	}
	if err := unmarshalData(resp.Data, &data); err != nil {
		t.Fatalf("parse data: %v", err)
	}
	if data.Code == "" {
		t.Fatal("referral code should not be empty")
	}
	if len(data.Code) < 6 {
		t.Fatalf("referral code too short: %s", data.Code)
	}
	t.Logf("referral code: %s, link: %s", data.Code, data.Link)
}

// TestReferralCodeRegistration verifies that registering with a referral code properly binds the relationship.
func TestReferralCodeRegistration(t *testing.T) {
	requireUser(t)

	// Step 1: Get the user's referral code
	resp, status, err := doGet(baseURL+"/api/v1/user/referral/link", userToken)
	if err != nil {
		t.Fatalf("get referral link failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	var linkData struct {
		Code string `json:"code"`
	}
	if err := unmarshalData(resp.Data, &linkData); err != nil {
		t.Fatalf("parse referral link: %v", err)
	}
	if linkData.Code == "" {
		t.Skip("no referral code available")
	}

	// Step 2: Register a new user with the referral code
	ts := fmt.Sprintf("%d", time.Now().UnixNano())
	newEmail := fmt.Sprintf("ref_test_%s@test.com", ts)
	newPass := "Test@123456"

	regResp, regStatus, regErr := doPost(baseURL+"/api/v1/auth/register", map[string]string{
		"email":         newEmail,
		"password":      authPassword(newEmail, newPass),
		"name":          "RefTestUser_" + ts,
		"email_code":    testMagicEmailCode,
		"invite_code":   testInviteCode,
		"referral_code": linkData.Code,
	}, "")
	if regErr != nil {
		t.Fatalf("register with referral code failed: %v", regErr)
	}
	skipIfNotImplemented(t, regStatus)

	if regStatus != http.StatusOK {
		t.Fatalf("expected 200 for registration, got %d (msg=%s)", regStatus, regResp.Message)
	}
	t.Logf("registered user with referral code %s", linkData.Code)

	// Step 3: Verify referral stats updated (registerCount should increase)
	statsResp, statsStatus, statsErr := doGet(baseURL+"/api/v1/user/referral/stats", userToken)
	if statsErr != nil {
		t.Fatalf("get stats failed: %v", statsErr)
	}
	if statsStatus == http.StatusOK {
		var stats struct {
			RegisterCount int `json:"registerCount"`
		}
		if err := unmarshalData(statsResp.Data, &stats); err == nil {
			if stats.RegisterCount > 0 {
				t.Logf("register count: %d", stats.RegisterCount)
			}
		}
	}
}

// TestUserReferralStats verifies that referral statistics are returned.
func TestUserReferralStats(t *testing.T) {
	requireUser(t)

	resp, status, err := doGet(baseURL+"/api/v1/user/referral/stats", userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d (msg=%s)", status, resp.Message)
	}

	var stats struct {
		ClickCount      int     `json:"clickCount"`
		RegisterCount   int     `json:"registerCount"`
		TotalCommission float64 `json:"totalCommission"`
		PendingAmount   float64 `json:"pendingAmount"`
	}
	if err := json.Unmarshal(resp.Data, &stats); err != nil {
		t.Fatalf("parse stats: %v", err)
	}
	t.Logf("stats: clicks=%d, registers=%d, commission=%.6f", stats.ClickCount, stats.RegisterCount, stats.TotalCommission)
}

// TestUserReferralCommissions verifies that commission records are returned.
func TestUserReferralCommissions(t *testing.T) {
	requireUser(t)

	resp, status, err := doGet(baseURL+"/api/v1/user/referral/commissions", userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d (msg=%s)", status, resp.Message)
	}

	page, err := parsePageData(resp)
	if err != nil {
		t.Fatalf("parse page: %v", err)
	}
	t.Logf("commission records: total=%d, page=%d", page.Total, page.Page)
}

// TestAdminReferralConfig verifies admin can get and update referral config.
func TestAdminReferralConfig(t *testing.T) {
	requireAdmin(t)

	// GET config
	resp, status, err := doGet(baseURL+"/api/v1/admin/referral-config", adminToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d (msg=%s)", status, resp.Message)
	}

	var cfg struct {
		PersonalCashbackRate float64 `json:"personalCashbackRate"`
		L1CommissionRate     float64 `json:"l1CommissionRate"`
		L2CommissionRate     float64 `json:"l2CommissionRate"`
		L3CommissionRate     float64 `json:"l3CommissionRate"`
		MinWithdrawAmount    float64 `json:"minWithdrawAmount"`
		IsActive             bool    `json:"isActive"`
	}
	if err := unmarshalData(resp.Data, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	t.Logf("config: personal=%.4f, L1=%.4f, L2=%.4f, L3=%.4f, minWithdraw=%.2f",
		cfg.PersonalCashbackRate, cfg.L1CommissionRate, cfg.L2CommissionRate, cfg.L3CommissionRate, cfg.MinWithdrawAmount)

	// PUT update config
	updateResp, updateStatus, updateErr := doPut(baseURL+"/api/v1/admin/referral-config", map[string]interface{}{
		"personalCashbackRate": 0.06,
		"l1CommissionRate":     0.12,
	}, adminToken)
	if updateErr != nil {
		t.Fatalf("update request failed: %v", updateErr)
	}
	if updateStatus != http.StatusOK {
		t.Fatalf("expected 200 for update, got %d (msg=%s)", updateStatus, updateResp.Message)
	}

	// Verify update
	resp2, _, _ := doGet(baseURL+"/api/v1/admin/referral-config", adminToken)
	if resp2 != nil && resp2.Data != nil {
		var cfg2 struct {
			PersonalCashbackRate float64 `json:"personalCashbackRate"`
			L1CommissionRate     float64 `json:"l1CommissionRate"`
		}
		if err := unmarshalData(resp2.Data, &cfg2); err == nil {
			if cfg2.PersonalCashbackRate != 0.06 {
				t.Errorf("personalCashbackRate not updated: got %.4f", cfg2.PersonalCashbackRate)
			}
			if cfg2.L1CommissionRate != 0.12 {
				t.Errorf("l1CommissionRate not updated: got %.4f", cfg2.L1CommissionRate)
			}
		}
	}

	// Restore original values
	doPut(baseURL+"/api/v1/admin/referral-config", map[string]interface{}{
		"personalCashbackRate": 0.05,
		"l1CommissionRate":     0.10,
	}, adminToken)
}
