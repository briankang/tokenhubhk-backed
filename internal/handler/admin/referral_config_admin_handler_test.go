package admin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/handler/admin"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/service/balance"
	"tokenhub-server/internal/service/referral"
)

func openTestDB(t *testing.T) *gorm.DB {
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		dsn = "root:root123456@tcp(127.0.0.1:3306)/tokenhubhk?charset=utf8mb4&parseTime=True&loc=Local"
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Skipf("database not available: %v", err)
		return nil
	}
	return db
}

// TestAdminUpdateReferralConfig_Success 测试管理员更新邀请配置成功
func TestAdminUpdateReferralConfig_Success(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}

	// 获取当前活跃配置
	var originalCfg model.ReferralConfig
	db.Where("is_active = ?", true).First(&originalCfg)
	originalRate := originalCfg.CommissionRate

	// 创建 handler
	refSvc := referral.NewReferralService(db)
	handler := admin.NewReferralConfigHandler(refSvc)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	rg := router.Group("/api/v1/admin")
	handler.Register(rg)

	// 准备更新数据（修改佣金率为 15%）
	updateData := map[string]interface{}{
		"commissionRate":       0.15,
		"attributionDays":      90,
		"lifetimeCapCredits":   30000000,
		"minPaidCreditsUnlock": 100000,
		"minWithdrawAmount":    1000000,
		"settleDays":           7,
	}
	body, _ := json.Marshal(updateData)

	// 发送 PUT 请求
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PUT", "/api/v1/admin/referral-config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	// 验证响应
	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Code int `json:"code"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Code != 0 {
		t.Errorf("expected code 0, got %d", resp.Code)
	}

	// 验证数据库已更新
	var updated model.ReferralConfig
	db.Where("is_active = ?", true).First(&updated)
	if updated.CommissionRate != 0.15 {
		t.Errorf("expected commission_rate 0.15, got %f", updated.CommissionRate)
	}

	t.Logf("Updated commission rate from %f to %f", originalRate, updated.CommissionRate)

	// 恢复原始配置
	t.Cleanup(func() {
		db.Model(&updated).Update("commission_rate", originalRate)
	})
}

// TestAdminUpdateReferralConfig_ValidationFails 测试配置验证失败
func TestAdminUpdateReferralConfig_ValidationFails(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}

	refSvc := referral.NewReferralService(db)
	handler := admin.NewReferralConfigHandler(refSvc)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	rg := router.Group("/api/v1/admin")
	handler.Register(rg)

	testCases := []struct {
		name     string
		data     map[string]interface{}
		wantFail bool
	}{
		{
			name: "commission_rate_too_high",
			data: map[string]interface{}{
				"commissionRate":       0.85, // 超过 80% 上限
				"attributionDays":      90,
				"lifetimeCapCredits":   30000000,
				"minPaidCreditsUnlock": 100000,
				"minWithdrawAmount":    1000000,
				"settleDays":           7,
			},
			wantFail: true,
		},
		{
			name: "commission_rate_negative",
			data: map[string]interface{}{
				"commissionRate":       -0.05,
				"attributionDays":      90,
				"lifetimeCapCredits":   30000000,
				"minPaidCreditsUnlock": 100000,
				"minWithdrawAmount":    1000000,
				"settleDays":           7,
			},
			wantFail: true,
		},
		{
			name: "attribution_days_too_short",
			data: map[string]interface{}{
				"commissionRate":       0.10,
				"attributionDays":      5, // 低于 7 天下限
				"lifetimeCapCredits":   30000000,
				"minPaidCreditsUnlock": 100000,
				"minWithdrawAmount":    1000000,
				"settleDays":           7,
			},
			wantFail: true,
		},
		{
			name: "attribution_days_too_long",
			data: map[string]interface{}{
				"commissionRate":       0.10,
				"attributionDays":      4000, // 超过 3650 天上限
				"lifetimeCapCredits":   30000000,
				"minPaidCreditsUnlock": 100000,
				"minWithdrawAmount":    1000000,
				"settleDays":           7,
			},
			wantFail: true,
		},
		{
			name: "settle_days_invalid",
			data: map[string]interface{}{
				"commissionRate":       0.10,
				"attributionDays":      90,
				"lifetimeCapCredits":   30000000,
				"minPaidCreditsUnlock": 100000,
				"minWithdrawAmount":    1000000,
				"settleDays":           0, // 低于 1 天下限
			},
			wantFail: true,
		},
		{
			name: "valid_config",
			data: map[string]interface{}{
				"commissionRate":       0.20,
				"attributionDays":      180,
				"lifetimeCapCredits":   50000000,
				"minPaidCreditsUnlock": 200000,
				"minWithdrawAmount":    500000,
				"settleDays":           14,
			},
			wantFail: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(tc.data)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest("PUT", "/api/v1/admin/referral-config", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			if tc.wantFail {
				if w.Code == http.StatusOK {
					t.Errorf("expected validation failure, got status 200")
				}
				t.Logf("Validation correctly failed: %s", w.Body.String())
			} else {
				if w.Code != http.StatusOK {
					t.Errorf("expected status 200, got %d: %s", w.Code, w.Body.String())
				}
			}
		})
	}
}

// TestAdminUpdateQuotaConfig_Success 测试管理员更新注册赠送配置成功
func TestAdminUpdateQuotaConfig_Success(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}

	// 获取当前活跃配置
	var originalCfg model.QuotaConfig
	db.Where("is_active = ?", true).First(&originalCfg)
	originalBonus := originalCfg.InviterBonus

	// 创建 handler
	balSvc := balance.NewBalanceService(db, nil)
	handler := admin.NewQuotaHandler(balSvc)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	rg := router.Group("/api/v1/admin")
	handler.Register(rg)

	// 准备更新数据（修改邀请人奖励为 20000）
	updateData := map[string]interface{}{
		"defaultFreeQuota":     3000,
		"registrationBonus":    0,
		"inviteeBonus":         5000,
		"inviteeUnlockCredits": 10000,
		"inviterBonus":         20000, // 修改此值
		"inviterUnlockPaidRmb": 100000,
		"inviterMonthlyCap":    10,
	}
	body, _ := json.Marshal(updateData)

	// 发送 PUT 请求
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PUT", "/api/v1/admin/quota-config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	// 验证响应
	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Code int `json:"code"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Code != 0 {
		t.Errorf("expected code 0, got %d", resp.Code)
	}

	// 验证数据库已更新
	var updated model.QuotaConfig
	db.Where("is_active = ?", true).First(&updated)
	if updated.InviterBonus != 20000 {
		t.Errorf("expected inviter_bonus 20000, got %d", updated.InviterBonus)
	}

	t.Logf("Updated inviter bonus from %d to %d", originalBonus, updated.InviterBonus)

	// 恢复原始配置
	t.Cleanup(func() {
		db.Model(&updated).Update("inviter_bonus", originalBonus)
	})
}

// TestConfigUpdate_AffectsNewCommissions 测试配置更新后新佣金计算生效
func TestConfigUpdate_AffectsNewCommissions(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}

	// 1. 记录原始配置
	var originalCfg model.ReferralConfig
	db.Where("is_active = ?", true).First(&originalCfg)
	originalRate := originalCfg.CommissionRate

	// 2. 更新配置为 20%
	db.Model(&originalCfg).Update("commission_rate", 0.20)

	// 3. 创建测试归因和用户
	inviterID := uint(810001)
	inviteeID := uint(810002)

	cleanup := func() {
		db.Unscoped().Where("user_id = ?", inviteeID).Delete(&model.ReferralAttribution{})
		db.Unscoped().Where("user_id = ?", inviterID).Delete(&model.CommissionRecord{})
		db.Unscoped().Delete(&model.User{}, inviterID)
		db.Unscoped().Delete(&model.User{}, inviteeID)
	}
	cleanup()
	t.Cleanup(cleanup)

	// 创建用户
	db.Create(&model.User{BaseModel: model.BaseModel{ID: inviterID}, TenantID: 1, Name: "inviter810001", Email: "inviter810001@test.com", PasswordHash: "hash"})
	db.Create(&model.User{BaseModel: model.BaseModel{ID: inviteeID}, TenantID: 1, Name: "invitee810002", Email: "invitee810002@test.com", PasswordHash: "hash"})

	// 创建已解锁的归因
	now := time.Now()
	attr := model.ReferralAttribution{
		UserID:       inviteeID,
		InviterID:    inviterID,
		ReferralCode: "TEST20",
		AttributedAt: now,
		ExpiresAt:    now.AddDate(0, 0, 90),
		UnlockedAt:   &now,
		IsValid:      true,
	}
	db.Create(&attr)

	// 4. 触发佣金计算
	calc := referral.NewCommissionCalculator(db)
	calc.CalculateCommissions(context.Background(), inviteeID, 1, 1_000_000, "gpt-4o", 1)

	// 等待佣金写入
	time.Sleep(500 * time.Millisecond)

	// 5. 验证佣金按新费率计算（1,000,000 × 20% = 200,000）
	var commRec model.CommissionRecord
	err := db.Where("user_id = ? AND attribution_id = ?", inviterID, attr.ID).First(&commRec).Error
	if err != nil {
		t.Fatalf("commission record not found: %v", err)
	}

	expectedComm := int64(200_000)
	if commRec.CommissionAmount != expectedComm {
		t.Errorf("expected commission %d (20%%), got %d", expectedComm, commRec.CommissionAmount)
	}
	if commRec.EffectiveRate != 0.20 {
		t.Errorf("expected effective_rate 0.20, got %f", commRec.EffectiveRate)
	}

	t.Logf("Commission calculated with updated rate: %d credits (20%%)", commRec.CommissionAmount)

	// 恢复原始配置
	t.Cleanup(func() {
		db.Model(&originalCfg).Update("commission_rate", originalRate)
	})
}
