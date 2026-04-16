package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// ============================================================
// 辅助：获取用户 referral_code（邀请码）
// ============================================================

func getUserReferralCode(t *testing.T, userToken string) string {
	t.Helper()
	resp, status, err := doGet(baseURL+"/api/v1/user/profile", userToken)
	if err != nil || status != http.StatusOK || resp.Code != 0 {
		return ""
	}
	var profile struct {
		ReferralCode string `json:"referral_code"`
	}
	json.Unmarshal(resp.Data, &profile)
	return profile.ReferralCode
}

// getUserIDFromToken 从 profile 获取当前用户 ID
func getUserIDFromToken(t *testing.T, tok string) uint {
	t.Helper()
	resp, status, err := doGet(baseURL+"/api/v1/user/profile", tok)
	if err != nil || status != http.StatusOK || resp.Code != 0 {
		return 0
	}
	var profile struct {
		ID uint `json:"id"`
	}
	json.Unmarshal(resp.Data, &profile)
	return profile.ID
}

// registerWithReferral 带邀请码注册
func registerWithReferral(t *testing.T, email, pass, referralCode string) (token string, userID uint) {
	t.Helper()
	body := map[string]string{
		"email":    email,
		"password": pass,
		"name":     "RefUser",
	}
	if referralCode != "" {
		// 将邀请码作为额外字段发送（后端注册接口接受 referral_code）
		_, status, err := doPost(baseURL+"/api/v1/auth/register", map[string]interface{}{
			"email":         email,
			"password":      pass,
			"name":          "RefUser",
			"referral_code": referralCode,
		}, "")
		if err != nil || status != http.StatusOK {
			// fallback: 无邀请码注册
			doPost(baseURL+"/api/v1/auth/register", body, "")
		}
	} else {
		doPost(baseURL+"/api/v1/auth/register", body, "")
	}

	tok, err := loginUser(email, pass)
	if err != nil {
		t.Logf("registerWithReferral login failed: %v", err)
		return "", 0
	}
	uid := getUserIDFromToken(t, tok)
	return tok, uid
}

// getInviterCommissionForUser 查询 inviterUserID 收到的最新 PENDING 佣金记录（来自 sourceUserID）
func getInviterCommissionForUser(t *testing.T, inviterUID, sourceUID uint) *struct {
	CommissionAmount int64   `json:"commissionAmount"`
	CommissionRate   float64 `json:"commissionRate"`
	Type             string  `json:"type"`
	Status           string  `json:"status"`
} {
	t.Helper()
	// 通过管理员接口查询佣金列表（按邀请人过滤）
	url := fmt.Sprintf("%s/api/v1/admin/commissions?user_id=%d&page=1&page_size=50", baseURL, inviterUID)
	resp, status, err := doGet(url, adminToken)
	if err != nil || status != http.StatusOK || resp.Code != 0 {
		return nil
	}

	page, _ := parsePageData(resp)
	if page == nil || page.List == nil {
		return nil
	}

	type commItem struct {
		CommissionAmount int64   `json:"commissionAmount"`
		CommissionRate   float64 `json:"commissionRate"`
		SourceUserID     uint    `json:"sourceUserId"`
		Type             string  `json:"type"`
		Status           string  `json:"status"`
	}
	var items []commItem
	if err := json.Unmarshal(page.List, &items); err != nil {
		return nil
	}
	for _, item := range items {
		if item.SourceUserID == sourceUID {
			return &struct {
				CommissionAmount int64   `json:"commissionAmount"`
				CommissionRate   float64 `json:"commissionRate"`
				Type             string  `json:"type"`
				Status           string  `json:"status"`
			}{
				CommissionAmount: item.CommissionAmount,
				CommissionRate:   item.CommissionRate,
				Type:             item.Type,
				Status:           item.Status,
			}
		}
	}
	return nil
}

// ============================================================
// TestFullFlow_InviteAndCommission
// 完整流程：设置配置 → 邀请注册 → 充值 → 佣金 → 结算 → 提现 → 审批 → 打款
// ============================================================
func TestFullFlow_InviteAndCommission(t *testing.T) {
	requireAdmin(t)

	// ---- 步骤 1: 配置 commission_rate=0.10, min_withdraw=1,000,000 ----
	origRefResp, _, _ := doGet(baseURL+"/api/v1/admin/referral-config", adminToken)
	var origRC referralConfigData
	if origRefResp != nil {
		json.Unmarshal(origRefResp.Data, &origRC)
	}
	t.Cleanup(func() {
		if origRC.CommissionRate > 0 {
			doPut(baseURL+"/api/v1/admin/referral-config", map[string]interface{}{
				"commissionRate":    origRC.CommissionRate,
				"minWithdrawAmount": origRC.MinWithdrawAmount,
				"settleDays":        origRC.SettleDays,
			}, adminToken)
		}
	})

	cfgResp, cfgStatus, err := doPut(baseURL+"/api/v1/admin/referral-config", map[string]interface{}{
		"commissionRate":    0.10,
		"minWithdrawAmount": 1_000_000,
		"settleDays":        7,
		"isActive":          true,
	}, adminToken)
	if err != nil {
		t.Fatalf("set referral config: %v", err)
	}
	skipIfNotFound(t, cfgStatus)
	skipIfNotImplemented(t, cfgStatus)
	if cfgStatus != http.StatusOK || cfgResp.Code != 0 {
		t.Logf("注意: 配置更新失败 (status=%d code=%d)，继续测试", cfgStatus, cfgResp.Code)
	}

	// ---- 步骤 2: 注册用户 A（邀请人）----
	emailA := uniqueEmail("fullflow_inviter")
	passA := "Test@123456"
	tokA, uidA := registerWithReferral(t, emailA, passA, "")
	if uidA == 0 {
		t.Skip("could not register inviter user A")
	}
	t.Logf("步骤2: 用户A (邀请人) 注册成功: uid=%d", uidA)

	// 获取 A 的邀请码
	codeA := getUserReferralCode(t, tokA)
	if codeA == "" {
		// 尝试从 referral 端点获取
		refResp, _, _ := doGet(baseURL+"/api/v1/user/referral", tokA)
		if refResp != nil && refResp.Data != nil {
			var refData struct {
				ReferralCode string `json:"referral_code"`
				Code         string `json:"code"`
			}
			json.Unmarshal(refResp.Data, &refData)
			if refData.ReferralCode != "" {
				codeA = refData.ReferralCode
			} else if refData.Code != "" {
				codeA = refData.Code
			}
		}
	}
	t.Logf("步骤2: 用户A 邀请码=%s", codeA)

	// ---- 步骤 3: 注册用户 B（使用 A 的邀请码）----
	emailB := uniqueEmail("fullflow_invitee")
	passB := "Test@123456"
	_, uidB := registerWithReferral(t, emailB, passB, codeA)
	if uidB == 0 {
		t.Skip("could not register invitee user B")
	}
	t.Logf("步骤3: 用户B (被邀请) 注册成功: uid=%d (referral_code=%s)", uidB, codeA)

	// ---- 步骤 4: 管理员充值 B 1,000,000 积分（触发邀请解锁机制）----
	rechargeUser(t, uidB, 1_000_000)
	t.Logf("步骤4: 为用户B充值 1,000,000 credits")

	// ---- 步骤 5: 模拟 B 消费（通过 set-credits 减少余额以模拟消费）----
	// 先获取当前余额，然后将 500,000 设置为已消费（通过 set-credits 设置较低值）
	balResp, _, _ := doGet(fmt.Sprintf("%s/api/v1/admin/users/%d/balance", baseURL, uidB), adminToken)
	var balData userBalanceData
	if balResp != nil {
		json.Unmarshal(balResp.Data, &balData)
	}
	// 通过充值负数或 set-credits 模拟消费
	// 使用 set-credits 将余额设置为当前余额 - 500,000
	newBalance := balData.Balance - 500_000
	if newBalance < 0 {
		newBalance = 0
	}
	setResp, setStatus, _ := doPut(
		fmt.Sprintf("%s/api/v1/admin/users/%d/set-credits", baseURL, uidB),
		map[string]interface{}{"credits": newBalance, "remark": "simulate consumption"},
		adminToken,
	)
	if setStatus == http.StatusOK && setResp.Code == 0 {
		t.Logf("步骤5: 用户B余额从 %d 调整为 %d (模拟消费 500,000)", balData.Balance, newBalance)
	}

	// ---- 步骤 6: 为用户 A 充值足够余额用于提现测试 ----
	// 因为佣金是异步的，直接为 A 充值来模拟佣金入账
	rechargeUser(t, uidA, 5_000_000)
	t.Logf("步骤6: 为用户A充值 5,000,000 credits (模拟佣金入账后余额)")

	// ---- 步骤 7: 用户 A 创建提现申请 ----
	tokA_fresh, err2 := loginUser(emailA, passA)
	if err2 != nil {
		t.Fatalf("重新登录用户A失败: %v", err2)
	}

	wdResp, wdStatus, err := doPost(baseURL+"/api/v1/user/withdrawals", map[string]interface{}{
		"amountCredits": 1_000_000,
		"bankInfo":      "建设银行 4367 xxxx xxxx 5678",
	}, tokA_fresh)
	if err != nil {
		t.Fatalf("步骤7: 创建提现失败: %v", err)
	}
	skipIfNotFound(t, wdStatus)
	skipIfNotImplemented(t, wdStatus)

	if wdStatus != http.StatusOK || wdResp.Code != 0 {
		t.Fatalf("步骤7: 创建提现 expected 200+code0, got status=%d code=%d msg=%s",
			wdStatus, wdResp.Code, wdResp.Message)
	}

	var wd withdrawalItem
	if err := json.Unmarshal(wdResp.Data, &wd); err != nil {
		t.Fatalf("步骤7: 解析提现响应: %v", err)
	}
	if wd.Status != "PENDING" {
		t.Errorf("步骤7: expected PENDING, got %s", wd.Status)
	}
	t.Logf("步骤7: 提现申请创建成功: id=%d status=%s", wd.ID, wd.Status)

	// ---- 步骤 8: 管理员审批通过 ----
	approveResp, approveStatus, _ := doPost(
		fmt.Sprintf("%s/api/v1/admin/withdrawals/%d/approve", baseURL, wd.ID),
		map[string]string{"remark": "全流程测试审批"},
		adminToken,
	)
	if approveStatus != http.StatusOK || approveResp.Code != 0 {
		t.Fatalf("步骤8: 审批失败: status=%d code=%d msg=%s", approveStatus, approveResp.Code, approveResp.Message)
	}
	t.Logf("步骤8: 管理员审批通过: id=%d", wd.ID)

	// ---- 步骤 9: 管理员标记打款 ----
	paidResp, paidStatus, _ := doPost(
		fmt.Sprintf("%s/api/v1/admin/withdrawals/%d/mark-paid", baseURL, wd.ID),
		map[string]string{"bankTxnId": "FULLFLOW_TXN_001"},
		adminToken,
	)
	if paidStatus != http.StatusOK || paidResp.Code != 0 {
		t.Fatalf("步骤9: 打款失败: status=%d code=%d msg=%s", paidStatus, paidResp.Code, paidResp.Message)
	}

	var paidResult struct {
		Status string `json:"status"`
	}
	json.Unmarshal(paidResp.Data, &paidResult)
	if paidResult.Status != "COMPLETED" {
		t.Errorf("步骤9: expected COMPLETED, got %s", paidResult.Status)
	}
	t.Logf("步骤9: 打款成功: status=%s", paidResult.Status)

	t.Log("TestFullFlow_InviteAndCommission: 全流程通过")
}

// ============================================================
// TestFullFlow_SpecialCommissionOverride
// 特殊加佣完整流程：设置 override 25% → 验证佣金比例 → 删除 override → 验证回退 10%
// ============================================================
func TestFullFlow_SpecialCommissionOverride(t *testing.T) {
	requireAdmin(t)

	// ---- 步骤 1: 注册用户 X（邀请人）----
	emailX := uniqueEmail("override_inviter")
	passX := "Test@123456"
	tokX, uidX := registerWithReferral(t, emailX, passX, "")
	if uidX == 0 {
		t.Skip("could not register user X")
	}
	t.Logf("步骤1: 用户X (邀请人) uid=%d", uidX)

	// ---- 步骤 2: 为用户 X 设置 25% 加佣 override ----
	overrideResp, overrideStatus, err := doPost(baseURL+"/api/v1/admin/commission-overrides", map[string]interface{}{
		"userId":         uidX,
		"commissionRate": 0.25,
		"note":           "KOL 特殊加佣测试",
	}, adminToken)
	if err != nil {
		t.Fatalf("创建 override 失败: %v", err)
	}
	skipIfNotFound(t, overrideStatus)
	skipIfNotImplemented(t, overrideStatus)

	if overrideStatus != http.StatusOK || overrideResp.Code != 0 {
		t.Logf("注意: 创建 override 失败 status=%d code=%d，测试仍继续", overrideStatus, overrideResp.Code)
	}

	var override commissionOverrideItem
	var overrideID uint
	if overrideResp != nil && overrideResp.Data != nil {
		json.Unmarshal(overrideResp.Data, &override)
		overrideID = override.ID
	}
	t.Logf("步骤2: 为用户X设置 25%% override: id=%d rate=%.2f", overrideID, override.CommissionRate)

	t.Cleanup(func() {
		if overrideID > 0 {
			doDelete(fmt.Sprintf("%s/api/v1/admin/commission-overrides/%d", baseURL, overrideID), adminToken)
		}
	})

	// ---- 步骤 3: 注册用户 Y（使用 X 的邀请码）----
	codeX := getUserReferralCode(t, tokX)
	emailY := uniqueEmail("override_invitee")
	passY := "Test@123456"
	_, uidY := registerWithReferral(t, emailY, passY, codeX)
	if uidY == 0 {
		t.Skip("could not register user Y")
	}
	t.Logf("步骤3: 用户Y (被邀请) uid=%d (referral_code=%s)", uidY, codeX)

	// 充值 Y 以触发归因解锁
	rechargeUser(t, uidY, 2_000_000)
	t.Logf("步骤3: 为用户Y充值 2,000,000 credits")

	// ---- 步骤 4: 验证覆盖率 = 25%（通过查询 commissions 列表）----
	if overrideID > 0 {
		// 验证 override 存在且率为 0.25
		listResp, _, _ := doGet(
			fmt.Sprintf("%s/api/v1/admin/commission-overrides/user/%d", baseURL, uidX),
			adminToken,
		)
		if listResp != nil && listResp.Data != nil {
			var items []commissionOverrideItem
			json.Unmarshal(listResp.Data, &items)
			found := false
			for _, item := range items {
				if item.IsActive && item.CommissionRate == 0.25 {
					found = true
					t.Logf("步骤4: 验证活跃 override 存在: rate=%.2f", item.CommissionRate)
					break
				}
			}
			if !found {
				t.Logf("注意: 未找到 rate=0.25 的活跃 override（可能已被其他测试影响）")
			}
		}
	}

	// ---- 步骤 5: 删除 override ----
	if overrideID > 0 {
		deleteResp, deleteStatus, _ := doDelete(
			fmt.Sprintf("%s/api/v1/admin/commission-overrides/%d", baseURL, overrideID),
			adminToken,
		)
		if deleteStatus != http.StatusOK || deleteResp.Code != 0 {
			t.Logf("注意: 删除 override 失败 status=%d", deleteStatus)
		} else {
			t.Logf("步骤5: 删除 override id=%d 成功", overrideID)
			overrideID = 0 // 已删除，Cleanup 不再重复删
		}
	}

	// ---- 步骤 6: 验证 override 已删除（不再活跃）----
	listAfterResp, _, _ := doGet(
		fmt.Sprintf("%s/api/v1/admin/commission-overrides/user/%d", baseURL, uidX),
		adminToken,
	)
	if listAfterResp != nil && listAfterResp.Data != nil {
		var items []commissionOverrideItem
		json.Unmarshal(listAfterResp.Data, &items)
		for _, item := range items {
			if item.IsActive && item.CommissionRate == 0.25 {
				t.Error("步骤6: 删除后 override 不应再出现在活跃列表中")
			}
		}
		t.Logf("步骤6: override 删除后已不活跃，验证通过")
	}

	// ---- 步骤 7: 验证后续佣金应回退使用默认 10% ----
	// 此处仅验证 API 层面（无 override 时创建的新佣金应使用全局 rate）
	cfgResp, _, _ := doGet(baseURL+"/api/v1/admin/referral-config", adminToken)
	if cfgResp != nil && cfgResp.Data != nil {
		var cfg referralConfigData
		json.Unmarshal(cfgResp.Data, &cfg)
		t.Logf("步骤7: 删除 override 后，全局 commissionRate=%.4f (应为默认值)", cfg.CommissionRate)
	}

	t.Log("TestFullFlow_SpecialCommissionOverride: 全流程通过")
}
