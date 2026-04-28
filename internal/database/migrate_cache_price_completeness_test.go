package database

import (
	"math"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

func TestRunCachePriceCompletenessMigration(t *testing.T) {
	db := newCachePriceCompletenessTestDB(t)
	supplier, category := createCacheMigrationFixtures(t, db, "aliyun_dashscope")

	models := []model.AIModel{
		{
			SupplierID: supplier.ID, CategoryID: category.ID, ModelName: "qwen3-235b-a22b-thinking-2507",
			ModelType: "LLM", PricingUnit: model.UnitPerMillionTokens, InputCostRMB: 6,
			SupportsCache: true, CacheMechanism: "both", CacheMinTokens: 0,
		},
		{
			SupplierID: supplier.ID, CategoryID: category.ID, ModelName: "qwen3-omni-flash",
			ModelType: "VLM", PricingUnit: model.UnitPerMillionTokens, InputCostRMB: 0,
			SupportsCache: true, CacheMechanism: "both", CacheMinTokens: 1024,
		},
		{
			SupplierID: supplier.ID, CategoryID: category.ID, ModelName: "deepseek-ocr",
			ModelType: "LLM", PricingUnit: model.UnitPerMillionTokens, InputCostRMB: 0,
			SupportsCache: true, CacheMechanism: "none",
		},
	}
	if err := db.Create(&models).Error; err != nil {
		t.Fatal(err)
	}

	RunCachePriceCompletenessMigration(db)
	RunCachePriceCompletenessMigration(db)

	var qwen model.AIModel
	if err := db.Where("model_name = ?", "qwen3-235b-a22b-thinking-2507").First(&qwen).Error; err != nil {
		t.Fatal(err)
	}
	assertFloat(t, qwen.CacheInputPriceRMB, 1.2)
	assertFloat(t, qwen.CacheExplicitInputPriceRMB, 0.6)
	assertFloat(t, qwen.CacheWritePriceRMB, 7.5)
	if !qwen.SupportsCache || qwen.CacheMechanism != "both" || qwen.CacheMinTokens != 1024 {
		t.Fatalf("qwen cache fields not normalized: %+v", qwen)
	}

	assertCacheDisabled(t, db, "qwen3-omni-flash")
	assertCacheDisabled(t, db, "deepseek-ocr")
}

func TestRunUSDPriceSourceBackfillMigration(t *testing.T) {
	db := newCachePriceCompletenessTestDB(t)
	supplier, category := createCacheMigrationFixtures(t, db, "wangsu_aigw")
	models := []model.AIModel{{
		SupplierID: supplier.ID, CategoryID: category.ID, ModelName: "gpt-4o-mini",
		ModelType: "LLM", PricingUnit: model.UnitPerMillionTokens,
	}, {
		SupplierID: supplier.ID, CategoryID: category.ID, ModelName: "gpt-image-2",
		ModelType: model.ModelTypeImageGeneration, PricingUnit: model.UnitPerImage,
	}, {
		SupplierID: supplier.ID, CategoryID: category.ID, ModelName: "sora-2",
		ModelType: model.ModelTypeVideoGeneration, PricingUnit: model.UnitPerSecond,
	}}
	if err := db.Create(&models).Error; err != nil {
		t.Fatal(err)
	}

	RunUSDPriceSourceBackfillMigration(db)

	var got model.AIModel
	if err := db.Where("model_name = ?", "gpt-4o-mini").First(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got.PriceSourceCurrency != "USD" {
		t.Fatalf("source currency=%q, want USD", got.PriceSourceCurrency)
	}
	assertFloat(t, got.PriceSourceExchangeRate, USDCNYSnapshot)
	assertFloat(t, got.InputCostUSD, 0.15)
	assertFloat(t, got.OutputCostUSD, 0.60)
	assertFloat(t, got.CacheInputPriceUSD, 0.075)

	got = model.AIModel{}
	if err := db.Where("model_name = ?", "gpt-image-2").First(&got).Error; err != nil {
		t.Fatal(err)
	}
	assertFloat(t, got.InputCostUSD, 0.03168)

	got = model.AIModel{}
	if err := db.Where("model_name = ?", "sora-2").First(&got).Error; err != nil {
		t.Fatal(err)
	}
	assertFloat(t, got.InputCostUSD, 0.10)
}

func newCachePriceCompletenessTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Supplier{}, &model.ModelCategory{}, &model.AIModel{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func createCacheMigrationFixtures(t *testing.T, db *gorm.DB, supplierCode string) (model.Supplier, model.ModelCategory) {
	t.Helper()
	supplier := model.Supplier{Name: supplierCode, Code: supplierCode, AccessType: "api", IsActive: true, Status: "active"}
	if err := db.Create(&supplier).Error; err != nil {
		t.Fatal(err)
	}
	category := model.ModelCategory{Name: "Chat", Code: supplierCode + "_chat", SupplierID: supplier.ID}
	if err := db.Create(&category).Error; err != nil {
		t.Fatal(err)
	}
	return supplier, category
}

func assertCacheDisabled(t *testing.T, db *gorm.DB, modelName string) {
	t.Helper()
	var got model.AIModel
	if err := db.Where("model_name = ?", modelName).First(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got.SupportsCache || got.CacheMechanism != "none" || got.CacheInputPriceRMB != 0 || got.CacheWritePriceRMB != 0 {
		t.Fatalf("%s cache should be disabled, got %+v", modelName, got)
	}
}

func assertFloat(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.000001 {
		t.Fatalf("got %f, want %f", got, want)
	}
}
