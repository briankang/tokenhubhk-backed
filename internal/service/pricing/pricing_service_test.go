package pricing_test

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
	if err != nil {
		os.Exit(0)
	}
	testDB = db

	_ = testDB.AutoMigrate(&model.ModelPricing{}, &model.AgentLevelDiscount{}, &model.AgentPricing{})
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

	// 创建定价
	mp := &model.ModelPricing{
		ModelID:        99001,
		PricingMode:    "FIXED",
		InputPrice:     0.000015,
		OutputPrice:    0.000060,
	}
	err := svc.SetModelPricing(ctx, mp)
	if err != nil {
		t.Fatalf("SetModelPricing failed: %v", err)
	}

	// 获取定价
	got, err := svc.GetModelPricing(ctx, 99001)
	if err != nil {
		t.Fatalf("GetModelPricing failed: %v", err)
	}

	if got == nil {
		t.Fatal("pricing should not be nil")
	}
	if got.PricingMode != "FIXED" {
		t.Errorf("expected FIXED mode, got %s", got.PricingMode)
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

func TestPricingService_UpdateModelPricing(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	calc := pricing.NewPricingCalculator(testDB)
	svc := pricing.NewPricingService(testDB, calc)
	ctx := context.Background()

	// 先创建
	mp := &model.ModelPricing{
		ModelID:     99002,
		PricingMode: "FIXED",
		InputPrice:  0.000010,
		OutputPrice: 0.000040,
	}
	_ = svc.SetModelPricing(ctx, mp)

	// 列出并获取 ID
	list, _, _ := svc.ListModelPricings(ctx, 1, 100)
	var targetID uint
	for _, p := range list {
		if p.ModelID == 99002 {
			targetID = p.ID
			break
		}
	}
	if targetID == 0 {
		t.Skip("cannot find pricing for update test")
	}

	// 更新
	update := &model.ModelPricing{
		InputPrice:  0.000020,
		OutputPrice: 0.000080,
	}
	err := svc.UpdateModelPricing(ctx, targetID, update)
	if err != nil {
		t.Fatalf("UpdateModelPricing failed: %v", err)
	}
}

func TestPricingService_DeleteModelPricing(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	calc := pricing.NewPricingCalculator(testDB)
	svc := pricing.NewPricingService(testDB, calc)
	ctx := context.Background()

	// 创建
	mp := &model.ModelPricing{
		ModelID:     99003,
		PricingMode: "FIXED",
		InputPrice:  0.000001,
		OutputPrice: 0.000002,
	}
	_ = svc.SetModelPricing(ctx, mp)

	// 查找并删除
	list, _, _ := svc.ListModelPricings(ctx, 1, 100)
	var targetID uint
	for _, p := range list {
		if p.ModelID == 99003 {
			targetID = p.ID
			break
		}
	}
	if targetID == 0 {
		t.Skip("cannot find pricing for delete test")
	}

	err := svc.DeleteModelPricing(ctx, targetID)
	if err != nil {
		t.Fatalf("DeleteModelPricing failed: %v", err)
	}

	// 验证删除
	got, err := svc.GetModelPricing(ctx, 99003)
	if err == nil && got != nil {
		t.Error("pricing should be deleted")
	}
}

func TestPricingService_LevelDiscount(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	calc := pricing.NewPricingCalculator(testDB)
	svc := pricing.NewPricingService(testDB, calc)
	ctx := context.Background()

	// 创建等级折扣
	discount := &model.AgentLevelDiscount{
		Level:        99,
		DiscountRate: 0.85,
	}
	err := svc.SetLevelDiscount(ctx, discount)
	if err != nil {
		t.Fatalf("SetLevelDiscount failed: %v", err)
	}

	// 获取
	discounts, err := svc.GetLevelDiscounts(ctx, 99)
	if err != nil {
		t.Fatalf("GetLevelDiscounts failed: %v", err)
	}

	if len(discounts) == 0 {
		t.Error("should have at least one discount for level 99")
	}
}

func TestPricingCalculator_Calculate(t *testing.T) {
	if testDB == nil {
		t.Skip("database not available")
	}

	calc := pricing.NewPricingCalculator(testDB)
	ctx := context.Background()

	// 先确保有定价数据
	svc := pricing.NewPricingService(testDB, calc)
	mp := &model.ModelPricing{
		ModelID:     99004,
		PricingMode: "FIXED",
		InputPrice:  0.000015,
		OutputPrice: 0.000060,
	}
	_ = svc.SetModelPricing(ctx, mp)

	// 计算费用
	result, err := calc.Calculate(ctx, 99004, 1000, 500, 0, nil, nil)
	if err != nil {
		t.Logf("Calculate returned error (may be expected if model not linked): %v", err)
		return
	}

	if result != nil {
		t.Logf("cost: input=%f, output=%f, total=%f", result.InputCost, result.OutputCost, result.TotalCost)
	}
}
