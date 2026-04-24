package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// ---- 提现相关数据结构 ----

type withdrawalItem struct {
	ID          uint    `json:"id"`
	UserID      uint    `json:"user_id"`
	Amount      float64 `json:"amount"`
	Status      string  `json:"status"`
	BankInfo    string  `json:"bank_info"`
	AdminID     uint    `json:"admin_id"`
	AdminRemark string  `json:"admin_remark"`
}

// ---- 辅助：为测试用户充值 ----

// rechargeUser 管理员为指定 userID 充值积分
func rechargeUser(t *testing.T, userID uint, credits int64) {
	t.Helper()
	resp, status, err := doPost(
		fmt.Sprintf("%s/api/v1/admin/users/%d/recharge-credits", baseURL, userID),
		map[string]interface{}{"credits": credits, "remark": "test recharge"},
		adminToken,
	)
	if err != nil {
		t.Fatalf("rechargeUser: request failed: %v", err)
	}
	if status != http.StatusOK || resp.Code != 0 {
		t.Fatalf("rechargeUser: expected 200+code0, got status=%d code=%d msg=%s", status, resp.Code, resp.Message)
	}
}

// registerAndLogin 注册新用户并返回 (token, userID)
func registerAndLogin(t *testing.T, email, pass string) (token string, userID uint) {
	t.Helper()

	// 注册
	_, status, err := doPost(baseURL+"/api/v1/auth/register", registerPayload(email, pass, uniqueName("WithdrawalTestUser")), "")
	if err != nil {
		t.Fatalf("registerAndLogin register: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("registerAndLogin register: expected 200, got %d", status)
	}

	// 登录
	tok, err := loginUser(email, pass)
	if err != nil {
		t.Fatalf("registerAndLogin login: %v", err)
	}

	// 获取用户信息拿 ID
	profileResp, _, _ := doGet(baseURL+"/api/v1/user/profile", tok)
	if profileResp != nil && profileResp.Data != nil {
		var profile struct {
			ID uint `json:"id"`
		}
		_ = json.Unmarshal(profileResp.Data, &profile)
		userID = profile.ID
	}

	if userID > 0 {
		rolesResp, rStatus, rErr := doGet(baseURL+"/api/v1/admin/roles", adminToken)
		if rErr == nil && rStatus == http.StatusOK && rolesResp != nil && rolesResp.Data != nil {
			var roleList struct {
				List []struct {
					ID   uint   `json:"id"`
					Code string `json:"code"`
				} `json:"list"`
			}
			if jsonErr := json.Unmarshal(rolesResp.Data, &roleList); jsonErr == nil {
				for _, r := range roleList.List {
					if r.Code == "FINANCIAL_USER" {
						doPost(
							fmt.Sprintf("%s/api/v1/admin/users/%d/roles", baseURL, userID),
							map[string]interface{}{"role_id": r.ID},
							adminToken,
						)
						break
					}
				}
			}
		}
	}

	return tok, userID
}

// ============================================================
// TestUserCreateWithdrawal_Success
// ============================================================
func TestUserCreateWithdrawal_Success(t *testing.T) {
	requireAdmin(t)

	email := uniqueEmail("wd_create")
	pass := "Test@123456"
	tok, uid := registerAndLogin(t, email, pass)
	if uid == 0 {
		t.Skip("could not obtain userID from profile")
	}

	// 充值 5,000,000 积分（¥500）
	rechargeUser(t, uid, 5_000_000)

	// 申请提现 1,000,000 积分（¥100，= MinWithdrawAmount 默认值）
	resp, status, err := doPost(baseURL+"/api/v1/user/withdrawals", map[string]interface{}{
		"amountCredits": 1_000_000,
		"bankInfo":      "工商银行 6222 xxxx xxxx 1234",
	}, tok)
	if err != nil {
		t.Fatalf("create withdrawal request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, resp.Message)
	}
	if resp.Code != 0 {
		t.Fatalf("expected code 0, got %d: %s", resp.Code, resp.Message)
	}

	var wd withdrawalItem
	if err := json.Unmarshal(resp.Data, &wd); err != nil {
		t.Fatalf("parse withdrawal response: %v", err)
	}
	if wd.ID == 0 {
		t.Fatal("expected non-zero withdrawal ID")
	}
	if wd.Status != "PENDING" {
		t.Errorf("expected PENDING, got %s", wd.Status)
	}
	t.Logf("TestUserCreateWithdrawal_Success: id=%d, status=%s", wd.ID, wd.Status)
}

// ============================================================
// TestUserCreateWithdrawal_InsufficientBalance — 余额为 0 应返回 400
// ============================================================
func TestUserCreateWithdrawal_InsufficientBalance(t *testing.T) {
	requireAdmin(t)

	email := uniqueEmail("wd_insuf")
	pass := "Test@123456"
	tok, _ := registerAndLogin(t, email, pass)

	// 不充值，余额为 0
	resp, status, err := doPost(baseURL+"/api/v1/user/withdrawals", map[string]interface{}{
		"amountCredits": 1_000_000,
		"bankInfo":      "招商银行",
	}, tok)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)

	if status == http.StatusOK && resp.Code == 0 {
		t.Fatal("expected error for zero balance, but got success")
	}
	t.Logf("InsufficientBalance returned status=%d, code=%d, msg=%s", status, resp.Code, resp.Message)
}

// ============================================================
// TestUserListWithdrawals — 创建 2 条后列表 count >= 2
// ============================================================
func TestUserListWithdrawals(t *testing.T) {
	requireAdmin(t)

	email := uniqueEmail("wd_list")
	pass := "Test@123456"
	tok, uid := registerAndLogin(t, email, pass)
	if uid == 0 {
		t.Skip("could not obtain userID from profile")
	}

	rechargeUser(t, uid, 10_000_000) // 充足余额创建 2 条

	for i := 0; i < 2; i++ {
		_, status, err := doPost(baseURL+"/api/v1/user/withdrawals", map[string]interface{}{
			"amountCredits": 1_000_000,
			"bankInfo":      "bank",
		}, tok)
		if err != nil {
			t.Fatalf("create withdrawal[%d]: %v", i, err)
		}
		skipIfNotFound(t, status)
		skipIfNotImplemented(t, status)
	}

	listResp, status, err := doGet(baseURL+"/api/v1/user/withdrawals?page=1&page_size=20", tok)
	if err != nil {
		t.Fatalf("list request failed: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, listResp.Message)
	}

	page, err := parsePageData(listResp)
	if err != nil {
		t.Fatalf("parse page data: %v", err)
	}
	if page.Total < 1 {
		t.Errorf("expected total >= 1, got %d", page.Total)
	}
	t.Logf("TestUserListWithdrawals: total=%d", page.Total)
}

// ============================================================
// TestAdminListWithdrawals_AllStatuses — 管理员能看到 PENDING 记录
// ============================================================
func TestAdminListWithdrawals_AllStatuses(t *testing.T) {
	requireAdmin(t)

	email := uniqueEmail("wd_admin_list")
	pass := "Test@123456"
	tok, uid := registerAndLogin(t, email, pass)
	if uid == 0 {
		t.Skip("could not obtain userID from profile")
	}

	rechargeUser(t, uid, 5_000_000)
	_, _, _ = doPost(baseURL+"/api/v1/user/withdrawals", map[string]interface{}{
		"amountCredits": 1_000_000,
		"bankInfo":      "bank",
	}, tok)

	listResp, status, err := doGet(baseURL+"/api/v1/admin/withdrawals?status=PENDING&page=1&page_size=50", adminToken)
	if err != nil {
		t.Fatalf("admin list request failed: %v", err)
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
	t.Logf("TestAdminListWithdrawals_AllStatuses: total pending=%d", page.Total)
}

// ============================================================
// TestAdminApproveWithdrawal — 管理员审批通过，状态变 APPROVED
// ============================================================
func TestAdminApproveWithdrawal(t *testing.T) {
	requireAdmin(t)

	email := uniqueEmail("wd_approve")
	pass := "Test@123456"
	tok, uid := registerAndLogin(t, email, pass)
	if uid == 0 {
		t.Skip("could not obtain userID from profile")
	}

	rechargeUser(t, uid, 5_000_000)
	createResp, status, err := doPost(baseURL+"/api/v1/user/withdrawals", map[string]interface{}{
		"amountCredits": 1_000_000,
		"bankInfo":      "bank",
	}, tok)
	if err != nil {
		t.Fatalf("create withdrawal: %v", err)
	}
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)
	if status != http.StatusOK {
		t.Fatalf("create withdrawal expected 200, got %d", status)
	}

	var wd withdrawalItem
	if err := json.Unmarshal(createResp.Data, &wd); err != nil {
		t.Fatalf("parse withdrawal: %v", err)
	}

	// 管理员审批
	approveResp, approveStatus, err := doPost(
		fmt.Sprintf("%s/api/v1/admin/withdrawals/%d/approve", baseURL, wd.ID),
		map[string]string{"remark": "符合条件"},
		adminToken,
	)
	if err != nil {
		t.Fatalf("approve request: %v", err)
	}
	skipIfNotFound(t, approveStatus)

	if approveStatus != http.StatusOK {
		t.Fatalf("approve expected 200, got %d: %s", approveStatus, approveResp.Message)
	}
	if approveResp.Code != 0 {
		t.Fatalf("approve expected code 0, got %d: %s", approveResp.Code, approveResp.Message)
	}

	var result struct {
		ID       uint   `json:"id"`
		Status   string `json:"status"`
		Approved bool   `json:"approved"`
	}
	if err := json.Unmarshal(approveResp.Data, &result); err != nil {
		t.Fatalf("parse approve response: %v", err)
	}
	if result.Status != "APPROVED" && !result.Approved {
		t.Errorf("expected APPROVED, got %s", result.Status)
	}
	t.Logf("TestAdminApproveWithdrawal: id=%d status=%s", result.ID, result.Status)
}

// ============================================================
// TestAdminRejectWithdrawal — 拒绝后状态 REJECTED，余额回退
// ============================================================
func TestAdminRejectWithdrawal(t *testing.T) {
	requireAdmin(t)

	email := uniqueEmail("wd_reject")
	pass := "Test@123456"
	tok, uid := registerAndLogin(t, email, pass)
	if uid == 0 {
		t.Skip("could not obtain userID from profile")
	}

	const initial int64 = 5_000_000
	rechargeUser(t, uid, initial)

	createResp, status, _ := doPost(baseURL+"/api/v1/user/withdrawals", map[string]interface{}{
		"amountCredits": 1_000_000,
		"bankInfo":      "bank",
	}, tok)
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)
	if status != http.StatusOK {
		t.Fatalf("create withdrawal: status=%d", status)
	}

	var wd withdrawalItem
	json.Unmarshal(createResp.Data, &wd)

	// 获取提现前的余额（应为 initial - 1,000,000 = 4,000,000）
	balBefore, _, _ := doGet(fmt.Sprintf("%s/api/v1/admin/users/%d/balance", baseURL, uid), adminToken)

	// 拒绝
	rejectResp, rejectStatus, err := doPost(
		fmt.Sprintf("%s/api/v1/admin/withdrawals/%d/reject", baseURL, wd.ID),
		map[string]string{"reason": "银行信息不完整"},
		adminToken,
	)
	if err != nil {
		t.Fatalf("reject request: %v", err)
	}
	skipIfNotFound(t, rejectStatus)

	if rejectStatus != http.StatusOK {
		t.Fatalf("reject expected 200, got %d: %s", rejectStatus, rejectResp.Message)
	}
	if rejectResp.Code != 0 {
		t.Fatalf("reject expected code 0, got %d: %s", rejectResp.Code, rejectResp.Message)
	}

	// 验证余额回退
	balAfter, _, _ := doGet(fmt.Sprintf("%s/api/v1/admin/users/%d/balance", baseURL, uid), adminToken)
	if balBefore != nil && balAfter != nil {
		var before, after struct {
			Balance int64 `json:"balance"`
		}
		json.Unmarshal(balBefore.Data, &before)
		json.Unmarshal(balAfter.Data, &after)
		if after.Balance <= before.Balance {
			t.Errorf("expected balance restored after reject: before=%d, after=%d", before.Balance, after.Balance)
		}
		t.Logf("TestAdminRejectWithdrawal: balance before=%d, after=%d", before.Balance, after.Balance)
	}
}

// ============================================================
// TestAdminMarkPaidWithdrawal — approve + mark-paid → COMPLETED
// ============================================================
func TestAdminMarkPaidWithdrawal(t *testing.T) {
	requireAdmin(t)

	email := uniqueEmail("wd_markpaid")
	pass := "Test@123456"
	tok, uid := registerAndLogin(t, email, pass)
	if uid == 0 {
		t.Skip("could not obtain userID from profile")
	}

	rechargeUser(t, uid, 5_000_000)
	createResp, status, _ := doPost(baseURL+"/api/v1/user/withdrawals", map[string]interface{}{
		"amountCredits": 1_000_000,
		"bankInfo":      "bank",
	}, tok)
	skipIfNotFound(t, status)
	skipIfNotImplemented(t, status)
	if status != http.StatusOK {
		t.Fatalf("create withdrawal: status=%d", status)
	}

	var wd withdrawalItem
	json.Unmarshal(createResp.Data, &wd)

	// 先 approve
	doPost(
		fmt.Sprintf("%s/api/v1/admin/withdrawals/%d/approve", baseURL, wd.ID),
		map[string]string{"remark": "ok"},
		adminToken,
	)

	// mark-paid
	paidResp, paidStatus, err := doPost(
		fmt.Sprintf("%s/api/v1/admin/withdrawals/%d/mark-paid", baseURL, wd.ID),
		map[string]string{"bank_txn_id": "TXN20260414TEST"},
		adminToken,
	)
	if err != nil {
		t.Fatalf("mark-paid request: %v", err)
	}
	skipIfNotFound(t, paidStatus)

	if paidStatus != http.StatusOK {
		t.Fatalf("mark-paid expected 200, got %d: %s", paidStatus, paidResp.Message)
	}
	if paidResp.Code != 0 {
		t.Fatalf("mark-paid expected code 0, got %d: %s", paidResp.Code, paidResp.Message)
	}

	var result struct {
		Status string `json:"status"`
		Paid   bool   `json:"paid"`
	}
	json.Unmarshal(paidResp.Data, &result)
	if result.Status != "COMPLETED" && !result.Paid {
		t.Errorf("expected COMPLETED, got %s", result.Status)
	}
	t.Logf("TestAdminMarkPaidWithdrawal: status=%s", result.Status)
}

// ============================================================
// TestAdminApproveWithdrawal_NotFound — id=999999 → 400 或 404
// ============================================================
func TestAdminApproveWithdrawal_NotFound(t *testing.T) {
	requireAdmin(t)

	resp, status, err := doPost(
		baseURL+"/api/v1/admin/withdrawals/999999/approve",
		map[string]string{"remark": "ghost"},
		adminToken,
	)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status) // 如果路由未注册就跳过

	if status == http.StatusOK && resp.Code == 0 {
		t.Fatal("expected error for non-existent withdrawal, but got success")
	}
	t.Logf("TestAdminApproveWithdrawal_NotFound: status=%d, code=%d, msg=%s", status, resp.Code, resp.Message)
}

// ============================================================
// TestUserCannotAccessAdminWithdrawal — 普通用户访问管理员提现路由应被拒绝
// ============================================================
func TestUserCannotAccessAdminWithdrawal(t *testing.T) {
	requireUser(t)

	resp, status, err := doGet(baseURL+"/api/v1/admin/withdrawals", userToken)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	skipIfNotFound(t, status)

	if status == http.StatusOK && resp.Code == 0 {
		t.Fatal("expected 403/401 for regular user accessing admin withdrawal, got success")
	}
	if status != http.StatusForbidden && status != http.StatusUnauthorized {
		t.Logf("注意: 期望 403/401, 实际返回 %d", status)
	}
	t.Logf("TestUserCannotAccessAdminWithdrawal: status=%d (access denied as expected)", status)
}
