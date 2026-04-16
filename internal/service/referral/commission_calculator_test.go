package referral_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/service/referral"
)

// TestMain 初始化 logger（commission_calculator 依赖 logger.L != nil 才会写入佣金记录）
func TestMain(m *testing.M) {
	// 使用 nop logger 避免写文件，但让 logger.L 非 nil 以通过守卫检查
	logger.L = zap.NewNop()
	os.Exit(m.Run())
}

// ---- 辅助函数 ----

// openTestDBForCalc 打开测试数据库连接（与 attribution_unlock_test.go 中 openTestDB 同名，但此文件中重新声明）
// 注意：attribution_unlock_test.go 也在 package referral_test 中，openTestDB 已定义
// 因此本文件直接使用 openTestDB

// seedReferralConfig 写入测试用 ReferralConfig 并返回记录 ID
func seedReferralConfig(t *testing.T, rate float64, lifetimeCap int64) uint {
	t.Helper()
	db := openTestDB(t)
	cfg := model.ReferralConfig{
		CommissionRate:       rate,
		AttributionDays:      90,
		LifetimeCapCredits:   lifetimeCap,
		MinPaidCreditsUnlock: 100000,
		MinWithdrawAmount:    1000000,
		SettleDays:           7,
		IsActive:             true,
	}
	// 先把所有活跃配置置为非活跃，再插入新的
	db.Model(&model.ReferralConfig{}).Where("is_active = ?", true).Update("is_active", false)
	if err := db.Create(&cfg).Error; err != nil {
		t.Fatalf("seedReferralConfig: %v", err)
	}
	return cfg.ID
}

// cleanupReferralConfig 恢复活跃配置（测试结束后回滚到默认 10%）
func cleanupReferralConfig(t *testing.T, id uint) {
	t.Helper()
	db := openTestDB(t)
	db.Unscoped().Delete(&model.ReferralConfig{}, id)
	// 确保至少有一条活跃配置
	var count int64
	db.Model(&model.ReferralConfig{}).Where("is_active = ?", true).Count(&count)
	if count == 0 {
		cfg := model.ReferralConfig{
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
}

// ensureTestTenantForCalc 确保 tenants 表中存在 ID=1 的测试租户（users FK 依赖）
func ensureTestTenantForCalc(db *gorm.DB) {
	db.Exec("INSERT INTO tenants (id, name, level, is_active, created_at, updated_at) VALUES (1, 'TestTenant', 1, 1, NOW(), NOW()) ON DUPLICATE KEY UPDATE id=id")
}

// seedUser 创建最小化 User 记录（供 CalculateCommissions 查询邀请人使用）
func seedUser(t *testing.T, userID uint) {
	t.Helper()
	db := openTestDB(t)
	// 先确保租户存在（users.tenant_id → tenants.id FK）
	ensureTestTenantForCalc(db)
	email := fmt.Sprintf("calc_test_%d_%d@test.local", userID, time.Now().UnixNano()%1000000)
	db.Exec(
		"INSERT INTO users (id, tenant_id, email, password_hash, name, role, is_active, created_at, updated_at) "+
			"VALUES (?,1,?,?,?,?,1,NOW(),NOW()) ON DUPLICATE KEY UPDATE id=id",
		userID, email, "$2a$10$dummy", "CalcTestUser", "USER",
	)
}

// seedAttribution 写入 ReferralAttribution
func seedAttribution(t *testing.T, userID, inviterID uint, unlockedAt *time.Time, expiresAt time.Time, isValid bool) uint {
	t.Helper()
	db := openTestDB(t)
	db.Unscoped().Where("user_id = ?", userID).Delete(&model.ReferralAttribution{})
	attr := model.ReferralAttribution{
		UserID:       userID,
		InviterID:    inviterID,
		ReferralCode: "CALCTEST",
		AttributedAt: time.Now(),
		ExpiresAt:    expiresAt,
		UnlockedAt:   unlockedAt,
		IsValid:      isValid,
	}
	if err := db.Create(&attr).Error; err != nil {
		t.Fatalf("seedAttribution: %v", err)
	}
	return attr.ID
}

// waitForCommission 轮询等待佣金记录写入（CalculateCommissions 是同步的，但避免竞争）
func waitForCommission(t *testing.T, inviterID uint, attrID *uint, timeout time.Duration) *model.CommissionRecord {
	t.Helper()
	db := openTestDB(t)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var rec model.CommissionRecord
		q := db.Where("user_id = ?", inviterID)
		if attrID != nil {
			q = q.Where("attribution_id = ?", *attrID)
		}
		if err := q.Order("id DESC").First(&rec).Error; err == nil {
			return &rec
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil
}

// ============================================================
// TestCalculateCommissions_DefaultRate — 无 override，使用默认 10% 佣金
// ============================================================
func TestCalculateCommissions_DefaultRate(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}
	ctx := context.Background()

	inviterID := uint(910001)
	sourceUserID := uint(910002)

	// 准备数据
	cfgID := seedReferralConfig(t, 0.10, 0)
	t.Cleanup(func() { cleanupReferralConfig(t, cfgID) })

	seedUser(t, inviterID)
	now := time.Now()
	attrID := seedAttribution(t, sourceUserID, inviterID, &now, now.AddDate(0, 0, 90), true)

	t.Cleanup(func() {
		db.Unscoped().Where("user_id = ?", sourceUserID).Delete(&model.ReferralAttribution{})
		db.Unscoped().Where("user_id = ?", inviterID).Delete(&model.CommissionRecord{})
		db.Unscoped().Delete(&model.User{}, inviterID)
	})

	calc := referral.NewCommissionCalculator(db)
	const orderCredits int64 = 500_000 // ¥50

	calc.CalculateCommissions(ctx, sourceUserID, 1, orderCredits, "gpt-4o", 1)

	// 等待写入
	rec := waitForCommission(t, inviterID, &attrID, 2*time.Second)
	if rec == nil {
		t.Fatal("expected commission record to be created, got nil")
	}

	expectedComm := int64(float64(orderCredits) * 0.10)
	if rec.CommissionAmount != expectedComm {
		t.Errorf("expected commission %d (10%%), got %d", expectedComm, rec.CommissionAmount)
	}
	if rec.EffectiveRate != 0.10 {
		t.Errorf("expected effective_rate=0.10, got %.4f", rec.EffectiveRate)
	}
	if rec.Type != "REFERRAL" {
		t.Errorf("expected type REFERRAL (no override), got %s", rec.Type)
	}
	t.Logf("DefaultRate: comm=%d, rate=%.2f", rec.CommissionAmount, rec.EffectiveRate)
}

// ============================================================
// TestCalculateCommissions_WithOverride — 活跃 override=25%，佣金 = 0.25 × amount
// ============================================================
func TestCalculateCommissions_WithOverride(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}
	ctx := context.Background()

	inviterID := uint(910011)
	sourceUserID := uint(910012)

	cfgID := seedReferralConfig(t, 0.10, 0)
	t.Cleanup(func() { cleanupReferralConfig(t, cfgID) })

	seedUser(t, inviterID)
	now := time.Now()
	attrID := seedAttribution(t, sourceUserID, inviterID, &now, now.AddDate(0, 0, 90), true)

	// 创建活跃 override，rate=0.25
	activeTrue := true
	override := model.UserCommissionOverride{
		UserID:         inviterID,
		IsActive:       &activeTrue,
		CommissionRate: 0.25,
		EffectiveFrom:  now.Add(-time.Hour),
		EffectiveTo:    nil,
		Note:           "KOL 合作",
		CreatedBy:      1,
	}
	db.Create(&override)

	t.Cleanup(func() {
		db.Unscoped().Where("user_id = ?", sourceUserID).Delete(&model.ReferralAttribution{})
		db.Unscoped().Where("user_id = ?", inviterID).Delete(&model.CommissionRecord{})
		db.Unscoped().Delete(&model.User{}, inviterID)
		db.Unscoped().Delete(&model.UserCommissionOverride{}, override.ID)
	})

	calc := referral.NewCommissionCalculator(db)
	const orderCredits int64 = 1_000_000 // ¥100

	calc.CalculateCommissions(ctx, sourceUserID, 1, orderCredits, "claude-3-5-sonnet", 2)

	rec := waitForCommission(t, inviterID, &attrID, 2*time.Second)
	if rec == nil {
		t.Fatal("expected commission record for override case, got nil")
	}

	expectedComm := int64(float64(orderCredits) * 0.25)
	if rec.CommissionAmount != expectedComm {
		t.Errorf("expected commission %d (25%%), got %d", expectedComm, rec.CommissionAmount)
	}
	if rec.EffectiveRate != 0.25 {
		t.Errorf("expected effective_rate=0.25, got %.4f", rec.EffectiveRate)
	}
	if rec.Type != "REFERRAL_OVERRIDE" {
		t.Errorf("expected type REFERRAL_OVERRIDE, got %s", rec.Type)
	}
	if rec.OverrideID == nil {
		t.Error("expected override_id to be set, got nil")
	}
	t.Logf("WithOverride: comm=%d, rate=%.2f, type=%s", rec.CommissionAmount, rec.EffectiveRate, rec.Type)
}

// ============================================================
// TestCalculateCommissions_OverrideExpired — EffectiveTo < now 退回默认 10%
// ============================================================
func TestCalculateCommissions_OverrideExpired(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}
	ctx := context.Background()

	inviterID := uint(910021)
	sourceUserID := uint(910022)

	cfgID := seedReferralConfig(t, 0.10, 0)
	t.Cleanup(func() { cleanupReferralConfig(t, cfgID) })

	seedUser(t, inviterID)
	now := time.Now()
	attrID := seedAttribution(t, sourceUserID, inviterID, &now, now.AddDate(0, 0, 90), true)

	// 创建已过期 override（EffectiveTo = 1 天前）
	activeTrue := true
	expiredTo := now.Add(-24 * time.Hour)
	override := model.UserCommissionOverride{
		UserID:         inviterID,
		IsActive:       &activeTrue,
		CommissionRate: 0.50, // 高比例，但已过期
		EffectiveFrom:  now.Add(-48 * time.Hour),
		EffectiveTo:    &expiredTo,
		Note:           "已过期",
		CreatedBy:      1,
	}
	db.Create(&override)

	t.Cleanup(func() {
		db.Unscoped().Where("user_id = ?", sourceUserID).Delete(&model.ReferralAttribution{})
		db.Unscoped().Where("user_id = ?", inviterID).Delete(&model.CommissionRecord{})
		db.Unscoped().Delete(&model.User{}, inviterID)
		db.Unscoped().Delete(&model.UserCommissionOverride{}, override.ID)
	})

	calc := referral.NewCommissionCalculator(db)
	const orderCredits int64 = 1_000_000

	calc.CalculateCommissions(ctx, sourceUserID, 1, orderCredits, "gpt-4o-mini", 1)

	rec := waitForCommission(t, inviterID, &attrID, 2*time.Second)
	if rec == nil {
		t.Fatal("expected commission record (fallback to default), got nil")
	}

	// 应使用默认 10%，而非过期 override 的 50%
	expectedComm := int64(float64(orderCredits) * 0.10)
	if rec.CommissionAmount != expectedComm {
		t.Errorf("expected commission %d (10%% default), got %d (rate might be %.4f)",
			expectedComm, rec.CommissionAmount, rec.EffectiveRate)
	}
	t.Logf("OverrideExpired: used rate=%.4f, comm=%d", rec.EffectiveRate, rec.CommissionAmount)
}

// ============================================================
// TestCalculateCommissions_AttributionNotUnlocked — UnlockedAt=nil 应跳过佣金
// ============================================================
func TestCalculateCommissions_AttributionNotUnlocked(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}
	ctx := context.Background()

	inviterID := uint(910031)
	sourceUserID := uint(910032)

	cfgID := seedReferralConfig(t, 0.10, 0)
	t.Cleanup(func() { cleanupReferralConfig(t, cfgID) })

	seedUser(t, inviterID)
	now := time.Now()
	attrID := seedAttribution(t, sourceUserID, inviterID, nil /* 未解锁 */, now.AddDate(0, 0, 90), true)

	t.Cleanup(func() {
		db.Unscoped().Where("user_id = ?", sourceUserID).Delete(&model.ReferralAttribution{})
		db.Unscoped().Where("user_id = ?", inviterID).Delete(&model.CommissionRecord{})
		db.Unscoped().Delete(&model.User{}, inviterID)
	})

	calc := referral.NewCommissionCalculator(db)
	calc.CalculateCommissions(ctx, sourceUserID, 1, 500_000, "gpt-4o", 1)

	// 等待并期望 没有 佣金记录写入
	time.Sleep(200 * time.Millisecond)
	var count int64
	db.Model(&model.CommissionRecord{}).
		Where("user_id = ? AND attribution_id = ?", inviterID, attrID).
		Count(&count)
	if count > 0 {
		t.Errorf("expected NO commission record for unlocked=nil attribution, got %d records", count)
	}
	t.Log("AttributionNotUnlocked: correctly no commission created")
}

// ============================================================
// TestCalculateCommissions_AttributionExpired — ExpiresAt < now 应跳过
// ============================================================
func TestCalculateCommissions_AttributionExpired(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}
	ctx := context.Background()

	inviterID := uint(910041)
	sourceUserID := uint(910042)

	cfgID := seedReferralConfig(t, 0.10, 0)
	t.Cleanup(func() { cleanupReferralConfig(t, cfgID) })

	seedUser(t, inviterID)
	now := time.Now()
	pastUnlock := now.Add(-30 * 24 * time.Hour)
	pastExpiry := now.Add(-24 * time.Hour) // 已过期

	attrID := seedAttribution(t, sourceUserID, inviterID, &pastUnlock, pastExpiry, true)

	t.Cleanup(func() {
		db.Unscoped().Where("user_id = ?", sourceUserID).Delete(&model.ReferralAttribution{})
		db.Unscoped().Where("user_id = ?", inviterID).Delete(&model.CommissionRecord{})
		db.Unscoped().Delete(&model.User{}, inviterID)
	})

	calc := referral.NewCommissionCalculator(db)
	calc.CalculateCommissions(ctx, sourceUserID, 1, 500_000, "gpt-4o", 1)

	time.Sleep(200 * time.Millisecond)
	var count int64
	db.Model(&model.CommissionRecord{}).
		Where("user_id = ? AND attribution_id = ?", inviterID, attrID).
		Count(&count)
	if count > 0 {
		t.Errorf("expected NO commission for expired attribution, got %d records", count)
	}
	t.Log("AttributionExpired: correctly no commission created")
}

// ============================================================
// TestCalculateCommissions_LifetimeCapReached — 已达终身上限，不再发放
// ============================================================
func TestCalculateCommissions_LifetimeCapReached(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}
	ctx := context.Background()

	inviterID := uint(910051)
	sourceUserID := uint(910052)

	const lifetimeCap int64 = 100_000 // 较小上限，便于测试
	cfgID := seedReferralConfig(t, 0.10, lifetimeCap)
	t.Cleanup(func() { cleanupReferralConfig(t, cfgID) })

	seedUser(t, inviterID)
	now := time.Now()
	attrID := seedAttribution(t, sourceUserID, inviterID, &now, now.AddDate(0, 0, 90), true)

	// 预先写入一条已达到上限的 SETTLED 佣金记录
	capRec := model.CommissionRecord{
		TenantID:         1,
		UserID:           inviterID,
		SourceUserID:     sourceUserID,
		SourceTenantID:   1,
		OrderAmount:      1_000_000,
		CommissionAmount: lifetimeCap, // 精确等于上限
		CommissionRate:   0.10,
		EffectiveRate:    0.10,
		Type:             "REFERRAL",
		Status:           "SETTLED",
		AttributionID:    &attrID,
		Credited:         true,
	}
	db.Create(&capRec)

	t.Cleanup(func() {
		db.Unscoped().Where("user_id = ?", sourceUserID).Delete(&model.ReferralAttribution{})
		db.Unscoped().Where("user_id = ?", inviterID).Delete(&model.CommissionRecord{})
		db.Unscoped().Delete(&model.User{}, inviterID)
	})

	calc := referral.NewCommissionCalculator(db)
	calc.CalculateCommissions(ctx, sourceUserID, 1, 500_000, "gpt-4o", 1)

	time.Sleep(200 * time.Millisecond)
	// 上限已达，不应有新记录
	var count int64
	db.Model(&model.CommissionRecord{}).
		Where("user_id = ? AND attribution_id = ? AND status = ?", inviterID, attrID, "PENDING").
		Count(&count)
	if count > 0 {
		t.Errorf("expected no new commission when cap reached, got %d records", count)
	}
	t.Log("LifetimeCapReached: correctly no new commission created")
}

// ============================================================
// TestCalculateCommissions_LifetimeCapPartial — 佣金超出上限，仅发放剩余额度
// ============================================================
func TestCalculateCommissions_LifetimeCapPartial(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}
	ctx := context.Background()

	inviterID := uint(910061)
	sourceUserID := uint(910062)

	const lifetimeCap int64 = 100_000
	cfgID := seedReferralConfig(t, 0.10, lifetimeCap)
	t.Cleanup(func() { cleanupReferralConfig(t, cfgID) })

	seedUser(t, inviterID)
	now := time.Now()
	attrID := seedAttribution(t, sourceUserID, inviterID, &now, now.AddDate(0, 0, 90), true)

	// 已赚了 90,000 积分（还剩 10,000 可发）
	const earned int64 = 90_000
	existingRec := model.CommissionRecord{
		TenantID:         1,
		UserID:           inviterID,
		SourceUserID:     sourceUserID,
		SourceTenantID:   1,
		OrderAmount:      900_000,
		CommissionAmount: earned,
		CommissionRate:   0.10,
		EffectiveRate:    0.10,
		Type:             "REFERRAL",
		Status:           "SETTLED",
		AttributionID:    &attrID,
		Credited:         true,
	}
	db.Create(&existingRec)

	t.Cleanup(func() {
		db.Unscoped().Where("user_id = ?", sourceUserID).Delete(&model.ReferralAttribution{})
		db.Unscoped().Where("user_id = ?", inviterID).Delete(&model.CommissionRecord{})
		db.Unscoped().Delete(&model.User{}, inviterID)
	})

	calc := referral.NewCommissionCalculator(db)
	// 订单 1,000,000 × 10% = 100,000，但上限只剩 10,000
	calc.CalculateCommissions(ctx, sourceUserID, 1, 1_000_000, "gpt-4o", 1)

	rec := waitForCommission(t, inviterID, &attrID, 2*time.Second)
	if rec == nil || rec.ID == existingRec.ID {
		t.Fatal("expected a new (partial) commission record, got nil")
	}

	const expected int64 = lifetimeCap - earned // 10,000
	if rec.CommissionAmount != expected {
		t.Errorf("expected partial commission %d, got %d", expected, rec.CommissionAmount)
	}
	t.Logf("LifetimeCapPartial: partial comm=%d (remaining cap)", rec.CommissionAmount)
}
