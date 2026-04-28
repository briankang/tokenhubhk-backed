package pricing

import (
	"encoding/json"
	"testing"

	"tokenhub-server/internal/model"
)

// TestExtractTierLabels_FromNames 含 Name 字段时直接用 Name。
func TestExtractTierLabels_FromNames(t *testing.T) {
	data := model.PriceTiersData{
		Tiers: []model.PriceTier{
			{Name: "tier_short", InputMin: 0, InputMax: ptrInt64(32000)},
			{Name: "tier_long", InputMin: 32000},
		},
	}
	raw, _ := json.Marshal(data)
	m := &model.AIModel{ModelType: model.ModelTypeLLM, ModelName: "qwen-tier-test", PriceTiers: raw}

	labels := extractTierLabelsFromAIModel(m)
	if len(labels) != 2 {
		t.Fatalf("got %d labels, want 2", len(labels))
	}
	if labels[0] != "tier_short" || labels[1] != "tier_long" {
		t.Fatalf("labels=%v want [tier_short tier_long]", labels)
	}
}

// TestExtractTierLabels_FromRange 无 Name 时按 InputMin/Max 自动生成。
func TestExtractTierLabels_FromRange(t *testing.T) {
	data := model.PriceTiersData{
		Tiers: []model.PriceTier{
			{InputMin: 0, InputMax: ptrInt64(32000)},
			{InputMin: 32000, InputMax: ptrInt64(200000)},
			{InputMin: 200000}, // 无上限
		},
	}
	raw, _ := json.Marshal(data)
	m := &model.AIModel{ModelType: model.ModelTypeLLM, PriceTiers: raw}

	labels := extractTierLabelsFromAIModel(m)
	want := []string{"0-32K tokens", "32K-200K tokens", "200K+ tokens"}
	if len(labels) != len(want) {
		t.Fatalf("got %d labels, want %d: %v", len(labels), len(want), labels)
	}
	for i, w := range want {
		if labels[i] != w {
			t.Fatalf("labels[%d]=%v want %v", i, labels[i], w)
		}
	}
}

// TestExtractTierLabels_EmptyOrInvalid 缺数据时必须返回 nil(避免插入空维度)。
func TestExtractTierLabels_EmptyOrInvalid(t *testing.T) {
	cases := []struct {
		name string
		m    *model.AIModel
	}{
		{"nil_model", nil},
		{"nil_tiers", &model.AIModel{}},
		{"empty_json", &model.AIModel{PriceTiers: []byte("")}},
		{"null_json", &model.AIModel{PriceTiers: []byte("null")}},
		{"empty_tiers_array", &model.AIModel{PriceTiers: []byte(`{"tiers":[]}`)}},
		{"malformed", &model.AIModel{PriceTiers: []byte(`{tiers:[`)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractTierLabelsFromAIModel(c.m); got != nil {
				t.Fatalf("expected nil, got %v", got)
			}
		})
	}
}

// TestBuildDefaultMatrix_LLMWithTiers 验证 LLM 含 PriceTiers 时,默认矩阵应:
//  1. 含 context_tier 维度,Label 为「输入 token 区间」
//  2. Values 列表与 tiers 数量一致(不再是空 nil 占位)
//  3. cells 数量等于 cartesian product(thinking 维度若有 → 2 × tier_count)
func TestBuildDefaultMatrix_LLMWithTiers(t *testing.T) {
	data := model.PriceTiersData{
		Tiers: []model.PriceTier{
			{Name: "tier_a", InputMin: 0, InputMax: ptrInt64(32000)},
			{Name: "tier_b", InputMin: 32000},
		},
	}
	raw, _ := json.Marshal(data)
	m := &model.AIModel{
		ModelName:   "qwen3-tiered",
		ModelType:   model.ModelTypeLLM,
		PricingUnit: model.UnitPerMillionTokens,
		PriceTiers:  raw,
	}
	pm := BuildDefaultMatrix(m, nil)
	if pm == nil {
		t.Fatal("nil matrix")
	}
	// 找 context_tier 维度
	var found *model.PriceDimension
	for i := range pm.Dimensions {
		if pm.Dimensions[i].Key == "context_tier" {
			found = &pm.Dimensions[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("context_tier dim missing; dims=%+v", pm.Dimensions)
	}
	if found.Label != "输入 token 区间" {
		t.Fatalf("label=%q want %q", found.Label, "输入 token 区间")
	}
	if len(found.Values) != 2 {
		t.Fatalf("values count=%d want 2", len(found.Values))
	}
	// 共 2 个 cell(只有 context_tier 维度;无 thinking)
	if len(pm.Cells) != 2 {
		t.Fatalf("cells=%d want 2", len(pm.Cells))
	}
}

// TestBuildDefaultMatrix_LLMNoTiers 无 PriceTiers 的 LLM 应:不含 context_tier 维度,单 cell。
func TestBuildDefaultMatrix_LLMNoTiers(t *testing.T) {
	m := &model.AIModel{
		ModelName:   "gpt-4-no-tier",
		ModelType:   model.ModelTypeLLM,
		PricingUnit: model.UnitPerMillionTokens,
	}
	pm := BuildDefaultMatrix(m, nil)
	if pm == nil {
		t.Fatal("nil matrix")
	}
	for _, d := range pm.Dimensions {
		if d.Key == "context_tier" {
			t.Fatalf("无 PriceTiers 不应出现 context_tier 维度,实际 dims=%+v", pm.Dimensions)
		}
	}
	if len(pm.Cells) != 1 {
		t.Fatalf("无维度应得 1 cell,got=%d", len(pm.Cells))
	}
}

// TestBuildDefaultMatrix_PrefillFromTiers 验证含阶梯模型,默认矩阵每个 cell 的
// OfficialInput/OfficialOutput 按 tier 的 InputPrice/OutputPrice 预填。
func TestBuildDefaultMatrix_PrefillFromTiers(t *testing.T) {
	data := model.PriceTiersData{
		Tiers: []model.PriceTier{
			{Name: "tier_a", InputMin: 0, InputMax: ptrInt64(32000), InputPrice: 1.2, OutputPrice: 3.0},
			{Name: "tier_b", InputMin: 32000, InputPrice: 1.8, OutputPrice: 4.5},
		},
	}
	raw, _ := json.Marshal(data)
	m := &model.AIModel{
		ModelName:   "qwen-tier-prefill",
		ModelType:   model.ModelTypeLLM,
		PricingUnit: model.UnitPerMillionTokens,
		PriceTiers:  raw,
	}
	pm := BuildDefaultMatrix(m, nil)
	if len(pm.Cells) != 2 {
		t.Fatalf("cells=%d want 2", len(pm.Cells))
	}
	for _, c := range pm.Cells {
		label, _ := c.DimValues["context_tier"].(string)
		switch label {
		case "tier_a":
			if c.OfficialInput == nil || *c.OfficialInput != 1.2 {
				t.Errorf("tier_a OfficialInput=%v want 1.2", c.OfficialInput)
			}
			if c.OfficialOutput == nil || *c.OfficialOutput != 3.0 {
				t.Errorf("tier_a OfficialOutput=%v want 3.0", c.OfficialOutput)
			}
			// 无 mp 传 nil,Selling 走 official × 0.85 默认折扣
			if c.SellingInput == nil || roundTo6(*c.SellingInput) != roundTo6(1.2*0.85) {
				t.Errorf("tier_a SellingInput=%v want %v", c.SellingInput, 1.2*0.85)
			}
		case "tier_b":
			if c.OfficialInput == nil || *c.OfficialInput != 1.8 {
				t.Errorf("tier_b OfficialInput=%v want 1.8", c.OfficialInput)
			}
			if c.OfficialOutput == nil || *c.OfficialOutput != 4.5 {
				t.Errorf("tier_b OfficialOutput=%v want 4.5", c.OfficialOutput)
			}
		default:
			t.Errorf("unexpected tier label %q", label)
		}
	}
}

// TestBuildDefaultMatrix_PrefillThinkingMode 验证 thinking_mode 维度的 on/off 行
// 输出价分别用 OutputCostThinkingRMB / OutputCostRMB。
func TestBuildDefaultMatrix_PrefillThinkingMode(t *testing.T) {
	m := &model.AIModel{
		ModelName:             "qwen3-with-thinking",
		ModelType:             model.ModelTypeLLM,
		PricingUnit:           model.UnitPerMillionTokens,
		InputCostRMB:          0.6,
		OutputCostRMB:         1.5,
		OutputCostThinkingRMB: 12.0,
	}
	pm := BuildDefaultMatrix(m, nil)
	// 应得 2 个 cell:thinking=off / thinking=on
	if len(pm.Cells) != 2 {
		t.Fatalf("cells=%d want 2", len(pm.Cells))
	}
	for _, c := range pm.Cells {
		mode, _ := c.DimValues["thinking_mode"].(string)
		if c.OfficialInput == nil || *c.OfficialInput != 0.6 {
			t.Errorf("thinking=%s: OfficialInput=%v want 0.6", mode, c.OfficialInput)
		}
		switch mode {
		case "off":
			if c.OfficialOutput == nil || *c.OfficialOutput != 1.5 {
				t.Errorf("thinking=off: OfficialOutput=%v want 1.5", c.OfficialOutput)
			}
		case "on":
			if c.OfficialOutput == nil || *c.OfficialOutput != 12.0 {
				t.Errorf("thinking=on: OfficialOutput=%v want 12.0", c.OfficialOutput)
			}
		default:
			t.Errorf("unexpected thinking mode %q", mode)
		}
	}
}

// TestBuildDefaultMatrix_PrefillSellingFromMP 验证已有 ModelPricing 时,
// Selling 字段优先从 mp.PriceTiers / mp.InputPriceRMB 读取,而非走折扣 fallback。
func TestBuildDefaultMatrix_PrefillSellingFromMP(t *testing.T) {
	aiTiers := model.PriceTiersData{
		Tiers: []model.PriceTier{
			{Name: "t1", InputPrice: 1.0, OutputPrice: 2.0},
		},
	}
	aiRaw, _ := json.Marshal(aiTiers)
	sellingIn := 0.7
	sellingOut := 1.4
	mpTiers := model.PriceTiersData{
		Tiers: []model.PriceTier{
			{Name: "t1", InputPrice: 1.0, OutputPrice: 2.0,
				SellingInputPrice: &sellingIn, SellingOutputPrice: &sellingOut},
		},
	}
	mpRaw, _ := json.Marshal(mpTiers)

	m := &model.AIModel{
		ModelName:   "selling-from-mp",
		ModelType:   model.ModelTypeLLM,
		PricingUnit: model.UnitPerMillionTokens,
		PriceTiers:  aiRaw,
	}
	mp := &model.ModelPricing{
		PriceTiers: mpRaw,
	}
	pm := BuildDefaultMatrix(m, mp)
	if len(pm.Cells) != 1 {
		t.Fatalf("cells=%d want 1", len(pm.Cells))
	}
	c := pm.Cells[0]
	if c.SellingInput == nil || *c.SellingInput != 0.7 {
		t.Errorf("SellingInput=%v want 0.7 (from tier override)", c.SellingInput)
	}
	if c.SellingOutput == nil || *c.SellingOutput != 1.4 {
		t.Errorf("SellingOutput=%v want 1.4 (from tier override)", c.SellingOutput)
	}
}

// TestBuildDefaultMatrix_PrefillSellingFromGlobalRate 验证无 mp.PriceTiers 售价覆盖时,
// 用 mp.GlobalDiscountRate 从 official 推导 selling。
func TestBuildDefaultMatrix_PrefillSellingFromGlobalRate(t *testing.T) {
	m := &model.AIModel{
		ModelName:     "rate-fallback",
		ModelType:     model.ModelTypeLLM,
		PricingUnit:   model.UnitPerMillionTokens,
		InputCostRMB:  2.0,
		OutputCostRMB: 4.0,
	}
	mp := &model.ModelPricing{
		GlobalDiscountRate: 0.7,
	}
	pm := BuildDefaultMatrix(m, mp)
	if len(pm.Cells) != 1 {
		t.Fatalf("cells=%d want 1", len(pm.Cells))
	}
	c := pm.Cells[0]
	if c.SellingInput == nil || roundTo6(*c.SellingInput) != roundTo6(2.0*0.7) {
		t.Errorf("SellingInput=%v want %v (official × 0.7)", c.SellingInput, 2.0*0.7)
	}
	if c.SellingOutput == nil || roundTo6(*c.SellingOutput) != roundTo6(4.0*0.7) {
		t.Errorf("SellingOutput=%v want %v (official × 0.7)", c.SellingOutput, 4.0*0.7)
	}
}

// TestBuildDefaultMatrix_PrefillZeroCostStaysNil 验证成本字段为 0 时,
// Official/Selling 都留 nil(不预填),让前端显示空白提示管理员手动填。
func TestBuildDefaultMatrix_PrefillZeroCostStaysNil(t *testing.T) {
	m := &model.AIModel{
		ModelName:     "no-cost-data",
		ModelType:     model.ModelTypeLLM,
		PricingUnit:   model.UnitPerMillionTokens,
		InputCostRMB:  0,
		OutputCostRMB: 0,
	}
	pm := BuildDefaultMatrix(m, nil)
	if len(pm.Cells) != 1 {
		t.Fatalf("cells=%d want 1", len(pm.Cells))
	}
	c := pm.Cells[0]
	if c.OfficialInput != nil {
		t.Errorf("OfficialInput=%v want nil (cost=0)", c.OfficialInput)
	}
	if c.OfficialOutput != nil {
		t.Errorf("OfficialOutput=%v want nil (cost=0)", c.OfficialOutput)
	}
	if c.SellingInput != nil {
		t.Errorf("SellingInput=%v want nil (no source)", c.SellingInput)
	}
}

// TestBuildDefaultMatrix_PrefillImagePerUnit 验证 Image 模型(per_unit)所有 cell
// 统一填 InputCostRMB 到 OfficialPerUnit。
func TestBuildDefaultMatrix_PrefillImagePerUnit(t *testing.T) {
	m := &model.AIModel{
		ModelName:    "seedream-img",
		ModelType:    model.ModelTypeImageGeneration,
		PricingUnit:  model.UnitPerImage,
		InputCostRMB: 0.25,
	}
	pm := BuildDefaultMatrix(m, nil)
	// Image 默认 3 维度:resolution(3) × quality(2) × mode(3) = 18 cells
	if len(pm.Cells) != 18 {
		t.Fatalf("cells=%d want 18 (3×2×3)", len(pm.Cells))
	}
	for i, c := range pm.Cells {
		if c.OfficialPerUnit == nil || *c.OfficialPerUnit != 0.25 {
			t.Errorf("cell[%d].OfficialPerUnit=%v want 0.25", i, c.OfficialPerUnit)
		}
		// Selling 走默认折扣
		want := roundTo6(0.25 * 0.85)
		if c.SellingPerUnit == nil || roundTo6(*c.SellingPerUnit) != want {
			t.Errorf("cell[%d].SellingPerUnit=%v want %v", i, c.SellingPerUnit, want)
		}
		// 单价类不应填 Input/Output 双列
		if c.OfficialInput != nil || c.OfficialOutput != nil {
			t.Errorf("cell[%d] should not set Input/Output for per_unit type", i)
		}
	}
}

// TestEffectiveDiscountRate 验证折扣率回退:mp.GlobalDiscountRate>0 → 取它,否则 0.85。
func TestEffectiveDiscountRate(t *testing.T) {
	if got := effectiveDiscountRate(nil); got != 0.85 {
		t.Errorf("nil mp: rate=%v want 0.85", got)
	}
	if got := effectiveDiscountRate(&model.ModelPricing{}); got != 0.85 {
		t.Errorf("zero rate: got=%v want 0.85", got)
	}
	if got := effectiveDiscountRate(&model.ModelPricing{GlobalDiscountRate: 0.6}); got != 0.6 {
		t.Errorf("custom rate: got=%v want 0.6", got)
	}
}

// TestLookupTierByLabel 验证按 tierDisplayLabel 反查能命中 Name 优先 / 区间 fallback。
func TestLookupTierByLabel(t *testing.T) {
	data := &model.PriceTiersData{
		Tiers: []model.PriceTier{
			{Name: "named_tier", InputPrice: 1.0},
			{InputMin: 0, InputMax: ptrInt64(32000), InputPrice: 2.0},
		},
	}
	if got := lookupTierByLabel(data, "named_tier"); got == nil || got.InputPrice != 1.0 {
		t.Errorf("Name 命中失败: got=%v", got)
	}
	if got := lookupTierByLabel(data, "0-32K tokens"); got == nil || got.InputPrice != 2.0 {
		t.Errorf("Range 命中失败: got=%v", got)
	}
	if got := lookupTierByLabel(data, "no-such"); got != nil {
		t.Errorf("expected nil for unknown label, got %v", got)
	}
	if got := lookupTierByLabel(nil, "anything"); got != nil {
		t.Errorf("expected nil for nil data, got %v", got)
	}
}

func ptrInt64(v int64) *int64 { return &v }
