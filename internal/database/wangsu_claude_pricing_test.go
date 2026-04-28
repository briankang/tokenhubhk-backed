package database

import (
	"encoding/json"
	"testing"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

func TestWangsuClaudeDefinitionsUseCurrentOfficialPricingAndCache(t *testing.T) {
	cases := []struct {
		modelName       string
		inputUSD        float64
		outputUSD       float64
		cacheReadUSD    float64
		cacheWriteUSD   float64
		cacheMinTokens  int
		contextWindow   int
		wantTieredPrice bool
	}{
		{"anthropic.claude-haiku-4-5", 1, 5, 0.10, 1.25, 4096, 200000, false},
		{"anthropic.claude-sonnet-4-5", 3, 15, 0.30, 3.75, 1024, 200000, false},
		{"anthropic.claude-sonnet-4-6", 3, 15, 0.30, 3.75, 2048, 1000000, false},
		{"anthropic.claude-opus-4-5", 5, 25, 0.50, 6.25, 4096, 200000, false},
		{"anthropic.claude-opus-4-6", 5, 25, 0.50, 6.25, 4096, 1000000, false},
		{"anthropic.claude-opus-4-7", 5, 25, 0.50, 6.25, 4096, 1000000, false},
	}

	for _, tc := range cases {
		t.Run(tc.modelName, func(t *testing.T) {
			def := mustFindWangsuModel(t, tc.modelName)

			assertFloat(t, def.InputUSDPerM, tc.inputUSD)
			assertFloat(t, def.OutputUSDPerM, tc.outputUSD)
			assertFloat(t, def.CacheReadUSDPerM, tc.cacheReadUSD)
			assertFloat(t, def.CacheWriteUSDPerM, tc.cacheWriteUSD)
			if def.CacheMechanism != "explicit" || def.CacheMinTokens != tc.cacheMinTokens {
				t.Fatalf("cache config=%s/%d, want explicit/%d", def.CacheMechanism, def.CacheMinTokens, tc.cacheMinTokens)
			}
			if def.ContextWindow != tc.contextWindow {
				t.Fatalf("context window=%d, want %d", def.ContextWindow, tc.contextWindow)
			}
			if gotTiered := len(def.PriceTiersUSD) > 0; gotTiered != tc.wantTieredPrice {
				t.Fatalf("tiered price=%v, want %v", gotTiered, tc.wantTieredPrice)
			}
		})
	}
}

func TestRunWangsuOfficialPricingMigrationBackfillsClaudePricing(t *testing.T) {
	db := newWangsuOfficialPricingTestDB(t)
	sup := model.Supplier{Name: "Wangsu", Code: "wangsu_aigw", AccessType: "api"}
	if err := db.Create(&sup).Error; err != nil {
		t.Fatal(err)
	}
	cat := model.ModelCategory{Name: "Claude", Code: "claude", SupplierID: sup.ID}
	if err := db.Create(&cat).Error; err != nil {
		t.Fatal(err)
	}

	for _, modelName := range []string{
		"anthropic.claude-haiku-4-5",
		"anthropic.claude-sonnet-4-6",
		"anthropic.claude-opus-4-5",
		"anthropic.claude-opus-4-6",
		"anthropic.claude-opus-4-7",
	} {
		ai := model.AIModel{
			SupplierID: sup.ID, CategoryID: cat.ID,
			ModelName: modelName, ModelType: "LLM", PricingUnit: model.UnitPerMillionTokens,
			InputCostUSD: 15, OutputCostUSD: 75,
			InputCostRMB: 1, OutputCostRMB: 1,
			SupportsCache: true, CacheMechanism: "explicit", CacheMinTokens: 1024,
			PriceTiers: mustDatabaseJSON(t, []map[string]any{{"label": "stale", "input_price_rmb": 1}}),
			Currency:   "CREDIT",
		}
		if err := db.Create(&ai).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.ModelPricing{ModelID: ai.ID, Currency: "CREDIT", PriceTiers: ai.PriceTiers}).Error; err != nil {
			t.Fatal(err)
		}
	}

	RunWangsuOfficialPricingMigration(db)

	assertClaudeMigrationRow(t, db, "anthropic.claude-haiku-4-5", 1, 5, 0.10, 1.25, 4096, 200000)
	assertClaudeMigrationRow(t, db, "anthropic.claude-sonnet-4-6", 3, 15, 0.30, 3.75, 2048, 1000000)
	assertClaudeMigrationRow(t, db, "anthropic.claude-opus-4-5", 5, 25, 0.50, 6.25, 4096, 200000)
	assertClaudeMigrationRow(t, db, "anthropic.claude-opus-4-6", 5, 25, 0.50, 6.25, 4096, 1000000)
	assertClaudeMigrationRow(t, db, "anthropic.claude-opus-4-7", 5, 25, 0.50, 6.25, 4096, 1000000)
}

func assertClaudeMigrationRow(t *testing.T, db *gorm.DB, modelName string, input, output, cacheRead, cacheWrite float64, minTokens, contextWindow int) {
	t.Helper()
	var ai model.AIModel
	if err := db.Where("model_name = ?", modelName).First(&ai).Error; err != nil {
		t.Fatal(err)
	}
	assertFloat(t, ai.InputCostUSD, input)
	assertFloat(t, ai.OutputCostUSD, output)
	assertFloat(t, ai.CacheInputPriceUSD, cacheRead)
	assertFloat(t, ai.CacheWritePriceUSD, cacheWrite)
	if ai.CacheMinTokens != minTokens || ai.ContextWindow != contextWindow {
		t.Fatalf("%s cache/context=%d/%d, want %d/%d", modelName, ai.CacheMinTokens, ai.ContextWindow, minTokens, contextWindow)
	}
	if len(ai.PriceTiers) > 0 && string(ai.PriceTiers) != "null" {
		var raw any
		if err := json.Unmarshal(ai.PriceTiers, &raw); err != nil || raw != nil {
			t.Fatalf("%s stale tiers not cleared: %s", modelName, string(ai.PriceTiers))
		}
	}
}

func mustDatabaseJSON(t *testing.T, v any) model.JSON {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return model.JSON(b)
}
