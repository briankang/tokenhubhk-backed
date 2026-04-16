package referral_test

import (
	"context"
	"testing"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/service/referral"
)

// TestReferralCodeGeneration_Uniqueness 测试邀请码生成唯一性
func TestReferralCodeGeneration_Uniqueness(t *testing.T) {
	codes := make(map[string]bool)
	iterations := 1000

	for i := 0; i < iterations; i++ {
		code, err := referral.GenerateCode()
		if err != nil {
			t.Fatalf("GenerateCode failed: %v", err)
		}
		if len(code) != 8 {
			t.Errorf("expected code length 8, got %d", len(code))
		}
		if codes[code] {
			t.Errorf("duplicate code generated: %s", code)
		}
		codes[code] = true
	}

	t.Logf("Generated %d unique codes", len(codes))
}

// TestReferralCodeGeneration_CharacterSet 测试邀请码字符集合法性
func TestReferralCodeGeneration_CharacterSet(t *testing.T) {
	validChars := "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	charMap := make(map[rune]bool)
	for _, c := range validChars {
		charMap[c] = true
	}

	for i := 0; i < 100; i++ {
		code, err := referral.GenerateCode()
		if err != nil {
			t.Fatalf("GenerateCode failed: %v", err)
		}
		for _, c := range code {
			if !charMap[c] {
				t.Errorf("invalid character '%c' in code %s", c, code)
			}
		}
	}

	t.Log("All generated codes use valid character set")
}

// TestGetOrCreateLink_Idempotent 测试邀请链接创建幂等性
func TestGetOrCreateLink_Idempotent(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}
	ctx := context.Background()

	userID := uint(820001)
	tenantID := uint(1)

	cleanup := func() {
		db.Unscoped().Where("user_id = ?", userID).Delete(&model.ReferralLink{})
		db.Unscoped().Delete(&model.User{}, userID)
	}
	cleanup()
	t.Cleanup(cleanup)

	ensureTestTenantForCalc(db)
	seedUser(t, userID)

	refSvc := referral.NewReferralService(db)

	// 第一次调用：创建
	link1, err := refSvc.GetOrCreateLink(ctx, userID, tenantID)
	if err != nil {
		t.Fatalf("first GetOrCreateLink failed: %v", err)
	}
	if link1.Code == "" {
		t.Fatal("expected non-empty code")
	}

	// 第二次调用：返回已存在的
	link2, err := refSvc.GetOrCreateLink(ctx, userID, tenantID)
	if err != nil {
		t.Fatalf("second GetOrCreateLink failed: %v", err)
	}

	// 验证返回同一条记录
	if link1.ID != link2.ID {
		t.Errorf("expected same link ID, got %d and %d", link1.ID, link2.ID)
	}
	if link1.Code != link2.Code {
		t.Errorf("expected same code, got %s and %s", link1.Code, link2.Code)
	}

	t.Logf("GetOrCreateLink is idempotent: code=%s, id=%d", link1.Code, link1.ID)
}

// TestFindByCode_Success 测试根据邀请码查找链接
func TestFindByCode_Success(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}
	ctx := context.Background()

	userID := uint(820101)
	tenantID := uint(1)

	cleanup := func() {
		db.Unscoped().Where("user_id = ?", userID).Delete(&model.ReferralLink{})
		db.Unscoped().Delete(&model.User{}, userID)
	}
	cleanup()
	t.Cleanup(cleanup)

	ensureTestTenantForCalc(db)
	seedUser(t, userID)

	refSvc := referral.NewReferralService(db)

	// 创建链接
	link, _ := refSvc.GetOrCreateLink(ctx, userID, tenantID)

	// 根据 code 查找
	found, err := refSvc.FindByCode(ctx, link.Code)
	if err != nil {
		t.Fatalf("FindByCode failed: %v", err)
	}
	if found.ID != link.ID {
		t.Errorf("expected link ID %d, got %d", link.ID, found.ID)
	}
	if found.UserID != userID {
		t.Errorf("expected user_id %d, got %d", userID, found.UserID)
	}

	t.Logf("FindByCode success: code=%s, user_id=%d", link.Code, found.UserID)
}

// TestFindByCode_NotFound 测试查找不存在的邀请码
func TestFindByCode_NotFound(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}
	ctx := context.Background()

	refSvc := referral.NewReferralService(db)

	_, err := refSvc.FindByCode(ctx, "NOTEXIST")
	if err == nil {
		t.Error("expected error for non-existent code, got nil")
	}

	t.Logf("FindByCode correctly returns error for non-existent code")
}

// TestIncrementClickCount 测试点击计数增加
func TestIncrementClickCount(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}
	ctx := context.Background()

	userID := uint(820201)
	tenantID := uint(1)

	cleanup := func() {
		db.Unscoped().Where("user_id = ?", userID).Delete(&model.ReferralLink{})
		db.Unscoped().Delete(&model.User{}, userID)
	}
	cleanup()
	t.Cleanup(cleanup)

	ensureTestTenantForCalc(db)
	seedUser(t, userID)

	refSvc := referral.NewReferralService(db)

	// 创建链接
	link, _ := refSvc.GetOrCreateLink(ctx, userID, tenantID)
	initialCount := link.ClickCount

	// 增加点击计数 3 次
	for i := 0; i < 3; i++ {
		err := refSvc.IncrementClickCount(ctx, link.Code)
		if err != nil {
			t.Fatalf("IncrementClickCount failed: %v", err)
		}
	}

	// 重新加载验证
	var updated model.ReferralLink
	db.First(&updated, link.ID)
	expectedCount := initialCount + 3
	if updated.ClickCount != expectedCount {
		t.Errorf("expected click_count %d, got %d", expectedCount, updated.ClickCount)
	}

	t.Logf("ClickCount incremented from %d to %d", initialCount, updated.ClickCount)
}

// TestIncrementRegisterCount 测试注册计数增加
func TestIncrementRegisterCount(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}
	ctx := context.Background()

	userID := uint(820301)
	tenantID := uint(1)

	cleanup := func() {
		db.Unscoped().Where("user_id = ?", userID).Delete(&model.ReferralLink{})
		db.Unscoped().Delete(&model.User{}, userID)
	}
	cleanup()
	t.Cleanup(cleanup)

	ensureTestTenantForCalc(db)
	seedUser(t, userID)

	refSvc := referral.NewReferralService(db)

	// 创建链接
	link, _ := refSvc.GetOrCreateLink(ctx, userID, tenantID)
	initialCount := link.RegisterCount

	// 增加注册计数 2 次
	for i := 0; i < 2; i++ {
		err := refSvc.IncrementRegisterCount(ctx, link.ID)
		if err != nil {
			t.Fatalf("IncrementRegisterCount failed: %v", err)
		}
	}

	// 重新加载验证
	var updated model.ReferralLink
	db.First(&updated, link.ID)
	expectedCount := initialCount + 2
	if updated.RegisterCount != expectedCount {
		t.Errorf("expected register_count %d, got %d", expectedCount, updated.RegisterCount)
	}

	t.Logf("RegisterCount incremented from %d to %d", initialCount, updated.RegisterCount)
}

// TestGetStats_EmptyState 测试无邀请数据时的统计
func TestGetStats_EmptyState(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}
	ctx := context.Background()

	userID := uint(820401)

	cleanup := func() {
		db.Unscoped().Where("user_id = ?", userID).Delete(&model.ReferralLink{})
		db.Unscoped().Delete(&model.User{}, userID)
	}
	cleanup()
	t.Cleanup(cleanup)

	refSvc := referral.NewReferralService(db)

	// 查询不存在的用户统计
	stats, err := refSvc.GetStats(ctx, userID)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}

	// 验证返回零值
	if stats.ClickCount != 0 {
		t.Errorf("expected click_count 0, got %d", stats.ClickCount)
	}
	if stats.RegisterCount != 0 {
		t.Errorf("expected register_count 0, got %d", stats.RegisterCount)
	}
	if stats.TotalCommission != 0 {
		t.Errorf("expected total_commission 0, got %f", stats.TotalCommission)
	}

	t.Log("GetStats correctly returns zero values for non-existent user")
}

// TestGetStats_WithData 测试有数据时的统计
func TestGetStats_WithData(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}
	ctx := context.Background()

	userID := uint(820501)
	tenantID := uint(1)

	cleanup := func() {
		db.Unscoped().Where("user_id = ?", userID).Delete(&model.ReferralLink{})
		db.Unscoped().Where("user_id = ?", userID).Delete(&model.CommissionRecord{})
		db.Unscoped().Delete(&model.User{}, userID)
	}
	cleanup()
	t.Cleanup(cleanup)

	ensureTestTenantForCalc(db)
	seedUser(t, userID)

	refSvc := referral.NewReferralService(db)

	// 创建链接并设置计数
	link, _ := refSvc.GetOrCreateLink(ctx, userID, tenantID)
	db.Model(link).Updates(map[string]interface{}{
		"click_count":    10,
		"register_count": 3,
	})

	// 创建佣金记录
	records := []model.CommissionRecord{
		{
			UserID:           userID,
			TenantID:         tenantID,
			CommissionAmount: 50000,
			Status:           "PENDING",
			Type:             "REFERRAL",
		},
		{
			UserID:           userID,
			TenantID:         tenantID,
			CommissionAmount: 100000,
			Status:           "SETTLED",
			Type:             "REFERRAL",
		},
		{
			UserID:           userID,
			TenantID:         tenantID,
			CommissionAmount: 80000,
			Status:           "WITHDRAWN",
			Type:             "REFERRAL",
		},
	}
	for _, rec := range records {
		db.Create(&rec)
	}

	// 查询统计
	stats, err := refSvc.GetStats(ctx, userID)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}

	// 验证统计数据
	if stats.ClickCount != 10 {
		t.Errorf("expected click_count 10, got %d", stats.ClickCount)
	}
	if stats.RegisterCount != 3 {
		t.Errorf("expected register_count 3, got %d", stats.RegisterCount)
	}
	if stats.PendingAmount != 50000 {
		t.Errorf("expected pending_amount 50000, got %f", stats.PendingAmount)
	}
	if stats.SettledAmount != 100000 {
		t.Errorf("expected settled_amount 100000, got %f", stats.SettledAmount)
	}
	if stats.WithdrawnAmount != 80000 {
		t.Errorf("expected withdrawn_amount 80000, got %f", stats.WithdrawnAmount)
	}
	expectedTotal := float64(50000 + 100000 + 80000)
	if stats.TotalCommission != expectedTotal {
		t.Errorf("expected total_commission %f, got %f", expectedTotal, stats.TotalCommission)
	}

	t.Logf("GetStats: clicks=%d, registers=%d, total=%.0f", stats.ClickCount, stats.RegisterCount, stats.TotalCommission)
}

// TestGetConfig_AutoCreate 测试配置自动创建
func TestGetConfig_AutoCreate(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}
	ctx := context.Background()

	// 先禁用所有活跃配置
	db.Model(&model.ReferralConfig{}).Where("is_active = ?", true).Update("is_active", false)

	refSvc := referral.NewReferralService(db)

	// 调用 GetConfig 应自动创建默认配置
	cfg, err := refSvc.GetConfig(ctx)
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}

	// 验证默认值
	if cfg.CommissionRate != 0.10 {
		t.Errorf("expected default commission_rate 0.10, got %f", cfg.CommissionRate)
	}
	if cfg.AttributionDays != 90 {
		t.Errorf("expected default attribution_days 90, got %d", cfg.AttributionDays)
	}
	if !cfg.IsActive {
		t.Error("expected is_active true")
	}

	t.Logf("GetConfig auto-created default config: rate=%f, days=%d", cfg.CommissionRate, cfg.AttributionDays)

	// 恢复：确保有活跃配置
	t.Cleanup(func() {
		var count int64
		db.Model(&model.ReferralConfig{}).Where("is_active = ?", true).Count(&count)
		if count == 0 {
			db.Model(&model.ReferralConfig{}).Where("id = ?", cfg.ID).Update("is_active", true)
		}
	})
}
