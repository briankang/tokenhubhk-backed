package database

import (
	"encoding/json"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

func TestRunTalkingDataSeed20PricingMigrationBackfillsTiersAndCache(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Supplier{}, &model.ModelCategory{}, &model.AIModel{}, &model.ModelPricing{}); err != nil {
		t.Fatal(err)
	}

	supplier := model.Supplier{Name: "TD", Code: "talkingdata", AccessType: "api", Discount: 0.9}
	if err := db.Create(&supplier).Error; err != nil {
		t.Fatal(err)
	}
	category := model.ModelCategory{Name: "Doubao", Code: "talkingdata_doubao_chat", SupplierID: supplier.ID}
	if err := db.Create(&category).Error; err != nil {
		t.Fatal(err)
	}
	item := model.AIModel{
		SupplierID: supplier.ID, CategoryID: category.ID,
		ModelName: "doubao-seed-2.0-pro", PricingUnit: model.UnitPerMillionTokens,
		InputCostRMB: 3.2, OutputCostRMB: 16, Currency: "CREDIT",
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	pricing := model.ModelPricing{
		ModelID: item.ID, InputPriceRMB: 4.8, OutputPriceRMB: 24,
		InputPricePerToken: 48000, OutputPricePerToken: 240000, Currency: "CREDIT",
	}
	if err := db.Create(&pricing).Error; err != nil {
		t.Fatal(err)
	}

	RunTalkingDataSeed20PricingMigration(db)
	RunTalkingDataSeed20PricingMigration(db)

	var got model.AIModel
	if err := db.First(&got, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !got.SupportsCache || got.CacheMechanism != "auto" {
		t.Fatalf("cache flags not backfilled: %+v", got)
	}
	assertFloat(t, got.CacheInputPriceRMB, 0.64)

	var costData model.PriceTiersData
	if err := json.Unmarshal(got.PriceTiers, &costData); err != nil {
		t.Fatal(err)
	}
	if len(costData.Tiers) != 3 {
		t.Fatalf("cost tier count = %d", len(costData.Tiers))
	}
	assertFloat(t, costData.Tiers[1].InputPrice, 4.8)
	assertFloat(t, costData.Tiers[1].CacheInputPrice, 0.96)
	if costData.Tiers[1].SellingInputPrice == nil {
		t.Fatal("expected selling input price on cost tier")
	}
	assertFloat(t, *costData.Tiers[1].SellingInputPrice, 7.2)

	var gotPricing model.ModelPricing
	if err := db.Where("model_id = ?", item.ID).First(&gotPricing).Error; err != nil {
		t.Fatal(err)
	}
	var sellData model.PriceTiersData
	if err := json.Unmarshal(gotPricing.PriceTiers, &sellData); err != nil {
		t.Fatal(err)
	}
	if len(sellData.Tiers) != 3 {
		t.Fatalf("selling tier count = %d", len(sellData.Tiers))
	}
	assertFloat(t, sellData.Tiers[1].InputPrice, 7.2)
	assertFloat(t, sellData.Tiers[1].OutputPrice, 36)
	assertFloat(t, sellData.Tiers[1].CacheInputPrice, 1.44)
}
