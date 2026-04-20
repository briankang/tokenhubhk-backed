package integration_test

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"
)

// ========================================================================
// T4 成本分析扣费联动集成测试
//
// 覆盖场景：
//   T4.1 SingleTier    — test-req-001: ernie-3.5-8k tier[0], 500 prompt + 200 cache_read + 120 output
//   T4.2 MultiTier     — test-req-002: ernie-3.5-8k tier[1], 150k prompt + 60k cache_read + 300 output
//   T4.3 CacheSavings  — 两条 request 都带 cache_read，验证 cache_savings_rmb 动态重算正确
//
// 数据准备：
//   预期 DB 中已存在 test-req-001 / test-req-002（见 seed_test_data.sql）
//   如果不存在，测试会 skip
// ========================================================================

type breakdownDTO struct {
	ModelName                    string  `json:"model_name"`
	ModelID                      uint    `json:"model_id"`
	ModelFound                   bool    `json:"model_found"`
	PricingFound                 bool    `json:"pricing_found"`
	PricingUnit                  string  `json:"pricing_unit"`
	CurrentInputPriceRMB         float64 `json:"current_input_price_rmb"`
	CurrentOutputPriceRMB        float64 `json:"current_output_price_rmb"`
	CurrentInputPricePerMillionI int64   `json:"current_input_price_per_million"`
	CurrentOutputPricePerMillionI int64  `json:"current_output_price_per_million"`
	CurrentInputCostRMB          float64 `json:"current_input_cost_rmb"`
	CacheInputPriceRMB           float64 `json:"cache_input_price_rmb"`
	CacheReadPerMillion          int64   `json:"cache_read_per_million"`
	CacheReadRatio               float64 `json:"cache_read_ratio"`
	CacheReadCost                int64   `json:"cache_read_cost"`
	CacheReadTokens              int64   `json:"cache_read_tokens"`
	CacheWriteCost               int64   `json:"cache_write_cost"`
	CacheWriteTokens             int64   `json:"cache_write_tokens"`
	CacheSavingsRMB              float64 `json:"cache_savings_rmb"`
	RegularInputCost             int64   `json:"regular_input_cost"`
	RegularInputTokens           int64   `json:"regular_input_tokens"`
	RecomputedInputCost          int64   `json:"recomputed_input_cost"`
	RecomputedOutputCost         int64   `json:"recomputed_output_cost"`
	RecomputedTotalCost          int64   `json:"recomputed_total_cost"`     // credits
	RecomputedTotalCostRMB       float64 `json:"recomputed_total_cost_rmb"` // 元
	RecordedCostCredits          int64   `json:"recorded_cost_credits"`
	RecordedCostRMB              float64 `json:"recorded_cost_rmb"`
	MatchedPriceTierIdx          int     `json:"matched_price_tier_idx"`
	SupportsCache                bool    `json:"supports_cache"`
	CacheMechanism               string  `json:"cache_mechanism"`
	Log                          struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		CacheReadTokens  int64 `json:"cache_read_tokens"`
		CacheWriteTokens int64 `json:"cache_write_tokens"`
	} `json:"log"`
}

// CurrentInputPricePerMillion / CurrentOutputPricePerMillion — DTO 便捷访问器
func (b *breakdownDTO) CurrentInputPricePerMillion() int64  { return b.CurrentInputPricePerMillionI }
func (b *breakdownDTO) CurrentOutputPricePerMillion() int64 { return b.CurrentOutputPricePerMillionI }

func fetchBreakdown(t *testing.T, requestID string) *breakdownDTO {
	t.Helper()
	if adminToken == "" {
		t.Skip("no admin token")
	}
	url := fmt.Sprintf("%s/api/v1/admin/api-call-logs/%s/cost-breakdown", baseURL, requestID)
	resp, statusCode, err := doGet(url, adminToken)
	if err != nil {
		t.Fatalf("fetch cost-breakdown: %v", err)
	}
	if statusCode == 404 || (resp != nil && resp.Code != 0) {
		t.Skipf("test log %q not found (seed_test_data.sql not applied?)", requestID)
	}
	var bd breakdownDTO
	if err := json.Unmarshal(resp.Data, &bd); err != nil {
		t.Fatalf("parse breakdown: %v", err)
	}
	return &bd
}

// TestCostBreakdown_SingleTier 验证 tier[0] 命中时的扣费重算
//
// test-req-001:
//   model=ernie-3.5-8k, prompt=500, cache_read=200, cache_write=50, completion=120
//   tier[0] (≤32k): input=0.8 元/M（成本）, output=2 元/M（成本）
//
// 验证点（API 内部自洽）：
//   1. matched_price_tier_idx == 0
//   2. regular_input_tokens == prompt - cache_read - cache_write = 250
//   3. regular_input_cost + cache_read_cost + cache_write_cost + recomputed_output_cost == recomputed_total_cost
//   4. regular_input_cost / regular_input_tokens × 1M == current_input_price_per_million
func TestCostBreakdown_SingleTier(t *testing.T) {
	bd := fetchBreakdown(t, "test-req-001")

	if !bd.ModelFound {
		t.Fatalf("test-req-001: model not found in DB")
	}
	if bd.MatchedPriceTierIdx != 0 {
		t.Errorf("matched_price_tier_idx=%d, expected 0 (≤32k should hit tier 0)",
			bd.MatchedPriceTierIdx)
	}

	// 不变量 3：加总自洽（4 个分项 + 阶梯）
	// 注意：recomputed_input_cost = regular + cache_read + cache_write
	//       recomputed_total_cost  = recomputed_input_cost + recomputed_output_cost
	expectedInput := bd.RegularInputCost + bd.CacheReadCost + bd.CacheWriteCost
	if bd.RecomputedInputCost != expectedInput {
		t.Errorf("recomputed_input_cost=%d, but sum(regular %d + cache_read %d + cache_write %d)=%d",
			bd.RecomputedInputCost,
			bd.RegularInputCost, bd.CacheReadCost, bd.CacheWriteCost, expectedInput)
	}
	expectedTotal := bd.RecomputedInputCost + bd.RecomputedOutputCost
	if bd.RecomputedTotalCost != expectedTotal {
		t.Errorf("recomputed_total_cost=%d, but sum(input %d + output %d)=%d",
			bd.RecomputedTotalCost, bd.RecomputedInputCost, bd.RecomputedOutputCost, expectedTotal)
	}

	// 不变量 4：tokens 分桶完备
	bucketSum := bd.RegularInputTokens + bd.CacheReadTokens + bd.CacheWriteTokens
	if bucketSum != bd.Log.PromptTokens {
		t.Errorf("token buckets don't sum to prompt: %d+%d+%d=%d, expected %d",
			bd.RegularInputTokens, bd.CacheReadTokens, bd.CacheWriteTokens,
			bucketSum, bd.Log.PromptTokens)
	}

	t.Logf("✓ tier[0] total: %d credits = ¥%.6f (recorded: %d credits = ¥%.6f)",
		bd.RecomputedTotalCost, bd.RecomputedTotalCostRMB,
		bd.RecordedCostCredits, bd.RecordedCostRMB)
}

// TestCostBreakdown_MultiTier 验证 tier[1] 命中时的扣费重算
//
// test-req-002:
//   model=ernie-3.5-8k, prompt=150000, cache_read=60000, completion=300
//   tier[1] (>32k): input=1.2 元/M, output=3 元/M （成本价）
//   售价可能经过加价
//
// 验证点（API 自洽性）：
//   1. matched_price_tier_idx == 1（tier[1] 更贵，复杂度溢价）
//   2. current_input_price_rmb > (tier[0] 价格) —— 更贵的阶梯
//   3. 加总自洽：regular + cache_read + cache_write + output = total
func TestCostBreakdown_MultiTier(t *testing.T) {
	bd := fetchBreakdown(t, "test-req-002")

	if !bd.ModelFound {
		t.Fatalf("test-req-002: model not found in DB")
	}
	if bd.MatchedPriceTierIdx != 1 {
		t.Errorf("matched_price_tier_idx=%d, expected 1 (>32k should hit tier 1)",
			bd.MatchedPriceTierIdx)
	}

	// 验证 tier[1] 比 tier[0] 贵（复杂度溢价）
	bd1 := fetchBreakdown(t, "test-req-001")
	if bd.CurrentInputPriceRMB <= bd1.CurrentInputPriceRMB {
		t.Errorf("tier[1] price (%.4f) should be GREATER than tier[0] (%.4f) — 复杂度溢价",
			bd.CurrentInputPriceRMB, bd1.CurrentInputPriceRMB)
	} else {
		t.Logf("✓ tier[1]=¥%.4f > tier[0]=¥%.4f (复杂度溢价 %.1f×)",
			bd.CurrentInputPriceRMB, bd1.CurrentInputPriceRMB,
			bd.CurrentInputPriceRMB/bd1.CurrentInputPriceRMB)
	}

	// 不变量：加总自洽（credits）
	partsSum := bd.RegularInputCost + bd.CacheReadCost + bd.CacheWriteCost + bd.RecomputedOutputCost
	if bd.RecomputedTotalCost != partsSum {
		t.Errorf("total=%d, sum of parts (%d+%d+%d+%d)=%d — breakdown not self-consistent",
			bd.RecomputedTotalCost,
			bd.RegularInputCost, bd.CacheReadCost, bd.CacheWriteCost, bd.RecomputedOutputCost,
			partsSum)
	} else {
		t.Logf("✓ tier[1] self-consistent: regular(%d) + cache_read(%d) + cache_write(%d) + output(%d) = total(%d)",
			bd.RegularInputCost, bd.CacheReadCost, bd.CacheWriteCost, bd.RecomputedOutputCost,
			bd.RecomputedTotalCost)
	}

	// 不变量：regular_input 推导的单价 = current_input_price_per_million
	if bd.RegularInputTokens > 0 && bd.RegularInputCost > 0 {
		actualPrice := float64(bd.RegularInputCost) / float64(bd.RegularInputTokens) * 1e6
		expectedPrice := float64(bd.CurrentInputPricePerMillion())
		if math.Abs(actualPrice-expectedPrice) > 1 {
			t.Errorf("regular price derived (%.2f) != current_input_price_per_million (%.2f)",
				actualPrice, expectedPrice)
		}
	}

	// 不变量：tokens 分桶完备（regular + cache_read + cache_write = prompt）
	bucketSum := bd.RegularInputTokens + bd.CacheReadTokens + bd.CacheWriteTokens
	if bucketSum != bd.Log.PromptTokens {
		t.Errorf("token buckets don't sum to prompt: regular(%d) + cache_read(%d) + cache_write(%d) = %d, expected %d",
			bd.RegularInputTokens, bd.CacheReadTokens, bd.CacheWriteTokens,
			bucketSum, bd.Log.PromptTokens)
	}

	t.Logf("✓ tier[1] total: %d credits = ¥%.6f", bd.RecomputedTotalCost, bd.RecomputedTotalCostRMB)
}

// TestCostBreakdown_CacheSavings 验证缓存节省金额计算正确
//
// cache_savings_rmb 的数学含义：
//   若所有 cache_read tokens 按 regular 全价计费，总成本会多多少
//   savings = cache_read_tokens × (current_input_price_per_million - cache_read_per_million) / 1M / 10000
//
// 注意：API 使用 cache_read_per_million（credits）而不是 cache_input_price_rmb，
// 因为 tier[1] 场景下 cache_input_price_rmb 可能是 tier[0] 的配置而非实际计费价
func TestCostBreakdown_CacheSavings(t *testing.T) {
	cases := []string{"test-req-001", "test-req-002"}

	for _, reqID := range cases {
		t.Run(reqID, func(t *testing.T) {
			bd := fetchBreakdown(t, reqID)
			if !bd.ModelFound || !bd.SupportsCache {
				t.Skipf("model does not support cache, skip")
			}
			if bd.Log.CacheReadTokens == 0 {
				t.Skipf("no cache_read_tokens in log, skip")
			}

			// 不变量 1：cache_read_ratio 必须 ≤ 1（缓存价低于原价）
			if bd.CacheReadRatio > 1.0 {
				t.Errorf("cache_read_ratio=%.3f > 1.0, cache price should be LESS than regular input",
					bd.CacheReadRatio)
			}

			// 不变量 2：cache_read_cost = cache_read_tokens × cache_read_per_million / 1M
			expectedCacheCost := bd.Log.CacheReadTokens * bd.CacheReadPerMillion / 1_000_000
			if math.Abs(float64(bd.CacheReadCost-expectedCacheCost)) > 1 {
				t.Errorf("cache_read_cost=%d, expected %d (= %d × %d / 1M)",
					bd.CacheReadCost, expectedCacheCost,
					bd.Log.CacheReadTokens, bd.CacheReadPerMillion)
			}

			// 不变量 3：savings = cache_read_tokens × (input_per_M - cache_read_per_M) / 1M / 10000 (credits → RMB)
			deltaPerMillion := bd.CurrentInputPricePerMillion() - bd.CacheReadPerMillion
			expectedSavings := float64(bd.Log.CacheReadTokens*deltaPerMillion) / 1e6 / 10000
			if math.Abs(bd.CacheSavingsRMB-expectedSavings) > 0.0001 {
				t.Errorf("cache_savings_rmb=%.6f, expected %.6f (diff=%.6f)\n"+
					"  formula: %d × (%d - %d) / 1M / 10000 = %.6f",
					bd.CacheSavingsRMB, expectedSavings, bd.CacheSavingsRMB-expectedSavings,
					bd.Log.CacheReadTokens, bd.CurrentInputPricePerMillion(), bd.CacheReadPerMillion,
					expectedSavings)
			} else {
				t.Logf("✓ cache savings: %d tokens × %d credits/M delta = ¥%.6f saved",
					bd.Log.CacheReadTokens, deltaPerMillion, expectedSavings)
			}
		})
	}
}

// TestCostBreakdown_PricingUnitConsistency 验证 pricing_unit 字段与 log 的 model 一致
func TestCostBreakdown_PricingUnitConsistency(t *testing.T) {
	bd := fetchBreakdown(t, "test-req-001")
	if bd.PricingUnit != "per_million_tokens" {
		t.Errorf("pricing_unit=%q, expected per_million_tokens for LLM", bd.PricingUnit)
	}
	if bd.CurrentInputPriceRMB <= 0 {
		t.Errorf("current_input_price_rmb should be > 0 for LLM with active pricing")
	}
}
