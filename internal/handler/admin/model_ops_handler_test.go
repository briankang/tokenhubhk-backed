package admin

import (
	"math"
	"strings"
	"testing"
	"time"

	"tokenhub-server/internal/model"
)

func TestModelOpsProfileCacheExpires(t *testing.T) {
	h := NewModelOpsHandler(nil)
	payload := modelOpsListResponse{Total: 3, Page: 1, PageSize: 30}

	h.setProfileCache("page=1", payload)
	got, ok := h.getProfileCache("page=1")
	if !ok || got.Total != payload.Total {
		t.Fatalf("expected cache hit, ok=%v total=%d", ok, got.Total)
	}

	h.profileCache["page=1"] = modelOpsCacheEntry{expiresAt: time.Now().Add(-time.Second), payload: payload}
	if _, ok := h.getProfileCache("page=1"); ok {
		t.Fatalf("expected expired cache miss")
	}
}

func TestBatchModelStatePatchBringsEnabledModelsOnline(t *testing.T) {
	enablePatch := batchModelStatePatch("enable")
	if enablePatch["is_active"] != true || enablePatch["status"] != "online" {
		t.Fatalf("enable patch = %#v, want is_active=true and status=online", enablePatch)
	}

	publicPatch := batchModelStatePatch("set_public")
	if publicPatch["is_active"] != true || publicPatch["status"] != "online" {
		t.Fatalf("set_public patch = %#v, want is_active=true and status=online", publicPatch)
	}

	disablePatch := batchModelStatePatch("disable")
	if disablePatch["is_active"] != false || disablePatch["status"] != "offline" {
		t.Fatalf("disable patch = %#v, want is_active=false and status=offline", disablePatch)
	}
}

func TestDescribeBatchChangeUsesReadableChineseStatus(t *testing.T) {
	item := model.AIModel{ModelName: "doubao-seed-2.0-code", IsActive: false, Status: "offline"}

	before, after, desc, warnings, ok := describeBatchChange(modelOpsBatchRequest{Action: "enable"}, item)

	if !ok || len(warnings) != 0 {
		t.Fatalf("ok=%v warnings=%v", ok, warnings)
	}
	if before != "停用/offline" || after != "已启用并上线" {
		t.Fatalf("before/after = %q/%q", before, after)
	}
	if !strings.Contains(desc, "启用模型") {
		t.Fatalf("description = %q", desc)
	}
}

func TestBuildModelOpsProfileTreatsOnlineActiveModelAsPublicCandidate(t *testing.T) {
	item := model.AIModel{
		BaseModel:     model.BaseModel{ID: 22},
		ModelName:     "doubao-seed-2.0-code",
		DisplayName:   "Doubao Seed 2.0 Code",
		ModelType:     model.ModelTypeLLM,
		PricingUnit:   model.UnitPerMillionTokens,
		Status:        "online",
		IsActive:      true,
		InputCostRMB:  1,
		OutputCostRMB: 2,
		Supplier:      model.Supplier{Name: "TD-火山引擎", Code: "talkingdata", Discount: 1},
		Category:      model.ModelCategory{Name: "文本模型"},
		Pricing:       &model.ModelPricing{InputPriceRMB: 1.2, OutputPriceRMB: 2.4, Currency: "CREDIT"},
	}

	got := buildModelOpsProfile(item, []model.ModelLabel{{ModelID: 22, LabelKey: "tag", LabelValue: "Doubao"}}, modelOpsRouteSummary{Total: 1, Active: 1, Healthy: 1}, modelOpsUsageSummary{})

	if got.HealthStatus != "healthy" {
		t.Fatalf("health status = %s", got.HealthStatus)
	}
	if got.PublicStatus != "visible" {
		t.Fatalf("public status = %s", got.PublicStatus)
	}
}

func TestClassifyCalculatorSeedance20UsesDedicatedFormula(t *testing.T) {
	item := model.AIModel{
		ModelName:     "doubao-seedance-2.0-1080p",
		ModelType:     model.ModelTypeVideoGeneration,
		PricingUnit:   model.UnitPerMillionTokens,
		OutputCostRMB: 51,
	}

	calculatorType, status, hint, notes := classifyCalculator(item)

	if calculatorType != "volc_seedance_2_video_formula" {
		t.Fatalf("calculator type = %s", calculatorType)
	}
	if status != "bound" {
		t.Fatalf("status = %s", status)
	}
	if hint == "" || len(notes) == 0 {
		t.Fatalf("expected Seedance 2.0 hint and compatibility notes")
	}
}

func TestBuildModelOpsProfileExposesSupplierPricingURL(t *testing.T) {
	item := model.AIModel{
		BaseModel:   model.BaseModel{ID: 9},
		ModelName:   "qianfan-singlepicocr",
		DisplayName: "qianfan-singlepicocr",
		ModelType:   model.ModelTypeVision,
		PricingUnit: model.UnitPerImage,
		Status:      "online",
		IsActive:    true,
		Supplier: model.Supplier{
			Name:       "百度千帆",
			Code:       "qianfan",
			PricingURL: "https://cloud.baidu.com/doc/qianfan-docs/s/Jm8r1826a",
			Discount:   1,
		},
		Category: model.ModelCategory{Name: "视觉多模态"},
	}

	got := buildModelOpsProfile(item, nil, modelOpsRouteSummary{}, modelOpsUsageSummary{})

	if got.PricingURL != item.Supplier.PricingURL {
		t.Fatalf("pricing url = %q, want %q", got.PricingURL, item.Supplier.PricingURL)
	}
}

func TestBuildPriceSummaryExposesCreditFields(t *testing.T) {
	item := model.AIModel{
		InputPricePerToken:    120,
		OutputPricePerToken:   240,
		OutputCostThinkingRMB: 3.5,
		InputCostRMB:          1.2,
		OutputCostRMB:         2.4,
		Currency:              "CREDIT",
		Supplier:              model.Supplier{Discount: 0.8},
		Pricing: &model.ModelPricing{
			InputPricePerToken:          180,
			OutputPricePerToken:         360,
			OutputPriceThinkingPerToken: 420,
			InputPriceRMB:               1.8,
			OutputPriceRMB:              3.6,
			OutputPriceThinkingRMB:      4.2,
			Currency:                    "CREDIT",
		},
	}

	got := buildPriceSummary(item)

	if got.OfficialInputCredits != 120 || got.OfficialOutputCredits != 240 {
		t.Fatalf("official credit prices = %d/%d", got.OfficialInputCredits, got.OfficialOutputCredits)
	}
	if got.SellingInputCredits != 180 || got.SellingOutputCredits != 360 || got.SellingOutputThinkingCredits != 420 {
		t.Fatalf("selling credit prices = %d/%d/%d", got.SellingInputCredits, got.SellingOutputCredits, got.SellingOutputThinkingCredits)
	}
	if got.Currency != "CREDIT" {
		t.Fatalf("currency = %s", got.Currency)
	}
}

func TestDiscountedCostForModelOpsUsageKeepsTinyRequestCost(t *testing.T) {
	item := model.AIModel{
		InputCostRMB:  0.8467,
		OutputCostRMB: 3.3867,
		Discount:      0.795,
		Supplier:      model.Supplier{Discount: 1},
	}
	usage := usageAgg{
		PromptTokens:     13,
		CompletionTokens: 3,
	}

	got := discountedCostForModelOpsUsage(item, usage)
	want := (0.8467*13 + 3.3867*3) / 1_000_000 * 0.795

	if got <= 0 {
		t.Fatalf("discounted cost should keep fractional RMB cost, got %f", got)
	}
	if math.Abs(got-want) > 0.000000001 {
		t.Fatalf("discounted cost = %.12f, want %.12f", got, want)
	}
}

func TestDiscountedCostForModelOpsUsageUsesCacheAndSupplierDiscount(t *testing.T) {
	item := model.AIModel{
		InputCostRMB:       1,
		OutputCostRMB:      2,
		SupportsCache:      true,
		CacheMechanism:     "auto",
		CacheInputPriceRMB: 0.2,
		CacheWritePriceRMB: 0,
		Supplier:           model.Supplier{Discount: 0.8},
	}
	usage := usageAgg{
		PromptTokens:     1000,
		CompletionTokens: 500,
		CacheReadTokens:  300,
		CacheWriteTokens: 100,
	}

	got := discountedCostForModelOpsUsage(item, usage)
	want := (float64(600)*1 + float64(300)*0.2 + float64(100)*1 + float64(500)*2) / 1_000_000 * 0.8

	if math.Abs(got-want) > 0.000000001 {
		t.Fatalf("discounted cache cost = %.12f, want %.12f", got, want)
	}
}

func TestClassifyCalculatorCoversCurrentDbGaps(t *testing.T) {
	cases := []struct {
		name string
		item model.AIModel
		want string
	}{
		{
			name: "seedance 1.0 token video",
			item: model.AIModel{ModelName: "doubao-seedance-1-0-pro-250528", ModelType: model.ModelTypeVideoGeneration, PricingUnit: model.UnitPerMillionTokens},
			want: "video_token_formula",
		},
		{
			name: "vision per image ocr",
			item: model.AIModel{ModelName: "qianfan-ocr", ModelType: model.ModelTypeVision, PricingUnit: model.UnitPerImage},
			want: "vision_image_unit",
		},
		{
			name: "3d generation",
			item: model.AIModel{ModelName: "seed3d", ModelType: "3DGeneration", PricingUnit: model.UnitPerMillionTokens},
			want: "three_d_generation_token",
		},
		{
			name: "asr duration",
			item: model.AIModel{ModelName: "paraformer", ModelType: model.ModelTypeASR, PricingUnit: model.UnitPerHour},
			want: "asr_duration",
		},
		{
			name: "rerank token",
			item: model.AIModel{ModelName: "bge-reranker", ModelType: model.ModelTypeRerank, PricingUnit: model.UnitPerMillionTokens},
			want: "rerank_token",
		},
		{
			name: "openai cached input inferred from official pricing",
			item: model.AIModel{ModelName: "gpt-5.4", ModelType: "Reasoning", PricingUnit: model.UnitPerMillionTokens, InputCostRMB: 8.875, OutputCostRMB: 71},
			want: "token_io_tiered_cache",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, status, _, _ := classifyCalculator(tc.item)
			if got != tc.want {
				t.Fatalf("calculator type = %s, want %s", got, tc.want)
			}
			if status != "bound" {
				t.Fatalf("status = %s", status)
			}
		})
	}
}

func TestCalculatorCatalogContainsAllClassifiedTypes(t *testing.T) {
	classified := []string{
		"token_io", "token_io_tiered_cache", "image_unit_matrix", "vision_image_unit",
		"volc_seedance_2_video_formula", "video_token_formula", "volc_seedance_1_5_video_matrix",
		"video_duration", "tts_character", "asr_duration", "embedding_token",
		"rerank_token", "rerank_call", "three_d_generation_token",
		"generic_per_image", "generic_per_second", "generic_per_minute", "generic_per_hour",
		"generic_per_call", "generic_per_10k_characters", "generic_per_million_characters",
	}
	catalog := map[string]bool{}
	for _, spec := range calculatorCatalog() {
		catalog[spec.Type] = true
	}
	for _, typ := range classified {
		if !catalog[typ] {
			t.Fatalf("calculator catalog missing %s", typ)
		}
	}
}

func TestCalculatorCatalogIsReviewableAndComplete(t *testing.T) {
	for _, spec := range calculatorCatalog() {
		normalized := normalizeCalculatorSpec(spec)
		if strings.TrimSpace(normalized.Type) == "" || strings.TrimSpace(normalized.Name) == "" {
			t.Fatalf("calculator identity is incomplete: %#v", normalized)
		}
		if strings.TrimSpace(normalized.Description) == "" || strings.TrimSpace(normalized.AccuracyLevel) == "" {
			t.Fatalf("%s missing description or accuracy level", normalized.Type)
		}
		if strings.TrimSpace(normalized.CompatibilityTip) == "" {
			t.Fatalf("%s missing compatibility tip", normalized.Type)
		}
		if len(normalized.ModelTypes) == 0 || len(normalized.PricingUnits) == 0 {
			t.Fatalf("%s missing model types or pricing units", normalized.Type)
		}
		if len(normalized.Fields) == 0 || len(normalized.Formula) == 0 {
			t.Fatalf("%s missing fields or formula", normalized.Type)
		}
		for _, field := range normalized.Fields {
			if strings.TrimSpace(field.Key) == "" || strings.TrimSpace(field.Label) == "" || strings.TrimSpace(field.Type) == "" {
				t.Fatalf("%s has incomplete field: %#v", normalized.Type, field)
			}
			if field.Type == "number" && strings.TrimSpace(field.Unit) == "" {
				t.Fatalf("%s.%s number field missing unit", normalized.Type, field.Key)
			}
			if field.Type == "select" && len(field.Options) == 0 {
				t.Fatalf("%s.%s select field missing options", normalized.Type, field.Key)
			}
			if strings.TrimSpace(field.Help) == "" {
				t.Fatalf("%s.%s missing help copy", normalized.Type, field.Key)
			}
		}
	}
}

func TestCalculatorCatalogBackfillsProviderPricingFields(t *testing.T) {
	required := []string{
		"processing_mode", "service_tier", "region_mode", "batch",
		"cache_ttl", "cache_control", "cache_mode", "cached_tokens", "cache_hit_tokens", "cache_miss_tokens",
		"input_text_tokens", "input_image_tokens", "input_video_tokens", "input_audio_tokens", "input_image_count",
		"width", "height", "size", "aspect_ratio", "output_format", "background", "style", "output_count", "video_count",
		"voice_tier", "voice_design_count", "voice_clone_count", "voice_id", "text_char_count", "sample_rate", "format", "speed", "pitch", "volume", "streaming", "realtime_session_minutes",
		"web_search_count", "search_tokens", "google_search_query_count", "maps_query_count", "tool_call_count", "file_search_count", "container_seconds", "container_minutes", "retrieval_count", "knowledge_search_count",
		"page_count", "document_count", "storage_gb", "storage_hours", "response_format", "tools", "tool_choice",
	}
	for _, spec := range calculatorCatalog() {
		normalized := normalizeCalculatorSpec(spec)
		fields := map[string]calculatorFieldSpec{}
		for _, field := range normalized.Fields {
			fields[field.Key] = field
		}
		for _, key := range required {
			field, ok := fields[key]
			if !ok {
				t.Fatalf("%s missing provider pricing field %s", normalized.Type, key)
			}
			if strings.TrimSpace(field.Help) == "" {
				t.Fatalf("%s.%s missing help", normalized.Type, key)
			}
		}
	}
}

func TestNormalizeTokenTierCacheCalculatorBackfillsOneHourCacheWriteField(t *testing.T) {
	spec := normalizeCalculatorSpec(calculatorSpec{
		Type:         "token_io_tiered_cache",
		Name:         "Legacy Token Cache",
		Description:  "legacy config from database",
		ModelTypes:   []string{"LLM"},
		PricingUnits: []string{model.UnitPerMillionTokens},
		Fields: []calculatorFieldSpec{
			{Key: "input_tokens", Label: "输入 tokens", Type: "number"},
			{Key: "output_tokens", Label: "输出 tokens", Type: "number"},
			{Key: "cache_read_tokens", Label: "缓存读 tokens", Type: "number"},
			{Key: "cache_write_tokens", Label: "缓存写 tokens", Type: "number"},
		},
		Formula: []string{"regular_input = input - cache_read - cache_write"},
	})

	var found bool
	for _, field := range spec.Fields {
		if field.Key == "cache_write_1h_tokens" {
			found = true
			if field.Unit != "tokens" || field.Type != "number" || strings.TrimSpace(field.Help) == "" {
				t.Fatalf("invalid 1h cache field: %#v", field)
			}
		}
	}
	if !found {
		t.Fatalf("expected cache_write_1h_tokens to be backfilled: %#v", spec.Fields)
	}
}

func TestCalculatePreviewSeedance20UsesFormulaTokens(t *testing.T) {
	item := model.AIModel{
		BaseModel:     model.BaseModel{ID: 626},
		ModelName:     "doubao-seedance-2.0-1080p",
		ModelType:     model.ModelTypeVideoGeneration,
		PricingUnit:   model.UnitPerMillionTokens,
		OutputCostRMB: 51,
		Pricing:       &model.ModelPricing{OutputPriceRMB: 76.5},
	}

	got := calculateModelPreview(item, map[string]interface{}{
		"input_contains_video": false,
		"resolution":           "720p",
		"output_seconds":       float64(5),
		"width":                float64(1280),
		"height":               float64(720),
		"fps":                  float64(24),
	})

	if got.CalculatorType != "volc_seedance_2_video_formula" {
		t.Fatalf("calculator type = %s", got.CalculatorType)
	}
	if len(got.Layers) != 1 {
		t.Fatalf("layers = %d", len(got.Layers))
	}
	if math.Abs(got.OfficialAmount-5.508) > 0.001 {
		t.Fatalf("official amount = %f", got.OfficialAmount)
	}
	if got.GrossProfit <= 0 {
		t.Fatalf("expected positive gross profit, got %f", got.GrossProfit)
	}
	if len(got.Warnings) > 1 {
		t.Fatalf("unexpected minimum-token warning: %v", got.Warnings)
	}
}

func TestCalculatePreviewSeedance15UsesAudioAndOfflineMatrix(t *testing.T) {
	item := model.AIModel{
		BaseModel:     model.BaseModel{ID: 627},
		ModelName:     "doubao-seedance-1.5-pro",
		ModelType:     model.ModelTypeVideoGeneration,
		PricingUnit:   model.UnitPerMillionTokens,
		OutputCostRMB: 16,
		Pricing:       &model.ModelPricing{OutputPriceRMB: 24},
	}

	got := calculateModelPreview(item, map[string]interface{}{
		"resolution":     "720p",
		"output_seconds": float64(5),
		"generate_audio": "false",
		"service_tier":   "flex",
	})

	if got.CalculatorType != "volc_seedance_1_5_video_matrix" {
		t.Fatalf("calculator type = %s", got.CalculatorType)
	}
	if math.Abs(got.OfficialAmount-0.432) > 0.001 {
		t.Fatalf("official amount = %f", got.OfficialAmount)
	}
	if got.SellingAmount <= got.OfficialAmount {
		t.Fatalf("expected selling amount to scale from platform price, official=%f selling=%f", got.OfficialAmount, got.SellingAmount)
	}
	if len(got.Formula) == 0 || !strings.Contains(strings.Join(got.Formula, " "), "Seedance 1.5") {
		t.Fatalf("expected Seedance formula, got %v", got.Formula)
	}
}

func TestCalculatePreviewAcceptsReasoningTokenAlias(t *testing.T) {
	item := model.AIModel{
		BaseModel:             model.BaseModel{ID: 5},
		ModelName:             "qwen-reasoner",
		ModelType:             model.ModelTypeLLM,
		PricingUnit:           model.UnitPerMillionTokens,
		InputCostRMB:          1,
		OutputCostRMB:         2,
		OutputCostThinkingRMB: 6,
		Pricing:               &model.ModelPricing{InputPriceRMB: 1.5, OutputPriceRMB: 3, OutputPriceThinkingRMB: 9},
	}

	got := calculateModelPreview(item, map[string]interface{}{
		"input_tokens":     float64(1000),
		"output_tokens":    float64(500),
		"reasoning_tokens": float64(2000),
	})

	if got.ThinkingTokens != 2000 {
		t.Fatalf("thinking tokens = %f", got.ThinkingTokens)
	}
	if math.Abs(got.OfficialAmount-0.014) > 0.000001 {
		t.Fatalf("official amount = %f", got.OfficialAmount)
	}
	if math.Abs(got.SellingAmount-0.021) > 0.000001 {
		t.Fatalf("selling amount = %f", got.SellingAmount)
	}
}

func TestCalculatePreviewTokenIO(t *testing.T) {
	item := model.AIModel{
		BaseModel:     model.BaseModel{ID: 1},
		ModelName:     "qwen-plus",
		ModelType:     model.ModelTypeLLM,
		PricingUnit:   model.UnitPerMillionTokens,
		InputCostRMB:  0.8,
		OutputCostRMB: 2,
		Pricing:       &model.ModelPricing{InputPriceRMB: 1.2, OutputPriceRMB: 3},
	}

	got := calculateModelPreview(item, map[string]interface{}{"input_tokens": float64(1000), "output_tokens": float64(500)})

	if got.OfficialAmount <= 0 {
		t.Fatalf("official amount = %f", got.OfficialAmount)
	}
	if len(got.Layers) != 2 {
		t.Fatalf("layers = %d", len(got.Layers))
	}
	if got.FinalDiscount <= 0 {
		t.Fatalf("final discount = %f", got.FinalDiscount)
	}
}

func TestCalculatePreviewFallsBackToLargestTier(t *testing.T) {
	max32k := int64(32000)
	max128k := int64(128000)
	tiers := model.PriceTiersData{Tiers: []model.PriceTier{
		{Name: "small", InputMin: 0, InputMax: &max32k, OutputMinExclusive: true, InputPrice: 1, OutputPrice: 2},
		{Name: "largest", InputMin: 32000, InputMinExclusive: true, InputMax: &max128k, OutputMinExclusive: true, InputPrice: 3, OutputPrice: 4},
	}}
	item := model.AIModel{
		BaseModel:     model.BaseModel{ID: 11},
		ModelName:     "tiered-model",
		ModelType:     model.ModelTypeLLM,
		PricingUnit:   model.UnitPerMillionTokens,
		InputCostRMB:  0.5,
		OutputCostRMB: 1,
		PriceTiers:    mustJSON(tiers),
	}

	got := calculateModelPreview(item, map[string]interface{}{"input_tokens": float64(1_000_000), "output_tokens": float64(2_000)})

	if len(got.TierMatches) == 0 || !got.TierMatches[0].Matched {
		t.Fatalf("expected largest tier fallback to be treated as matched: %#v", got.TierMatches)
	}
	if got.TierMatches[0].Name != "largest" {
		t.Fatalf("tier name = %q, want largest", got.TierMatches[0].Name)
	}
	if math.Abs(got.OfficialAmount-3.008) > 0.000001 {
		t.Fatalf("official amount = %f", got.OfficialAmount)
	}
}

func TestCalculatePreviewTieredCacheThinkingBreakdown(t *testing.T) {
	sellInput := 1.6
	sellOutput := 4.8
	sellThinking := 8.0
	costTiers := model.PriceTiersData{Tiers: []model.PriceTier{{
		Name:                "32k-128k",
		InputMin:            32000,
		InputMax:            int64Ptr(128000),
		OutputMinExclusive:  true,
		InputPrice:          1.0,
		OutputPrice:         3.0,
		CacheInputPrice:     0.2,
		CacheWritePrice:     1.25,
		OutputPriceThinking: 5.0,
	}}}
	sellTiers := model.PriceTiersData{Tiers: []model.PriceTier{{
		Name:                       "platform-32k-128k",
		InputMin:                   32000,
		InputMax:                   int64Ptr(128000),
		OutputMinExclusive:         true,
		SellingInputPrice:          &sellInput,
		SellingOutputPrice:         &sellOutput,
		CacheInputPrice:            0.32,
		CacheWritePrice:            2.0,
		SellingOutputThinkingPrice: &sellThinking,
	}}}
	item := model.AIModel{
		BaseModel:             model.BaseModel{ID: 4},
		ModelName:             "qwen-plus-cache",
		ModelType:             model.ModelTypeLLM,
		PricingUnit:           model.UnitPerMillionTokens,
		InputCostRMB:          1,
		OutputCostRMB:         3,
		OutputCostThinkingRMB: 5,
		SupportsCache:         true,
		CacheMechanism:        "both",
		Discount:              0.8,
		PriceTiers:            mustJSON(costTiers),
		Pricing: &model.ModelPricing{
			InputPriceRMB:          1.5,
			OutputPriceRMB:         4.5,
			OutputPriceThinkingRMB: 7.5,
			PriceTiers:             mustJSON(sellTiers),
		},
	}

	got := calculateModelPreview(item, map[string]interface{}{
		"input_tokens":       float64(100000),
		"output_tokens":      float64(2000),
		"cache_read_tokens":  float64(40000),
		"cache_write_tokens": float64(10000),
		"thinking_tokens":    float64(1000),
		"enable_thinking":    true,
		"reasoning_effort":   "high",
	})

	if got.RegularInputTokens != 50000 {
		t.Fatalf("regular input tokens = %f", got.RegularInputTokens)
	}
	if len(got.TierMatches) != 2 || !got.TierMatches[0].Matched || !got.TierMatches[1].Matched {
		t.Fatalf("expected cost and selling tiers to match: %#v", got.TierMatches)
	}
	if len(got.Steps) < 12 {
		t.Fatalf("expected detailed steps, got %d", len(got.Steps))
	}
	if got.SpecialParams["enable_thinking"] != true || got.SpecialParams["reasoning_effort"] != "high" {
		t.Fatalf("special params missing: %#v", got.SpecialParams)
	}
	if math.Abs(got.OfficialAmount-0.0815) > 0.000001 {
		t.Fatalf("official amount = %f", got.OfficialAmount)
	}
	if math.Abs(got.EffectiveCost-0.0652) > 0.000001 {
		t.Fatalf("effective cost = %f", got.EffectiveCost)
	}
	if math.Abs(got.SellingAmount-0.1304) > 0.000001 {
		t.Fatalf("selling amount = %f", got.SellingAmount)
	}
	if got.CacheSavings <= 0 {
		t.Fatalf("expected positive cache savings")
	}
}

func TestCalculatePreviewImageUnit(t *testing.T) {
	item := model.AIModel{
		BaseModel:     model.BaseModel{ID: 2},
		ModelName:     "seedream-4.0",
		ModelType:     model.ModelTypeImageGeneration,
		PricingUnit:   model.UnitPerImage,
		OutputCostRMB: 0.2,
		Pricing:       &model.ModelPricing{OutputPriceRMB: 0.3},
	}

	got := calculateModelPreview(item, map[string]interface{}{"image_count": float64(4)})

	if got.CalculatorType != "image_unit_matrix" {
		t.Fatalf("calculator type = %s", got.CalculatorType)
	}
	if math.Abs(got.OfficialAmount-0.8) > 0.001 {
		t.Fatalf("official amount = %f", got.OfficialAmount)
	}
	if got.SellingAmount <= got.OfficialAmount {
		t.Fatalf("selling amount = %f, want above official amount %f", got.SellingAmount, got.OfficialAmount)
	}
}

func TestCalculatePreviewTokenCacheStorage(t *testing.T) {
	item := model.AIModel{
		BaseModel:            model.BaseModel{ID: 7},
		ModelName:            "gemini-cache",
		ModelType:            model.ModelTypeLLM,
		PricingUnit:          model.UnitPerMillionTokens,
		InputCostRMB:         2,
		OutputCostRMB:        8,
		SupportsCache:        true,
		CacheMechanism:       "explicit",
		CacheInputPriceRMB:   0.2,
		CacheWritePriceRMB:   2.5,
		CacheStoragePriceRMB: 4.5,
		Pricing:              &model.ModelPricing{InputPriceRMB: 3, OutputPriceRMB: 12},
	}

	got := calculateModelPreview(item, map[string]interface{}{
		"input_tokens":         float64(100000),
		"output_tokens":        float64(1000),
		"cache_storage_tokens": float64(50000),
		"cache_storage_hours":  float64(2),
		"cache_ttl":            "1h",
	})

	var found bool
	for _, step := range got.Steps {
		if step.Label == "official_cache_storage" {
			found = true
			if math.Abs(step.Amount-0.45) > 0.0001 {
				t.Fatalf("storage amount = %f, want 0.45", step.Amount)
			}
		}
	}
	if !found {
		t.Fatalf("expected official_cache_storage step: %#v", got.Steps)
	}
	if got.SpecialParams["cache_ttl"] != "1h" {
		t.Fatalf("cache_ttl special param missing: %#v", got.SpecialParams)
	}
}

func TestCalculatePreviewAcceptsProviderAliasAndSpecialParams(t *testing.T) {
	item := model.AIModel{
		BaseModel:     model.BaseModel{ID: 8},
		ModelName:     "gemini-multimodal-cache",
		ModelType:     model.ModelTypeLLM,
		PricingUnit:   model.UnitPerMillionTokens,
		InputCostRMB:  2,
		OutputCostRMB: 6,
		SupportsCache: true,
		Pricing:       &model.ModelPricing{InputPriceRMB: 3, OutputPriceRMB: 9},
	}

	got := calculateModelPreview(item, map[string]interface{}{
		"input_text_tokens":         float64(1000),
		"input_image_tokens":        float64(2000),
		"cache_hit_tokens":          float64(500),
		"completion_tokens":         float64(1000),
		"processing_mode":           "batch",
		"region_mode":               "us",
		"google_search_query_count": float64(2),
		"realtime_session_minutes":  float64(3),
		"voice_clone_count":         float64(1),
		"tools":                     "[]",
		"tool_choice":               "auto",
	})

	if got.RegularInputTokens != 2500 {
		t.Fatalf("regular input tokens = %f, want multimodal input minus cache hit", got.RegularInputTokens)
	}
	if got.CacheReadTokens != 500 {
		t.Fatalf("cache read tokens = %f", got.CacheReadTokens)
	}
	if got.SpecialParams["processing_mode"] != "batch" || got.SpecialParams["region_mode"] != "us" {
		t.Fatalf("missing provider special params: %#v", got.SpecialParams)
	}
	if got.SpecialParams["google_search_query_count"] == nil || got.SpecialParams["voice_clone_count"] == nil || got.SpecialParams["tools"] == nil {
		t.Fatalf("missing extended special params: %#v", got.SpecialParams)
	}
}

func TestCalculatePreviewGeminiIgnoresCacheWriteTokens(t *testing.T) {
	max200k := int64(200000)
	costTiers := model.PriceTiersData{Tiers: []model.PriceTier{
		{Name: "Gemini 3 Pro <=200K", InputMin: 0, InputMax: &max200k, OutputMinExclusive: true, InputPrice: 14.2, OutputPrice: 85.2, CacheInputPrice: 1.42},
		{Name: "Gemini 3 Pro >200K", InputMin: 200000, InputMinExclusive: true, OutputMinExclusive: true, InputPrice: 28.4, OutputPrice: 127.8, CacheInputPrice: 2.84},
	}}
	item := model.AIModel{
		BaseModel:                  model.BaseModel{ID: 651},
		ModelName:                  "gemini.gemini-3-pro-preview",
		ModelType:                  model.ModelTypeVision,
		PricingUnit:                model.UnitPerMillionTokens,
		InputCostRMB:               28.4,
		OutputCostRMB:              127.8,
		SupportsCache:              true,
		CacheMechanism:             "both",
		CacheMinTokens:             4096,
		CacheInputPriceRMB:         2.84,
		CacheExplicitInputPriceRMB: 2.84,
		CacheStoragePriceRMB:       31.95,
		PriceTiers:                 mustJSON(costTiers),
		Pricing:                    &model.ModelPricing{InputPriceRMB: 28.4, OutputPriceRMB: 127.8},
	}

	got := calculateModelPreview(item, map[string]interface{}{
		"input_tokens":          float64(300000),
		"output_tokens":         float64(1000),
		"cache_write_tokens":    float64(100000),
		"cache_write_1h_tokens": float64(50000),
	})

	if got.RegularInputTokens != 300000 {
		t.Fatalf("regular input tokens = %f, want cache write ignored and regular input unchanged", got.RegularInputTokens)
	}
	if got.CacheWriteTokens != 0 || got.CacheWrite1hTokens != 0 {
		t.Fatalf("cache write tokens should be ignored for Gemini, got %f/%f", got.CacheWriteTokens, got.CacheWrite1hTokens)
	}
	if math.Abs(got.OfficialAmount-8.6478) > 0.000001 {
		t.Fatalf("official amount = %f, want Gemini input+visible output only", got.OfficialAmount)
	}
	for _, step := range got.Steps {
		if strings.Contains(step.Label, "cache_write") && step.Amount > 0 {
			t.Fatalf("unexpected charged cache write step: %#v", step)
		}
	}
	if !strings.Contains(strings.Join(got.Warnings, " "), "cache_write_tokens ignored") {
		t.Fatalf("expected ignored cache write warning, got %v", got.Warnings)
	}
	joinedFormula := strings.Join(got.Formula, " ")
	if strings.Contains(joinedFormula, "cache_write_5m_tokens") {
		t.Fatalf("Gemini formula should not show generic cache write split: %v", got.Formula)
	}
	if !strings.Contains(joinedFormula, "cache_write_tokens ignored for Gemini") {
		t.Fatalf("Gemini formula should explain ignored cache write tokens: %v", got.Formula)
	}
}

func TestCalculatorCatalogMarksOutputTokensAsVisibleOutput(t *testing.T) {
	for _, spec := range calculatorCatalog() {
		if spec.Type != "token_io" && spec.Type != "token_io_tiered_cache" {
			continue
		}
		var found bool
		for _, field := range spec.Fields {
			if field.Key == "output_tokens" {
				found = true
				if !strings.Contains(strings.ToLower(field.Label), "visible") {
					t.Fatalf("%s output_tokens label = %q, want visible output wording", spec.Type, field.Label)
				}
			}
		}
		if !found {
			t.Fatalf("%s missing output_tokens field", spec.Type)
		}
	}
}

func int64Ptr(v int64) *int64 {
	return &v
}

func TestBuildModelOpsProfileUsesBoundCalculatorLabel(t *testing.T) {
	item := model.AIModel{
		BaseModel:     model.BaseModel{ID: 3},
		ModelName:     "custom-video-model",
		ModelType:     model.ModelTypeVideoGeneration,
		PricingUnit:   model.UnitPerSecond,
		OutputCostRMB: 0.01,
	}
	labels := []model.ModelLabel{
		{ModelID: 3, LabelKey: "calculator", LabelValue: "video_duration"},
	}

	got := buildModelOpsProfile(item, labels, modelOpsRouteSummary{Total: 1, Active: 1, Healthy: 1}, modelOpsUsageSummary{})

	if got.CalculatorType != "video_duration" {
		t.Fatalf("calculator type = %s", got.CalculatorType)
	}
	if got.CalculatorStatus != "bound" {
		t.Fatalf("calculator status = %s", got.CalculatorStatus)
	}
	if len(got.CompatibilityNotes) == 0 {
		t.Fatalf("expected bound compatibility note")
	}
}

func TestSortModelOpsProfilesActiveFirst(t *testing.T) {
	profiles := []modelOpsProfile{
		{ID: 1, ModelName: "disabled-a", IsActive: false},
		{ID: 2, ModelName: "enabled-a", IsActive: true},
		{ID: 3, ModelName: "disabled-b", IsActive: false},
		{ID: 4, ModelName: "enabled-b", IsActive: true},
	}

	sortModelOpsProfiles(profiles)

	got := []string{profiles[0].ModelName, profiles[1].ModelName, profiles[2].ModelName, profiles[3].ModelName}
	want := []string{"enabled-a", "enabled-b", "disabled-a", "disabled-b"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %#v, want %#v", got, want)
		}
	}
}

func TestDescribeBatchChangeUnbindCalculator(t *testing.T) {
	before, after, desc, warnings, ok := describeBatchChange(modelOpsBatchRequest{Action: "unbind_calculator"}, model.AIModel{})
	if !ok {
		t.Fatalf("expected executable unbind action: %v", warnings)
	}
	if before == "" || after == "" || desc == "" {
		t.Fatalf("unexpected description: %s %s %s", before, after, desc)
	}
}

func TestCalculatorSpecConfigRoundTrip(t *testing.T) {
	spec := calculatorCatalog()[0]
	cfg := calculatorSpecToConfig(spec)
	got := configToCalculatorSpec(cfg)

	if got.Type != spec.Type || got.Name != spec.Name {
		t.Fatalf("round trip identity failed: %#v", got)
	}
	if !got.IsActive || got.Version == "" || got.Source == "" {
		t.Fatalf("metadata lost: active=%v version=%q source=%q", got.IsActive, got.Version, got.Source)
	}
	if len(got.Fields) == 0 || len(got.Formula) == 0 {
		t.Fatalf("schema/formula lost")
	}
}
