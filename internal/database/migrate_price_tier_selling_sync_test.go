package database

import (
	"encoding/json"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

func TestRunPriceTierSellingSyncMigrationBackfillsSellingAndCacheTiers(t *testing.T) {
	db := newPriceTierSyncTestDB(t)
	supplier := createPriceTierSyncSupplier(t, db)
	category := createPriceTierSyncCategory(t, db, supplier.ID)

	costJSON := mustPriceTierJSON(t, []model.PriceTier{
		{InputMin: 0, InputMax: int64Ptr(32000), InputPrice: 2.5, OutputPrice: 10},
		{InputMin: 32000, InputMinExclusive: true, InputMax: int64Ptr(128000), InputPrice: 6, OutputPrice: 24},
	})
	item := model.AIModel{
		SupplierID: supplier.ID, CategoryID: category.ID,
		ModelName: "qwen3-max", PricingUnit: model.UnitPerMillionTokens,
		InputCostRMB: 2.5, OutputCostRMB: 10,
		SupportsCache: true, CacheMechanism: "auto", CacheInputPriceRMB: 0.5,
		PriceTiers: costJSON, Currency: "CREDIT",
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ModelPricing{
		ModelID: item.ID, InputPriceRMB: 2.125, OutputPriceRMB: 8.5,
		InputPricePerToken: 21250, OutputPricePerToken: 85000, Currency: "CREDIT",
	}).Error; err != nil {
		t.Fatal(err)
	}

	RunPriceTierSellingSyncMigration(db)
	RunPriceTierSellingSyncMigration(db)

	var got model.AIModel
	if err := db.First(&got, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	var gotCost model.PriceTiersData
	if err := json.Unmarshal(got.PriceTiers, &gotCost); err != nil {
		t.Fatal(err)
	}
	if len(gotCost.Tiers) != 2 {
		t.Fatalf("cost tiers=%d, want 2", len(gotCost.Tiers))
	}
	assertFloat(t, gotCost.Tiers[0].CacheInputPrice, 0.5)
	assertFloat(t, gotCost.Tiers[1].CacheInputPrice, 1.2)
	if gotCost.Tiers[1].SellingInputPrice == nil {
		t.Fatal("missing selling override on cost tier")
	}
	assertFloat(t, *gotCost.Tiers[1].SellingInputPrice, 5.1)

	var gotPricing model.ModelPricing
	if err := db.Where("model_id = ?", item.ID).First(&gotPricing).Error; err != nil {
		t.Fatal(err)
	}
	var gotSell model.PriceTiersData
	if err := json.Unmarshal(gotPricing.PriceTiers, &gotSell); err != nil {
		t.Fatal(err)
	}
	if len(gotSell.Tiers) != 2 {
		t.Fatalf("sell tiers=%d, want 2", len(gotSell.Tiers))
	}
	assertFloat(t, gotSell.Tiers[1].InputPrice, 5.1)
	assertFloat(t, gotSell.Tiers[1].OutputPrice, 20.4)
	assertFloat(t, gotSell.Tiers[1].CacheInputPrice, 1.02)
}

func TestRunPriceTierSellingSyncMigrationReplacesMismatchedSellingTiers(t *testing.T) {
	db := newPriceTierSyncTestDB(t)
	supplier := createPriceTierSyncSupplier(t, db)
	category := createPriceTierSyncCategory(t, db, supplier.ID)

	costJSON := mustPriceTierJSON(t, []model.PriceTier{
		{InputMin: 0, InputMax: int64Ptr(100000), InputPrice: 0.4, OutputPrice: 1},
	})
	sellJSON := mustPriceTierJSON(t, []model.PriceTier{
		{InputMin: 0, InputMax: int64Ptr(100000), InputPrice: 1.2, OutputPrice: 3},
		{InputMin: 100000, InputMinExclusive: true, InputPrice: 2.4, OutputPrice: 6},
	})
	item := model.AIModel{
		SupplierID: supplier.ID, CategoryID: category.ID,
		ModelName: "ernie-3.5-8k", PricingUnit: model.UnitPerMillionTokens,
		InputCostRMB: 0.4, OutputCostRMB: 1,
		SupportsCache: true, CacheMechanism: "auto", CacheInputPriceRMB: 0.16,
		PriceTiers: costJSON, Currency: "CREDIT",
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ModelPricing{
		ModelID: item.ID, InputPriceRMB: 1.2, OutputPriceRMB: 3,
		InputPricePerToken: 12000, OutputPricePerToken: 30000, Currency: "CREDIT", PriceTiers: sellJSON,
	}).Error; err != nil {
		t.Fatal(err)
	}

	RunPriceTierSellingSyncMigration(db)

	var gotPricing model.ModelPricing
	if err := db.Where("model_id = ?", item.ID).First(&gotPricing).Error; err != nil {
		t.Fatal(err)
	}
	var gotSell model.PriceTiersData
	if err := json.Unmarshal(gotPricing.PriceTiers, &gotSell); err != nil {
		t.Fatal(err)
	}
	if len(gotSell.Tiers) != 1 {
		t.Fatalf("sell tiers=%d, want 1", len(gotSell.Tiers))
	}
	assertFloat(t, gotSell.Tiers[0].InputPrice, 1.2)
	assertFloat(t, gotSell.Tiers[0].OutputPrice, 3)
	assertFloat(t, gotSell.Tiers[0].CacheInputPrice, 0.48)
}

func TestRunPriceTierSellingSyncMigrationConvertsLegacyRMBTierFields(t *testing.T) {
	db := newPriceTierSyncTestDB(t)
	supplier := createPriceTierSyncSupplier(t, db)
	category := createPriceTierSyncCategory(t, db, supplier.ID)

	costJSON := model.JSON(`[
		{"label":"<=200K","input_price_rmb":8.875,"output_price_rmb":71,"cache_read_rmb":2.21875,"max_input_tokens":200000},
		{"label":">200K","input_price_rmb":17.75,"output_price_rmb":106.5,"cache_read_rmb":4.4375,"max_input_tokens":0}
	]`)
	item := model.AIModel{
		SupplierID: supplier.ID, CategoryID: category.ID,
		ModelName: "gemini.gemini-3-pro-preview", PricingUnit: model.UnitPerMillionTokens,
		InputCostRMB: 8.875, OutputCostRMB: 71,
		SupportsCache: true, CacheMechanism: "auto", CacheInputPriceRMB: 2.21875,
		PriceTiers: costJSON, Currency: "CREDIT",
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ModelPricing{
		ModelID: item.ID, InputPriceRMB: 9.172313, OutputPriceRMB: 73.3785,
		InputPricePerToken: 91723, OutputPricePerToken: 733785, Currency: "CREDIT",
		PriceTiers: model.JSON(`[{"label":"<=200K","input_price_rmb":9.172313,"output_price_rmb":73.3785,"max_input_tokens":200000}]`),
	}).Error; err != nil {
		t.Fatal(err)
	}

	RunPriceTierSellingSyncMigration(db)

	var got model.AIModel
	if err := db.First(&got, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	var gotCost model.PriceTiersData
	if err := json.Unmarshal(got.PriceTiers, &gotCost); err != nil {
		t.Fatal(err)
	}
	if len(gotCost.Tiers) != 2 {
		t.Fatalf("cost tiers=%d, want 2", len(gotCost.Tiers))
	}
	assertFloat(t, gotCost.Tiers[0].InputPrice, 8.875)
	assertFloat(t, gotCost.Tiers[0].CacheInputPrice, 2.21875)

	var gotPricing model.ModelPricing
	if err := db.Where("model_id = ?", item.ID).First(&gotPricing).Error; err != nil {
		t.Fatal(err)
	}
	var gotSell model.PriceTiersData
	if err := json.Unmarshal(gotPricing.PriceTiers, &gotSell); err != nil {
		t.Fatal(err)
	}
	if len(gotSell.Tiers) != 2 {
		t.Fatalf("sell tiers=%d, want 2", len(gotSell.Tiers))
	}
	assertFloat(t, gotSell.Tiers[0].InputPrice, 9.172313)
	assertFloat(t, gotSell.Tiers[0].OutputPrice, 73.3785)
	assertFloat(t, gotSell.Tiers[0].CacheInputPrice, 2.293078)
}

func newPriceTierSyncTestDB(t *testing.T) *gorm.DB {
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

func createPriceTierSyncSupplier(t *testing.T, db *gorm.DB) model.Supplier {
	t.Helper()
	supplier := model.Supplier{Name: "Provider", Code: "provider", AccessType: "api"}
	if err := db.Create(&supplier).Error; err != nil {
		t.Fatal(err)
	}
	return supplier
}

func createPriceTierSyncCategory(t *testing.T, db *gorm.DB, supplierID uint) model.ModelCategory {
	t.Helper()
	category := model.ModelCategory{Name: "Chat", Code: "chat", SupplierID: supplierID}
	if err := db.Create(&category).Error; err != nil {
		t.Fatal(err)
	}
	return category
}

func mustPriceTierJSON(t *testing.T, tiers []model.PriceTier) model.JSON {
	t.Helper()
	data := model.PriceTiersData{Tiers: tiers, Currency: "CNY", UpdatedAt: time.Now()}
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return model.JSON(b)
}

func int64Ptr(v int64) *int64 {
	return &v
}
