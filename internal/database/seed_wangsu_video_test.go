package database

import (
	"math"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

func TestRunSeedWangsuVideo(t *testing.T) {
	db := newWangsuVideoSeedTestDB(t)

	RunSeedWangsuVideo(db)

	var ch model.Channel
	if err := db.Where("name = ?", wangsuVideoChannelName).First(&ch).Error; err != nil {
		t.Fatal(err)
	}
	if ch.Endpoint != wangsuVideoEndpoint {
		t.Fatalf("endpoint=%q, want %q", ch.Endpoint, wangsuVideoEndpoint)
	}
	if ch.ApiPath != "/videos" || ch.SupportedCapabilities != "video" {
		t.Fatalf("channel path/capabilities=%q/%q", ch.ApiPath, ch.SupportedCapabilities)
	}
	if ch.APIKey != wangsuVideoAPIKey {
		t.Fatalf("seeded API key mismatch")
	}

	var models []model.AIModel
	if err := db.Where("model_type = ?", model.ModelTypeVideoGeneration).Find(&models).Error; err != nil {
		t.Fatal(err)
	}
	if len(models) != len(wangsuVideoModels) {
		t.Fatalf("models=%d, want %d", len(models), len(wangsuVideoModels))
	}

	var sora model.AIModel
	if err := db.Where("model_name = ?", "sora-2").First(&sora).Error; err != nil {
		t.Fatal(err)
	}
	wantCost := round6(0.10 * USDCNYSnapshot * wangsuVideoDiscount)
	if math.Abs(sora.InputCostRMB-wantCost) > 0.000001 {
		t.Fatalf("sora cost=%f, want %f", sora.InputCostRMB, wantCost)
	}
	if sora.PriceSourceCurrency != "USD" {
		t.Fatalf("sora source currency=%q, want USD", sora.PriceSourceCurrency)
	}
	if math.Abs(sora.PriceSourceExchangeRate-USDCNYSnapshot) > 0.000001 {
		t.Fatalf("sora exchange rate=%f, want %f", sora.PriceSourceExchangeRate, USDCNYSnapshot)
	}
	if math.Abs(sora.InputCostUSD-0.10) > 0.000001 {
		t.Fatalf("sora source usd=%f, want %f", sora.InputCostUSD, 0.10)
	}
	var pricing model.ModelPricing
	if err := db.Where("model_id = ?", sora.ID).First(&pricing).Error; err != nil {
		t.Fatal(err)
	}
	wantSell := round6(0.10 * USDCNYSnapshot)
	if math.Abs(pricing.InputPriceRMB-wantSell) > 0.000001 {
		t.Fatalf("sora sell=%f, want %f", pricing.InputPriceRMB, wantSell)
	}

	var mappings int64
	db.Model(&model.ChannelModel{}).Where("channel_id = ?", ch.ID).Count(&mappings)
	if mappings != int64(len(wangsuVideoModels)) {
		t.Fatalf("channel mappings=%d, want %d", mappings, len(wangsuVideoModels))
	}

	RunSeedWangsuVideo(db)
	var channelCount int64
	db.Model(&model.Channel{}).Where("name = ?", wangsuVideoChannelName).Count(&channelCount)
	if channelCount != 1 {
		t.Fatalf("channel count after rerun=%d, want 1", channelCount)
	}
}

func newWangsuVideoSeedTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.Supplier{},
		&model.ModelCategory{},
		&model.AIModel{},
		&model.ModelPricing{},
		&model.Channel{},
		&model.ChannelModel{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}
