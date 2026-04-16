package referral_test

import (
	"context"
	"testing"
	"time"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/service/referral"
)

// TestReferralFlow_E2E_Complete 端到端完整流程测试
// 场景：邀请人A → 被邀者B注册 → B消费达标 → 解锁归因 → 产生佣金 → 佣金结算
func TestReferralFlow_E2E_Complete(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}
	ctx := context.Background()

	// 1. 准备测试用户
	inviterID := uint(800001)
	inviteeID := uint(800002)
	tenantID := uint(1)

	// 清理遗留数据
	cleanup := func() {
		db.Unscoped().Where("user_id IN (?, ?)", inviterID, inviteeID).Delete(&model.ReferralLink{})
		db.Unscoped().Where("user_id = ?", inviteeID).Delete(&model.ReferralAttribution{})
		db.Unscoped().Where("user_id IN (?, ?)", inviterID, inviteeID).Delete(&model.UserBalance{})
		db.Unscoped().Where("user_id = ?", inviterID).Delete(&model.CommissionRecord{})
		db.Unscoped().Delete(&model.User{}, inviterID)
		db.Unscoped().Delete(&model.User{}, inviteeID)
	}
	cleanup()
	t.Cleanup(cleanup)

	ensureTestTenantForCalc(db)
	seedUser(t, inviterID)
	seedUser(t, inviteeID)

	// 2. 邀请人生成邀请码
	refSvc := referral.NewReferralService(db)
	link, err := refSvc.GetOrCreateLink(ctx, inviterID, tenantID)
	if err != nil {
		t.Fatalf("GetOrCreateLink failed: %v", err)
	}
	if link.Code == "" {
		t.Fatal("expected non-empty referral code")
	}
	t.Logf("Step 1: Inviter generated code: %s", link.Code)

	// 3. 被邀者通过邀请码注册（模拟 auth handler 中的逻辑）
	err = referral.CreateReferralAttribution(ctx, db, &model.User{
		BaseModel: model.BaseModel{ID: inviteeID},
		TenantID:  tenantID,
	}, link.Code)
	if err != nil {
		t.Fatalf("CreateReferralAttribution failed: %v", err)
	}

	// 验证归因记录已创建且未解锁
	var attr model.ReferralAttribution
	if err := db.Where("user_id = ?", inviteeID).First(&attr).Error; err != nil {
		t.Fatalf("attribution not found: %v", err)
	}
	if attr.InviterID != inviterID {
		t.Errorf("expected inviter_id=%d, got %d", inviterID, attr.InviterID)
	}
	if attr.UnlockedAt != nil {
		t.Error("attribution should not be unlocked initially")
	}
	t.Logf("Step 2: Attribution created, unlocked_at=NULL")

	// 4. 被邀者消费未达标（50000 积分 < 100000 门槛）
	ub := model.UserBalance{
		UserID:        inviteeID,
		TenantID:      tenantID,
		Balance:       0,
		FreeQuota:     0,
		TotalConsumed: 50000,
	}
	if err := db.Create(&ub).Error; err != nil {
		t.Fatalf("create balance: %v", err)
	}

	referral.TryUnlockAttribution(ctx, db, inviteeID)

	db.First(&attr, attr.ID)
	if attr.UnlockedAt != nil {
		t.Error("attribution should remain locked below threshold")
	}
	t.Logf("Step 3: Consumed 50000 credits, still locked")

	// 5. 被邀者继续消费达标（累计 150000 积分 >= 100000 门槛）
	db.Model(&ub).Update("total_consumed", 150000)

	referral.TryUnlockAttribution(ctx, db, inviteeID)

	db.First(&attr, attr.ID)
	if attr.UnlockedAt == nil {
		t.Fatal("attribution should be unlocked after reaching threshold")
	}
	t.Logf("Step 4: Consumed 150000 credits, unlocked at %v", attr.UnlockedAt)

	// 6. 被邀者再次消费，触发佣金计算（订单 1,000,000 积分）
	db.Model(&ub).Update("total_consumed", 1150000)

	calc := referral.NewCommissionCalculator(db)
	calc.CalculateCommissions(ctx, inviteeID, tenantID, 1_000_000, "gpt-4o", 1)

	// 等待异步佣金写入（最多2秒）
	commRec := waitForCommission(t, inviterID, &attr.ID, 2*time.Second)
	if commRec == nil {
		t.Fatal("commission record not created")
	}

	// 验证佣金金额（1,000,000 × 10% = 100,000）
	expectedComm := int64(100_000)
	if commRec.CommissionAmount != expectedComm {
		t.Errorf("expected commission %d, got %d", expectedComm, commRec.CommissionAmount)
	}
	if commRec.Status != "PENDING" {
		t.Errorf("expected status PENDING, got %s", commRec.Status)
	}
	if !commRec.Credited {
		t.Error("commission should be credited to balance")
	}
	t.Logf("Step 5: Commission created, amount=%d, status=%s", commRec.CommissionAmount, commRec.Status)

	// 7. 验证邀请人余额增加
	var inviterBalance model.UserBalance
	if err := db.Where("user_id = ?", inviterID).First(&inviterBalance).Error; err != nil {
		t.Fatalf("inviter balance not found: %v", err)
	}
	if inviterBalance.Balance != expectedComm {
		t.Errorf("expected inviter balance %d, got %d", expectedComm, inviterBalance.Balance)
	}
	t.Logf("Step 6: Inviter balance updated to %d", inviterBalance.Balance)

	// 8. 模拟结算（PENDING → SETTLED）
	db.Model(commRec).Update("status", "SETTLED")

	var settled model.CommissionRecord
	db.First(&settled, commRec.ID)
	if settled.Status != "SETTLED" {
		t.Errorf("expected status SETTLED, got %s", settled.Status)
	}
	t.Logf("Step 7: Commission settled")

	// 9. 验证统计数据
	stats, err := refSvc.GetStats(ctx, inviterID)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}
	if stats.RegisterCount != 1 {
		t.Errorf("expected register_count=1, got %d", stats.RegisterCount)
	}
	if stats.SettledAmount != float64(expectedComm) {
		t.Errorf("expected settled_amount=%d, got %.0f", expectedComm, stats.SettledAmount)
	}
	t.Logf("Step 8: Stats verified, register_count=%d, settled_amount=%.0f", stats.RegisterCount, stats.SettledAmount)
}

// TestReferralFlow_AttributionExpiry 归因窗口过期测试
// 场景：被邀者注册90天后消费，归因已过期，不产生佣金
func TestReferralFlow_AttributionExpiry(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}
	ctx := context.Background()

	inviterID := uint(800101)
	inviteeID := uint(800102)

	cleanup := func() {
		db.Unscoped().Where("user_id = ?", inviteeID).Delete(&model.ReferralAttribution{})
		db.Unscoped().Where("user_id = ?", inviterID).Delete(&model.CommissionRecord{})
		db.Unscoped().Delete(&model.User{}, inviterID)
		db.Unscoped().Delete(&model.User{}, inviteeID)
	}
	cleanup()
	t.Cleanup(cleanup)

	ensureTestTenantForCalc(db)
	seedUser(t, inviterID)
	seedUser(t, inviteeID)

	// 创建已过期的归因（expires_at 设为昨天）
	yesterday := time.Now().AddDate(0, 0, -1)
	attr := model.ReferralAttribution{
		UserID:       inviteeID,
		InviterID:    inviterID,
		ReferralCode: "EXPIRED",
		AttributedAt: time.Now().AddDate(0, 0, -91),
		ExpiresAt:    yesterday,
		UnlockedAt:   &yesterday, // 已解锁
		IsValid:      true,
	}
	if err := db.Create(&attr).Error; err != nil {
		t.Fatalf("create attribution: %v", err)
	}

	// 触发佣金计算
	calc := referral.NewCommissionCalculator(db)
	calc.CalculateCommissions(ctx, inviteeID, 1, 1_000_000, "gpt-4o", 1)

	// 等待1秒，验证没有佣金记录生成
	time.Sleep(1 * time.Second)

	var count int64
	db.Model(&model.CommissionRecord{}).Where("user_id = ?", inviterID).Count(&count)
	if count != 0 {
		t.Errorf("expected no commission for expired attribution, got %d records", count)
	}

	// 验证归因已被标记为无效
	db.First(&attr, attr.ID)
	if attr.IsValid {
		t.Error("expired attribution should be marked invalid")
	}
	if attr.InvalidReason != "EXPIRED" {
		t.Errorf("expected invalid_reason=EXPIRED, got %s", attr.InvalidReason)
	}
	t.Logf("Attribution expired and marked invalid: %s", attr.InvalidReason)
}

// TestReferralFlow_LifetimeCapReached 终身上限测试
// 场景：邀请人从同一被邀者累计获得佣金达到终身上限后，不再产生新佣金
func TestReferralFlow_LifetimeCapReached(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}
	ctx := context.Background()

	inviterID := uint(800201)
	inviteeID := uint(800202)

	cleanup := func() {
		db.Unscoped().Where("user_id = ?", inviteeID).Delete(&model.ReferralAttribution{})
		db.Unscoped().Where("user_id = ?", inviterID).Delete(&model.CommissionRecord{})
		db.Unscoped().Delete(&model.User{}, inviterID)
		db.Unscoped().Delete(&model.User{}, inviteeID)
	}
	cleanup()
	t.Cleanup(cleanup)

	ensureTestTenantForCalc(db)
	seedUser(t, inviterID)
	seedUser(t, inviteeID)

	// 设置较低的终身上限（500,000 积分 = ¥50）
	cfgID := seedReferralConfig(t, 0.10, 500_000)
	t.Cleanup(func() { cleanupReferralConfig(t, cfgID) })

	// 创建已解锁的归因
	now := time.Now()
	attr := model.ReferralAttribution{
		UserID:       inviteeID,
		InviterID:    inviterID,
		ReferralCode: "CAPTEST",
		AttributedAt: now,
		ExpiresAt:    now.AddDate(0, 0, 90),
		UnlockedAt:   &now,
		IsValid:      true,
	}
	if err := db.Create(&attr).Error; err != nil {
		t.Fatalf("create attribution: %v", err)
	}

	// 第一笔消费：4,000,000 积分 × 10% = 400,000 佣金（未达上限）
	calc := referral.NewCommissionCalculator(db)
	calc.CalculateCommissions(ctx, inviteeID, 1, 4_000_000, "gpt-4o", 1)

	rec1 := waitForCommission(t, inviterID, &attr.ID, 2*time.Second)
	if rec1 == nil {
		t.Fatal("first commission not created")
	}
	if rec1.CommissionAmount != 400_000 {
		t.Errorf("expected first commission 400000, got %d", rec1.CommissionAmount)
	}
	t.Logf("First commission: %d (below cap)", rec1.CommissionAmount)

	// 第二笔消费：2,000,000 积分 × 10% = 200,000，但上限只剩 100,000
	calc.CalculateCommissions(ctx, inviteeID, 1, 2_000_000, "gpt-4o", 1)

	rec2 := waitForCommission(t, inviterID, &attr.ID, 2*time.Second)
	if rec2 == nil || rec2.ID == rec1.ID {
		t.Fatal("second commission not created")
	}
	if rec2.CommissionAmount != 100_000 {
		t.Errorf("expected partial commission 100000, got %d", rec2.CommissionAmount)
	}
	t.Logf("Second commission: %d (capped to remaining)", rec2.CommissionAmount)

	// 第三笔消费：应该不产生佣金（已达上限）
	calc.CalculateCommissions(ctx, inviteeID, 1, 1_000_000, "gpt-4o", 1)

	time.Sleep(1 * time.Second)
	var count int64
	db.Model(&model.CommissionRecord{}).Where("user_id = ? AND attribution_id = ?", inviterID, attr.ID).Count(&count)
	if count != 2 {
		t.Errorf("expected 2 commission records (cap reached), got %d", count)
	}
	t.Logf("Third order: no commission (lifetime cap reached)")
}

// TestReferralFlow_MultipleInvitees 多个被邀者测试
// 场景：邀请人A邀请B和C，两人分别消费，各自独立计算佣金和终身上限
func TestReferralFlow_MultipleInvitees(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}
	ctx := context.Background()

	inviterID := uint(800301)
	inviteeB := uint(800302)
	inviteeC := uint(800303)

	cleanup := func() {
		db.Unscoped().Where("user_id = ?", inviterID).Delete(&model.ReferralLink{})
		db.Unscoped().Where("user_id IN (?, ?)", inviteeB, inviteeC).Delete(&model.ReferralAttribution{})
		db.Unscoped().Where("user_id = ?", inviterID).Delete(&model.CommissionRecord{})
		db.Unscoped().Delete(&model.User{}, inviterID)
		db.Unscoped().Delete(&model.User{}, inviteeB)
		db.Unscoped().Delete(&model.User{}, inviteeC)
	}
	cleanup()
	t.Cleanup(cleanup)

	ensureTestTenantForCalc(db)
	seedUser(t, inviterID)
	seedUser(t, inviteeB)
	seedUser(t, inviteeC)

	// 邀请人生成邀请码
	refSvc := referral.NewReferralService(db)
	link, _ := refSvc.GetOrCreateLink(ctx, inviterID, 1)

	// B 和 C 分别通过邀请码注册
	referral.CreateReferralAttribution(ctx, db, &model.User{BaseModel: model.BaseModel{ID: inviteeB}, TenantID: 1}, link.Code)
	referral.CreateReferralAttribution(ctx, db, &model.User{BaseModel: model.BaseModel{ID: inviteeC}, TenantID: 1}, link.Code)

	// 解锁两个归因
	now := time.Now()
	db.Model(&model.ReferralAttribution{}).Where("user_id IN (?, ?)", inviteeB, inviteeC).Update("unlocked_at", now)

	var attrB, attrC model.ReferralAttribution
	db.Where("user_id = ?", inviteeB).First(&attrB)
	db.Where("user_id = ?", inviteeC).First(&attrC)

	// B 消费 5,000,000 积分 → 500,000 佣金
	calc := referral.NewCommissionCalculator(db)
	calc.CalculateCommissions(ctx, inviteeB, 1, 5_000_000, "gpt-4o", 1)

	recB := waitForCommission(t, inviterID, &attrB.ID, 2*time.Second)
	if recB == nil {
		t.Fatal("commission from B not created")
	}
	if recB.CommissionAmount != 500_000 {
		t.Errorf("expected commission from B = 500000, got %d", recB.CommissionAmount)
	}
	t.Logf("Commission from B: %d", recB.CommissionAmount)

	// C 消费 3,000,000 积分 → 300,000 佣金
	calc.CalculateCommissions(ctx, inviteeC, 1, 3_000_000, "gpt-4o", 1)

	recC := waitForCommission(t, inviterID, &attrC.ID, 2*time.Second)
	if recC == nil {
		t.Fatal("commission from C not created")
	}
	if recC.CommissionAmount != 300_000 {
		t.Errorf("expected commission from C = 300000, got %d", recC.CommissionAmount)
	}
	t.Logf("Commission from C: %d", recC.CommissionAmount)

	// 验证邀请人总佣金 = 800,000
	stats, _ := refSvc.GetStats(ctx, inviterID)
	if stats.TotalCommission != 800_000 {
		t.Errorf("expected total commission 800000, got %.0f", stats.TotalCommission)
	}
	if stats.RegisterCount != 2 {
		t.Errorf("expected register_count=2, got %d", stats.RegisterCount)
	}
	t.Logf("Total commission: %.0f, register_count: %d", stats.TotalCommission, stats.RegisterCount)
}

// TestReferralFlow_ConfigChange 配置动态变更测试
// 场景：管理员修改佣金率后，新订单按新费率计算
func TestReferralFlow_ConfigChange(t *testing.T) {
	db := openTestDB(t)
	if db == nil {
		return
	}
	ctx := context.Background()

	inviterID := uint(800401)
	inviteeID := uint(800402)

	cleanup := func() {
		db.Unscoped().Where("user_id = ?", inviteeID).Delete(&model.ReferralAttribution{})
		db.Unscoped().Where("user_id = ?", inviterID).Delete(&model.CommissionRecord{})
		db.Unscoped().Delete(&model.User{}, inviterID)
		db.Unscoped().Delete(&model.User{}, inviteeID)
	}
	cleanup()
	t.Cleanup(cleanup)

	ensureTestTenantForCalc(db)
	seedUser(t, inviterID)
	seedUser(t, inviteeID)

	// 初始配置：10% 佣金率
	cfgID := seedReferralConfig(t, 0.10, 30_000_000)
	t.Cleanup(func() { cleanupReferralConfig(t, cfgID) })

	// 创建已解锁的归因
	now := time.Now()
	attr := model.ReferralAttribution{
		UserID:       inviteeID,
		InviterID:    inviterID,
		ReferralCode: "CFGTEST",
		AttributedAt: now,
		ExpiresAt:    now.AddDate(0, 0, 90),
		UnlockedAt:   &now,
		IsValid:      true,
	}
	db.Create(&attr)

	// 第一笔订单：1,000,000 × 10% = 100,000
	calc := referral.NewCommissionCalculator(db)
	calc.CalculateCommissions(ctx, inviteeID, 1, 1_000_000, "gpt-4o", 1)

	rec1 := waitForCommission(t, inviterID, &attr.ID, 2*time.Second)
	if rec1 == nil {
		t.Fatal("first commission not created")
	}
	if rec1.CommissionAmount != 100_000 {
		t.Errorf("expected 100000 at 10%%, got %d", rec1.CommissionAmount)
	}
	if rec1.EffectiveRate != 0.10 {
		t.Errorf("expected effective_rate=0.10, got %.4f", rec1.EffectiveRate)
	}
	t.Logf("First order (10%%): commission=%d", rec1.CommissionAmount)

	// 管理员修改配置：佣金率改为 15%
	db.Model(&model.ReferralConfig{}).Where("id = ?", cfgID).Update("commission_rate", 0.15)

	// 第二笔订单：1,000,000 × 15% = 150,000
	calc.CalculateCommissions(ctx, inviteeID, 1, 1_000_000, "gpt-4o", 1)

	rec2 := waitForCommission(t, inviterID, &attr.ID, 2*time.Second)
	if rec2 == nil || rec2.ID == rec1.ID {
		t.Fatal("second commission not created")
	}
	if rec2.CommissionAmount != 150_000 {
		t.Errorf("expected 150000 at 15%%, got %d", rec2.CommissionAmount)
	}
	if rec2.EffectiveRate != 0.15 {
		t.Errorf("expected effective_rate=0.15, got %.4f", rec2.EffectiveRate)
	}
	t.Logf("Second order (15%%): commission=%d", rec2.CommissionAmount)
}
