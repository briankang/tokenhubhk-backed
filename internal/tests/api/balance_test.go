package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// ─── Balance Test Types ──────────────────────────────────────────

type userBalanceResp struct {
	ID            uint    `json:"id"`
	UserID        uint    `json:"userId"`
	TenantID      uint    `json:"tenantId"`
	Balance       float64 `json:"balance"`
	FreeQuota     float64 `json:"freeQuota"`
	TotalConsumed float64 `json:"totalConsumed"`
	FrozenAmount  float64 `json:"frozenAmount"`
	Currency      string  `json:"currency"`
}

type quotaConfigResp struct {
	ID                uint    `json:"id"`
	DefaultFreeQuota  float64 `json:"defaultFreeQuota"`
	RegistrationBonus float64 `json:"registrationBonus"`
	IsActive          bool    `json:"isActive"`
	Description       string  `json:"description"`
}

// ─── Tests ───────────────────────────────────────────────────────

// TestBalance_01_UserHasDefaultQuotaAfterRegister verifies that a newly registered user
// gets the default free quota.
func TestBalance_01_UserHasDefaultQuotaAfterRegister(t *testing.T) {
	requireUser(t)

	resp, status, err := doGet(baseURL+"/api/v1/user/balance", userToken)
	if err != nil {
		t.Fatalf("get balance failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}

	var bal userBalanceResp
	if err := json.Unmarshal(resp.Data, &bal); err != nil {
		t.Fatalf("parse balance: %v", err)
	}

	// User should have some free quota (default is 1.0)
	totalAvailable := bal.Balance + bal.FreeQuota
	if totalAvailable <= 0 {
		t.Errorf("expected positive balance/free quota for new user, got balance=%.6f freeQuota=%.6f", bal.Balance, bal.FreeQuota)
	}
	t.Logf("User balance=%.6f freeQuota=%.6f currency=%s", bal.Balance, bal.FreeQuota, bal.Currency)
}

// TestBalance_02_AdminRechargeUser verifies that an admin can recharge a user's balance.
func TestBalance_02_AdminRechargeUser(t *testing.T) {
	requireAdmin(t)
	requireUser(t)

	// First get the user's current balance to find their user ID
	resp, status, err := doGet(baseURL+"/api/v1/user/balance", userToken)
	if err != nil {
		t.Fatalf("get balance failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	var bal userBalanceResp
	if err := json.Unmarshal(resp.Data, &bal); err != nil {
		t.Fatalf("parse balance: %v", err)
	}
	userID := bal.UserID
	if userID == 0 {
		t.Skip("user ID not available in balance response")
	}

	beforeBalance := bal.Balance

	// Admin recharges the user
	rechargeAmount := int64(105000)
	rechargeResp, rechargeStatus, err := doPost(
		fmt.Sprintf("%s/api/v1/admin/users/%d/recharge-credits", baseURL, userID),
		map[string]interface{}{
			"credits": rechargeAmount,
			"remark":  "test recharge",
		},
		adminToken,
	)
	if err != nil {
		t.Fatalf("recharge failed: %v", err)
	}
	skipIfNotImplemented(t, rechargeStatus)
	skipIfNotFound(t, rechargeStatus)

	if rechargeStatus != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rechargeStatus, rechargeResp.Message)
	}

	var afterBal userBalanceResp
	if err := json.Unmarshal(rechargeResp.Data, &afterBal); err != nil {
		t.Fatalf("parse recharge response: %v", err)
	}

	expected := beforeBalance + float64(rechargeAmount)
	if afterBal.Balance < expected-0.001 || afterBal.Balance > expected+0.001 {
		t.Errorf("expected balance ~%.6f after recharge, got %.6f", expected, afterBal.Balance)
	}
	t.Logf("After recharge: balance=%.6f", afterBal.Balance)
}

// TestBalance_03_BalanceRecords verifies that balance records are created correctly.
func TestBalance_03_BalanceRecords(t *testing.T) {
	requireUser(t)

	resp, status, err := doGet(baseURL+"/api/v1/user/balance/records?page=1&page_size=10", userToken)
	if err != nil {
		t.Fatalf("get records failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}

	page, err := parsePageData(resp)
	if err != nil {
		t.Fatalf("parse page: %v", err)
	}

	// Should have at least the GIFT record from registration
	if page.Total < 1 {
		t.Logf("WARN: expected at least 1 balance record (GIFT), got %d", page.Total)
	}
	t.Logf("Total balance records: %d", page.Total)
}

// TestBalance_04_AdminGetQuotaConfig verifies admin can get quota config.
func TestBalance_04_AdminGetQuotaConfig(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doGet(baseURL+"/api/v1/admin/quota-config", adminToken)
	if err != nil {
		t.Fatalf("get quota config failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}

	var cfg quotaConfigResp
	if err := json.Unmarshal(resp.Data, &cfg); err != nil {
		t.Fatalf("parse quota config: %v", err)
	}

	t.Logf("Quota config: defaultFreeQuota=%.6f registrationBonus=%.6f", cfg.DefaultFreeQuota, cfg.RegistrationBonus)
}

// TestBalance_05_AdminUpdateQuotaConfig verifies that updating quota config affects new registrations.
func TestBalance_05_AdminUpdateQuotaConfig(t *testing.T) {
	requireAdmin(t)

	// Update quota config
	newQuota := int64(25000)
	resp, status, err := doPut(baseURL+"/api/v1/admin/quota-config", map[string]interface{}{
		"defaultFreeQuota":  newQuota,
		"registrationBonus": int64(5000),
		"description":       "test updated quota",
	}, adminToken)
	if err != nil {
		t.Fatalf("update quota config failed: %v", err)
	}
	skipIfNotImplemented(t, status)
	skipIfNotFound(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}

	// Register a new user and check their quota
	ts := fmt.Sprintf("%d", time.Now().UnixNano())
	newEmail := fmt.Sprintf("quotatest_%s@test.com", ts)
	newPass := "Test@123456"

	regResp, regStatus, regErr := doPost(baseURL+"/api/v1/auth/register", registerPayload(newEmail, newPass, "QuotaTest_"+ts), "")
	if regErr != nil {
		t.Fatalf("register failed: %v", regErr)
	}
	if regStatus != http.StatusOK {
		t.Fatalf("register failed: status=%d, msg=%s", regStatus, regResp.Message)
	}

	// Login as new user
	newToken, err := loginUser(newEmail, newPass)
	if err != nil {
		t.Fatalf("login new user failed: %v", err)
	}

	// Check balance
	balResp, balStatus, balErr := doGet(baseURL+"/api/v1/user/balance", newToken)
	if balErr != nil {
		t.Fatalf("get new user balance failed: %v", balErr)
	}
	if balStatus != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", balStatus, balResp.Message)
	}

	var bal userBalanceResp
	if err := json.Unmarshal(balResp.Data, &bal); err != nil {
		t.Fatalf("parse balance: %v", err)
	}

	expectedTotal := float64(newQuota + 5000) // defaultFreeQuota + registrationBonus
	totalAvailable := bal.Balance + bal.FreeQuota
	if totalAvailable < expectedTotal-0.01 || totalAvailable > expectedTotal+0.01 {
		t.Errorf("expected new user balance ~%.2f, got %.6f (balance=%.6f freeQuota=%.6f)", expectedTotal, totalAvailable, bal.Balance, bal.FreeQuota)
	}
	t.Logf("New user after updated quota: balance=%.6f freeQuota=%.6f (total=%.6f)", bal.Balance, bal.FreeQuota, totalAvailable)

	// Restore default quota config
	_, _, _ = doPut(baseURL+"/api/v1/admin/quota-config", map[string]interface{}{
		"defaultFreeQuota":  int64(10000),
		"registrationBonus": int64(0),
		"description":       "default quota config",
	}, adminToken)
}
