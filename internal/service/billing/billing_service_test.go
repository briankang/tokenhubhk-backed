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
	if out.Snapshot["calculator_type"] != "token_io" {
		t.Fatalf("snapshot calculator_type missing: %#v", out.Snapshot)
	}
	if formulas, ok := out.Snapshot["calculator_formula"].([]string); !ok || len(formulas) == 0 {
		t.Fatalf("snapshot calculator_formula missing: %#v", out.Snapshot)
	}

	var bal model.UserBalance
	if err := db.Where("user_id = ?", uint(7)).First(&bal).Error; err != nil {
		t.Fatalf("load balance: %v", err)
	}
	if bal.Balance != 99980 {
		t.Fatalf("balance = %d, want 99980", bal.Balance)
	}
}

func TestBillingServiceSnapshotIncludesTokenQuoteLineItems(t *testing.T) {
	db := newBillingTestDB(t)
	seedBillingModel(t, db, 10000, 20000)
	seedBillingBalance(t, db, 19, 1, 100000)

	svc := NewService(db, pricing.NewPricingCalculator(db), balancesvc.NewBalanceService(db, nil))
	out, err := svc.SettleUsage(context.Background(), UsageRequest{
		RequestID: "req-token-quote",
		UserID:    19,
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

	quote := quoteFromSnapshot(t, out)
	if quoteString(t, quote, "quote_id") != "req-token-quote" {
		t.Fatalf("quote_id missing: %#v", quote)
	}
	if quoteInt64(t, quote, "total_credits") != out.CostCredits {
		t.Fatalf("quote total = %d, want outcome total %d", quoteInt64(t, quote, "total_credits"), out.CostCredits)
	}
	if quoteString(t, quote, "quote_hash") == "" {
		t.Fatalf("quote_hash should not be empty: %#v", quote)
	}

	lines := quoteLineItems(t, quote)
	input := findQuoteLine(t, lines, "regular_input")
	if quoteInt64(t, input, "quantity") != 1000 {
		t.Fatalf("regular input quantity = %d, want 1000", quoteInt64(t, input, "quantity"))
	}
	if quoteInt64(t, input, "unit_price_credits") != 10000 {
		t.Fatalf("regular input unit price = %d, want 10000", quoteInt64(t, input, "unit_price_credits"))
	}
	if quoteInt64(t, input, "cost_credits") != 10 {
		t.Fatalf("regular input cost = %d, want 10", quoteInt64(t, input, "cost_credits"))
	}

	output := findQuoteLine(t, lines, "output")
	if quoteInt64(t, output, "quantity") != 500 {
		t.Fatalf("output quantity = %d, want 500", quoteInt64(t, output, "quantity"))
	}
	if quoteInt64(t, output, "cost_credits") != 10 {
		t.Fatalf("output cost = %d, want 10", quoteInt64(t, output, "cost_credits"))
	}
	assertQuoteLineSum(t, quote)
}

func TestBillingServiceSnapshotIncludesCacheQuoteLineItems(t *testing.T) {
	db := newBillingTestDB(t)
	seedBillingModel(t, db, 10000, 20000)
	if err := db.Model(&model.AIModel{}).Where("id = ?", uint(1)).
		Updates(map[string]interface{}{
			"supports_cache":                 true,
			"cache_mechanism":                "both",
			"cache_input_price_rmb":          0.2,
			"cache_explicit_input_price_rmb": 0.1,
			"cache_write_price_rmb":          1.25,
		}).Error; err != nil {
		t.Fatalf("update cache pricing: %v", err)
	}
	seedBillingBalance(t, db, 20, 1, 100000)

	svc := NewService(db, pricing.NewPricingCalculator(db), balancesvc.NewBalanceService(db, nil))
	out, err := svc.SettleUsage(context.Background(), UsageRequest{
		RequestID: "req-cache-quote",
		UserID:    20,
		TenantID:  1,
		ModelName: "qwen-test",
		Usage: provider.Usage{
			PromptTokens:     1000,
			CompletionTokens: 500,
			TotalTokens:      1500,
			CacheReadTokens:  200,
			CacheWriteTokens: 100,
		},
	})
	if err != nil {
		t.Fatalf("SettleUsage returned error: %v", err)
	}

	quote := quoteFromSnapshot(t, out)
	lines := quoteLineItems(t, quote)

	regular := findQuoteLine(t, lines, "regular_input")
	if quoteInt64(t, regular, "quantity") != 700 {
		t.Fatalf("regular input quantity = %d, want 700", quoteInt64(t, regular, "quantity"))
	}
	if quoteInt64(t, regular, "cost_credits") != 7 {
		t.Fatalf("regular input cost = %d, want 7", quoteInt64(t, regular, "cost_credits"))
	}

	cacheRead := findQuoteLine(t, lines, "cache_read_input")
	if quoteInt64(t, cacheRead, "quantity") != 200 {
		t.Fatalf("cache read quantity = %d, want 200", quoteInt64(t, cacheRead, "quantity"))
	}
	if quoteInt64(t, cacheRead, "unit_price_credits") != 1000 {
		t.Fatalf("cache read unit price = %d, want 1000", quoteInt64(t, cacheRead, "unit_price_credits"))
	}

	cacheWrite := findQuoteLine(t, lines, "cache_write_input")
	if quoteInt64(t, cacheWrite, "quantity") != 100 {
		t.Fatalf("cache write quantity = %d, want 100", quoteInt64(t, cacheWrite, "quantity"))
	}
	if quoteInt64(t, cacheWrite, "unit_price_credits") != 12500 {
		t.Fatalf("cache write unit price = %d, want 12500", quoteInt64(t, cacheWrite, "unit_price_credits"))
	}

	output := findQuoteLine(t, lines, "output")
	if quoteInt64(t, output, "cost_credits") != 10 {
		t.Fatalf("output cost = %d, want 10", quoteInt64(t, output, "cost_credits"))
	}
	if quoteInt64(t, quote, "total_credits") != out.CostCredits {
		t.Fatalf("quote total = %d, want %d", quoteInt64(t, quote, "total_credits"), out.CostCredits)
	}
	assertQuoteLineSum(t, quote)
}

func TestBillingServiceSettleUsageFallsBackToInactiveModel(t *testing.T) {
	db := newBillingTestDB(t)
	seedBillingModel(t, db, 10000, 20000)
	if err := db.Model(&model.AIModel{}).Where("id = ?", uint(1)).Update("is_active", false).Error; err != nil {
		t.Fatalf("deactivate model: %v", err)
	}
	seedBillingBalance(t, db, 17, 1, 100000)

	svc := NewService(db, pricing.NewPricingCalculator(db), balancesvc.NewBalanceService(db, nil))
	out, err := svc.SettleUsage(context.Background(), UsageRequest{
		RequestID: "req-inactive-token",
		UserID:    17,
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
	if out.BillingStatus != BillingStatusSettled {
		t.Fatalf("status = %s, want settled", out.BillingStatus)
	}
	if out.CostCredits != 20 {
		t.Fatalf("cost credits = %d, want 20", out.CostCredits)
	}
	if out.Snapshot["model_name"] != "qwen-test" {
		t.Fatalf("snapshot model_name missing: %#v", out.Snapshot)
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
	quote := quoteFromSnapshot(t, out)
	thinking := findQuoteLine(t, quoteLineItems(t, quote), "thinking_output")
	if quoteInt64(t, thinking, "cost_credits") != 20 {
		t.Fatalf("thinking surcharge cost = %d, want 20", quoteInt64(t, thinking, "cost_credits"))
	}
	assertQuoteLineSum(t, quote)
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
	if out.Snapshot["calculator_type"] != "image_unit" {
		t.Fatalf("snapshot calculator_type missing: %#v", out.Snapshot)
	}
	quote := quoteFromSnapshot(t, out)
	image := findQuoteLine(t, quoteLineItems(t, quote), "image_unit")
	if quoteInt64(t, image, "quantity") != 2 {
		t.Fatalf("image quantity = %d, want 2", quoteInt64(t, image, "quantity"))
	}
	if quoteInt64(t, image, "unit_price_credits") != 300 {
		t.Fatalf("image unit price = %d, want 300", quoteInt64(t, image, "unit_price_credits"))
	}
	if quoteInt64(t, image, "cost_credits") != 600 {
		t.Fatalf("image cost = %d, want 600", quoteInt64(t, image, "cost_credits"))
	}
	assertQuoteLineSum(t, quote)
}

func TestBillingServiceSettleUnitUsageFallsBackToInactiveModel(t *testing.T) {
	db := newBillingTestDB(t)
	seedBillingModel(t, db, 0, 0)
	if err := db.Model(&model.AIModel{}).Where("id = ?", uint(1)).
		Updates(map[string]interface{}{
			"is_active":      false,
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
	seedBillingBalance(t, db, 18, 1, 100000)

	svc := NewService(db, pricing.NewPricingCalculator(db), balancesvc.NewBalanceService(db, nil))
	out, err := svc.SettleUnitUsage(context.Background(), UnitUsageRequest{
		RequestID: "req-inactive-image",
		UserID:    18,
		TenantID:  1,
		ModelName: "qwen-test",
		Usage:     pricing.UsageInput{ImageCount: 2},
	})
	if err != nil {
		t.Fatalf("SettleUnitUsage returned error: %v", err)
	}
	if out.BillingStatus != BillingStatusSettled {
		t.Fatalf("status = %s, want settled", out.BillingStatus)
	}
	if out.CostCredits != 600 {
		t.Fatalf("cost credits = %d, want 600", out.CostCredits)
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

func quoteFromSnapshot(t *testing.T, out *UsageOutcome) map[string]interface{} {
	t.Helper()
	if out == nil || out.Snapshot == nil {
		t.Fatal("outcome snapshot is nil")
	}
	raw := out.Snapshot["quote"]
	quote, ok := raw.(map[string]interface{})
	if !ok {
		t.Fatalf("snapshot quote missing or wrong type: %#v", out.Snapshot)
	}
	return quote
}

func quoteLineItems(t *testing.T, quote map[string]interface{}) []map[string]interface{} {
	t.Helper()
	raw := quote["line_items"]
	lines, ok := raw.([]map[string]interface{})
	if !ok {
		t.Fatalf("quote line_items missing or wrong type: %#v", quote)
	}
	if len(lines) == 0 {
		t.Fatalf("quote line_items should not be empty: %#v", quote)
	}
	return lines
}

func findQuoteLine(t *testing.T, lines []map[string]interface{}, component string) map[string]interface{} {
	t.Helper()
	for _, line := range lines {
		if quoteString(t, line, "component") == component {
			return line
		}
	}
	t.Fatalf("quote line component %q not found in %#v", component, lines)
	return nil
}

func assertQuoteLineSum(t *testing.T, quote map[string]interface{}) {
	t.Helper()
	var sum int64
	for _, line := range quoteLineItems(t, quote) {
		sum += quoteInt64(t, line, "cost_credits")
	}
	if sum != quoteInt64(t, quote, "total_credits") {
		t.Fatalf("quote line sum = %d, total = %d, quote=%#v", sum, quoteInt64(t, quote, "total_credits"), quote)
	}
}

func quoteString(t *testing.T, m map[string]interface{}, key string) string {
	t.Helper()
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("%s should be string, got %T (%#v)", key, v, v)
	}
	return s
}

func quoteInt64(t *testing.T, m map[string]interface{}, key string) int64 {
	t.Helper()
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case int32:
		return int64(n)
	case uint:
		return int64(n)
	case float64:
		return int64(n)
	default:
		t.Fatalf("%s should be numeric, got %T (%#v)", key, v, v)
	}
	return 0
}
