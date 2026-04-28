package pricing

import (
	"context"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

func newPricingCalculatorTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.AIModel{},
		&model.ModelPricing{},
		&model.AgentLevelDiscount{},
		&model.AgentPricing{},
		&model.UserModelDiscount{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestCalculateCostZeroPricedModelDoesNotApplyMinimumCharge(t *testing.T) {
	db := newPricingCalculatorTestDB(t)
	if err := db.Create(&model.ModelPricing{
		ModelID:             1,
		InputPricePerToken:  0,
		OutputPricePerToken: 0,
		Currency:            "CREDIT",
	}).Error; err != nil {
		t.Fatalf("create pricing: %v", err)
	}

	got, err := NewPricingCalculator(db).CalculateCost(context.Background(), 0, 1, 0, 0, 12, 3)
	if err != nil {
		t.Fatalf("CalculateCost returned error: %v", err)
	}
	if got.TotalCost != 0 {
		t.Fatalf("zero-priced model should not be minimum charged, got %d", got.TotalCost)
	}
}

func TestCalculateCostPositivePricedTinyRequestStillAppliesMinimumCharge(t *testing.T) {
	db := newPricingCalculatorTestDB(t)
	if err := db.Create(&model.ModelPricing{
		ModelID:             1,
		InputPricePerToken:  1,
		OutputPricePerToken: 1,
		Currency:            "CREDIT",
	}).Error; err != nil {
		t.Fatalf("create pricing: %v", err)
	}

	got, err := NewPricingCalculator(db).CalculateCost(context.Background(), 0, 1, 0, 0, 1, 0)
	if err != nil {
		t.Fatalf("CalculateCost returned error: %v", err)
	}
	if got.TotalCost != 1 {
		t.Fatalf("positive-priced tiny request should be minimum charged, got %d", got.TotalCost)
	}
}
