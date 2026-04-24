package database

import (
	"encoding/json"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/service/pricescraper"
)

func TestAliyunDeprecationNoticeModels(t *testing.T) {
	deprecated := pricescraper.GetAliyunDeprecatedModels()

	cases := []struct {
		name       string
		retireDate string
	}{
		{"qwen-max", "2026-07-13"},
		{"qwen-vl-plus", "2026-07-13"},
		{"qwen-max-latest", "2026-05-13"},
		{"qwen-vl-max-2025-04-08", "2026-05-13"},
		{"qwen2.5-coder-32b-instruct", "2026-05-13"},
		{"qwen3-4b", "2026-05-13"},
		{"qwen2.5-coder-3b-instruct", "2026-05-13"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dep, ok := deprecated[tc.name]
			if !ok {
				t.Fatalf("missing deprecated model %s", tc.name)
			}
			if dep.RetireDate != tc.retireDate {
				t.Fatalf("RetireDate=%s, want %s", dep.RetireDate, tc.retireDate)
			}
			if dep.Replacement != "qwen3.6-plus" {
				t.Fatalf("Replacement=%s, want qwen3.6-plus", dep.Replacement)
			}
		})
	}

	if _, ok := deprecated["qwen-plus"]; ok {
		t.Fatalf("qwen-plus must stay available; it is not in the 2026-04-13 Alibaba notices")
	}
}

func TestRunAliyunDeprecationMigration(t *testing.T) {
	db := newAliyunDeprecationTestDB(t)

	supplier := model.Supplier{Name: "Alibaba Cloud Bailian", Code: "aliyun_dashscope", AccessType: "api", IsActive: true}
	if err := db.Create(&supplier).Error; err != nil {
		t.Fatal(err)
	}
	category := model.ModelCategory{Name: "Qwen", Code: "qwen_chat", SupplierID: supplier.ID}
	if err := db.Create(&category).Error; err != nil {
		t.Fatal(err)
	}

	models := []model.AIModel{
		{SupplierID: supplier.ID, CategoryID: category.ID, ModelName: "qwen-max", DisplayName: "Qwen Max", IsActive: true, Status: "online", SupplierStatus: "Active"},
		{SupplierID: supplier.ID, CategoryID: category.ID, ModelName: "qwen2.5-coder-32b-instruct", IsActive: true, Status: "online", SupplierStatus: "Active"},
		{SupplierID: supplier.ID, CategoryID: category.ID, ModelName: "qwen-plus", IsActive: true, Status: "online", SupplierStatus: "Active"},
	}
	if err := db.Create(&models).Error; err != nil {
		t.Fatal(err)
	}

	channelModels, _ := json.Marshal([]string{"qwen-max", "qwen-plus", "qwen2.5-coder-32b-instruct"})
	channel := model.Channel{
		Name:       "aliyun",
		SupplierID: supplier.ID,
		Type:       "openai",
		Endpoint:   "https://dashscope.aliyuncs.com/compatible-mode/v1",
		APIKey:     "test",
		Models:     model.JSON(channelModels),
		Status:     "active",
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}

	if err := RunAliyunDeprecationMigration(db); err != nil {
		t.Fatal(err)
	}

	var retired model.AIModel
	if err := db.Where("model_name = ?", "qwen-max").First(&retired).Error; err != nil {
		t.Fatal(err)
	}
	if retired.Status != "offline" || retired.IsActive || retired.SupplierStatus != "Deprecated" {
		t.Fatalf("qwen-max not retired correctly: status=%s active=%v supplier_status=%s", retired.Status, retired.IsActive, retired.SupplierStatus)
	}

	var kept model.AIModel
	if err := db.Where("model_name = ?", "qwen-plus").First(&kept).Error; err != nil {
		t.Fatal(err)
	}
	if kept.Status != "online" || !kept.IsActive || kept.SupplierStatus != "Active" {
		t.Fatalf("qwen-plus should stay available: status=%s active=%v supplier_status=%s", kept.Status, kept.IsActive, kept.SupplierStatus)
	}

	var updatedChannel model.Channel
	if err := db.First(&updatedChannel, channel.ID).Error; err != nil {
		t.Fatal(err)
	}
	var remaining []string
	if err := json.Unmarshal(updatedChannel.Models, &remaining); err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0] != "qwen-plus" {
		t.Fatalf("channel models=%v, want [qwen-plus]", remaining)
	}
}

func newAliyunDeprecationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Supplier{}, &model.ModelCategory{}, &model.AIModel{}, &model.Channel{}); err != nil {
		t.Fatal(err)
	}
	return db
}
