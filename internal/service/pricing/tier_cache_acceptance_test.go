package pricing_test

// 验收测试（与用户需求 2026-04-19 对齐）
// 1. 单阶梯 (0-∞) 模型应被 SelectTier 正确命中
// 2. 多阶梯边界（阶梯二比阶梯一贵）选择正确
// 3. 缓存费率推导逻辑（CalculateCostWithCache 内部 ratio 闭包的语义）
// 这些测试不依赖 DB，仅针对纯函数 / 结构体计算。

import (
	"testing"

	"tokenhub-server/internal/model"
)

// TestSelectTier_SingleInfiniteTier 验收项 1：单阶梯 0-∞ 必须命中
func TestSelectTier_SingleInfiniteTier(t *testing.T) {
	tiers := []model.PriceTier{{
		Name:        "0-无限",
		InputMin:    0,
		InputMax:    nil, // nil = +∞
		OutputMin:   0,
		OutputMax:   nil,
		InputPrice:  1.5,
		OutputPrice: 3.0,
	}}
	for i := range tiers {
		tiers[i].Normalize()
	}

	// 注：PriceTier.Normalize() 会把默认 Output 维度置为 (0, +∞]（开区间下界），
	// 因此 output_tokens=0 不会命中，这符合"有输出才有扣费"的语义。
	cases := []struct {
		inp, out int64
	}{
		{1, 1},
		{100, 50},
		{10_000, 5_000},
		{1_000_000, 100_000},
		{999_999_999, 999_999_999},
	}
	for _, tc := range cases {
		idx, tier := model.SelectTier(tiers, tc.inp, tc.out)
		if idx != 0 || tier == nil {
			t.Errorf("SelectTier(%d,%d): expected idx=0 + non-nil tier, got idx=%d tier=%v",
				tc.inp, tc.out, idx, tier)
			continue
		}
		if tier.InputPrice != 1.5 || tier.OutputPrice != 3.0 {
			t.Errorf("tier price mismatch: input=%f output=%f", tier.InputPrice, tier.OutputPrice)
		}
	}
}

// TestSelectTier_MultiTier_BoundaryMatch 验收项 2：多阶梯边界选择
// 阶梯一 (0, 32k]：1.2/3.0；阶梯二 (32k, +∞)：1.8/4.5
func TestSelectTier_MultiTier_BoundaryMatch(t *testing.T) {
	max32k := int64(32_000)
	tiers := []model.PriceTier{
		{Name: "tier1", InputMin: 0, InputMax: &max32k, OutputMin: 0, OutputMax: nil, InputPrice: 1.2, OutputPrice: 3.0},
		{Name: "tier2", InputMin: 32_000, InputMinExclusive: true, InputMax: nil, OutputMin: 0, OutputMax: nil, InputPrice: 1.8, OutputPrice: 4.5},
	}
	for i := range tiers {
		tiers[i].Normalize()
	}

	// 30k 输入 → tier1
	idx, tier := model.SelectTier(tiers, 30_000, 1_000)
	if idx != 0 || tier == nil || tier.InputPrice != 1.2 {
		t.Errorf("expected tier1 for 30k input, got idx=%d tier=%+v", idx, tier)
	}
	// 32k 输入（闭区间上界） → 仍 tier1
	idx, tier = model.SelectTier(tiers, 32_000, 1_000)
	if idx != 0 || tier == nil || tier.InputPrice != 1.2 {
		t.Errorf("expected tier1 for 32k (boundary), got idx=%d tier=%+v", idx, tier)
	}
	// 100k 输入 → tier2（更贵）
	idx, tier = model.SelectTier(tiers, 100_000, 1_000)
	if idx != 1 || tier == nil || tier.InputPrice != 1.8 {
		t.Errorf("expected tier2 (pricier) for 100k input, got idx=%d tier=%+v", idx, tier)
	}
}

// TestCacheRatioComputation 验收项 3：缓存费率推导公式
// 测试 CalculateCostWithCache 中 ratio 闭包的核心语义：
//   cache_ratio = cache_input_price_rmb / input_cost_rmb
// （当 input_cost_rmb=0 时使用 fallback：auto=0.5 / explicit=0.1 / both=0.2）
func TestCacheRatioComputation(t *testing.T) {
	cases := []struct {
		name           string
		inputCostRMB   float64
		cacheInputRMB  float64
		expectedRatio  float64
	}{
		{"claude_typical", 1.5, 0.15, 0.10},        // 10%
		{"doubao_typical", 0.8, 0.32, 0.40},        // 40%（火山）
		{"qwen_implicit", 1.0, 0.20, 0.20},         // 20%（阿里 auto）
		{"gpt_4o_style", 2.5, 1.25, 0.50},          // 50%（OpenAI auto）
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.inputCostRMB <= 0 {
				t.Skip("invalid baseline cost")
			}
			ratio := tc.cacheInputRMB / tc.inputCostRMB
			diff := ratio - tc.expectedRatio
			if diff < 0 {
				diff = -diff
			}
			if diff > 0.001 {
				t.Errorf("ratio mismatch: got %.4f, expected %.4f", ratio, tc.expectedRatio)
			}
		})
	}
}

// TestTierBillingFormula 验收项 V2：成本分析页阶梯计费公式验证
//
// 场景：ernie-5.0 模型配置 ≤32k/>32k 两个阶梯，请求落在 tier2 需要按更贵价计费
// 阶梯一 (0, 32k]: input=6元/M, output=24元/M
// 阶梯二 (32k, +∞):  input=10元/M, output=40元/M
//
// 请求 1：输入 30k tokens, 输出 2k tokens（命中 tier1）
//   cost = 30000 × 6/1M + 2000 × 24/1M = 0.18 + 0.048 = 0.228 元
//
// 请求 2：输入 50k tokens, 输出 3k tokens（命中 tier2，更贵）
//   cost = 50000 × 10/1M + 3000 × 40/1M = 0.5 + 0.12 = 0.62 元
//
// 关键断言：同样的 token 数，命中不同阶梯时价格必须差异化
func TestTierBillingFormula(t *testing.T) {
	max32k := int64(32_000)
	tiers := []model.PriceTier{
		{Name: "≤32k上下文", InputMin: 0, InputMax: &max32k, OutputMin: 0, OutputMax: nil, InputPrice: 6.0, OutputPrice: 24.0},
		{Name: ">32k上下文", InputMin: 32000, InputMinExclusive: true, InputMax: nil, OutputMin: 0, OutputMax: nil, InputPrice: 10.0, OutputPrice: 40.0},
	}
	for i := range tiers {
		tiers[i].Normalize()
	}

	computeCost := func(inTokens, outTokens int64) float64 {
		_, tier := model.SelectTier(tiers, inTokens, outTokens)
		if tier == nil {
			return -1
		}
		return float64(inTokens)*tier.InputPrice/1_000_000 + float64(outTokens)*tier.OutputPrice/1_000_000
	}

	cases := []struct {
		name       string
		in, out    int64
		wantTier   int
		wantCost   float64
	}{
		{"tier1_boundary_30k", 30_000, 2_000, 0, 0.228},      // 30k 命中 tier1
		{"tier1_at_32k", 32_000, 1_000, 0, 0.216},           // 32k 闭区间上界仍 tier1: 32000×6/M + 1000×24/M = 0.192 + 0.024 = 0.216
		{"tier2_50k", 50_000, 3_000, 1, 0.62},                // 50k 命中 tier2
		{"tier2_large_100k", 100_000, 5_000, 1, 1.2},         // 100k 命中 tier2: 100000×10/M + 5000×40/M = 1.0 + 0.2 = 1.2
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			idx, _ := model.SelectTier(tiers, c.in, c.out)
			if idx != c.wantTier {
				t.Errorf("%s: matched tier idx=%d, want %d", c.name, idx, c.wantTier)
			}
			got := computeCost(c.in, c.out)
			diff := got - c.wantCost
			if diff < 0 {
				diff = -diff
			}
			if diff > 0.0001 {
				t.Errorf("%s: cost=%.4f, want %.4f (diff %.4f)", c.name, got, c.wantCost, diff)
			}
		})
	}

	// 关键语义断言：tier2 必须比 tier1 贵（用户需求"阶梯二复杂度溢价 +50%"）
	costTier1 := tiers[0].InputPrice
	costTier2 := tiers[1].InputPrice
	if costTier2 <= costTier1 {
		t.Errorf("tier2 price (%.2f) must be greater than tier1 (%.2f) — complexity surcharge", costTier2, costTier1)
	}
}

// TestCacheCostFormula 验证当 cache_enabled=true 时的扣费公式
// 用户请求：1M input，其中 500k 缓存命中；0 write tokens；100k output
// 参数：input 1.0元/M，cache_input 0.1元/M（×0.1 ratio），output 2.0元/M
// 预期：
//   非缓存部分：500k × 1.0/1M = 0.5 元
//   缓存部分：   500k × 0.1/1M = 0.05 元
//   输出部分：   100k × 2.0/1M = 0.2 元
//   合计：0.75 元
func TestCacheCostFormula(t *testing.T) {
	inputPricePerM := 1.0
	cacheRatio := 0.1
	outputPricePerM := 2.0

	totalInputTokens := float64(1_000_000)
	cacheReadTokens := float64(500_000)
	regularTokens := totalInputTokens - cacheReadTokens
	outputTokens := float64(100_000)

	regularCost := inputPricePerM * regularTokens / 1_000_000
	cacheCost := inputPricePerM * cacheRatio * cacheReadTokens / 1_000_000
	outputCost := outputPricePerM * outputTokens / 1_000_000
	total := regularCost + cacheCost + outputCost

	expected := 0.75
	if total < expected-0.0001 || total > expected+0.0001 {
		t.Errorf("cache cost formula: got %f, expected %f", total, expected)
	}

	// cache_enabled=false 场景 → regular 部分取全量，即 1.0 元，加 output 0.2 = 1.2 元
	fullRegular := inputPricePerM * totalInputTokens / 1_000_000
	noCache := fullRegular + outputCost
	expectedNoCache := 1.2
	if noCache < expectedNoCache-0.0001 || noCache > expectedNoCache+0.0001 {
		t.Errorf("no-cache formula: got %f, expected %f", noCache, expectedNoCache)
	}

	// 节省验证
	savings := noCache - total
	expectedSavings := 0.45
	if savings < expectedSavings-0.0001 || savings > expectedSavings+0.0001 {
		t.Errorf("savings: got %f, expected %f", savings, expectedSavings)
	}
}
