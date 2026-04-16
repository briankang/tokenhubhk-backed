package public_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/handler/public"
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

// TestGetReferralConfig_Success 测试获取邀请配置成功
func TestGetReferralConfig_Success(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}

	// 确保有活跃配置
	var cfg model.ReferralConfig
	err := db.Where("is_active = ?", true).First(&cfg).Error
	if err != nil {
		// 创建默认配置
		cfg = model.ReferralConfig{
			CommissionRate:       0.10,
			AttributionDays:      90,
			LifetimeCapCredits:   30000000,
			MinPaidCreditsUnlock: 100000,
			MinWithdrawAmount:    1000000,
			SettleDays:           7,
			IsActive:             true,
		}
		db.Create(&cfg)
	}

	// 创建 handler
	refSvc := referral.NewReferralService(db)
	balSvc := balance.NewBalanceService(db, nil)
	handler := public.NewConfigHandler(refSvc, balSvc)

	// 创建测试路由
	gin.SetMode(gin.TestMode)
	router := gin.New()
	rg := router.Group("/api/v1/public")
	handler.Register(rg)

	// 发送请求
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/public/referral-config", nil)
	router.ServeHTTP(w, req)

	// 验证响应
	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp struct {
		Code int `json:"code"`
		Data struct {
			CommissionRate       float64 `json:"commissionRate"`
			AttributionDays      int     `json:"attributionDays"`
			LifetimeCapCredits   int64   `json:"lifetimeCapCredits"`
			MinPaidCreditsUnlock int64   `json:"minPaidCreditsUnlock"`
			MinWithdrawAmount    int64   `json:"minWithdrawAmount"`
			SettleDays           int     `json:"settleDays"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.Code != 0 {
		t.Errorf("expected code 0, got %d", resp.Code)
	}
	if resp.Data.CommissionRate != 0.10 {
		t.Errorf("expected commission_rate 0.10, got %f", resp.Data.CommissionRate)
	}
	if resp.Data.AttributionDays != 90 {
		t.Errorf("expected attribution_days 90, got %d", resp.Data.AttributionDays)
	}
	if resp.Data.LifetimeCapCredits != 30000000 {
		t.Errorf("expected lifetime_cap 30000000, got %d", resp.Data.LifetimeCapCredits)
	}
	if resp.Data.MinPaidCreditsUnlock != 100000 {
		t.Errorf("expected min_unlock 100000, got %d", resp.Data.MinPaidCreditsUnlock)
	}
	if resp.Data.MinWithdrawAmount != 1000000 {
		t.Errorf("expected min_withdraw 1000000, got %d", resp.Data.MinWithdrawAmount)
	}
	if resp.Data.SettleDays != 7 {
		t.Errorf("expected settle_days 7, got %d", resp.Data.SettleDays)
	}

	t.Logf("ReferralConfig API response: %+v", resp.Data)
}

// TestGetQuotaConfig_Success 测试获取注册赠送配置成功
func TestGetQuotaConfig_Success(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}

	// 确保有活跃配置
	var cfg model.QuotaConfig
	err := db.Where("is_active = ?", true).First(&cfg).Error
	if err != nil {
		// 创建默认配置
		cfg = model.QuotaConfig{
			DefaultFreeQuota:     3000,
			RegistrationBonus:    0,
			InviteeBonus:         5000,
			InviteeUnlockCredits: 10000,
			InviterBonus:         10000,
			InviterUnlockPaidRMB: 100000,
			InviterMonthlyCap:    10,
			IsActive:             true,
		}
		db.Create(&cfg)
	}

	// 创建 handler
	refSvc := referral.NewReferralService(db)
	balSvc := balance.NewBalanceService(db, nil)
	handler := public.NewConfigHandler(refSvc, balSvc)

	// 创建测试路由
	gin.SetMode(gin.TestMode)
	router := gin.New()
	rg := router.Group("/api/v1/public")
	handler.Register(rg)

	// 发送请求
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/public/quota-config", nil)
	router.ServeHTTP(w, req)

	// 验证响应
	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp struct {
		Code int `json:"code"`
		Data struct {
			DefaultFreeQuota     int64 `json:"defaultFreeQuota"`
			RegistrationBonus    int64 `json:"registrationBonus"`
			InviteeBonus         int64 `json:"inviteeBonus"`
			InviteeUnlockCredits int64 `json:"inviteeUnlockCredits"`
			InviterBonus         int64 `json:"inviterBonus"`
			InviterUnlockPaidRMB int64 `json:"inviterUnlockPaidRmb"`
			InviterMonthlyCap    int   `json:"inviterMonthlyCap"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.Code != 0 {
		t.Errorf("expected code 0, got %d", resp.Code)
	}
	if resp.Data.DefaultFreeQuota != 3000 {
		t.Errorf("expected default_free_quota 3000, got %d", resp.Data.DefaultFreeQuota)
	}
	if resp.Data.InviteeBonus != 5000 {
		t.Errorf("expected invitee_bonus 5000, got %d", resp.Data.InviteeBonus)
	}
	if resp.Data.InviterBonus != 10000 {
		t.Errorf("expected inviter_bonus 10000, got %d", resp.Data.InviterBonus)
	}
	if resp.Data.InviterMonthlyCap != 10 {
		t.Errorf("expected inviter_monthly_cap 10, got %d", resp.Data.InviterMonthlyCap)
	}

	t.Logf("QuotaConfig API response: %+v", resp.Data)
}

// TestConfigAPIs_NoDeprecatedFields 验证公开API不返回废弃字段
func TestConfigAPIs_NoDeprecatedFields(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}

	refSvc := referral.NewReferralService(db)
	balSvc := balance.NewBalanceService(db, nil)
	handler := public.NewConfigHandler(refSvc, balSvc)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	rg := router.Group("/api/v1/public")
	handler.Register(rg)

	// 测试 referral-config
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/public/referral-config", nil)
	router.ServeHTTP(w, req)

	var refResp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &refResp)

	data, ok := refResp["data"].(map[string]interface{})
	if !ok {
		t.Fatal("expected data object")
	}

	// 验证不包含废弃字段
	deprecatedFields := []string{"personalCashbackRate", "l1CommissionRate", "l2CommissionRate", "l3CommissionRate"}
	for _, field := range deprecatedFields {
		if _, exists := data[field]; exists {
			t.Errorf("response should not contain deprecated field: %s", field)
		}
	}

	t.Log("Verified: no deprecated fields in public API response")
}
