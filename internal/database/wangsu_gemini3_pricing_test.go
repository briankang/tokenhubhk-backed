package database

import (
	"encoding/json"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

func TestWangsuGemini3ProDefinitionsUseOfficialPricingAndModalities(t *testing.T) {
	for _, modelName := range []string{"gemini.gemini-3-pro-preview", "gemini.gemini-3.1-pro-preview"} {
		def := mustFindWangsuModel(t, modelName)

		if def.ModelType != "VLM" {
			t.Fatalf("%s model type=%q, want VLM", modelName, def.ModelType)
		}
		if def.ContextWindow != 1048576 {
			t.Fatalf("%s context window=%d, want 1048576", modelName, def.ContextWindow)
		}
		assertFloat(t, def.InputUSDPerM, 2)
		assertFloat(t, def.OutputUSDPerM, 12)
		assertFloat(t, def.CacheReadUSDPerM, 0.2)
		assertFloat(t, def.CacheExplicitReadUSDPerM, 0.2)
		assertFloat(t, def.CacheStorageUSDPerMHour, 4.5)
		if def.CacheMechanism != "both" || def.CacheMinTokens != 4096 {
			t.Fatalf("%s cache config=%s/%d, want both/4096", modelName, def.CacheMechanism, def.CacheMinTokens)
		}
		assertStringSet(t, def.InputModalities, []string{"text", "image", "video", "audio", "pdf"})
		assertStringSet(t, def.OutputModalities, []string{"text"})

		if len(def.PriceTiersUSD) != 2 {
			t.Fatalf("%s tiers=%d, want 2", modelName, len(def.PriceTiersUSD))
		}
		assertGemini3ProTier(t, def.PriceTiersUSD[0], "<=200K", 200000, 2, 12, 0.2)
		assertGemini3ProTier(t, def.PriceTiersUSD[1], ">200K", 0, 4, 18, 0.4)
	}
}

func TestRunWangsuOfficialPricingMigrationBackfillsGemini3ProCacheAndModalities(t *testing.T) {
	db := newWangsuOfficialPricingTestDB(t)
	sup := model.Supplier{Name: "Wangsu", Code: "wangsu_aigw", AccessType: "api"}
	if err := db.Create(&sup).Error; err != nil {
		t.Fatal(err)
	}
	cat := model.ModelCategory{Name: "Gemini", Code: "gemini", SupplierID: sup.ID}
	if err := db.Create(&cat).Error; err != nil {
		t.Fatal(err)
	}

	for _, modelName := range []string{"gemini.gemini-3-pro-preview", "gemini.gemini-3.1-pro-preview"} {
		ai := model.AIModel{
			SupplierID: sup.ID, CategoryID: cat.ID,
			ModelName: modelName, ModelType: "VLM", PricingUnit: model.UnitPerMillionTokens,
			InputCostRMB: 1, OutputCostRMB: 1,
			SupportsCache: true, CacheMechanism: "auto", CacheMinTokens: 0,
			Currency: "CREDIT",
		}
		if err := db.Create(&ai).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.ModelPricing{ModelID: ai.ID, Currency: "CREDIT"}).Error; err != nil {
			t.Fatal(err)
		}
	}

	RunWangsuOfficialPricingMigration(db)
	RunPriceTierSellingSyncMigration(db)

	for _, modelName := range []string{"gemini.gemini-3-pro-preview", "gemini.gemini-3.1-pro-preview"} {
		var ai model.AIModel
		if err := db.Where("model_name = ?", modelName).First(&ai).Error; err != nil {
			t.Fatal(err)
		}
		assertFloat(t, ai.InputCostUSD, 2)
		assertFloat(t, ai.OutputCostUSD, 12)
		assertFloat(t, ai.CacheInputPriceUSD, 0.2)
		assertFloat(t, ai.CacheExplicitInputPriceUSD, 0.2)
		assertFloat(t, ai.CacheStoragePriceUSD, 4.5)
		assertFloat(t, ai.CacheInputPriceRMB, round6(0.2*USDCNYSnapshot))
		assertFloat(t, ai.CacheExplicitInputPriceRMB, round6(0.2*USDCNYSnapshot))
		assertFloat(t, ai.CacheStoragePriceRMB, round6(4.5*USDCNYSnapshot))
		if ai.ContextWindow != 1048576 || ai.MaxInputTokens != 1048576 {
			t.Fatalf("%s context/max input=%d/%d, want 1048576/1048576", modelName, ai.ContextWindow, ai.MaxInputTokens)
		}
		if ai.CacheMechanism != "both" || ai.CacheMinTokens != 4096 {
			t.Fatalf("%s cache config=%s/%d, want both/4096", modelName, ai.CacheMechanism, ai.CacheMinTokens)
		}
		assertJSONStrings(t, ai.InputModalities, []string{"text", "image", "video", "audio", "pdf"})
		assertJSONStrings(t, ai.OutputModalities, []string{"text"})

		var tiers model.PriceTiersData
		if err := json.Unmarshal(ai.PriceTiers, &tiers); err != nil {
			t.Fatal(err)
		}
		if len(tiers.Tiers) != 2 {
			t.Fatalf("%s cost tiers=%d, want 2", modelName, len(tiers.Tiers))
		}
		assertFloat(t, tiers.Tiers[0].InputPrice, round6(2*USDCNYSnapshot))
		assertFloat(t, tiers.Tiers[0].OutputPrice, round6(12*USDCNYSnapshot))
		assertFloat(t, tiers.Tiers[0].CacheInputPrice, round6(0.2*USDCNYSnapshot))
		assertFloat(t, tiers.Tiers[1].InputPrice, round6(4*USDCNYSnapshot))
		assertFloat(t, tiers.Tiers[1].OutputPrice, round6(18*USDCNYSnapshot))
		assertFloat(t, tiers.Tiers[1].CacheInputPrice, round6(0.4*USDCNYSnapshot))
	}
}

func mustFindWangsuModel(t *testing.T, modelName string) WangsuModelCapability {
	t.Helper()
	for _, def := range wangsuModels {
		if def.ModelName == modelName {
			return def
		}
	}
	t.Fatalf("missing wangsu model %s", modelName)
	return WangsuModelCapability{}
}

func assertGemini3ProTier(t *testing.T, tier PriceTierUSD, label string, maxInput int, input, output, cache float64) {
	t.Helper()
	if tier.Label != label || tier.MaxInputTokens != maxInput {
		t.Fatalf("tier identity=%s/%d, want %s/%d", tier.Label, tier.MaxInputTokens, label, maxInput)
	}
	assertFloat(t, tier.InputUSDPerM, input)
	assertFloat(t, tier.OutputUSDPerM, output)
	assertFloat(t, tier.CacheReadUSDPerM, cache)
}

func assertStringSet(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("strings=%v, want %v", got, want)
	}
	seen := map[string]bool{}
	for _, v := range got {
		seen[v] = true
	}
	for _, v := range want {
		if !seen[v] {
			t.Fatalf("strings=%v, missing %q", got, v)
		}
	}
}

func assertJSONStrings(t *testing.T, raw model.JSON, want []string) {
	t.Helper()
	var got []string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	assertStringSet(t, got, want)
}

func newWangsuOfficialPricingTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Supplier{}, &model.ModelCategory{}, &model.AIModel{}, &model.ModelPricing{}); err != nil {
		t.Fatal(err)
	}
	return db
}
