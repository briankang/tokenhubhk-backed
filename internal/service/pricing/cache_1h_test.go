package pricing_test

import (
	"context"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/service/pricing"
)

func TestCalculateCostWithCacheChargesAnthropicOneHourWritesAtDoubleInput(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.AIModel{}, &model.ModelPricing{}); err != nil {
		t.Fatal(err)
	}

	aiModel := model.AIModel{
		ModelName:          "anthropic.claude-opus-4-6",
		ModelType:          model.ModelTypeLLM,
		PricingUnit:        model.UnitPerMillionTokens,
		InputCostRMB:       10,
		OutputCostRMB:      20,
		SupportsCache:      true,
		CacheMechanism:     "explicit",
		CacheInputPriceRMB: 1,
		CacheWritePriceRMB: 12.5,
	}
	if err := db.Create(&aiModel).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ModelPricing{
		ModelID:             aiModel.ID,
		InputPriceRMB:       10,
		OutputPriceRMB:      20,
		InputPricePerToken:  100000,
		OutputPricePerToken: 200000,
		Currency:            "CREDIT",
	}).Error; err != nil {
		t.Fatal(err)
	}

	got, err := pricing.NewPricingCalculator(db).CalculateCostWithCache(context.Background(), 0, &aiModel, 0, 0, pricing.CacheUsageInput{
		InputTokens:        1_000_000,
		CacheWriteTokens:   400_000,
		CacheWrite1hTokens: 100_000,
	})
	if err != nil {
		t.Fatal(err)
	}

	// regular: 600k * 100000 / 1M = 60000
	// 5m write: 300k * 125000 / 1M = 37500
	// 1h write: 100k * 200000 / 1M = 20000
	if got.InputCost != 117500 {
		t.Fatalf("input cost=%d, want 117500", got.InputCost)
	}
	if got.CacheWriteCost != 57500 {
		t.Fatalf("cache write cost=%d, want 57500", got.CacheWriteCost)
	}
	if got.CacheWriteTokens != 400000 || got.CacheWrite1hTokens != 100000 {
		t.Fatalf("cache write tokens=%d/%d, want 400000/100000", got.CacheWriteTokens, got.CacheWrite1hTokens)
	}
}
