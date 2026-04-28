package database

import (
	"math"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
)

func TestRunSeedWangsuImageGateway(t *testing.T) {
	db := newWangsuImageSeedTestDB(t)

	RunSeedWangsuImageGateway(db)
	RunSeedWangsuImageGateway(db)

	var sup model.Supplier
	if err := db.Where("code = ? AND access_type = ?", "wangsu_aigw", "api").First(&sup).Error; err != nil {
		t.Fatal(err)
	}

	var ch model.Channel
	if err := db.Where("name = ?", "网宿-图片生成").First(&ch).Error; err != nil {
		t.Fatal(err)
	}
	if ch.Status != "active" || !ch.Verified {
		t.Fatalf("channel status=%s verified=%v, want active/true", ch.Status, ch.Verified)
	}
	if ch.Endpoint != wangsuImageGatewayEndpoint {
		t.Fatalf("endpoint=%s, want %s", ch.Endpoint, wangsuImageGatewayEndpoint)
	}
	if ch.APIKey != wangsuImageDefaultKey {
		t.Fatalf("api key was not seeded")
	}
	if ch.SupportedCapabilities != "image" || ch.ApiPath != "/images/generations" {
		t.Fatalf("unexpected image channel config: caps=%s path=%s", ch.SupportedCapabilities, ch.ApiPath)
	}

	var modelCount int64
	db.Model(&model.AIModel{}).Where("supplier_id = ? AND model_type = ?", sup.ID, model.ModelTypeImageGeneration).Count(&modelCount)
	if modelCount != int64(len(wangsuImageModels)) {
		t.Fatalf("model count=%d, want %d", modelCount, len(wangsuImageModels))
	}

	assertSeededImageModel(t, db, "dall-e-3", 0.04)
	assertSeededImageModel(t, db, "gpt-image-1.5", 0.034)
	assertSeededImageModel(t, db, "gpt-image-2", 0.03168)
	assertSeededImageModel(t, db, "flux.1-schnell", 0.002831)

	var mappingCount int64
	db.Model(&model.ChannelModel{}).Where("channel_id = ? AND is_active = ?", ch.ID, true).Count(&mappingCount)
	if mappingCount != int64(len(wangsuImageModels)) {
		t.Fatalf("channel model count=%d, want %d", mappingCount, len(wangsuImageModels))
	}
}

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 0.000001
}

func assertSeededImageModel(t *testing.T, db *gorm.DB, modelName string, officialUSD float64) {
	t.Helper()
	var ai model.AIModel
	if err := db.Where("model_name = ?", modelName).First(&ai).Error; err != nil {
		t.Fatal(err)
	}
	if ai.PricingUnit != model.UnitPerImage {
		t.Fatalf("%s pricing unit=%s, want per_image", modelName, ai.PricingUnit)
	}

	wantCost := round6(officialUSD * USDCNYSnapshot * wangsuImageCostDiscount)
	if !almostEqual(ai.InputCostRMB, wantCost) {
		t.Fatalf("%s cost=%f, want %f", modelName, ai.InputCostRMB, wantCost)
	}
	if ai.PriceSourceCurrency != "USD" {
		t.Fatalf("%s source currency=%q, want USD", modelName, ai.PriceSourceCurrency)
	}
	if !almostEqual(ai.PriceSourceExchangeRate, USDCNYSnapshot) {
		t.Fatalf("%s exchange rate=%f, want %f", modelName, ai.PriceSourceExchangeRate, USDCNYSnapshot)
	}
	if !almostEqual(ai.InputCostUSD, officialUSD) {
		t.Fatalf("%s source usd=%f, want %f", modelName, ai.InputCostUSD, officialUSD)
	}

	var pricing model.ModelPricing
	if err := db.Where("model_id = ?", ai.ID).First(&pricing).Error; err != nil {
		t.Fatal(err)
	}
	wantSell := round6(officialUSD * USDCNYSnapshot)
	if !almostEqual(pricing.InputPriceRMB, wantSell) {
		t.Fatalf("%s sale=%f, want %f", modelName, pricing.InputPriceRMB, wantSell)
	}
	if pricing.InputPricePerToken != credits.RMBToCredits(wantSell) {
		t.Fatalf("%s sale credits=%d, want %d", modelName, pricing.InputPricePerToken, credits.RMBToCredits(wantSell))
	}
}

func newWangsuImageSeedTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.Supplier{},
		&model.ModelCategory{},
		&model.AIModel{},
		&model.Channel{},
		&model.ChannelModel{},
		&model.ModelPricing{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}
