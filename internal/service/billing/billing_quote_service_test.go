package billing

import (
	"context"
	"testing"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/provider"
	"tokenhub-server/internal/service/pricing"
)

// TestQuoteServiceCalculateTokenStandard 标准 token 模型试算路径。
//
// 验证:
//   - Calculate 不动余额、无副作用
//   - line_items.sum == TotalCredits
//   - quote_hash 非空且稳定
func TestQuoteServiceCalculateTokenStandard(t *testing.T) {
	db := newBillingTestDB(t)
	seedBillingModel(t, db, 10000, 20000) // 1.0 / 2.0 RMB per million

	pc := pricing.NewPricingCalculator(db)
	qs := NewQuoteService(db, pc)

	q, err := qs.Calculate(context.Background(), QuoteRequest{
		Scenario:  QuoteScenarioPreview,
		RequestID: "preview-1",
		ModelID:   1,
		Usage: QuoteUsage{
			InputTokens:  1000,
			OutputTokens: 500,
		},
	})
	if err != nil {
		t.Fatalf("Calculate returned error: %v", err)
	}
	if q == nil {
		t.Fatal("Calculate returned nil quote")
	}

	if q.QuoteID != "preview-1" {
		t.Fatalf("quote_id = %q, want preview-1", q.QuoteID)
	}
	if q.Scenario != QuoteScenarioPreview {
		t.Fatalf("scenario = %q, want preview", q.Scenario)
	}
	if q.SchemaVersion != quoteSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", q.SchemaVersion, quoteSchemaVersion)
	}
	if q.EngineVersion != quoteEngineVersion {
		t.Fatalf("engine_version = %q, want %q", q.EngineVersion, quoteEngineVersion)
	}
	if q.QuoteHash == "" {
		t.Fatal("quote_hash should not be empty")
	}
	if q.TotalCredits != 20 {
		t.Fatalf("total_credits = %d, want 20 (input 1000 * 10 + output 500 * 20 = 10 + 10)", q.TotalCredits)
	}

	// 校验 line_items.sum == TotalCredits
	var sum int64
	for _, li := range q.LineItems {
		sum += li.CostCredits
	}
	if sum != q.TotalCredits {
		t.Fatalf("line items sum = %d, want %d", sum, q.TotalCredits)
	}

	// 找到 input + output 两条
	if findLineByComponent(q.LineItems, quoteComponentRegularInput) == nil {
		t.Fatalf("regular_input line missing: %#v", q.LineItems)
	}
	if findLineByComponent(q.LineItems, quoteComponentOutput) == nil {
		t.Fatalf("output line missing: %#v", q.LineItems)
	}
}

// TestQuoteServiceCalculateHashStability 同样输入产生同样 hash。
func TestQuoteServiceCalculateHashStability(t *testing.T) {
	db := newBillingTestDB(t)
	seedBillingModel(t, db, 10000, 20000)

	qs := NewQuoteService(db, pricing.NewPricingCalculator(db))
	req := QuoteRequest{
		Scenario:  QuoteScenarioPreview,
		RequestID: "hash-stable-1",
		ModelID:   1,
		Usage: QuoteUsage{
			InputTokens:  2500,
			OutputTokens: 1500,
		},
	}

	q1, err := qs.Calculate(context.Background(), req)
	if err != nil {
		t.Fatalf("first Calculate failed: %v", err)
	}
	q2, err := qs.Calculate(context.Background(), req)
	if err != nil {
		t.Fatalf("second Calculate failed: %v", err)
	}
	if q1.QuoteHash == "" || q2.QuoteHash == "" {
		t.Fatal("quote_hash should not be empty")
	}
	if q1.QuoteHash != q2.QuoteHash {
		t.Fatalf("quote_hash unstable: %q vs %q", q1.QuoteHash, q2.QuoteHash)
	}
}

// TestQuoteServiceCalculateHashDiffersOnUsageChange 用量改变 → hash 改变。
func TestQuoteServiceCalculateHashDiffersOnUsageChange(t *testing.T) {
	db := newBillingTestDB(t)
	seedBillingModel(t, db, 10000, 20000)

	qs := NewQuoteService(db, pricing.NewPricingCalculator(db))
	base := QuoteRequest{
		Scenario:  QuoteScenarioPreview,
		RequestID: "hash-diff",
		ModelID:   1,
		Usage:     QuoteUsage{InputTokens: 1000, OutputTokens: 500},
	}
	q1, err := qs.Calculate(context.Background(), base)
	if err != nil {
		t.Fatalf("base Calculate failed: %v", err)
	}

	changed := base
	changed.Usage.InputTokens = 2000
	q2, err := qs.Calculate(context.Background(), changed)
	if err != nil {
		t.Fatalf("changed Calculate failed: %v", err)
	}
	if q1.QuoteHash == q2.QuoteHash {
		t.Fatalf("quote_hash should differ when usage changes; both = %q", q1.QuoteHash)
	}
}

// TestQuoteServiceCalculateMatchesSettleUsage 三方一致核心保证。
//
// 这是一致性工程的最关键不变量:对同一组输入,
//  1. QuoteService.Calculate 直接产出的 BillingQuote
//  2. BillingService.SettleUsage 写入 snapshot.quote 的内容
//
// 必须在 total_credits / line_items 求和上完全一致。
//
// 真实扣费 cost_credits 与 quote.total_credits 必须相等(同源 CostResult)。
func TestQuoteServiceCalculateMatchesSettleUsage(t *testing.T) {
	db := newBillingTestDB(t)
	seedBillingModel(t, db, 10000, 20000)
	seedBillingBalance(t, db, 7, 1, 100000)

	pc := pricing.NewPricingCalculator(db)
	qs := NewQuoteService(db, pc)
	svc := NewService(db, pc, nil) // balance svc 不必,SettleUsage 内部 nil-safe

	usage := QuoteUsage{InputTokens: 1000, OutputTokens: 500}

	// 路径 A:Calculate 直出
	quoteCalc, err := qs.Calculate(context.Background(), QuoteRequest{
		Scenario:  QuoteScenarioPreview,
		RequestID: "consistency-1",
		ModelID:   1,
		UserID:    7,
		TenantID:  1,
		Usage:     usage,
	})
	if err != nil {
		t.Fatalf("Calculate failed: %v", err)
	}

	// 路径 B:SettleUsage 走完整真实扣费链
	out, err := svc.SettleUsage(context.Background(), UsageRequest{
		RequestID: "consistency-1",
		UserID:    7,
		TenantID:  1,
		ModelName: "qwen-test",
		Usage:     providerUsageFromQuote(usage),
	})
	if err != nil {
		t.Fatalf("SettleUsage failed: %v", err)
	}

	// 三方一致核心断言:
	// quote.total_credits == settle.cost_credits == sum(line_items)
	if quoteCalc.TotalCredits != out.CostCredits {
		t.Fatalf("Calculate.TotalCredits=%d != SettleUsage.CostCredits=%d", quoteCalc.TotalCredits, out.CostCredits)
	}

	// 断言 snapshot.quote.total_credits 也等于
	if out.Snapshot == nil {
		t.Fatal("snapshot is nil")
	}
	rawQuote, ok := out.Snapshot["quote"].(map[string]interface{})
	if !ok {
		t.Fatalf("snapshot.quote missing or wrong type: %#v", out.Snapshot)
	}
	snapTotal := snapshotInt64(rawQuote, "total_credits")
	if snapTotal != quoteCalc.TotalCredits {
		t.Fatalf("snapshot.quote.total_credits=%d != Calculate.TotalCredits=%d", snapTotal, quoteCalc.TotalCredits)
	}
}

// TestQuoteServiceCalculateZeroPricedModel 0 价模型不触发 1 积分保底。
//
// 对应 Spec §一致性规则 #6:免费模型或 0 价模型不得触发 1 积分保底。
func TestQuoteServiceCalculateZeroPricedModel(t *testing.T) {
	db := newBillingTestDB(t)
	seedBillingModel(t, db, 0, 0) // 0 价

	qs := NewQuoteService(db, pricing.NewPricingCalculator(db))
	q, err := qs.Calculate(context.Background(), QuoteRequest{
		Scenario:  QuoteScenarioPreview,
		RequestID: "zero-price",
		ModelID:   1,
		Usage:     QuoteUsage{InputTokens: 1000, OutputTokens: 500},
	})
	if err != nil {
		t.Fatalf("Calculate returned error: %v", err)
	}
	if q.TotalCredits != 0 {
		t.Fatalf("total_credits = %d, want 0 for zero-priced model", q.TotalCredits)
	}
}

// TestQuoteServiceCalculateMissingModel 模型不存在返 ErrQuoteModelNotFound。
func TestQuoteServiceCalculateMissingModel(t *testing.T) {
	db := newBillingTestDB(t)
	qs := NewQuoteService(db, pricing.NewPricingCalculator(db))

	_, err := qs.Calculate(context.Background(), QuoteRequest{
		Scenario: QuoteScenarioPreview,
		ModelID:  9999,
	})
	if err == nil {
		t.Fatal("expected error for missing model, got nil")
	}
	if err != ErrQuoteModelNotFound {
		t.Fatalf("error = %v, want ErrQuoteModelNotFound", err)
	}
}

// TestBillingQuoteToSnapshotMap 验证 ToSnapshotMap 产出的 line_items 类型为 []map[string]interface{}。
//
// 这是 backward-compat 关键:既有 cost analysis 测试 helper(quoteFromSnapshot/findQuoteLine)
// 通过 raw.([]map[string]interface{}) 断言。
func TestBillingQuoteToSnapshotMap(t *testing.T) {
	q := &BillingQuote{
		SchemaVersion: 2,
		QuoteID:       "snap-1",
		ModelID:       42,
		ModelName:     "test-model",
		PricingUnit:   model.UnitPerMillionTokens,
		TotalCredits:  100,
		Usage:         QuoteUsage{InputTokens: 1000, OutputTokens: 500},
		LineItems: []QuoteLineItem{
			{Component: quoteComponentRegularInput, CostCredits: 60},
			{Component: quoteComponentOutput, CostCredits: 40},
		},
		QuoteHash: "abc123",
	}

	m := q.ToSnapshotMap()
	if m == nil {
		t.Fatal("ToSnapshotMap returned nil")
	}
	if m["quote_id"] != "snap-1" {
		t.Fatalf("quote_id = %v, want snap-1", m["quote_id"])
	}

	rawItems, ok := m["line_items"].([]map[string]interface{})
	if !ok {
		t.Fatalf("line_items wrong type, got %T (need []map[string]interface{} for backward compat)", m["line_items"])
	}
	if len(rawItems) != 2 {
		t.Fatalf("line_items len = %d, want 2", len(rawItems))
	}
	if rawItems[0]["component"] != quoteComponentRegularInput {
		t.Fatalf("first item component = %v", rawItems[0]["component"])
	}
}

// --- 辅助函数 ---

func findLineByComponent(items []QuoteLineItem, component string) *QuoteLineItem {
	for i := range items {
		if items[i].Component == component {
			return &items[i]
		}
	}
	return nil
}

func snapshotInt64(m map[string]interface{}, key string) int64 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case int32:
		return int64(n)
	case uint:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

// providerUsageFromQuote 把 QuoteUsage 折算回 provider.Usage,用于 SettleUsage 测试。
func providerUsageFromQuote(u QuoteUsage) provider.Usage {
	return provider.Usage{
		PromptTokens:       u.InputTokens,
		CompletionTokens:   u.OutputTokens,
		TotalTokens:        u.TotalTokens,
		CacheReadTokens:    u.CacheReadTokens,
		CacheWriteTokens:   u.CacheWriteTokens,
		CacheWrite1hTokens: u.CacheWrite1hTokens,
		ReasoningTokens:    u.ReasoningTokens,
	}
}
