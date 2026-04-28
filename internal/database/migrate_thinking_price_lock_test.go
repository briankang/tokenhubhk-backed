package database

import (
	"encoding/json"
	"testing"

	"tokenhub-server/internal/model"
)

// TestPatchTiersWithThinkingPrice_DefaultLock 验证默认锁定策略
//
// 场景：未在 overrides 表中的模型 → thinking 价应等于 output 价（同价锁定）
func TestPatchTiersWithThinkingPrice_DefaultLock(t *testing.T) {
	original := model.PriceTiersData{
		Tiers: []model.PriceTier{
			{Name: "0-32k", InputMin: 0, OutputPrice: 4.8, InputPrice: 0.8},
			{Name: "32k-128k", InputMin: 32000, OutputPrice: 12, InputPrice: 2},
			{Name: "128k-256k", InputMin: 128000, OutputPrice: 24, InputPrice: 4},
		},
		Currency: "CNY",
	}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}

	// 没有 overrides 命中 → 走默认锁定
	patched, count, err := patchTiersWithThinkingPrice(raw, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("expected 3 tiers updated, got %d", count)
	}

	var result model.PriceTiersData
	if err := json.Unmarshal(patched, &result); err != nil {
		t.Fatal(err)
	}

	wantThinking := []float64{4.8, 12, 24}
	for i, tier := range result.Tiers {
		if tier.OutputPriceThinking != wantThinking[i] {
			t.Errorf("tier[%d] %s: thinking=%v, want %v (locked at output)",
				i, tier.Name, tier.OutputPriceThinking, wantThinking[i])
		}
	}
}

// TestPatchTiersWithThinkingPrice_ExplicitOverride 验证显式覆盖
//
// 场景：qwen3.5-flash 的 0-128k 阶梯 thinking=8 ≠ output=2
func TestPatchTiersWithThinkingPrice_ExplicitOverride(t *testing.T) {
	original := model.PriceTiersData{
		Tiers: []model.PriceTier{
			{Name: "0-128k", InputMin: 0, OutputPrice: 2, InputPrice: 0.8},
		},
	}
	raw, _ := json.Marshal(original)

	overrides := []thinkingPriceOverride{
		{ModelName: "qwen3.5-flash", TierName: "0-128k", TierOutputThinkingRMB: 8, OutputThinkingRMB: 8},
	}

	patched, count, err := patchTiersWithThinkingPrice(raw, overrides, 0)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 tier updated, got %d", count)
	}

	var result model.PriceTiersData
	_ = json.Unmarshal(patched, &result)

	if result.Tiers[0].OutputPriceThinking != 8 {
		t.Errorf("expected thinking=8 (override), got %v", result.Tiers[0].OutputPriceThinking)
	}
	// 非思考价不应被改
	if result.Tiers[0].OutputPrice != 2 {
		t.Errorf("non-thinking output should not change, got %v", result.Tiers[0].OutputPrice)
	}
}

// TestPatchTiersWithThinkingPrice_SkipAlreadySet 验证已配置的 tier 不被覆盖
//
// 场景：管理员已经手动配置了 thinking 价 → 迁移不应覆盖
func TestPatchTiersWithThinkingPrice_SkipAlreadySet(t *testing.T) {
	original := model.PriceTiersData{
		Tiers: []model.PriceTier{
			{Name: "0-32k", InputMin: 0, OutputPrice: 4, OutputPriceThinking: 99}, // 管理员已设
			{Name: "32k+", InputMin: 32000, OutputPrice: 8},                       // 未设
		},
	}
	raw, _ := json.Marshal(original)

	patched, count, err := patchTiersWithThinkingPrice(raw, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 tier updated (only the unset one), got %d", count)
	}

	var result model.PriceTiersData
	_ = json.Unmarshal(patched, &result)

	if result.Tiers[0].OutputPriceThinking != 99 {
		t.Errorf("管理员已配置的 99 不应被覆盖，got %v", result.Tiers[0].OutputPriceThinking)
	}
	if result.Tiers[1].OutputPriceThinking != 8 {
		t.Errorf("未配置 tier 应锁定为 output=8，got %v", result.Tiers[1].OutputPriceThinking)
	}
}

// TestPatchTiersWithThinkingPrice_FallbackToTopOutput 验证 tier 自身 OutputPrice=0 时走 fallback
//
// 场景：某些 tier output_price 是 0（被错填）→ 用顶层 output_cost 兜底
func TestPatchTiersWithThinkingPrice_FallbackToTopOutput(t *testing.T) {
	original := model.PriceTiersData{
		Tiers: []model.PriceTier{
			{Name: "Default", InputMin: 0, OutputPrice: 0}, // tier 自己 output=0
		},
	}
	raw, _ := json.Marshal(original)

	const fallbackOutput = 16.0
	patched, count, err := patchTiersWithThinkingPrice(raw, nil, fallbackOutput)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 tier updated, got %d", count)
	}

	var result model.PriceTiersData
	_ = json.Unmarshal(patched, &result)
	if result.Tiers[0].OutputPriceThinking != fallbackOutput {
		t.Errorf("应使用顶层 fallback %v, got %v", fallbackOutput, result.Tiers[0].OutputPriceThinking)
	}
}

// TestPatchTiersWithThinkingPrice_EmptyOrNil 验证边界
func TestPatchTiersWithThinkingPrice_EmptyOrNil(t *testing.T) {
	// nil 输入
	out, count, err := patchTiersWithThinkingPrice(nil, nil, 10)
	if err != nil || count != 0 || out != nil {
		t.Fatal("nil input should return nil/0/nil err")
	}
	// 空 tiers
	emptyData := model.PriceTiersData{Tiers: []model.PriceTier{}}
	raw, _ := json.Marshal(emptyData)
	out, count, err = patchTiersWithThinkingPrice(raw, nil, 10)
	if err != nil || count != 0 {
		t.Fatalf("empty tiers should yield count=0, got %d err=%v", count, err)
	}
}

// TestPatchModelPricingTiers_DefaultLock 验证售价侧同价锁定
//
// 场景：mp.PriceTiers 应为每个 tier 设置 selling_output_thinking_price = selling_output_price
func TestPatchModelPricingTiers_DefaultLock(t *testing.T) {
	sellOut := 4.8
	original := model.PriceTiersData{
		Tiers: []model.PriceTier{
			{
				Name:               "Default",
				OutputPrice:        4,
				SellingOutputPrice: &sellOut,
			},
		},
	}
	raw, _ := json.Marshal(original)

	out, count, err := patchModelPricingTiers(raw, nil, 4 /*costOutput*/, 4.8 /*sellOutput*/)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 update, got %d", count)
	}

	var result model.PriceTiersData
	_ = json.Unmarshal(out, &result)
	if result.Tiers[0].SellingOutputThinkingPrice == nil {
		t.Fatal("expected non-nil selling_output_thinking_price")
	}
	if *result.Tiers[0].SellingOutputThinkingPrice != 4.8 {
		t.Errorf("default lock 应等于 selling_output_price=4.8, got %v",
			*result.Tiers[0].SellingOutputThinkingPrice)
	}
}

// TestPatchModelPricingTiers_OverrideRatioPropagation 验证覆盖通过比例传播到售价
//
// 场景：成本侧 thinking_cost=8 / output_cost=2（4 倍）→ 售价侧 thinking_sell = sell × 4
func TestPatchModelPricingTiers_OverrideRatioPropagation(t *testing.T) {
	sellOut := 2.4 // 售价
	original := model.PriceTiersData{
		Tiers: []model.PriceTier{
			{
				Name:               "0-128k",
				OutputPrice:        2.0, // 成本 2
				SellingOutputPrice: &sellOut,
			},
		},
	}
	raw, _ := json.Marshal(original)

	overrides := []thinkingPriceOverride{
		{ModelName: "qwen3.5-flash", TierName: "0-128k", TierOutputThinkingRMB: 8, OutputThinkingRMB: 8},
	}

	out, count, err := patchModelPricingTiers(raw, overrides, 2.0, 2.4)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 update, got %d", count)
	}

	var result model.PriceTiersData
	_ = json.Unmarshal(out, &result)
	if result.Tiers[0].SellingOutputThinkingPrice == nil {
		t.Fatal("expected non-nil selling_output_thinking_price")
	}
	// 覆盖逻辑：thinking_sell = thinking_cost × (output_price / cost_output) = 8 × (2.0 / 2.0) = 8
	// 注：本场景 output_price=2 也是成本（tier 内），所以 ratio=1
	got := *result.Tiers[0].SellingOutputThinkingPrice
	if got != 8.0 {
		t.Errorf("expected thinking_sell=8.0, got %v", got)
	}
}

// TestThinkingPriceOverrides_ContainsKnownDifferent 验证 overrides 表至少包含已知差价模型
func TestThinkingPriceOverrides_ContainsKnownDifferent(t *testing.T) {
	overrides := thinkingPriceOverrides()

	knownDifferentModels := []string{
		"qwen3.5-flash", "qwen-flash", "qvq-72b-preview",
		"qvq-max-preview", "qwen3-omni-flash",
	}

	overrideNames := make(map[string]bool, len(overrides))
	for _, o := range overrides {
		overrideNames[o.ModelName] = true
	}

	for _, name := range knownDifferentModels {
		if !overrideNames[name] {
			t.Errorf("overrides 表缺少已知差价模型 %s（参考阿里云 2026-04 文档页）", name)
		}
	}
}

// TestThinkingPriceOverrides_RatioSanityCheck 验证覆盖值合理性（thinking 应 ≥ output 的 2-5 倍）
func TestThinkingPriceOverrides_RatioSanityCheck(t *testing.T) {
	overrides := thinkingPriceOverrides()

	// 已知（output_price → thinking_price）的预期对应
	knownPairs := map[string]struct{ output, thinking float64 }{
		"qwen3.5-flash":    {2, 8},  // 4x
		"qwen-flash":       {1.5, 6}, // 4x
		"qvq-72b-preview":  {8, 24}, // 3x
		"qvq-max-preview":  {12, 36}, // 3x
		"qwen3-omni-flash": {1.5, 6}, // 4x
	}

	for _, o := range overrides {
		want, ok := knownPairs[o.ModelName]
		if !ok {
			continue
		}
		if o.TierOutputThinkingRMB != want.thinking {
			t.Errorf("%s tier=%s: thinking=%v, expected %v (output_price=%v)",
				o.ModelName, o.TierName, o.TierOutputThinkingRMB, want.thinking, want.output)
		}
	}
}
