package pricing

import (
	"context"
	"sync"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

func TestGetPlatformPriceUsesLocalCache(t *testing.T) {
	platformPriceLocalCache = sync.Map{}
	t.Cleanup(func() {
		platformPriceLocalCache = sync.Map{}
	})

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.ModelPricing{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Create(&model.ModelPricing{ModelID: 701, InputPricePerToken: 1, OutputPricePerToken: 2}).Error; err != nil {
		t.Fatalf("create pricing: %v", err)
	}

	calc := NewPricingCalculator(db)
	first, err := calc.getPlatformPrice(context.Background(), 701)
	if err != nil {
		t.Fatalf("first get: %v", err)
	}
	if first.OutputPricePerToken != 2 {
		t.Fatalf("unexpected first price: %+v", first)
	}

	if err := db.Where("model_id = ?", 701).Delete(&model.ModelPricing{}).Error; err != nil {
		t.Fatalf("delete pricing: %v", err)
	}
	second, err := calc.getPlatformPrice(context.Background(), 701)
	if err != nil {
		t.Fatalf("second get should use cache: %v", err)
	}
	if second.OutputPricePerToken != 2 {
		t.Fatalf("unexpected cached price: %+v", second)
	}
}
