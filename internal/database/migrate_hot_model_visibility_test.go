package database

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

func TestRunHotModelVisibilityMigration(t *testing.T) {
	db := newHotModelVisibilityTestDB(t)

	supplier := model.Supplier{Name: "Test Supplier", Code: "test_supplier", AccessType: "api", IsActive: true, Status: "active"}
	if err := db.Create(&supplier).Error; err != nil {
		t.Fatal(err)
	}
	category := model.ModelCategory{Name: "Chat", Code: "chat", SupplierID: supplier.ID}
	if err := db.Create(&category).Error; err != nil {
		t.Fatal(err)
	}

	models := []model.AIModel{
		{SupplierID: supplier.ID, CategoryID: category.ID, ModelName: "hot-5-star", IsActive: false, Status: "offline"},
		{SupplierID: supplier.ID, CategoryID: category.ID, ModelName: "warm-4-star", IsActive: true, Status: "online"},
		{SupplierID: supplier.ID, CategoryID: category.ID, ModelName: "missing-from-trending", IsActive: true, Status: "online"},
		{SupplierID: supplier.ID, CategoryID: category.ID, ModelName: "case-match-model", IsActive: false, Status: "error"},
	}
	if err := db.Create(&models).Error; err != nil {
		t.Fatal(err)
	}

	trending := []model.TrendingModel{
		{ModelName: "hot-5-star", DisplayName: "Hot", SupplierName: "Test", LaunchYearMonth: "2026-04", PopularityStars: 5, SourceURL: "https://example.com/hot", IsActive: true},
		{ModelName: "warm-4-star", DisplayName: "Warm", SupplierName: "Test", LaunchYearMonth: "2026-04", PopularityStars: 4, SourceURL: "https://example.com/warm", IsActive: true},
		{ModelName: "CASE-MATCH-MODEL", DisplayName: "Case", SupplierName: "Test", LaunchYearMonth: "2026-04", PopularityStars: 5, SourceURL: "https://example.com/case", IsActive: true},
	}
	if err := db.Create(&trending).Error; err != nil {
		t.Fatal(err)
	}

	if err := RunHotModelVisibilityMigration(db); err != nil {
		t.Fatalf("RunHotModelVisibilityMigration: %v", err)
	}

	assertModelVisibility(t, db, "hot-5-star", true, "online")
	assertModelVisibility(t, db, "case-match-model", true, "online")
	assertModelVisibility(t, db, "warm-4-star", false, "offline")
	assertModelVisibility(t, db, "missing-from-trending", false, "offline")
}

func TestRunHotModelVisibilityMigrationSkipsWhenReferenceIsEmpty(t *testing.T) {
	db := newHotModelVisibilityTestDB(t)

	supplier := model.Supplier{Name: "Test Supplier", Code: "test_supplier", AccessType: "api", IsActive: true, Status: "active"}
	if err := db.Create(&supplier).Error; err != nil {
		t.Fatal(err)
	}
	category := model.ModelCategory{Name: "Chat", Code: "chat", SupplierID: supplier.ID}
	if err := db.Create(&category).Error; err != nil {
		t.Fatal(err)
	}
	ai := model.AIModel{SupplierID: supplier.ID, CategoryID: category.ID, ModelName: "existing-online", IsActive: true, Status: "online"}
	if err := db.Create(&ai).Error; err != nil {
		t.Fatal(err)
	}

	if err := RunHotModelVisibilityMigration(db); err != nil {
		t.Fatalf("RunHotModelVisibilityMigration: %v", err)
	}

	assertModelVisibility(t, db, "existing-online", true, "online")
}

func newHotModelVisibilityTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Supplier{}, &model.ModelCategory{}, &model.AIModel{}, &model.TrendingModel{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func assertModelVisibility(t *testing.T, db *gorm.DB, modelName string, wantActive bool, wantStatus string) {
	t.Helper()
	var ai model.AIModel
	if err := db.Where("model_name = ?", modelName).First(&ai).Error; err != nil {
		t.Fatal(err)
	}
	if ai.IsActive != wantActive || ai.Status != wantStatus {
		t.Fatalf("%s visibility active/status=%v/%s, want %v/%s", modelName, ai.IsActive, ai.Status, wantActive, wantStatus)
	}
}
