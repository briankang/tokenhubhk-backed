package modelalias

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/model"
)

func setupAliasServiceTest(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.Supplier{}, &model.ModelCategory{}, &model.AIModel{}, &model.ModelAlias{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewService(db), db
}

func seedAliasModel(t *testing.T, db *gorm.DB, supplierID uint, name string) {
	t.Helper()
	cat := model.ModelCategory{SupplierID: supplierID, Name: "LLM", Code: "llm"}
	if err := db.FirstOrCreate(&cat, model.ModelCategory{SupplierID: supplierID, Code: "llm"}).Error; err != nil {
		t.Fatalf("create category: %v", err)
	}
	item := model.AIModel{
		SupplierID:  supplierID,
		CategoryID:  cat.ID,
		ModelName:   name,
		DisplayName: name,
		IsActive:    true,
		Status:      "online",
		ModelType:   model.ModelTypeLLM,
		PricingUnit: model.UnitPerMillionTokens,
		Source:      "auto",
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("create model %s: %v", name, err)
	}
}

func TestResolveMapsAliasToTarget(t *testing.T) {
	svc, db := setupAliasServiceTest(t)
	sup := model.Supplier{Name: "Aliyun", Code: "aliyun_dashscope", IsActive: true, Status: "active", AccessType: "api"}
	if err := db.Create(&sup).Error; err != nil {
		t.Fatalf("create supplier: %v", err)
	}
	seedAliasModel(t, db, sup.ID, "qwen3.6-plus-2026-04-02")
	alias := &model.ModelAlias{
		AliasName:          "qwen3.6-plus",
		TargetModelName:    "qwen3.6-plus-2026-04-02",
		SupplierID:         sup.ID,
		AliasType:          model.ModelAliasTypeStable,
		ResolutionStrategy: model.ModelAliasResolutionFixed,
		Source:             model.ModelAliasSourceManual,
		Confidence:         1,
		IsActive:           true,
		IsPublic:           true,
	}
	if _, err := svc.Create(alias); err != nil {
		t.Fatalf("create alias: %v", err)
	}

	got, err := svc.Resolve("qwen3.6-plus")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !got.IsAlias || got.ResolvedModel != "qwen3.6-plus-2026-04-02" || got.Alias == nil {
		t.Fatalf("unexpected resolution: %#v", got)
	}
}

func TestResolveIgnoresInactiveAlias(t *testing.T) {
	svc, db := setupAliasServiceTest(t)
	if err := db.Create(&model.ModelAlias{
		AliasName:          "demo-latest",
		TargetModelName:    "demo-2026-04-01",
		AliasType:          model.ModelAliasTypeLatest,
		ResolutionStrategy: model.ModelAliasResolutionFixed,
		IsActive:           false,
		IsPublic:           true,
		Source:             model.ModelAliasSourceManual,
	}).Error; err != nil {
		t.Fatalf("create alias: %v", err)
	}
	if err := db.Model(&model.ModelAlias{}).Where("alias_name = ?", "demo-latest").Update("is_active", false).Error; err != nil {
		t.Fatalf("force alias inactive: %v", err)
	}

	got, err := svc.Resolve("demo-latest")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.IsAlias || got.ResolvedModel != "demo-latest" {
		t.Fatalf("inactive alias should not resolve: %#v", got)
	}
}

func TestResolveDetectsAliasLoop(t *testing.T) {
	svc, db := setupAliasServiceTest(t)
	for _, item := range []model.ModelAlias{
		{AliasName: "a", TargetModelName: "b", IsActive: true, IsPublic: true, AliasType: model.ModelAliasTypeCustom, Source: model.ModelAliasSourceManual},
		{AliasName: "b", TargetModelName: "a", IsActive: true, IsPublic: true, AliasType: model.ModelAliasTypeCustom, Source: model.ModelAliasSourceManual},
	} {
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("create alias: %v", err)
		}
	}
	if _, err := svc.Resolve("a"); err == nil {
		t.Fatalf("expected loop error")
	}
}

func TestSuggestAliasesFindsStableSnapshotPair(t *testing.T) {
	svc, db := setupAliasServiceTest(t)
	sup := model.Supplier{Name: "Aliyun", Code: "aliyun_dashscope", IsActive: true, Status: "active", AccessType: "api"}
	if err := db.Create(&sup).Error; err != nil {
		t.Fatalf("create supplier: %v", err)
	}
	seedAliasModel(t, db, sup.ID, "qwen3.6-plus")
	seedAliasModel(t, db, sup.ID, "qwen3.6-plus-2026-04-02")

	suggestions, err := svc.SuggestAliases(SuggestOptions{SupplierID: sup.ID})
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if len(suggestions) != 1 {
		t.Fatalf("suggestions = %#v", suggestions)
	}
	if suggestions[0].AliasName != "qwen3.6-plus" || suggestions[0].TargetModelName != "qwen3.6-plus-2026-04-02" {
		t.Fatalf("unexpected suggestion: %#v", suggestions[0])
	}
}
