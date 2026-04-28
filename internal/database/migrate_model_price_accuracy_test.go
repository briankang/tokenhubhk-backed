package database

import (
	"encoding/json"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

func TestRunModelPriceAccuracyMigrationFillsMissingAndLowMarginSelling(t *testing.T) {
	db := newModelPriceAccuracyTestDB(t)
	supplier, category := createModelPriceAccuracyFixtures(t, db, 0.9)

	missing := model.AIModel{
		SupplierID: supplier.ID, CategoryID: category.ID,
		ModelName: "qwen3-tts-vd-realtime", PricingUnit: model.UnitPer10kCharacters,
		InputCostRMB: 0.08, Currency: "CREDIT",
	}
	if err := db.Create(&missing).Error; err != nil {
		t.Fatal(err)
	}

	lowMargin := model.AIModel{
		SupplierID: supplier.ID, CategoryID: category.ID,
		ModelName: "deepseek-r1", PricingUnit: model.UnitPerMillionTokens,
		InputCostRMB: 1, OutputCostRMB: 4, Currency: "CREDIT",
	}
	if err := db.Create(&lowMargin).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ModelPricing{
		ModelID: lowMargin.ID, InputPriceRMB: 0.9, OutputPriceRMB: 3.6,
		InputPricePerToken: 9000, OutputPricePerToken: 36000, Currency: "CREDIT",
	}).Error; err != nil {
		t.Fatal(err)
	}

	RunModelPriceAccuracyMigration(db)
	RunModelPriceAccuracyMigration(db)

	var missingPrice model.ModelPricing
	if err := db.Where("model_id = ?", missing.ID).First(&missingPrice).Error; err != nil {
		t.Fatal(err)
	}
	assertPriceAccuracyFloat(t, missingPrice.InputPriceRMB, 0.08)
	if missingPrice.InputPricePerToken != 800 {
		t.Fatalf("input credits = %d, want 800", missingPrice.InputPricePerToken)
	}

	var lowMarginPrice model.ModelPricing
	if err := db.Where("model_id = ?", lowMargin.ID).First(&lowMarginPrice).Error; err != nil {
		t.Fatal(err)
	}
	assertPriceAccuracyFloat(t, lowMarginPrice.InputPriceRMB, 1)
	assertPriceAccuracyFloat(t, lowMarginPrice.OutputPriceRMB, 4)
}

func TestRunModelPriceAccuracyMigrationKeepsHealthyMarkupAndRaisesTierOverrides(t *testing.T) {
	db := newModelPriceAccuracyTestDB(t)
	supplier, category := createModelPriceAccuracyFixtures(t, db, 0.8)

	costJSON := mustPriceAccuracyTierJSON(t, []model.PriceTier{
		{InputMin: 0, InputMax: priceAccuracyInt64Ptr(32000), InputPrice: 3.2, OutputPrice: 16, CacheInputPrice: 0.64},
		{InputMin: 32000, InputMinExclusive: true, InputMax: priceAccuracyInt64Ptr(128000), InputPrice: 4.8, OutputPrice: 24, CacheInputPrice: 0.96},
	})
	item := model.AIModel{
		SupplierID: supplier.ID, CategoryID: category.ID,
		ModelName: "doubao-seed-2.0-pro", PricingUnit: model.UnitPerMillionTokens,
		InputCostRMB: 3.2, OutputCostRMB: 16, Currency: "CREDIT",
		Discount: 0.9, PriceTiers: costJSON,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	healthyPrice := model.ModelPricing{
		ModelID: item.ID, InputPriceRMB: 4.8, OutputPriceRMB: 24,
		InputPricePerToken: 48000, OutputPricePerToken: 240000, Currency: "CREDIT",
	}
	if err := db.Create(&healthyPrice).Error; err != nil {
		t.Fatal(err)
	}

	RunModelPriceAccuracyMigration(db)

	var got model.ModelPricing
	if err := db.Where("model_id = ?", item.ID).First(&got).Error; err != nil {
		t.Fatal(err)
	}
	assertPriceAccuracyFloat(t, got.InputPriceRMB, 4.8)
	assertPriceAccuracyFloat(t, got.OutputPriceRMB, 24)

	var tiers model.PriceTiersData
	if err := json.Unmarshal(got.PriceTiers, &tiers); err != nil {
		t.Fatal(err)
	}
	if len(tiers.Tiers) != 2 {
		t.Fatalf("tiers=%d, want 2", len(tiers.Tiers))
	}
	assertPriceAccuracyFloat(t, tiers.Tiers[0].InputPrice, 3.2)
	assertPriceAccuracyFloat(t, tiers.Tiers[1].InputPrice, 4.8)
}

func newModelPriceAccuracyTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Supplier{}, &model.ModelCategory{}, &model.AIModel{}, &model.ModelPricing{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func createModelPriceAccuracyFixtures(t *testing.T, db *gorm.DB, discount float64) (model.Supplier, model.ModelCategory) {
	t.Helper()
	supplier := model.Supplier{Name: "Provider", Code: "provider", AccessType: "api", Discount: discount}
	if err := db.Create(&supplier).Error; err != nil {
		t.Fatal(err)
	}
	category := model.ModelCategory{Name: "Models", Code: "models", SupplierID: supplier.ID}
	if err := db.Create(&category).Error; err != nil {
		t.Fatal(err)
	}
	return supplier, category
}

func mustPriceAccuracyTierJSON(t *testing.T, tiers []model.PriceTier) model.JSON {
	t.Helper()
	data := model.PriceTiersData{Tiers: tiers, Currency: "CNY", UpdatedAt: time.Now()}
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return model.JSON(b)
}

func priceAccuracyInt64Ptr(v int64) *int64 {
	return &v
}

func assertPriceAccuracyFloat(t *testing.T, got, want float64) {
	t.Helper()
	if got < want-0.000001 || got > want+0.000001 {
		t.Fatalf("got %f, want %f", got, want)
	}
}
