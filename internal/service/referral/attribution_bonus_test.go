package referral_test

import (
	"context"
	"testing"
	"time"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/service/referral"
)

// TestTryGrantInviteeBonus_Success 消费达标 + 未发放时应发放奖励
func TestTryGrantInviteeBonus_Success(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}
	ctx := context.Background()

	// 确保配置存在并 bonus > 0
	var qc model.QuotaConfig
	if err := db.Where("is_active = ?", true).First(&qc).Error; err != nil {
		qc = model.QuotaConfig{InviteeBonus: 5000, InviteeUnlockCredits: 10000, IsActive: true}
		db.Create(&qc)
		defer db.Unscoped().Delete(&qc)
	} else {
		origBonus := qc.InviteeBonus
		origUnlock := qc.InviteeUnlockCredits
		db.Model(&qc).Updates(map[string]interface{}{"invitee_bonus": 5000, "invitee_unlock_credits": 10000})
		defer db.Model(&qc).Updates(map[string]interface{}{
			"invitee_bonus":          origBonus,
			"invitee_unlock_credits": origUnlock,
		})
	}

	userID := uint(900401)

	db.Unscoped().Where("user_id = ?", userID).Delete(&model.ReferralAttribution{})
	db.Unscoped().Where("user_id = ?", userID).Delete(&model.UserBalance{})
	db.Where("user_id = ? AND type = ?", userID, "INVITEE_BONUS").Delete(&model.BalanceRecord{})

	attr := model.ReferralAttribution{
		UserID: userID, InviterID: 900402, ReferralCode: "BNS1",
		AttributedAt: time.Now(), ExpiresAt: time.Now().AddDate(0, 0, 90),
		IsValid: true,
	}
	db.Create(&attr)
	defer db.Unscoped().Delete(&attr)

	// 先创建 users 行（user_balances → users FK 约束）
	seedUser(t, userID)
	ub := model.UserBalance{
		UserID: userID, TenantID: 1, FreeQuota: 0, TotalConsumed: 20000,
	}
	db.Create(&ub)
	defer db.Unscoped().Delete(&ub)

	referral.TryGrantInviteeBonus(ctx, db, userID)

	var after model.ReferralAttribution
	db.First(&after, attr.ID)
	if !after.InviteeBonusGranted {
		t.Fatal("invitee_bonus_granted should be true after threshold met")
	}
	if after.InviteeBonusAt == nil {
		t.Fatal("invitee_bonus_at should be set")
	}

	var afterUB model.UserBalance
	db.First(&afterUB, ub.ID)
	if afterUB.FreeQuota != 5000 {
		t.Errorf("FreeQuota = %d, want 5000", afterUB.FreeQuota)
	}

	// 二次调用应幂等
	referral.TryGrantInviteeBonus(ctx, db, userID)
	var afterUB2 model.UserBalance
	db.First(&afterUB2, ub.ID)
	if afterUB2.FreeQuota != 5000 {
		t.Errorf("second call not idempotent: FreeQuota = %d", afterUB2.FreeQuota)
	}
}

// TestTryGrantInviteeBonus_BelowThreshold 未达门槛不发放
func TestTryGrantInviteeBonus_BelowThreshold(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}
	ctx := context.Background()

	var qc model.QuotaConfig
	if err := db.Where("is_active = ?", true).First(&qc).Error; err != nil {
		qc = model.QuotaConfig{InviteeBonus: 5000, InviteeUnlockCredits: 10000, IsActive: true}
		db.Create(&qc)
		defer db.Unscoped().Delete(&qc)
	} else {
		orig := qc.InviteeUnlockCredits
		db.Model(&qc).Updates(map[string]interface{}{"invitee_bonus": 5000, "invitee_unlock_credits": 10000})
		defer db.Model(&qc).Update("invitee_unlock_credits", orig)
	}

	userID := uint(900501)
	db.Unscoped().Where("user_id = ?", userID).Delete(&model.ReferralAttribution{})
	db.Unscoped().Where("user_id = ?", userID).Delete(&model.UserBalance{})

	attr := model.ReferralAttribution{
		UserID: userID, InviterID: 900502, ReferralCode: "BNS2",
		AttributedAt: time.Now(), ExpiresAt: time.Now().AddDate(0, 0, 90), IsValid: true,
	}
	db.Create(&attr)
	defer db.Unscoped().Delete(&attr)

	// 先创建 users 行（user_balances → users FK 约束）
	seedUser(t, userID)
	ub := model.UserBalance{UserID: userID, TenantID: 1, TotalConsumed: 5000} // 低于门槛
	db.Create(&ub)
	defer db.Unscoped().Delete(&ub)

	referral.TryGrantInviteeBonus(ctx, db, userID)

	var after model.ReferralAttribution
	db.First(&after, attr.ID)
	if after.InviteeBonusGranted {
		t.Error("should not grant bonus below threshold")
	}
}
