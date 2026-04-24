package billing

import (
	"context"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/provider"
	balancesvc "tokenhub-server/internal/service/balance"
	"tokenhub-server/internal/service/pricing"
)

func TestBillingServiceSettleUsageDeductsBalanceAndBuildsSnapshot(t *testing.T) {
	db := newBillingTestDB(t)
	seedBillingModel(t, db, 10000, 20000)
	seedBillingBalance(t, db, 7, 1, 100000)

	svc := NewService(db, pricing.NewPricingCalculator(db), balancesvc.NewBalanceService(db, nil))
	out, err := svc.SettleUsage(context.Background(), UsageRequest{
		RequestID: "req-basic",
		UserID:    7,
		TenantID:  1,
		ModelName: "qwen-test",
		Usage: provider.Usage{
			PromptTokens:     1000,
			CompletionTokens: 500,
			TotalTokens:      1500,
		},
	})
	if err != nil {
		t.Fatalf("SettleUsage returned error: %v", err)
	}
	if out.CostCredits != 20 {
		t.Fatalf("cost credits = %d, want 20", out.CostCredits)
	}
	if out.CostUnits != 200000 {
		t.Fatalf("cost units = %d, want 200000", out.CostUnits)
	}
	if out.BillingStatus != BillingStatusSettled {
		t.Fatalf("status = %s, want settled", out.BillingStatus)
	}
	if out.Snapshot["request_id"] != "req-basic" {
		t.Fatalf("snapshot request_id missing: %#v", out.Snapshot)
	}
	if out.Snapshot["total_cost_units"] != int64(200000) {
		t.Fatalf("snapshot total_cost_units missing: %#v", out.Snapshot)
	}

	var bal model.UserBalance
	if err := db.Where("user_id = ?", uint(7)).First(&bal).Error; err != nil {
		t.Fatalf("load balance: %v", err)
	}
	if bal.Balance != 99980 {
		t.Fatalf("balance = %d, want 99980", bal.Balance)
	}
}

func TestBillingServiceSettleUsageMarksUnderCollectedOnDeductFailure(t *testing.T) {
	db := newBillingTestDB(t)
	seedBillingModel(t, db, 10000, 20000)
	seedBillingBalance(t, db, 8, 1, 5)

	svc := NewService(db, pricing.NewPricingCalculator(db), balancesvc.NewBalanceService(db, nil))
	out, err := svc.SettleUsage(context.Background(), UsageRequest{
		RequestID: "req-under",
		UserID:    8,
		TenantID:  1,
		ModelName: "qwen-test",
		Usage: provider.Usage{
			PromptTokens:     1000,
			CompletionTokens: 500,
			TotalTokens:      1500,
		},
	})
	if err == nil {
		t.Fatal("expected deduct failure")
	}
	if out == nil {
		t.Fatal("expected outcome on deduct failure")
	}
	if out.BillingStatus != BillingStatusDeductFailed {
		t.Fatalf("status = %s, want deduct_failed", out.BillingStatus)
	}
	if out.UnderCollectedCredits != 20 {
		t.Fatalf("under_collected = %d, want 20", out.UnderCollectedCredits)
	}
	if out.ActualCostCredits != 0 {
		t.Fatalf("actual = %d, want 0", out.ActualCostCredits)
	}
}

func TestBillingServiceSettleUsageAppliesThinkingSurcharge(t *testing.T) {
	db := newBillingTestDB(t)
	seedBillingModel(t, db, 10000, 20000)
	if err := db.Model(&model.ModelPricing{}).Where("model_id = ?", uint(1)).
		Update("output_price_thinking_rmb", 4.0).Error; err != nil {
		t.Fatalf("update thinking price: %v", err)
	}
	seedBillingBalance(t, db, 9, 1, 100000)

	svc := NewService(db, pricing.NewPricingCalculator(db), balancesvc.NewBalanceService(db, nil))
	out, err := svc.SettleUsage(context.Background(), UsageRequest{
		RequestID:    "req-thinking",
		UserID:       9,
		TenantID:     1,
		ModelName:    "qwen-test",
		ThinkingMode: true,
		Usage: provider.Usage{
			PromptTokens:     0,
			CompletionTokens: 1000,
			TotalTokens:      1000,
		},
	})
	if err != nil {
		t.Fatalf("SettleUsage returned error: %v", err)
	}
	// Base output: 2 RMB/M => 20 credits for 1000 tokens.
	// Thinking output: 4 RMB/M adds another 20 credits.
	if out.CostCredits != 40 {
		t.Fatalf("cost credits = %d, want 40", out.CostCredits)
	}
	if out.Snapshot["thinking_mode_applied"] != true {
		t.Fatalf("thinking snapshot not applied: %#v", out.Snapshot)
	}
}

func TestBillingServiceFreezeAndSettleTokenUsage(t *testing.T) {
	db := newBillingTestDB(t)
	seedBillingModel(t, db, 10000, 20000)
	seedBillingBalance(t, db, 12, 1, 100000)

	svc := NewService(db, pricing.NewPricingCalculator(db), balancesvc.NewBalanceService(db, nil))
	freeze, err := svc.FreezeUsage(context.Background(), UsageRequest{
		RequestID: "req-token-freeze",
		UserID:    12,
		TenantID:  1,
		ModelName: "qwen-test",
		Usage: provider.Usage{
			PromptTokens:     1000,
			CompletionTokens: 1000,
			TotalTokens:      2000,
		},
	})
	if err != nil {
		t.Fatalf("FreezeUsage returned error: %v", err)
	}
	if freeze.FreezeID == "" {
		t.Fatal("freeze id should not be empty")
	}
	if freeze.EstimatedCostCredits != 30 {
		t.Fatalf("estimated credits = %d, want 30", freeze.EstimatedCostCredits)
	}

	out, err := svc.SettleUsage(context.Background(), UsageRequest{
		RequestID: "req-token-freeze",
		UserID:    12,
		TenantID:  1,
		ModelName: "qwen-test",
		FreezeID:  freeze.FreezeID,
		Usage: provider.Usage{
			PromptTokens:     1000,
			CompletionTokens: 500,
			TotalTokens:      1500,
		},
	})
	if err != nil {
		t.Fatalf("SettleUsage returned error: %v", err)
	}
	if out.CostCredits != 20 {
		t.Fatalf("actual credits = %d, want 20", out.CostCredits)
	}

	var bal model.UserBalance
	if err := db.Where("user_id = ?", uint(12)).First(&bal).Error; err != nil {
		t.Fatalf("load balance: %v", err)
	}
	if bal.FrozenAmount != 0 {
		t.Fatalf("frozen amount = %d, want 0", bal.FrozenAmount)
	}
	if bal.Balance != 99980 {
		t.Fatalf("balance = %d, want 99980", bal.Balance)
	}
}

func TestBillingServiceSettleUsageReleasesFreezeWhenModelMissing(t *testing.T) {
	db := newBillingTestDB(t)
	seedBillingModel(t, db, 10000, 20000)
	seedBillingBalance(t, db, 13, 1, 100000)

	svc := NewService(db, pricing.NewPricingCalculator(db), balancesvc.NewBalanceService(db, nil))
	freeze, err := svc.FreezeUsage(context.Background(), UsageRequest{
		RequestID: "req-missing-model",
		UserID:    13,
		TenantID:  1,
		ModelName: "qwen-test",
		Usage: provider.Usage{
			PromptTokens:     1000,
			CompletionTokens: 1000,
			TotalTokens:      2000,
		},
	})
	if err != nil {
		t.Fatalf("FreezeUsage returned error: %v", err)
	}
	if freeze.FreezeID == "" {
		t.Fatal("freeze id should not be empty")
	}

	_, err = svc.SettleUsage(context.Background(), UsageRequest{
		RequestID: "req-missing-model",
		UserID:    13,
		TenantID:  1,
		ModelName: "missing-model",
		FreezeID:  freeze.FreezeID,
		Usage: provider.Usage{
			PromptTokens:     1000,
			CompletionTokens: 1000,
			TotalTokens:      2000,
		},
	})
	if err != nil {
		t.Fatalf("SettleUsage with missing model should release without error: %v", err)
	}

	var bal model.UserBalance
	if err := db.Where("user_id = ?", uint(13)).First(&bal).Error; err != nil {
		t.Fatalf("load balance: %v", err)
	}
	if bal.FrozenAmount != 0 {
		t.Fatalf("frozen amount = %d, want 0", bal.FrozenAmount)
	}
	if bal.Balance != 100000 {
		t.Fatalf("balance = %d, want 100000", bal.Balance)
	}
}

func TestBillingServiceSettleUnitUsageDeductsImageCount(t *testing.T) {
	db := newBillingTestDB(t)
	seedBillingModel(t, db, 0, 0)
	if err := db.Model(&model.AIModel{}).Where("id = ?", uint(1)).
		Updates(map[string]interface{}{
			"pricing_unit":   model.UnitPerImage,
			"input_cost_rmb": 0.02,
		}).Error; err != nil {
		t.Fatalf("update model unit: %v", err)
	}
	if err := db.Model(&model.ModelPricing{}).Where("model_id = ?", uint(1)).
		Updates(map[string]interface{}{
			"input_price_rmb":       0.03,
			"input_price_per_token": int64(300),
		}).Error; err != nil {
		t.Fatalf("update model pricing: %v", err)
	}
	seedBillingBalance(t, db, 10, 1, 100000)

	svc := NewService(db, pricing.NewPricingCalculator(db), balancesvc.NewBalanceService(db, nil))
	out, err := svc.SettleUnitUsage(context.Background(), UnitUsageRequest{
		RequestID: "req-image",
		UserID:    10,
		TenantID:  1,
		ModelName: "qwen-test",
		Usage:     pricing.UsageInput{ImageCount: 2},
	})
	if err != nil {
		t.Fatalf("SettleUnitUsage returned error: %v", err)
	}
	if out.CostCredits != 600 {
		t.Fatalf("cost credits = %d, want 600", out.CostCredits)
	}
	if out.BillingStatus != BillingStatusSettled {
		t.Fatalf("status = %s, want settled", out.BillingStatus)
	}
	if out.Snapshot["image_count"] != 2 {
		t.Fatalf("image_count snapshot missing: %#v", out.Snapshot)
	}
}

func TestBillingServiceFreezeAndSettleUnitUsage(t *testing.T) {
	db := newBillingTestDB(t)
	seedBillingModel(t, db, 0, 0)
	if err := db.Model(&model.AIModel{}).Where("id = ?", uint(1)).
		Updates(map[string]interface{}{
			"pricing_unit":   model.UnitPerImage,
			"input_cost_rmb": 0.02,
		}).Error; err != nil {
		t.Fatalf("update model unit: %v", err)
	}
	if err := db.Model(&model.ModelPricing{}).Where("model_id = ?", uint(1)).
		Update("input_price_rmb", 0.03).Error; err != nil {
		t.Fatalf("update model pricing: %v", err)
	}
	seedBillingBalance(t, db, 11, 1, 100000)

	svc := NewService(db, pricing.NewPricingCalculator(db), balancesvc.NewBalanceService(db, nil))
	freeze, err := svc.FreezeUnitUsage(context.Background(), UnitUsageRequest{
		RequestID: "req-freeze",
		UserID:    11,
		TenantID:  1,
		ModelName: "qwen-test",
		Usage:     pricing.UsageInput{ImageCount: 2},
	})
	if err != nil {
		t.Fatalf("FreezeUnitUsage returned error: %v", err)
	}
	if freeze.FreezeID == "" {
		t.Fatal("freeze id should not be empty")
	}
	if freeze.EstimatedCostCredits != 600 {
		t.Fatalf("estimated credits = %d, want 600", freeze.EstimatedCostCredits)
	}

	var frozen model.UserBalance
	if err := db.Where("user_id = ?", uint(11)).First(&frozen).Error; err != nil {
		t.Fatalf("load frozen balance: %v", err)
	}
	if frozen.FrozenAmount != 600 {
		t.Fatalf("frozen amount = %d, want 600", frozen.FrozenAmount)
	}

	out, err := svc.SettleUnitUsage(context.Background(), UnitUsageRequest{
		RequestID: "req-freeze",
		UserID:    11,
		TenantID:  1,
		ModelName: "qwen-test",
		Usage:     pricing.UsageInput{ImageCount: 1},
		FreezeID:  freeze.FreezeID,
	})
	if err != nil {
		t.Fatalf("SettleUnitUsage returned error: %v", err)
	}
	if out.CostCredits != 300 {
		t.Fatalf("actual credits = %d, want 300", out.CostCredits)
	}

	var settled model.UserBalance
	if err := db.Where("user_id = ?", uint(11)).First(&settled).Error; err != nil {
		t.Fatalf("load settled balance: %v", err)
	}
	if settled.FrozenAmount != 0 {
		t.Fatalf("frozen amount after settle = %d, want 0", settled.FrozenAmount)
	}
	if settled.Balance != 99700 {
		t.Fatalf("balance = %d, want 99700", settled.Balance)
	}
}

func newBillingTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(
		&model.AIModel{},
		&model.ModelPricing{},
		&model.UserModelDiscount{},
		&model.AgentPricing{},
		&model.AgentLevelDiscount{},
		&model.UserBalance{},
		&model.BalanceRecord{},
		&model.FreezeRecord{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

func seedBillingModel(t *testing.T, db *gorm.DB, inputPerMillion, outputPerMillion int64) {
	t.Helper()
	m := model.AIModel{
		BaseModel:     model.BaseModel{ID: 1},
		CategoryID:    1,
		SupplierID:    1,
		ModelName:     "qwen-test",
		DisplayName:   "Qwen Test",
		IsActive:      true,
		Status:        "online",
		ModelType:     model.ModelTypeLLM,
		PricingUnit:   model.UnitPerMillionTokens,
		InputCostRMB:  1.0,
		OutputCostRMB: 2.0,
	}
	if err := db.Create(&m).Error; err != nil {
		t.Fatalf("create model: %v", err)
	}
	mp := model.ModelPricing{
		ModelID:             m.ID,
		InputPricePerToken:  inputPerMillion,
		OutputPricePerToken: outputPerMillion,
		InputPriceRMB:       1.0,
		OutputPriceRMB:      2.0,
		Currency:            "CREDIT",
	}
	if err := db.Create(&mp).Error; err != nil {
		t.Fatalf("create pricing: %v", err)
	}
}

func seedBillingBalance(t *testing.T, db *gorm.DB, userID, tenantID uint, amount int64) {
	t.Helper()
	ub := model.UserBalance{
		UserID:     userID,
		TenantID:   tenantID,
		Balance:    amount,
		BalanceRMB: float64(amount) / 10000.0,
		Currency:   "CREDIT",
	}
	if err := db.Create(&ub).Error; err != nil {
		t.Fatalf("create balance: %v", err)
	}
}
