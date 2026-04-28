package database

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/model"
)

func setupModelAliasSeedTest(t *testing.T) *gorm.DB {
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
	return db
}

func seedAliasTargetModel(t *testing.T, db *gorm.DB, supplierCode, modelName string) {
	t.Helper()
	sup := model.Supplier{
		Name:       supplierCode,
		Code:       supplierCode,
		IsActive:   true,
		Status:     "active",
		AccessType: "api",
	}
	if err := db.FirstOrCreate(&sup, model.Supplier{Code: supplierCode, AccessType: "api"}).Error; err != nil {
		t.Fatalf("create supplier %s: %v", supplierCode, err)
	}
	cat := model.ModelCategory{SupplierID: sup.ID, Name: "LLM", Code: supplierCode + "_llm"}
	if err := db.FirstOrCreate(&cat, model.ModelCategory{SupplierID: sup.ID, Code: cat.Code}).Error; err != nil {
		t.Fatalf("create category %s: %v", cat.Code, err)
	}
	item := model.AIModel{
		SupplierID:  sup.ID,
		CategoryID:  cat.ID,
		ModelName:   modelName,
		DisplayName: modelName,
		IsActive:    true,
		Status:      "online",
		ModelType:   model.ModelTypeLLM,
		PricingUnit: model.UnitPerMillionTokens,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("create model %s: %v", modelName, err)
	}
}

func assertSeededAlias(t *testing.T, db *gorm.DB, aliasName, targetName, aliasType string) {
	t.Helper()
	var alias model.ModelAlias
	if err := db.Where("alias_name = ?", aliasName).First(&alias).Error; err != nil {
		t.Fatalf("find alias %s: %v", aliasName, err)
	}
	if alias.TargetModelName != targetName || alias.AliasType != aliasType || !alias.IsActive || !alias.IsPublic {
		t.Fatalf("alias %s = %+v, want target=%s type=%s active/public", aliasName, alias, targetName, aliasType)
	}
	if alias.Source != model.ModelAliasSourceRule || alias.ResolutionStrategy != model.ModelAliasResolutionFixed {
		t.Fatalf("alias %s source/strategy = %s/%s", aliasName, alias.Source, alias.ResolutionStrategy)
	}
}

func TestRunSeedModelAliasesCreatesCommonAliasesForAvailableTargets(t *testing.T) {
	db := setupModelAliasSeedTest(t)
	seededTargets := map[string]bool{}
	for _, def := range defaultModelAliasDefs {
		if seededTargets[def.TargetModelName] {
			continue
		}
		seedAliasTargetModel(t, db, "default_supplier", def.TargetModelName)
		seededTargets[def.TargetModelName] = true
	}

	RunSeedModelAliases(db)

	for _, def := range defaultModelAliasDefs {
		assertSeededAlias(t, db, def.AliasName, def.TargetModelName, def.AliasType)
	}

	var count int64
	if err := db.Model(&model.ModelAlias{}).Count(&count).Error; err != nil {
		t.Fatalf("count aliases: %v", err)
	}
	if int(count) != len(defaultModelAliasDefs) {
		t.Fatalf("alias count=%d want %d", count, len(defaultModelAliasDefs))
	}
}

func TestRunSeedModelAliasesSkipsAliasesWhenTargetModelIsMissing(t *testing.T) {
	db := setupModelAliasSeedTest(t)
	seedAliasTargetModel(t, db, "openai", "gpt-4o")

	RunSeedModelAliases(db)

	var count int64
	if err := db.Model(&model.ModelAlias{}).Where("alias_name = ?", "qwen-latest").Count(&count).Error; err != nil {
		t.Fatalf("count qwen-latest: %v", err)
	}
	if count != 0 {
		t.Fatalf("qwen-latest should be skipped when target is missing, count=%d", count)
	}
	assertSeededAlias(t, db, "gpt-4", "gpt-4o", model.ModelAliasTypeCompat)
}

func TestRunSeedModelAliasesIsIdempotentAndPreservesExistingAlias(t *testing.T) {
	db := setupModelAliasSeedTest(t)
	seedAliasTargetModel(t, db, "aliyun_dashscope", "qwen-plus")

	existing := model.ModelAlias{
		AliasName:          "qwen",
		TargetModelName:    "custom-qwen-target",
		AliasType:          model.ModelAliasTypeCustom,
		ResolutionStrategy: model.ModelAliasResolutionManual,
		IsPublic:           false,
		IsActive:           true,
		Source:             model.ModelAliasSourceManual,
		Confidence:         0.5,
		Notes:              "admin managed",
	}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatalf("create existing alias: %v", err)
	}
	if err := db.Model(&model.ModelAlias{}).Where("alias_name = ?", "qwen").Update("is_public", false).Error; err != nil {
		t.Fatalf("force existing alias private: %v", err)
	}

	RunSeedModelAliases(db)
	RunSeedModelAliases(db)

	var aliases []model.ModelAlias
	if err := db.Where("alias_name = ?", "qwen").Find(&aliases).Error; err != nil {
		t.Fatalf("find qwen aliases: %v", err)
	}
	if len(aliases) != 1 {
		t.Fatalf("qwen aliases len=%d want 1: %+v", len(aliases), aliases)
	}
	got := aliases[0]
	if got.TargetModelName != "custom-qwen-target" || got.Source != model.ModelAliasSourceManual || got.IsPublic {
		t.Fatalf("existing alias was overwritten: %+v", got)
	}
}
