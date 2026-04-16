package withdrawal_test

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
	"tokenhub-server/internal/service/balance"
	"tokenhub-server/internal/service/withdrawal"
)

// ---- 测试数据库连接 ----

var testDB *gorm.DB

func TestMain(m *testing.M) {
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		dsn = "root:tokenhubhk_pass@tcp(localhost:3306)/tokenhubhk?charset=utf8mb4&parseTime=True&loc=Local"
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		os.Exit(0) // 数据库不可用时跳过所有测试
	}
	testDB = db

	// 自动迁移测试所需的表（顺序：先父表后子表）
	_ = testDB.AutoMigrate(
		&model.Tenant{},
		&model.User{},
		&model.UserBalance{},
		&model.BalanceRecord{},
		&model.WithdrawalRequest{},
		&model.CommissionRecord{},
		&model.ReferralConfig{},
		&model.ReferralAttribution{},
	)

	// 确保租户 ID=1 存在（user_balances → users → tenants FK 链）
	ensureTestTenant(db)

	// 确保有一条活跃的 ReferralConfig（为了提现门槛测试）
	ensureReferralConfig(db)

	code := m.Run()
	os.Exit(code)
}

// ensureTestTenant 确保 tenants 表中存在 ID=1 的测试租户
func ensureTestTenant(db *gorm.DB) {
	var count int64
	db.Model(&model.Tenant{}).Where("id = ?", 1).Count(&count)
	if count == 0 {
		db.Exec("INSERT INTO tenants (id, name, level, is_active, created_at, updated_at) VALUES (1, 'TestTenant', 1, 1, NOW(), NOW()) ON DUPLICATE KEY UPDATE id=id")
	}
}

// ensureReferralConfig 保证数据库中有一条活跃的 ReferralConfig
func ensureReferralConfig(db *gorm.DB) {
	var count int64
	db.Model(&model.ReferralConfig{}).Where("is_active = ?", true).Count(&count)
	if count == 0 {
		cfg := model.ReferralConfig{
			CommissionRate:       0.10,
			AttributionDays:      90,
			LifetimeCapCredits:   30000000,
			MinPaidCreditsUnlock: 100000,
			MinWithdrawAmount:    1000000, // 最低提现 ¥100
			SettleDays:           7,
			IsActive:             true,
		}
		db.Create(&cfg)
	}
}

// newSvc 创建提现服务实例（使用真实 DB，无 Redis）
func newSvc(t *testing.T) *withdrawal.Service {
	t.Helper()
	if testDB == nil {
		t.Skip("database not available")
	}
	balanceSvc := balance.NewBalanceService(testDB, nil)
	return withdrawal.NewService(testDB, balanceSvc)
}

// seedUser 在 users 表中为测试用户创建最小化记录（解决 user_balances → users FK 约束）
func seedUser(t *testing.T, userID uint) {
	t.Helper()
	email := fmt.Sprintf("wd_test_%d_%d@test.local", userID, time.Now().UnixNano()%1000000)
	testDB.Exec(
		"INSERT INTO users (id, tenant_id, email, password_hash, name, role, is_active, created_at, updated_at) "+
			"VALUES (?,1,?,?,?,?,1,NOW(),NOW()) ON DUPLICATE KEY UPDATE id=id",
		userID, email, "$2a$10$dummy", "WdTestUser", "USER",
	)
}

// seedBalance 为测试用户初始化余额（先确保 users 行存在）
func seedBalance(t *testing.T, userID uint, amount int64) {
	t.Helper()
	seedUser(t, userID)
	testDB.Unscoped().Where("user_id = ?", userID).Delete(&model.UserBalance{})
	ub := model.UserBalance{
		TenantID:   1,
		UserID:     userID,
		Balance:    amount,
		BalanceRMB: credits.CreditsToRMB(amount),
	}
	if err := testDB.Create(&ub).Error; err != nil {
		t.Fatalf("seedBalance: %v", err)
	}
}

// uidCounter 是单调递增的原子计数器，保证每次调用 nextUID 都返回唯一 ID
var uidCounter uint64

// nextUID 使用原子递增计数器生成唯一测试用户 ID（范围 800001 起），
// 避免同一毫秒内多个测试并发调用时出现 ID 碰撞。
func nextUID() uint {
	seq := atomic.AddUint64(&uidCounter, 1)
	return uint(800000 + seq)
}

// ============================================================
// TestCreateWithdrawal_Success — 余额充足，提现成功，余额被冻结
// ============================================================
func TestCreateWithdrawal_Success(t *testing.T) {
	svc := newSvc(t)
	ctx := context.Background()
	userID := nextUID()

	const initialBalance int64 = 5_000_000 // ¥500
	const withdrawAmt int64 = 1_000_000    // ¥100（= MinWithdrawAmount）

	seedBalance(t, userID, initialBalance)
	t.Cleanup(func() {
		testDB.Unscoped().Where("user_id = ?", userID).Delete(&model.UserBalance{})
		testDB.Unscoped().Where("user_id = ?", userID).Delete(&model.WithdrawalRequest{})
		testDB.Unscoped().Where("user_id = ?", userID).Delete(&model.BalanceRecord{})
	})

	req, err := svc.CreateWithdrawal(ctx, userID, withdrawAmt, "工商银行 6222...")
	if err != nil {
		t.Fatalf("CreateWithdrawal failed: %v", err)
	}

	// 断言：状态为 PENDING
	if req.Status != "PENDING" {
		t.Errorf("expected status PENDING, got %s", req.Status)
	}
	if req.UserID != userID {
		t.Errorf("expected userID %d, got %d", userID, req.UserID)
	}
	// 金额（RMB）
	expectedRMB := credits.CreditsToRMB(withdrawAmt)
	if req.Amount != expectedRMB {
		t.Errorf("expected amount %.4f, got %.4f", expectedRMB, req.Amount)
	}

	// 断言：余额已被冻结（减少）
	var ub model.UserBalance
	testDB.Where("user_id = ?", userID).First(&ub)
	expectedRemaining := initialBalance - withdrawAmt
	if ub.Balance != expectedRemaining {
		t.Errorf("expected remaining balance %d, got %d", expectedRemaining, ub.Balance)
	}

	t.Logf("CreateWithdrawal_Success: req.ID=%d, remaining balance=%d credits", req.ID, ub.Balance)
}

// ============================================================
// TestCreateWithdrawal_InsufficientBalance — 余额不足应返回错误
// ============================================================
func TestCreateWithdrawal_InsufficientBalance(t *testing.T) {
	svc := newSvc(t)
	ctx := context.Background()
	userID := nextUID()

	// 只有 500,000 积分 < 1,000,000 MinWithdrawAmount
	seedBalance(t, userID, 500_000)
	t.Cleanup(func() {
		testDB.Unscoped().Where("user_id = ?", userID).Delete(&model.UserBalance{})
	})

	_, err := svc.CreateWithdrawal(ctx, userID, 1_000_000, "bank")
	if err == nil {
		t.Fatal("expected error for insufficient balance, got nil")
	}
	t.Logf("InsufficientBalance error (expected): %v", err)
}

// ============================================================
// TestCreateWithdrawal_BelowMinAmount — 金额低于最低提现门槛应报错
// ============================================================
func TestCreateWithdrawal_BelowMinAmount(t *testing.T) {
	svc := newSvc(t)
	ctx := context.Background()
	userID := nextUID()

	seedBalance(t, userID, 10_000_000) // 余额很多，但提现金额低于门槛
	t.Cleanup(func() {
		testDB.Unscoped().Where("user_id = ?", userID).Delete(&model.UserBalance{})
	})

	const belowMin int64 = 100 // 远低于 MinWithdrawAmount(1,000,000)
	_, err := svc.CreateWithdrawal(ctx, userID, belowMin, "bank")
	if err == nil {
		t.Fatal("expected error for below-min amount, got nil")
	}
	t.Logf("BelowMinAmount error (expected): %v", err)
}

// ============================================================
// TestApprove_Success — PENDING → APPROVED，AdminID 写入
// ============================================================
func TestApprove_Success(t *testing.T) {
	svc := newSvc(t)
	ctx := context.Background()
	userID := nextUID()

	seedBalance(t, userID, 5_000_000)
	t.Cleanup(func() {
		testDB.Unscoped().Where("user_id = ?", userID).Delete(&model.UserBalance{})
		testDB.Unscoped().Where("user_id = ?", userID).Delete(&model.WithdrawalRequest{})
		testDB.Unscoped().Where("user_id = ?", userID).Delete(&model.BalanceRecord{})
	})

	req, err := svc.CreateWithdrawal(ctx, userID, 1_000_000, "bank")
	if err != nil {
		t.Fatalf("create withdrawal: %v", err)
	}

	adminID := uint(1)
	if err := svc.Approve(ctx, req.ID, adminID, "看起来没问题"); err != nil {
		t.Fatalf("Approve failed: %v", err)
	}

	// 重新加载验证状态
	var updated model.WithdrawalRequest
	testDB.First(&updated, req.ID)

	if updated.Status != "APPROVED" {
		t.Errorf("expected APPROVED, got %s", updated.Status)
	}
	if updated.AdminID != adminID {
		t.Errorf("expected adminID %d, got %d", adminID, updated.AdminID)
	}
	if updated.AdminRemark != "看起来没问题" {
		t.Errorf("expected remark, got %s", updated.AdminRemark)
	}
	t.Logf("Approve_Success: status=%s, adminID=%d", updated.Status, updated.AdminID)
}

// ============================================================
// TestApprove_AlreadyProcessed — 重复审批应报错
// ============================================================
func TestApprove_AlreadyProcessed(t *testing.T) {
	svc := newSvc(t)
	ctx := context.Background()
	userID := nextUID()

	seedBalance(t, userID, 5_000_000)
	t.Cleanup(func() {
		testDB.Unscoped().Where("user_id = ?", userID).Delete(&model.UserBalance{})
		testDB.Unscoped().Where("user_id = ?", userID).Delete(&model.WithdrawalRequest{})
		testDB.Unscoped().Where("user_id = ?", userID).Delete(&model.BalanceRecord{})
	})

	req, _ := svc.CreateWithdrawal(ctx, userID, 1_000_000, "bank")
	_ = svc.Approve(ctx, req.ID, 1, "first approve")

	// 再次审批，应该失败
	err := svc.Approve(ctx, req.ID, 1, "second approve")
	if err == nil {
		t.Fatal("expected error for double approve, got nil")
	}
	t.Logf("AlreadyProcessed error (expected): %v", err)
}

// ============================================================
// TestReject_Success — PENDING → REJECTED，余额回退
// ============================================================
func TestReject_Success(t *testing.T) {
	svc := newSvc(t)
	ctx := context.Background()
	userID := nextUID()

	const initial int64 = 5_000_000
	const withdrawAmt int64 = 1_000_000

	seedBalance(t, userID, initial)
	t.Cleanup(func() {
		testDB.Unscoped().Where("user_id = ?", userID).Delete(&model.UserBalance{})
		testDB.Unscoped().Where("user_id = ?", userID).Delete(&model.WithdrawalRequest{})
		testDB.Unscoped().Where("user_id = ?", userID).Delete(&model.BalanceRecord{})
	})

	req, _ := svc.CreateWithdrawal(ctx, userID, withdrawAmt, "bank")

	// 冻结后余额
	var ubAfterFreeze model.UserBalance
	testDB.Where("user_id = ?", userID).First(&ubAfterFreeze)
	frozenBalance := ubAfterFreeze.Balance // = 4,000,000

	if err := svc.Reject(ctx, req.ID, 1, "不符合要求"); err != nil {
		t.Fatalf("Reject failed: %v", err)
	}

	var updated model.WithdrawalRequest
	testDB.First(&updated, req.ID)
	if updated.Status != "REJECTED" {
		t.Errorf("expected REJECTED, got %s", updated.Status)
	}

	// 余额应恢复到 initial
	var ubAfterReject model.UserBalance
	testDB.Where("user_id = ?", userID).First(&ubAfterReject)
	if ubAfterReject.Balance != initial {
		t.Errorf("expected balance restored to %d after reject, got %d (was frozen at %d)",
			initial, ubAfterReject.Balance, frozenBalance)
	}
	t.Logf("Reject_Success: balance restored to %d credits", ubAfterReject.Balance)
}

// ============================================================
// TestReject_AlreadyProcessed — 非 PENDING 状态下拒绝应报错
// ============================================================
func TestReject_AlreadyProcessed(t *testing.T) {
	svc := newSvc(t)
	ctx := context.Background()
	userID := nextUID()

	seedBalance(t, userID, 5_000_000)
	t.Cleanup(func() {
		testDB.Unscoped().Where("user_id = ?", userID).Delete(&model.UserBalance{})
		testDB.Unscoped().Where("user_id = ?", userID).Delete(&model.WithdrawalRequest{})
		testDB.Unscoped().Where("user_id = ?", userID).Delete(&model.BalanceRecord{})
	})

	req, _ := svc.CreateWithdrawal(ctx, userID, 1_000_000, "bank")
	_ = svc.Approve(ctx, req.ID, 1, "approved")

	// 尝试对 APPROVED 状态执行拒绝，应失败
	err := svc.Reject(ctx, req.ID, 1, "oops")
	if err == nil {
		t.Fatal("expected error rejecting non-PENDING withdrawal, got nil")
	}
	t.Logf("Reject_AlreadyProcessed error (expected): %v", err)
}

// ============================================================
// TestMarkPaid_Success — APPROVED → COMPLETED，bankTxnID 写入
// ============================================================
func TestMarkPaid_Success(t *testing.T) {
	svc := newSvc(t)
	ctx := context.Background()
	userID := nextUID()

	seedBalance(t, userID, 5_000_000)
	t.Cleanup(func() {
		testDB.Unscoped().Where("user_id = ?", userID).Delete(&model.UserBalance{})
		testDB.Unscoped().Where("user_id = ?", userID).Delete(&model.WithdrawalRequest{})
		testDB.Unscoped().Where("user_id = ?", userID).Delete(&model.BalanceRecord{})
	})

	req, _ := svc.CreateWithdrawal(ctx, userID, 1_000_000, "bank")
	_ = svc.Approve(ctx, req.ID, 1, "approved")

	const bankTxnID = "TXN20260414001"
	if err := svc.MarkPaid(ctx, req.ID, 1, bankTxnID); err != nil {
		t.Fatalf("MarkPaid failed: %v", err)
	}

	var updated model.WithdrawalRequest
	testDB.First(&updated, req.ID)
	if updated.Status != "COMPLETED" {
		t.Errorf("expected COMPLETED, got %s", updated.Status)
	}
	// bankTxnID 追加到 admin_remark
	if updated.AdminRemark == "" {
		t.Error("expected admin_remark to contain txn ID")
	}
	t.Logf("MarkPaid_Success: status=%s, remark=%s", updated.Status, updated.AdminRemark)
}

// ============================================================
// TestMarkPaid_NotApproved — 非 APPROVED 状态下调用 MarkPaid 应报错
// ============================================================
func TestMarkPaid_NotApproved(t *testing.T) {
	svc := newSvc(t)
	ctx := context.Background()
	userID := nextUID()

	seedBalance(t, userID, 5_000_000)
	t.Cleanup(func() {
		testDB.Unscoped().Where("user_id = ?", userID).Delete(&model.UserBalance{})
		testDB.Unscoped().Where("user_id = ?", userID).Delete(&model.WithdrawalRequest{})
		testDB.Unscoped().Where("user_id = ?", userID).Delete(&model.BalanceRecord{})
	})

	req, _ := svc.CreateWithdrawal(ctx, userID, 1_000_000, "bank")
	// 此时仍是 PENDING，直接尝试 mark-paid
	err := svc.MarkPaid(ctx, req.ID, 1, "TXN")
	if err == nil {
		t.Fatal("expected error marking paid on PENDING withdrawal, got nil")
	}
	t.Logf("MarkPaid_NotApproved error (expected): %v", err)
}

// ============================================================
// TestListUserWithdrawals_Paginated — 3 条记录，page_size=2，断言 total=3
// ============================================================
func TestListUserWithdrawals_Paginated(t *testing.T) {
	svc := newSvc(t)
	ctx := context.Background()
	userID := nextUID()

	seedBalance(t, userID, 50_000_000) // ¥5000，足够创建多条
	t.Cleanup(func() {
		testDB.Unscoped().Where("user_id = ?", userID).Delete(&model.UserBalance{})
		testDB.Unscoped().Where("user_id = ?", userID).Delete(&model.WithdrawalRequest{})
		testDB.Unscoped().Where("user_id = ?", userID).Delete(&model.BalanceRecord{})
	})

	// 创建 3 条提现记录（每次都要重新 seedBalance，因为余额被冻结）
	for i := 0; i < 3; i++ {
		// 每次提现后余额减少 1,000,000，初始 50,000,000 足够
		_, err := svc.CreateWithdrawal(ctx, userID, 1_000_000, "bank")
		if err != nil {
			t.Fatalf("create withdrawal[%d]: %v", i, err)
		}
	}

	// 第一页：page=1, page_size=2
	list1, total, err := svc.ListUserWithdrawals(ctx, userID, 1, 2)
	if err != nil {
		t.Fatalf("ListUserWithdrawals page1: %v", err)
	}
	if total != 3 {
		t.Errorf("expected total=3, got %d", total)
	}
	if len(list1) != 2 {
		t.Errorf("expected 2 items on page 1, got %d", len(list1))
	}

	// 第二页：page=2, page_size=2
	list2, total2, err := svc.ListUserWithdrawals(ctx, userID, 2, 2)
	if err != nil {
		t.Fatalf("ListUserWithdrawals page2: %v", err)
	}
	if total2 != 3 {
		t.Errorf("expected total=3 on page2, got %d", total2)
	}
	if len(list2) != 1 {
		t.Errorf("expected 1 item on page 2, got %d", len(list2))
	}
	t.Logf("ListUserWithdrawals_Paginated: total=%d, page1=%d, page2=%d", total, len(list1), len(list2))
}

// ============================================================
// TestAutoSettleAndCredit — PENDING 佣金超过 settleDays 后应结算并入账余额
// ============================================================
func TestAutoSettleAndCredit(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}
	svc := newSvc(t)
	ctx := context.Background()

	inviterID := nextUID()
	sourceUserID := nextUID() + 1

	// 初始化邀请人 users 行（user_balances → users FK 约束）
	seedUser(t, inviterID)

	// 初始化邀请人余额（防止表约束失败）
	testDB.Unscoped().Where("user_id = ?", inviterID).Delete(&model.UserBalance{})
	inviterBalance := model.UserBalance{
		TenantID:   1,
		UserID:     inviterID,
		Balance:    0,
		BalanceRMB: 0,
	}
	testDB.Create(&inviterBalance)

	t.Cleanup(func() {
		testDB.Unscoped().Where("user_id = ?", inviterID).Delete(&model.UserBalance{})
		testDB.Unscoped().Where("user_id = ?", inviterID).Delete(&model.CommissionRecord{})
		testDB.Unscoped().Where("user_id = ?", sourceUserID).Delete(&model.CommissionRecord{})
		testDB.Unscoped().Where("user_id = ?", inviterID).Delete(&model.BalanceRecord{})
	})

	// 直接插入一条 PENDING 佣金记录，设置 created_at 为 10 天前（超过 SettleDays=7）
	past := time.Now().AddDate(0, 0, -10)
	const commAmt int64 = 50_000 // ¥5 佣金

	rec := model.CommissionRecord{
		TenantID:            1,
		UserID:              inviterID,
		SourceUserID:        sourceUserID,
		SourceTenantID:      1,
		OrderAmount:         500_000,
		OrderAmountRMB:      credits.CreditsToRMB(500_000),
		CommissionRate:      0.10,
		CommissionAmount:    commAmt,
		CommissionAmountRMB: credits.CreditsToRMB(commAmt),
		Type:                "REFERRAL",
		Status:              "PENDING",
		Credited:            false,
	}
	if err := testDB.Create(&rec).Error; err != nil {
		t.Fatalf("create commission record: %v", err)
	}
	// 手动把 created_at 设为过去
	testDB.Model(&rec).UpdateColumn("created_at", past)

	// 执行结算
	settled, credited, err := svc.AutoSettleAndCredit(ctx)
	if err != nil {
		t.Fatalf("AutoSettleAndCredit: %v", err)
	}
	if settled == 0 {
		t.Error("expected at least 1 settled commission, got 0")
	}
	if credited == 0 {
		t.Error("expected at least 1 credited commission, got 0")
	}

	// 验证记录状态已变为 SETTLED
	var updated model.CommissionRecord
	testDB.First(&updated, rec.ID)
	if updated.Status != "SETTLED" {
		t.Errorf("expected SETTLED, got %s", updated.Status)
	}
	if !updated.Credited {
		t.Error("expected credited=true after settle")
	}

	// 验证邀请人余额增加了佣金金额
	var ubAfter model.UserBalance
	testDB.Where("user_id = ?", inviterID).First(&ubAfter)
	if ubAfter.Balance < commAmt {
		t.Errorf("expected inviter balance >= %d after settle, got %d", commAmt, ubAfter.Balance)
	}

	t.Logf("AutoSettleAndCredit: settled=%d, credited=%d, inviter balance=%d", settled, credited, ubAfter.Balance)
}
