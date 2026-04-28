package database

import (
	"encoding/json"
	"testing"

	"tokenhub-server/internal/model"
)

func TestRunPriceTierInheritanceMigrationCopiesExplicitAliyunVersionedTiers(t *testing.T) {
	db := newModelPriceAccuracyTestDB(t)
	supplier, category := createModelPriceAccuracyFixtures(t, db, 1)
	supplier.Code = "aliyun_dashscope"
	if err := db.Save(&supplier).Error; err != nil {
		t.Fatal(err)
	}

	sourceTiers := mustPriceAccuracyTierJSON(t, []model.PriceTier{
		{InputMin: 0, InputMax: priceAccuracyInt64Ptr(32000), InputPrice: 0.8, OutputPrice: 2, CacheInputPrice: 0.16},
		{InputMin: 32000, InputMinExclusive: true, InputMax: priceAccuracyInt64Ptr(128000), InputPrice: 1.2, OutputPrice: 3, CacheInputPrice: 0.24},
		{InputMin: 128000, InputMinExclusive: true, InputPrice: 2, OutputPrice: 5, CacheInputPrice: 0.4},
	})
	source := model.AIModel{
		SupplierID: supplier.ID, CategoryID: category.ID,
		ModelName: "qwen-plus", PricingUnit: model.UnitPerMillionTokens,
		InputCostRMB: 0.8, OutputCostRMB: 2, Currency: "CREDIT", PriceTiers: sourceTiers,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	target := model.AIModel{
		SupplierID: supplier.ID, CategoryID: category.ID,
		ModelName: "qwen-plus-2025-09-11", PricingUnit: model.UnitPerMillionTokens,
		InputCostRMB: 0.8, OutputCostRMB: 2, Currency: "CREDIT",
		PriceTiers: mustPriceAccuracyTierJSON(t, []model.PriceTier{
			{InputMin: 0, OutputMin: 0, InputPrice: 0.8, OutputPrice: 2, CacheInputPrice: 0.16},
		}),
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}

	RunPriceTierInheritanceMigration(db)
	RunPriceTierInheritanceMigration(db)

	var got model.AIModel
	if err := db.Preload("Pricing").Where("id = ?", target.ID).First(&got).Error; err != nil {
		t.Fatal(err)
	}
	var costTiers model.PriceTiersData
	if err := json.Unmarshal(got.PriceTiers, &costTiers); err != nil {
		t.Fatal(err)
	}
	if len(costTiers.Tiers) != 3 {
		t.Fatalf("cost tiers=%d, want 3", len(costTiers.Tiers))
	}
	assertPriceAccuracyFloat(t, costTiers.Tiers[1].OutputPrice, 3)
	if costTiers.Tiers[0].SellingInputPrice != nil {
		t.Fatal("model cost tiers should not contain selling overrides")
	}

	if got.Pricing == nil {
		t.Fatal("pricing tiers were not created")
	}
	var sellingTiers model.PriceTiersData
	if err := json.Unmarshal(got.Pricing.PriceTiers, &sellingTiers); err != nil {
		t.Fatal(err)
	}
	if len(sellingTiers.Tiers) != 3 {
		t.Fatalf("selling tiers=%d, want 3", len(sellingTiers.Tiers))
	}
	assertPriceAccuracyFloat(t, sellingTiers.Tiers[2].InputPrice, 2)
}

func TestRunPriceTierInheritanceMigrationFlattensOfficialFlatAliyunLegacyModels(t *testing.T) {
	db := newModelPriceAccuracyTestDB(t)
	supplier, category := createModelPriceAccuracyFixtures(t, db, 1)
	supplier.Code = "aliyun_dashscope"
	if err := db.Save(&supplier).Error; err != nil {
		t.Fatal(err)
	}

	target := model.AIModel{
		SupplierID: supplier.ID, CategoryID: category.ID,
		ModelName: "qwen-plus-2025-04-28", PricingUnit: model.UnitPerMillionTokens,
		InputCostRMB: 0.8, OutputCostRMB: 2, Currency: "CREDIT",
		PriceTiers: mustPriceAccuracyTierJSON(t, []model.PriceTier{
			{InputMin: 0, InputMax: priceAccuracyInt64Ptr(128000), InputPrice: 0.8, OutputPrice: 2},
			{InputMin: 128000, InputMinExclusive: true, InputPrice: 2.4, OutputPrice: 20},
		}),
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ModelPricing{
		ModelID: target.ID, InputPriceRMB: 0.8, OutputPriceRMB: 2, Currency: "CREDIT",
		PriceTiers: mustPriceAccuracyTierJSON(t, []model.PriceTier{
			{InputMin: 0, InputMax: priceAccuracyInt64Ptr(128000), InputPrice: 0.8, OutputPrice: 2},
			{InputMin: 128000, InputMinExclusive: true, InputPrice: 2.4, OutputPrice: 20},
		}),
	}).Error; err != nil {
		t.Fatal(err)
	}

	RunPriceTierInheritanceMigration(db)

	var got model.AIModel
	if err := db.Preload("Pricing").Where("id = ?", target.ID).First(&got).Error; err != nil {
		t.Fatal(err)
	}
	var costTiers model.PriceTiersData
	if err := json.Unmarshal(got.PriceTiers, &costTiers); err != nil {
		t.Fatal(err)
	}
	if len(costTiers.Tiers) != 1 {
		t.Fatalf("cost tiers=%d, want 1", len(costTiers.Tiers))
	}
	assertPriceAccuracyFloat(t, costTiers.Tiers[0].InputPrice, 0.8)
	if costTiers.Tiers[0].InputMax != nil {
		t.Fatal("flattened default tier should not have an input max")
	}
	var sellingTiers model.PriceTiersData
	if err := json.Unmarshal(got.Pricing.PriceTiers, &sellingTiers); err != nil {
		t.Fatal(err)
	}
	if len(sellingTiers.Tiers) != 1 {
		t.Fatalf("selling tiers=%d, want 1", len(sellingTiers.Tiers))
	}
}

func TestRunPriceTierInheritanceMigrationSkipsDifferentBasePrice(t *testing.T) {
	db := newModelPriceAccuracyTestDB(t)
	supplier, category := createModelPriceAccuracyFixtures(t, db, 1)
	supplier.Code = "aliyun_dashscope"
	if err := db.Save(&supplier).Error; err != nil {
		t.Fatal(err)
	}

	source := model.AIModel{
		SupplierID: supplier.ID, CategoryID: category.ID,
		ModelName: "qwen-plus", PricingUnit: model.UnitPerMillionTokens,
		InputCostRMB: 0.8, OutputCostRMB: 2, Currency: "CREDIT",
		PriceTiers: mustPriceAccuracyTierJSON(t, []model.PriceTier{
			{InputMin: 0, InputMax: priceAccuracyInt64Ptr(32000), InputPrice: 0.8, OutputPrice: 2},
			{InputMin: 32000, InputMinExclusive: true, InputPrice: 2.4, OutputPrice: 20},
		}),
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	target := model.AIModel{
		SupplierID: supplier.ID, CategoryID: category.ID,
		ModelName: "qwen-plus-2025-09-11", PricingUnit: model.UnitPerMillionTokens,
		InputCostRMB: 3, OutputCostRMB: 12, Currency: "CREDIT",
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}

	RunPriceTierInheritanceMigration(db)

	var got model.AIModel
	if err := db.Where("id = ?", target.ID).First(&got).Error; err != nil {
		t.Fatal(err)
	}
	if len(got.PriceTiers) != 0 {
		t.Fatalf("unexpected inherited tiers: %s", string(got.PriceTiers))
	}
}

func TestRunPriceTierInheritanceMigrationAddsTencentHY20OfficialTiers(t *testing.T) {
	db := newModelPriceAccuracyTestDB(t)
	supplier, category := createModelPriceAccuracyFixtures(t, db, 1)
	supplier.Code = "tencent_hunyuan"
	if err := db.Save(&supplier).Error; err != nil {
		t.Fatal(err)
	}

	item := model.AIModel{
		SupplierID: supplier.ID, CategoryID: category.ID,
		ModelName: "hunyuan-2.0-thinking-20251109", PricingUnit: model.UnitPerMillionTokens,
		InputCostRMB: 3.975, OutputCostRMB: 15.9, Currency: "CREDIT",
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}

	RunPriceTierInheritanceMigration(db)
	RunPriceTierInheritanceMigration(db)

	var got model.AIModel
	if err := db.Preload("Pricing").Where("id = ?", item.ID).First(&got).Error; err != nil {
		t.Fatal(err)
	}
	var costTiers model.PriceTiersData
	if err := json.Unmarshal(got.PriceTiers, &costTiers); err != nil {
		t.Fatal(err)
	}
	if len(costTiers.Tiers) != 2 {
		t.Fatalf("cost tiers=%d, want 2", len(costTiers.Tiers))
	}
	assertPriceAccuracyFloat(t, costTiers.Tiers[0].InputPrice, 3.975)
	assertPriceAccuracyFloat(t, costTiers.Tiers[1].InputPrice, 5.3)
	assertPriceAccuracyFloat(t, got.InputCostRMB, 3.975)
	if got.SupportsCache {
		t.Fatal("Tencent HY 2.0 tiers should not enable cache")
	}
	if got.Pricing == nil {
		t.Fatal("pricing tiers were not created")
	}
	var sellingTiers model.PriceTiersData
	if err := json.Unmarshal(got.Pricing.PriceTiers, &sellingTiers); err != nil {
		t.Fatal(err)
	}
	if len(sellingTiers.Tiers) != 2 {
		t.Fatalf("selling tiers=%d, want 2", len(sellingTiers.Tiers))
	}
	assertPriceAccuracyFloat(t, sellingTiers.Tiers[1].OutputPrice, 21.2)
}
