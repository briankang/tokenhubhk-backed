package database

import (
	"encoding/json"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

func TestRunModelCapabilityDefaultsMigration(t *testing.T) {
	db := newCapabilityMigrationTestDB(t)

	aliyun := model.Supplier{Name: "阿里云百炼", Code: "aliyun_dashscope", AccessType: "api", IsActive: true}
	tencent := model.Supplier{Name: "腾讯混元", Code: "tencent_hunyuan", AccessType: "api", IsActive: true}
	if err := db.Create(&aliyun).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&tencent).Error; err != nil {
		t.Fatal(err)
	}
	cat := model.ModelCategory{Name: "chat", Code: "chat", SupplierID: aliyun.ID}
	if err := db.Create(&cat).Error; err != nil {
		t.Fatal(err)
	}

	overBroad, _ := json.Marshal(map[string]bool{
		"supports_thinking":      true,
		"supports_web_search":    true,
		"supports_vision":        true,
		"supports_function_call": true,
		"supports_json_mode":     true,
	})
	models := []model.AIModel{
		{SupplierID: aliyun.ID, CategoryID: cat.ID, ModelName: "qwen-turbo", ModelType: "LLM", Features: overBroad},
		{SupplierID: aliyun.ID, CategoryID: cat.ID, ModelName: "qwen-vl-plus", ModelType: "VLM"},
		{SupplierID: aliyun.ID, CategoryID: cat.ID, ModelName: "qwen3-30b-a3b-thinking-2507", ModelType: "LLM"},
		{SupplierID: tencent.ID, CategoryID: cat.ID, ModelName: "hunyuan-2.0-thinking-20251109", ModelType: "LLM"},
		{SupplierID: aliyun.ID, CategoryID: cat.ID, ModelName: "qwen-image-plus", ModelType: model.ModelTypeImageGeneration, Features: overBroad},
	}
	if err := db.Create(&models).Error; err != nil {
		t.Fatal(err)
	}

	if err := RunModelCapabilityDefaultsMigration(db); err != nil {
		t.Fatal(err)
	}

	assertFeature := func(modelName, key string, want bool) {
		t.Helper()
		var m model.AIModel
		if err := db.Where("model_name = ?", modelName).First(&m).Error; err != nil {
			t.Fatal(err)
		}
		var feats map[string]bool
		_ = json.Unmarshal(m.Features, &feats)
		if feats[key] != want {
			t.Fatalf("%s %s=%v, want %v; features=%s", modelName, key, feats[key], want, string(m.Features))
		}
	}

	assertFeature("qwen-turbo", "supports_function_call", true)
	assertFeature("qwen-turbo", "supports_json_mode", true)
	assertFeature("qwen-turbo", "supports_web_search", false)
	assertFeature("qwen-turbo", "supports_vision", false)
	assertFeature("qwen-vl-plus", "supports_vision", true)
	assertFeature("qwen3-30b-a3b-thinking-2507", "supports_thinking", true)
	assertFeature("hunyuan-2.0-thinking-20251109", "supports_web_search", true)
	assertFeature("qwen-image-plus", "supports_function_call", false)

	var sup model.Supplier
	if err := db.Where("code = ?", "aliyun_dashscope").First(&sup).Error; err != nil {
		t.Fatal(err)
	}
	var defaults map[string]bool
	_ = json.Unmarshal(sup.DefaultFeatures, &defaults)
	if defaults["supports_vision"] || defaults["supports_web_search"] || defaults["supports_thinking"] {
		t.Fatalf("aliyun defaults too broad: %s", string(sup.DefaultFeatures))
	}
	if !defaults["supports_function_call"] || !defaults["supports_json_mode"] {
		t.Fatalf("aliyun defaults missing conservative capabilities: %s", string(sup.DefaultFeatures))
	}
}

func newCapabilityMigrationTestDB(t *testing.T) *gorm.DB {
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
