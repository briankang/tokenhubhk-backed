package balance_test

import (
	"context"
	"os"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/service/balance"
)

var (
	testDB    *gorm.DB
	testRedis *goredis.Client
)

func TestMain(m *testing.M) {
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		dsn = "root:tokenhubhk_pass@tcp(localhost:3306)/tokenhubhk?charset=utf8mb4&parseTime=True&loc=Local"
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		os.Exit(0)
	}
	testDB = db

	redisAddr := os.Getenv("TEST_REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6380"
	}
	testRedis = goredis.NewClient(&goredis.Options{Addr: redisAddr})
	if err := testRedis.Ping(context.Background()).Err(); err != nil {
		testRedis = nil
	}

	_ = testDB.AutoMigrate(&model.UserBalance{}, &model.BalanceRecord{})
	code := m.Run()
	os.Exit(code)
}

func TestBalanceService_InitBalance(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := balance.NewBalanceService(testDB, testRedis)
	ctx := context.Background()

	// 使用一个不太可能冲突的用户 ID
	userID := uint(90001)
	tenantID := uint(1)

	err := svc.InitBalance(ctx, userID, tenantID)
	if err != nil {
		t.Fatalf("InitBalance failed: %v", err)
	}

	// 获取余额
	bal, err := svc.GetBalance(ctx, userID, tenantID)
	if err != nil {
		t.Fatalf("GetBalance failed: %v", err)
	}

	if bal == nil {
		t.Fatal("balance should not be nil after init")
	}
	t.Logf("user %d balance: %d credits", userID, bal.Balance)
}

func TestBalanceService_Recharge(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := balance.NewBalanceService(testDB, testRedis)
	ctx := context.Background()

	userID := uint(90002)
	tenantID := uint(1)

	// 先初始化
	_ = svc.InitBalance(ctx, userID, tenantID)

	// 获取初始余额
	initBal, _ := svc.GetBalance(ctx, userID, tenantID)
	initAmount := int64(0)
	if initBal != nil {
		initAmount = initBal.Balance
	}

	// 充值
	rechargeAmount := int64(100000) // 10 RMB
	updatedBal, err := svc.Recharge(ctx, userID, tenantID, rechargeAmount, "unit test recharge", "ut-001")
	if err != nil {
		t.Fatalf("Recharge failed: %v", err)
	}

	if updatedBal.Balance != initAmount+rechargeAmount {
		t.Errorf("expected balance %d, got %d", initAmount+rechargeAmount, updatedBal.Balance)
	}
}

func TestBalanceService_Deduct(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := balance.NewBalanceService(testDB, testRedis)
	ctx := context.Background()

	userID := uint(90003)
	tenantID := uint(1)

	// 初始化并充值
	_ = svc.InitBalance(ctx, userID, tenantID)
	_, _ = svc.Recharge(ctx, userID, tenantID, 100000, "deduct test recharge", "ut-deduct-001")

	// 获取充值后余额
	balBefore, _ := svc.GetBalance(ctx, userID, tenantID)

	// 扣减
	deductAmount := int64(10000) // 1 RMB
	_, err := svc.Deduct(ctx, userID, tenantID, deductAmount, "unit test deduction", "ut-deduct-002")
	if err != nil {
		t.Fatalf("Deduct failed: %v", err)
	}

	// 验证余额减少
	balAfter, _ := svc.GetBalance(ctx, userID, tenantID)
	if balAfter.Balance != balBefore.Balance-deductAmount {
		t.Errorf("expected balance %d after deduction, got %d", balBefore.Balance-deductAmount, balAfter.Balance)
	}
}

func TestBalanceService_RechargeRMB(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := balance.NewBalanceService(testDB, testRedis)
	ctx := context.Background()

	userID := uint(90004)
	tenantID := uint(1)

	_ = svc.InitBalance(ctx, userID, tenantID)

	// RMB 充值（应自动转换为积分）
	_, err := svc.RechargeRMB(ctx, userID, tenantID, 1.0, "RMB recharge test", "ut-rmb-001")
	if err != nil {
		t.Fatalf("RechargeRMB failed: %v", err)
	}

	bal, _ := svc.GetBalance(ctx, userID, tenantID)
	// 1 RMB = 10000 积分
	if bal.Balance < 10000 {
		t.Errorf("expected at least 10000 credits after 1 RMB recharge, got %d", bal.Balance)
	}
}

func TestBalanceService_HasSufficientBalance(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := balance.NewBalanceService(testDB, testRedis)
	ctx := context.Background()

	userID := uint(90005)
	tenantID := uint(1)

	_ = svc.InitBalance(ctx, userID, tenantID)
	_, _ = svc.Recharge(ctx, userID, tenantID, 100000, "sufficient balance test", "ut-suf-001")

	has, err := svc.HasSufficientBalance(ctx, userID)
	if err != nil {
		t.Fatalf("HasSufficientBalance failed: %v", err)
	}
	if !has {
		t.Error("user with 100000 credits should have sufficient balance")
	}
}

func TestBalanceService_ListRecords(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := balance.NewBalanceService(testDB, testRedis)
	ctx := context.Background()

	records, err := svc.ListRecords(ctx, 1, 1, 10)
	if err != nil {
		t.Fatalf("ListRecords failed: %v", err)
	}

	// 应返回非 nil 切片
	if records == nil {
		t.Error("records should not be nil")
	}
}

func TestBalanceService_QuotaConfig(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := balance.NewBalanceService(testDB, testRedis)
	ctx := context.Background()

	// 获取配额配置
	cfg, err := svc.GetQuotaConfig(ctx)
	if err != nil {
		t.Fatalf("GetQuotaConfig failed: %v", err)
	}

	if cfg == nil {
		t.Log("no quota config found (may not be initialized)")
		return
	}

	t.Logf("quota config: %+v", cfg)
}

func TestBalanceService_FreezeAndSettle(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := balance.NewBalanceService(testDB, testRedis)
	ctx := context.Background()

	userID := uint(90006)
	tenantID := uint(1)

	_ = svc.InitBalance(ctx, userID, tenantID)
	_, _ = svc.Recharge(ctx, userID, tenantID, 100000, "freeze test recharge", "ut-frz-001")

	// 冻结
	freezeID, err := svc.FreezeBalance(ctx, userID, tenantID, 5000, "test-model", "ut-frz-req-001")
	if err != nil {
		t.Fatalf("FreezeBalance failed: %v", err)
	}

	if freezeID == 0 {
		t.Fatal("freeze ID should not be 0")
	}

	// 结算（实际消费 3000）
	err = svc.SettleBalance(ctx, freezeID, 3000)
	if err != nil {
		t.Fatalf("SettleBalance failed: %v", err)
	}
}

func TestBalanceService_FreezeAndRelease(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := balance.NewBalanceService(testDB, testRedis)
	ctx := context.Background()

	userID := uint(90007)
	tenantID := uint(1)

	_ = svc.InitBalance(ctx, userID, tenantID)
	_, _ = svc.Recharge(ctx, userID, tenantID, 100000, "release test recharge", "ut-rel-001")

	balBefore, _ := svc.GetBalance(ctx, userID, tenantID)

	// 冻结
	freezeID, err := svc.FreezeBalance(ctx, userID, tenantID, 5000, "test-model", "ut-rel-req-001")
	if err != nil {
		t.Fatalf("FreezeBalance failed: %v", err)
	}

	// 释放（取消请求，不扣费）
	err = svc.ReleaseFrozen(ctx, freezeID)
	if err != nil {
		t.Fatalf("ReleaseFrozen failed: %v", err)
	}

	// 余额应恢复
	balAfter, _ := svc.GetBalance(ctx, userID, tenantID)
	if balAfter.Balance != balBefore.Balance {
		t.Errorf("balance should be restored after release, before=%d, after=%d", balBefore.Balance, balAfter.Balance)
	}
}

func TestBalanceService_GetReconciliationReport(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := balance.NewBalanceService(testDB, testRedis)
	ctx := context.Background()

	report, err := svc.GetReconciliationReport(ctx)
	if err != nil {
		t.Fatalf("GetReconciliationReport failed: %v", err)
	}

	if report == nil {
		t.Error("report should not be nil")
	}
}

func TestBalanceService_CheckQuota(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	svc := balance.NewBalanceService(testDB, testRedis)
	ctx := context.Background()

	err := svc.CheckQuota(ctx, 1)
	// CheckQuota 应该成功或返回具体配额错误，不应 panic
	if err != nil {
		t.Logf("CheckQuota returned error (may be expected): %v", err)
	}
}
