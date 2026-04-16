package referral_test

import (
	"context"
	"os"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/model"
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

// TestTryUnlockAttribution_BelowThreshold 消费未达门槛时不解锁
func TestTryUnlockAttribution_BelowThreshold(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}
	ctx := context.Background()

	// 使用特殊 userID 隔离
	userID := uint(900101)
	inviterID := uint(900102)

	// 清理遗留数据(Unscoped 硬删,避免软删 + unique index 冲突)
	db.Unscoped().Where("user_id = ?", userID).Delete(&model.ReferralAttribution{})
	db.Unscoped().Where("user_id = ?", userID).Delete(&model.UserBalance{})

	// 创建归因快照(未解锁)
	attr := model.ReferralAttribution{
		UserID:       userID,
		InviterID:    inviterID,
		ReferralCode: "TESTCODE",
		AttributedAt: time.Now(),
		ExpiresAt:    time.Now().AddDate(0, 0, 90),
		UnlockedAt:   nil,
		IsValid:      true,
	}
	if err := db.Create(&attr).Error; err != nil {
		t.Fatalf("create attribution: %v", err)
	}
	defer db.Unscoped().Delete(&attr)

	// 先创建 users 行（user_balances → users FK 约束）
	seedUser(t, userID)
	// 创建余额,消费 50000 (远低于默认门槛 100000)
	ub := model.UserBalance{
		UserID:        userID,
		TenantID:      1,
		Balance:       0,
		TotalConsumed: 50000,
	}
	if err := db.Create(&ub).Error; err != nil {
		t.Fatalf("create balance: %v", err)
	}
	defer db.Unscoped().Delete(&ub)

	// 调用解锁逻辑
	referral.TryUnlockAttribution(ctx, db, userID)

	// 验证 unlocked_at 仍为 NULL
	var after model.ReferralAttribution
	if err := db.First(&after, attr.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.UnlockedAt != nil {
		t.Errorf("unlocked_at should remain NULL below threshold, got %v", after.UnlockedAt)
	}
}

// TestTryUnlockAttribution_AboveThreshold 消费达标后解锁
func TestTryUnlockAttribution_AboveThreshold(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}
	ctx := context.Background()

	userID := uint(900201)
	inviterID := uint(900202)

	db.Unscoped().Where("user_id = ?", userID).Delete(&model.ReferralAttribution{})
	db.Unscoped().Where("user_id = ?", userID).Delete(&model.UserBalance{})

	attr := model.ReferralAttribution{
		UserID:       userID,
		InviterID:    inviterID,
		ReferralCode: "TESTCODE2",
		AttributedAt: time.Now(),
		ExpiresAt:    time.Now().AddDate(0, 0, 90),
		UnlockedAt:   nil,
		IsValid:      true,
	}
	if err := db.Create(&attr).Error; err != nil {
		t.Fatalf("create attribution: %v", err)
	}
	defer db.Unscoped().Delete(&attr)

	// 先创建 users 行（user_balances → users FK 约束）
	seedUser(t, userID)
	// 消费 150000 (超过默认门槛 100000)
	ub := model.UserBalance{
		UserID:        userID,
		TenantID:      1,
		Balance:       0,
		TotalConsumed: 150000,
	}
	if err := db.Create(&ub).Error; err != nil {
		t.Fatalf("create balance: %v", err)
	}
	defer db.Unscoped().Delete(&ub)

	referral.TryUnlockAttribution(ctx, db, userID)

	var after model.ReferralAttribution
	if err := db.First(&after, attr.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.UnlockedAt == nil {
		t.Errorf("unlocked_at should be set after crossing threshold")
	}
}

// TestTryUnlockAttribution_Idempotent 已解锁的归因重复调用不覆盖时间
func TestTryUnlockAttribution_Idempotent(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}
	ctx := context.Background()

	userID := uint(900301)
	inviterID := uint(900302)

	db.Unscoped().Where("user_id = ?", userID).Delete(&model.ReferralAttribution{})
	db.Unscoped().Where("user_id = ?", userID).Delete(&model.UserBalance{})

	pastTime := time.Now().Add(-24 * time.Hour)
	attr := model.ReferralAttribution{
		UserID:       userID,
		InviterID:    inviterID,
		ReferralCode: "TESTCODE3",
		AttributedAt: time.Now(),
		ExpiresAt:    time.Now().AddDate(0, 0, 90),
		UnlockedAt:   &pastTime,
		IsValid:      true,
	}
	if err := db.Create(&attr).Error; err != nil {
		t.Fatalf("create attribution: %v", err)
	}
	defer db.Unscoped().Delete(&attr)

	// 先创建 users 行（user_balances → users FK 约束）
	seedUser(t, userID)
	ub := model.UserBalance{
		UserID:        userID,
		TenantID:      1,
		Balance:       0,
		TotalConsumed: 500000,
	}
	if err := db.Create(&ub).Error; err != nil {
		t.Fatalf("create balance: %v", err)
	}
	defer db.Unscoped().Delete(&ub)

	referral.TryUnlockAttribution(ctx, db, userID)

	var after model.ReferralAttribution
	if err := db.First(&after, attr.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.UnlockedAt == nil {
		t.Fatal("should not lose existing unlocked_at")
	}
	// 时间差应小于 1 秒(幂等性保证:未被覆盖)
	if after.UnlockedAt.Unix() != pastTime.Unix() {
		t.Errorf("unlocked_at was overwritten: got %v, want %v", after.UnlockedAt, pastTime)
	}
}
