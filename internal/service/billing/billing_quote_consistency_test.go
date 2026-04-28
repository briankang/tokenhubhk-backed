package billing

import (
	"context"
	"testing"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/provider"
	balancesvc "tokenhub-server/internal/service/balance"
	"tokenhub-server/internal/service/pricing"
)

// TestThreeWayConsistencyTokenStandard 是计价一致性工程的金标准用例。
//
// 同一组输入,经过三条路径必须产出完全一致的金额与 quote_hash:
//  1. QuoteService.Calculate(charge)        — 真实扣费前的纯计算
//  2. BillingService.SettleUsage             — 真实扣费链(写 snapshot)
//  3. extractQuoteFromSnapshot(snapshot)     — 成本分析渲染路径(handler 层)
//
// 此测试是 Spec §一致性规则 #3 的代码层面保证:
// "sum(line_items where section='selling').amount_credits 必须等于 total_cost_credits"
// 同时也验证 charge scenario 下三条路径的 hash 一致性。
func TestThreeWayConsistencyTokenStandard(t *testing.T) {
	db := newBillingTestDB(t)
	seedBillingModel(t, db, 10000, 20000) // 1.0 / 2.0 RMB per million
	seedBillingBalance(t, db, 10, 1, 100000)

	pc := pricing.NewPricingCalculator(db)
	qs := NewQuoteService(db, pc)
	svc := NewService(db, pc, balancesvc.NewBalanceService(db, nil))

	usage := QuoteUsage{InputTokens: 1000, OutputTokens: 500}
	requestID := "consistency-token-1"

	// === 路径 1: Calculate (charge scenario,与 SettleUsage 同场景以保证 hash 可比) ===
	quoteA, err := qs.Calculate(context.Background(), QuoteRequest{
		Scenario:  QuoteScenarioCharge,
		RequestID: requestID,
		ModelID:   1,
		UserID:    10,
		TenantID:  1,
		Usage:     usage,
	})
	if err != nil {
		t.Fatalf("Calculate failed: %v", err)
	}

	// === 路径 2: SettleUsage (真实扣费,写 snapshot.quote) ===
	out, err := svc.SettleUsage(context.Background(), UsageRequest{
		RequestID: requestID,
		UserID:    10,
		TenantID:  1,
		ModelName: "qwen-test",
		Usage: provider.Usage{
			PromptTokens:     usage.InputTokens,
			CompletionTokens: usage.OutputTokens,
		},
	})
	if err != nil {
		t.Fatalf("SettleUsage failed: %v", err)
	}

	// === 路径 3: snapshot.quote (成本分析读取的快照) ===
	rawQuote, ok := out.Snapshot["quote"].(map[string]interface{})
	if !ok || rawQuote == nil {
		t.Fatalf("snapshot.quote missing: %#v", out.Snapshot)
	}

	// === 不变量 1:total_credits 三方一致 ===
	if quoteA.TotalCredits != out.CostCredits {
		t.Fatalf("path1 vs path2 total_credits: %d != %d", quoteA.TotalCredits, out.CostCredits)
	}
	snapTotal := snapshotInt64(rawQuote, "total_credits")
	if snapTotal != quoteA.TotalCredits {
		t.Fatalf("path1 vs path3 total_credits: %d != %d", quoteA.TotalCredits, snapTotal)
	}

	// === 不变量 2:quote_hash 三方一致(同 scenario/同 request_id/同 usage 必产生同 hash)===
	snapHash, _ := rawQuote["quote_hash"].(string)
	if quoteA.QuoteHash != snapHash {
		t.Fatalf("path1 vs path3 quote_hash mismatch:\n  path1=%q\n  path3=%q", quoteA.QuoteHash, snapHash)
	}

	// === 不变量 3:line_items 求和等于 total ===
	var lineSum int64
	if items, ok := rawQuote["line_items"].([]map[string]interface{}); ok {
		for _, li := range items {
			lineSum += snapshotInt64(li, "cost_credits")
		}
	}
	if lineSum != snapTotal {
		t.Fatalf("snapshot line items sum=%d != total=%d", lineSum, snapTotal)
	}

	// === 不变量 4:实扣金额等于应扣金额(deduct 成功路径)===
	if out.ActualCostCredits != out.CostCredits {
		t.Fatalf("actual=%d != cost=%d", out.ActualCostCredits, out.CostCredits)
	}
}

// TestThreeWayConsistencyCachePath 缓存场景下三方一致。
func TestThreeWayConsistencyCachePath(t *testing.T) {
	db := newBillingTestDB(t)
	seedBillingModel(t, db, 10000, 20000)
	if err := db.Model(&model.AIModel{}).Where("id = ?", uint(1)).
		Updates(map[string]interface{}{
			"supports_cache":                 true,
			"cache_mechanism":                "both",
			"cache_input_price_rmb":          0.2,
			"cache_explicit_input_price_rmb": 0.1,
			"cache_write_price_rmb":          1.25,
		}).Error; err != nil {
		t.Fatalf("update cache pricing: %v", err)
	}
	seedBillingBalance(t, db, 11, 1, 100000)

	pc := pricing.NewPricingCalculator(db)
	qs := NewQuoteService(db, pc)
	svc := NewService(db, pc, balancesvc.NewBalanceService(db, nil))

	usage := QuoteUsage{
		InputTokens:      1000,
		OutputTokens:     500,
		CacheReadTokens:  200,
		CacheWriteTokens: 100,
	}
	requestID := "consistency-cache-1"

	quoteA, err := qs.Calculate(context.Background(), QuoteRequest{
		Scenario:  QuoteScenarioCharge,
		RequestID: requestID,
		ModelID:   1,
		UserID:    11,
		TenantID:  1,
		Usage:     usage,
	})
	if err != nil {
		t.Fatalf("Calculate failed: %v", err)
	}

	out, err := svc.SettleUsage(context.Background(), UsageRequest{
		RequestID: requestID,
		UserID:    11,
		TenantID:  1,
		ModelName: "qwen-test",
		Usage: provider.Usage{
			PromptTokens:     usage.InputTokens,
			CompletionTokens: usage.OutputTokens,
			CacheReadTokens:  usage.CacheReadTokens,
			CacheWriteTokens: usage.CacheWriteTokens,
		},
	})
	if err != nil {
		t.Fatalf("SettleUsage failed: %v", err)
	}

	if quoteA.TotalCredits != out.CostCredits {
		t.Fatalf("Calculate vs SettleUsage total: %d != %d", quoteA.TotalCredits, out.CostCredits)
	}
	rawQuote, _ := out.Snapshot["quote"].(map[string]interface{})
	if rawQuote == nil {
		t.Fatal("snapshot.quote missing")
	}
	snapHash, _ := rawQuote["quote_hash"].(string)
	if snapHash != quoteA.QuoteHash {
		t.Fatalf("hash mismatch: calc=%q snap=%q", quoteA.QuoteHash, snapHash)
	}
	// 缓存路径必有 cache_read_input + cache_write_input + regular_input + output 四条 line item
	gotComponents := map[string]bool{}
	if items, ok := rawQuote["line_items"].([]map[string]interface{}); ok {
		for _, li := range items {
			if c, _ := li["component"].(string); c != "" {
				gotComponents[c] = true
			}
		}
	}
	for _, want := range []string{
		quoteComponentRegularInput,
		quoteComponentCacheRead,
		quoteComponentCacheWrite,
		quoteComponentOutput,
	} {
		if !gotComponents[want] {
			t.Fatalf("cache path missing line item %q in %v", want, gotComponents)
		}
	}
}

// TestThreeWayConsistencyImageUnit 图片按张计费场景下三方一致。
func TestThreeWayConsistencyImageUnit(t *testing.T) {
	db := newBillingTestDB(t)
	// 图片模型种子
	imgModel := model.AIModel{
		BaseModel:     model.BaseModel{ID: 2},
		ModelName:     "test-image-model",
		DisplayName:   "Test Image",
		IsActive:      true,
		Status:        "online",
		ModelType:     model.ModelTypeImageGeneration,
		PricingUnit:   model.UnitPerImage,
		InputCostRMB:  0.1,
		OutputCostRMB: 0.1,
	}
	if err := db.Create(&imgModel).Error; err != nil {
		t.Fatalf("seed image model: %v", err)
	}
	imgPricing := model.ModelPricing{
		ModelID:             imgModel.ID,
		InputPricePerToken:  2000, // 2.0 RMB / image
		OutputPricePerToken: 2000,
		InputPriceRMB:       0.2,
		OutputPriceRMB:      0.2,
		Currency:            "CREDIT",
	}
	if err := db.Create(&imgPricing).Error; err != nil {
		t.Fatalf("seed image pricing: %v", err)
	}
	seedBillingBalance(t, db, 12, 1, 100000)

	pc := pricing.NewPricingCalculator(db)
	qs := NewQuoteService(db, pc)
	svc := NewService(db, pc, balancesvc.NewBalanceService(db, nil))

	usage := QuoteUsage{ImageCount: 3}
	requestID := "consistency-image-1"

	quoteA, err := qs.Calculate(context.Background(), QuoteRequest{
		Scenario:  QuoteScenarioCharge,
		RequestID: requestID,
		ModelID:   imgModel.ID,
		UserID:    12,
		TenantID:  1,
		Usage:     usage,
	})
	if err != nil {
		t.Fatalf("Calculate failed: %v", err)
	}

	out, err := svc.SettleUnitUsage(context.Background(), UnitUsageRequest{
		RequestID: requestID,
		UserID:    12,
		TenantID:  1,
		ModelName: imgModel.ModelName,
		Usage: pricing.UsageInput{
			ImageCount: usage.ImageCount,
		},
	})
	if err != nil {
		t.Fatalf("SettleUnitUsage failed: %v", err)
	}

	if quoteA.TotalCredits != out.CostCredits {
		t.Fatalf("Calculate vs SettleUnitUsage total: %d != %d", quoteA.TotalCredits, out.CostCredits)
	}
	rawQuote, _ := out.Snapshot["quote"].(map[string]interface{})
	if rawQuote == nil {
		t.Fatal("unit snapshot.quote missing")
	}
	if h, _ := rawQuote["quote_hash"].(string); h != quoteA.QuoteHash {
		t.Fatalf("hash mismatch: calc=%q snap=%q", quoteA.QuoteHash, h)
	}
	// 图片路径必有 image_unit line item
	if items, ok := rawQuote["line_items"].([]map[string]interface{}); ok {
		hasImage := false
		for _, li := range items {
			if c, _ := li["component"].(string); c == quoteComponentImage {
				hasImage = true
				break
			}
		}
		if !hasImage {
			t.Fatalf("image path missing image_unit line item: %v", items)
		}
	}
}

// TestQuoteHashScenarioSensitivity 不同 scenario 下 hash 不同(preview vs charge)。
//
// 这保证管理员试算与真实扣费即便用量相同也产生不同的 quote_id/quote_hash,
// 便于审计追踪请求来源。
func TestQuoteHashScenarioSensitivity(t *testing.T) {
	db := newBillingTestDB(t)
	seedBillingModel(t, db, 10000, 20000)

	qs := NewQuoteService(db, pricing.NewPricingCalculator(db))
	usage := QuoteUsage{InputTokens: 1000, OutputTokens: 500}

	previewQuote, err := qs.Calculate(context.Background(), QuoteRequest{
		Scenario:  QuoteScenarioPreview,
		RequestID: "scenario-test",
		ModelID:   1,
		Usage:     usage,
	})
	if err != nil {
		t.Fatalf("preview Calculate failed: %v", err)
	}

	chargeQuote, err := qs.Calculate(context.Background(), QuoteRequest{
		Scenario:  QuoteScenarioCharge,
		RequestID: "scenario-test",
		ModelID:   1,
		Usage:     usage,
	})
	if err != nil {
		t.Fatalf("charge Calculate failed: %v", err)
	}

	// total 相同(同样的定价计算),但 hash 不同(因为 scenario 是 hash 输入之一)
	if previewQuote.TotalCredits != chargeQuote.TotalCredits {
		t.Fatalf("scenario should not change total_credits: preview=%d, charge=%d", previewQuote.TotalCredits, chargeQuote.TotalCredits)
	}
	if previewQuote.QuoteHash == chargeQuote.QuoteHash {
		t.Fatalf("preview and charge should have different quote_hash, both = %q", previewQuote.QuoteHash)
	}
}
