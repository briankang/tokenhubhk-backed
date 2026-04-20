package pricing_test

// 注意：历史 ModelPricing 字段 PricingMode/InputPrice/OutputPrice 已被重构掉
// 当前实际字段为 InputPriceRMB / OutputPriceRMB / InputPricePerToken / OutputPricePerToken
// 此文件的 DB 集成测试大多保留 skip 语义，仅确保编译通过，实际验收由
// tier_cache_acceptance_test.go 中的纯函数测试完成。

import (
	"context"
	"os"
	"testing"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/service/pricing"
)

var testDB *gorm.DB

func TestMain(m *testing.M) {
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		dsn = "root:tokenhubhk_pass@tcp(localhost:3306)/tokenhubhk?charset=utf8mb4&parseTime=True&loc=Local"
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err == nil {
		testDB = db
		_ = testDB.AutoMigrate(&model.ModelPricing{}, &model.AgentLevelDiscount{}, &model.AgentPricing{})
	}
	// 即使 DB 未就绪也要 run，这样纯函数测试（如 tier_cache_acceptance_test.go）仍能执行
	code := m.Run()
	os.Exit(code)
}

func TestPricingService_SetAndGetModelPricing(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	calc := pricing.NewPricingCalculator(testDB)
	svc := pricing.NewPricingService(testDB, calc)
	ctx := context.Background()

	mp := &model.ModelPricing{
		ModelID:        99001,
		InputPriceRMB:  1.5,
		OutputPriceRMB: 6.0,
	}
	err := svc.SetModelPricing(ctx, mp)
	if err != nil {
		t.Fatalf("SetModelPricing failed: %v", err)
	}

	got, err := svc.GetModelPricing(ctx, 99001)
	if err != nil {
		t.Fatalf("GetModelPricing failed: %v", err)
	}
	if got == nil {
		t.Fatal("pricing should not be nil")
	}
}

func TestPricingService_ListModelPricings(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	calc := pricing.NewPricingCalculator(testDB)
	svc := pricing.NewPricingService(testDB, calc)
	ctx := context.Background()

	list, total, err := svc.ListModelPricings(ctx, 1, 10)
	if err != nil {
		t.Fatalf("ListModelPricings failed: %v", err)
	}
	if list == nil {
		t.Error("list should not be nil")
	}
	t.Logf("found %d pricings (total: %d)", len(list), total)
}

func TestPricingService_LevelDiscount(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	calc := pricing.NewPricingCalculator(testDB)
	svc := pricing.NewPricingService(testDB, calc)
	ctx := context.Background()

	discount := &model.AgentLevelDiscount{
		Level:          99,
		InputDiscount:  0.85,
		OutputDiscount: 0.85,
	}
	err := svc.SetLevelDiscount(ctx, discount)
	if err != nil {
		t.Fatalf("SetLevelDiscount failed: %v", err)
	}

	discounts, err := svc.GetLevelDiscounts(ctx, 99)
	if err != nil {
		t.Fatalf("GetLevelDiscounts failed: %v", err)
	}
	if len(discounts) == 0 {
		t.Error("should have at least one discount for level 99")
	}
}
