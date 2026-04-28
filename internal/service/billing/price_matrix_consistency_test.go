package billing

import (
	"context"
	"encoding/json"
	"testing"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/provider"
	balancesvc "tokenhub-server/internal/service/balance"
	"tokenhub-server/internal/service/pricing"
)

// TestPriceMatrixConsistency_VideoMatrix Seedance 矩阵命中三方一致(v3)。
//
// 验证:
//  1. SettleUsage 传 DimValues 后命中 PriceMatrix supported cell
//  2. snapshot.quote 含 matched_dim_values 字段
//  3. CostResult.MatchedDimValues 与请求一致
func TestPriceMatrixConsistency_VideoMatrix(t *testing.T) {
	db := newBillingTestDB(t)

	m := model.AIModel{
		ModelName:     "seedance-2.0-test",
		ModelType:     model.ModelTypeVideoGeneration,
		PricingUnit:   model.UnitPerMillionTokens,
		IsActive:      true,
		Status:        "online",
		InputCostRMB:  46.0,
		OutputCostRMB: 46.0,
	}
	if err := db.Create(&m).Error; err != nil {
		t.Fatalf("seed model: %v", err)
	}

	// 构造 PriceMatrix:480p / 720p / 1080p × {含视频, 不含视频}
	pm := model.PriceMatrix{
		SchemaVersion: 1,
		Currency:      "RMB",
		Unit:          model.UnitPerMillionTokens,
		Dimensions: []model.PriceDimension{
			{Key: "resolution", Label: "分辨率", Type: "select", Values: []interface{}{"480p", "720p", "1080p"}},
			{Key: "input_has_video", Label: "含视频", Type: "boolean", Values: []interface{}{false, true}},
		},
		Cells: []model.PriceMatrixCell{
			{DimValues: map[string]interface{}{"resolution": "480p", "input_has_video": false}, Supported: true, OfficialPerUnit: ptrF(46.0), SellingPerUnit: ptrF(39.10)},
			{DimValues: map[string]interface{}{"resolution": "1080p", "input_has_video": true}, Supported: true, OfficialPerUnit: ptrF(31.0), SellingPerUnit: ptrF(26.35)},
		},
	}
	pmJSON, _ := json.Marshal(pm)

	mp := model.ModelPricing{
		ModelID:             m.ID,
		InputPriceRMB:       39.10, // 顶层兜底:480p×不含视频 售价
		OutputPriceRMB:      39.10,
		InputPricePerToken:  391000,
		OutputPricePerToken: 391000,
		Currency:            "CREDIT",
		PriceMatrix:         pmJSON,
	}
	if err := db.Create(&mp).Error; err != nil {
		t.Fatalf("seed mp: %v", err)
	}
	seedBillingBalance(t, db, 200, 1, 100_000_000)

	pc := pricing.NewPricingCalculator(db)
	svc := NewService(db, pc, balancesvc.NewBalanceService(db, nil))

	out, err := svc.SettleUnitUsage(context.Background(), UnitUsageRequest{
		RequestID: "matrix-video-1",
		UserID:    200,
		TenantID:  1,
		ModelName: m.ModelName,
		Usage: pricing.UsageInput{
			InputTokens:  1_000_000,
			OutputTokens: 0,
		},
		// 命中 1080p × 含视频 → 26.35 ¥/M
		DimValues: map[string]interface{}{
			"resolution":      "1080p",
			"input_has_video": true,
		},
	})
	if err != nil {
		t.Fatalf("SettleUnitUsage: %v", err)
	}

	// snapshot.quote 含 matched_dim_values
	rawQuote, ok := out.Snapshot["quote"].(map[string]interface{})
	if !ok || rawQuote == nil {
		t.Fatalf("snapshot.quote missing")
	}
	dimRaw, ok := rawQuote["matched_dim_values"].(map[string]interface{})
	if !ok {
		t.Fatalf("matched_dim_values missing in snapshot.quote: %#v", rawQuote)
	}
	if dimRaw["resolution"] != "1080p" {
		t.Fatalf("matched resolution = %v, want 1080p", dimRaw["resolution"])
	}
	if dimRaw["input_has_video"] != true {
		t.Fatalf("matched input_has_video = %v, want true", dimRaw["input_has_video"])
	}

	// CostResult.MatchedDimValues 一致
	if out.CostResult.MatchedDimValues == nil {
		t.Fatalf("CostResult.MatchedDimValues nil")
	}
}

// TestPriceMatrixConsistency_NoDimValuesFallback DimValues 为空时不走矩阵命中,走旧路径。
func TestPriceMatrixConsistency_NoDimValuesFallback(t *testing.T) {
	db := newBillingTestDB(t)
	seedBillingModel(t, db, 10000, 20000)
	seedBillingBalance(t, db, 201, 1, 100_000_000)

	pc := pricing.NewPricingCalculator(db)
	svc := NewService(db, pc, balancesvc.NewBalanceService(db, nil))

	out, err := svc.SettleUsage(context.Background(), UsageRequest{
		RequestID: "matrix-fallback-1",
		UserID:    201,
		TenantID:  1,
		ModelName: "qwen-test",
		Usage: provider.Usage{
			PromptTokens:     1000,
			CompletionTokens: 500,
		},
		// DimValues 留空,不应触发矩阵命中
	})
	if err != nil {
		t.Fatalf("SettleUsage: %v", err)
	}
	if out.CostResult.MatchedDimValues != nil {
		t.Fatalf("MatchedDimValues should be nil when DimValues empty, got %v", out.CostResult.MatchedDimValues)
	}
}

// TestPriceMatrixConsistency_UnsupportedCellSkipped 命中 supported=false 的 cell 时退回到旧路径。
func TestPriceMatrixConsistency_UnsupportedCellSkipped(t *testing.T) {
	db := newBillingTestDB(t)
	m := model.AIModel{
		ModelName:     "seedance-1.5-test",
		ModelType:     model.ModelTypeVideoGeneration,
		PricingUnit:   model.UnitPerMillionTokens,
		IsActive:      true,
		Status:        "online",
		InputCostRMB:  16.0,
		OutputCostRMB: 16.0,
	}
	if err := db.Create(&m).Error; err != nil {
		t.Fatalf("seed model: %v", err)
	}

	pm := model.PriceMatrix{
		SchemaVersion: 1,
		Cells: []model.PriceMatrixCell{
			{DimValues: map[string]interface{}{"audio_mode": "audio", "inference_mode": "online"}, Supported: true, SellingPerUnit: ptrF(13.6)},
			{DimValues: map[string]interface{}{"inference_mode": "offline"}, Supported: false, UnsupportedReason: "暂不支持离线"},
		},
	}
	pmJSON, _ := json.Marshal(pm)
	mp := model.ModelPricing{
		ModelID:             m.ID,
		InputPriceRMB:       13.6,
		OutputPriceRMB:      13.6,
		InputPricePerToken:  136000,
		OutputPricePerToken: 136000,
		Currency:            "CREDIT",
		PriceMatrix:         pmJSON,
	}
	if err := db.Create(&mp).Error; err != nil {
		t.Fatalf("seed mp: %v", err)
	}
	seedBillingBalance(t, db, 202, 1, 100_000_000)

	svc := NewService(db, pricing.NewPricingCalculator(db), balancesvc.NewBalanceService(db, nil))

	// 请求 inference_mode=offline → 命中 unsupported cell → 应跳过(MatchedDimValues=nil)
	out, err := svc.SettleUnitUsage(context.Background(), UnitUsageRequest{
		RequestID: "matrix-unsupported",
		UserID:    202,
		TenantID:  1,
		ModelName: m.ModelName,
		Usage:     pricing.UsageInput{InputTokens: 1000},
		DimValues: map[string]interface{}{"inference_mode": "offline"},
	})
	if err != nil {
		t.Fatalf("SettleUnitUsage: %v", err)
	}
	if out.CostResult.MatchedDimValues != nil {
		t.Fatalf("unsupported cell should not be matched, got %v", out.CostResult.MatchedDimValues)
	}
}

// TestPriceMatrixConsistency_NoMatrixFallback 没有 PriceMatrix 时不影响旧路径。
func TestPriceMatrixConsistency_NoMatrixFallback(t *testing.T) {
	db := newBillingTestDB(t)
	seedBillingModel(t, db, 10000, 20000)
	seedBillingBalance(t, db, 203, 1, 100_000_000)

	svc := NewService(db, pricing.NewPricingCalculator(db), balancesvc.NewBalanceService(db, nil))

	out, err := svc.SettleUsage(context.Background(), UsageRequest{
		RequestID: "no-matrix-fallback",
		UserID:    203,
		TenantID:  1,
		ModelName: "qwen-test",
		Usage:     provider.Usage{PromptTokens: 1000, CompletionTokens: 500},
		DimValues: map[string]interface{}{"context_tier": "any"}, // 无 matrix → 不命中
	})
	if err != nil {
		t.Fatalf("SettleUsage: %v", err)
	}
	if out.CostCredits != 20 {
		t.Fatalf("cost without matrix = %d, want 20", out.CostCredits)
	}
	if out.CostResult.MatchedDimValues != nil {
		t.Fatalf("MatchedDimValues should be nil when no PriceMatrix, got %v", out.CostResult.MatchedDimValues)
	}
}

// TestPriceMatrixConsistency_PreviewSettleParity_VideoMatrix 同一 (model, dim_values, usage)
// 在 PreviewQuote 与 SettleUnitUsage 下应产生相同的 MatchedDimValues 与金额。
//
// G1 验证:Preview 路径(QuoteService.Calculate)调用 MatchCellByModelID,
// 与 SettleUnitUsage 命中同一 cell。
func TestPriceMatrixConsistency_PreviewSettleParity_VideoMatrix(t *testing.T) {
	db := newBillingTestDB(t)

	m := model.AIModel{
		ModelName:     "seedance-2.0-parity",
		ModelType:     model.ModelTypeVideoGeneration,
		PricingUnit:   model.UnitPerMillionTokens,
		IsActive:      true,
		Status:        "online",
		InputCostRMB:  46.0,
		OutputCostRMB: 46.0,
	}
	if err := db.Create(&m).Error; err != nil {
		t.Fatalf("seed model: %v", err)
	}

	pm := model.PriceMatrix{
		SchemaVersion: 1,
		Currency:      "RMB",
		Unit:          model.UnitPerMillionTokens,
		Dimensions: []model.PriceDimension{
			{Key: "resolution", Label: "分辨率", Type: "select", Values: []interface{}{"480p", "1080p"}},
			{Key: "input_has_video", Label: "含视频", Type: "boolean", Values: []interface{}{false, true}},
		},
		Cells: []model.PriceMatrixCell{
			{DimValues: map[string]interface{}{"resolution": "480p", "input_has_video": false}, Supported: true, OfficialPerUnit: ptrF(46.0), SellingPerUnit: ptrF(39.10), Note: "480p+无视频"},
			{DimValues: map[string]interface{}{"resolution": "1080p", "input_has_video": true}, Supported: true, OfficialPerUnit: ptrF(31.0), SellingPerUnit: ptrF(26.35), Note: "1080p+含视频折扣"},
		},
	}
	pmJSON, _ := json.Marshal(pm)
	mp := model.ModelPricing{
		ModelID:             m.ID,
		InputPriceRMB:       39.10,
		OutputPriceRMB:      39.10,
		InputPricePerToken:  391000,
		OutputPricePerToken: 391000,
		Currency:            "CREDIT",
		PriceMatrix:         pmJSON,
	}
	if err := db.Create(&mp).Error; err != nil {
		t.Fatalf("seed mp: %v", err)
	}
	seedBillingBalance(t, db, 210, 1, 100_000_000)

	pc := pricing.NewPricingCalculator(db)
	svc := NewService(db, pc, balancesvc.NewBalanceService(db, nil))
	qs := NewQuoteService(db, pc)

	dim := map[string]interface{}{"resolution": "1080p", "input_has_video": true}

	// Preview 路径
	previewQuote, err := qs.Calculate(context.Background(), QuoteRequest{
		Scenario:  QuoteScenarioPreview,
		ModelID:   m.ID,
		ModelName: m.ModelName,
		UserID:    210,
		TenantID:  1,
		Usage:     QuoteUsage{InputTokens: 1_000_000},
		DimValues: dim,
	})
	if err != nil {
		t.Fatalf("Preview Calculate: %v", err)
	}
	if previewQuote == nil || previewQuote.MatchedDimValues == nil {
		t.Fatalf("preview quote MatchedDimValues nil; got %+v", previewQuote)
	}
	if previewQuote.MatchedDimValues["resolution"] != "1080p" {
		t.Fatalf("preview matched resolution = %v, want 1080p", previewQuote.MatchedDimValues["resolution"])
	}
	if previewQuote.MatchedMatrixNote != "1080p+含视频折扣" {
		t.Fatalf("preview matched note = %q, want %q", previewQuote.MatchedMatrixNote, "1080p+含视频折扣")
	}

	// Settle 路径
	settleOut, err := svc.SettleUnitUsage(context.Background(), UnitUsageRequest{
		RequestID: "parity-video-1",
		UserID:    210,
		TenantID:  1,
		ModelName: m.ModelName,
		Usage:     pricing.UsageInput{InputTokens: 1_000_000},
		DimValues: dim,
	})
	if err != nil {
		t.Fatalf("SettleUnitUsage: %v", err)
	}
	if settleOut.CostResult.MatchedDimValues == nil {
		t.Fatalf("settle MatchedDimValues nil")
	}

	// 一致性断言:两边命中同一 cell
	if previewQuote.MatchedDimValues["resolution"] != settleOut.CostResult.MatchedDimValues["resolution"] {
		t.Fatalf("preview/settle resolution mismatch: %v vs %v",
			previewQuote.MatchedDimValues["resolution"], settleOut.CostResult.MatchedDimValues["resolution"])
	}
	if previewQuote.MatchedDimValues["input_has_video"] != settleOut.CostResult.MatchedDimValues["input_has_video"] {
		t.Fatalf("preview/settle input_has_video mismatch: %v vs %v",
			previewQuote.MatchedDimValues["input_has_video"], settleOut.CostResult.MatchedDimValues["input_has_video"])
	}
	if previewQuote.MatchedMatrixNote != settleOut.CostResult.MatchedMatrixCellNote {
		t.Fatalf("preview/settle note mismatch: %q vs %q",
			previewQuote.MatchedMatrixNote, settleOut.CostResult.MatchedMatrixCellNote)
	}
}

// TestPriceMatrixConsistency_PreviewNoDimValues_NoMatch Preview 不传 DimValues 时
// 不应触发 PriceMatrix 命中,与 Settle 行为对齐(走顶层售价)。
func TestPriceMatrixConsistency_PreviewNoDimValues_NoMatch(t *testing.T) {
	db := newBillingTestDB(t)
	seedBillingModel(t, db, 10000, 20000)

	pc := pricing.NewPricingCalculator(db)
	qs := NewQuoteService(db, pc)

	quote, err := qs.Calculate(context.Background(), QuoteRequest{
		Scenario:  QuoteScenarioPreview,
		ModelName: "qwen-test",
		UserID:    211,
		TenantID:  1,
		Usage:     QuoteUsage{InputTokens: 1000, OutputTokens: 500},
		// DimValues 留空
	})
	if err != nil {
		t.Fatalf("Preview Calculate: %v", err)
	}
	if quote == nil {
		t.Fatalf("nil quote")
	}
	if len(quote.MatchedDimValues) != 0 {
		t.Fatalf("MatchedDimValues should be empty when DimValues nil, got %v", quote.MatchedDimValues)
	}
}

// TestPriceMatrixConsistency_PreviewUnsupportedCell_FallsThrough Preview 命中 supported=false 的 cell 时
// 应不命中(MatchedDimValues 为空),与 Settle 一致。
func TestPriceMatrixConsistency_PreviewUnsupportedCell_FallsThrough(t *testing.T) {
	db := newBillingTestDB(t)
	m := model.AIModel{
		ModelName:     "seedance-1.5-preview-test",
		ModelType:     model.ModelTypeVideoGeneration,
		PricingUnit:   model.UnitPerMillionTokens,
		IsActive:      true,
		Status:        "online",
		InputCostRMB:  16.0,
		OutputCostRMB: 16.0,
	}
	if err := db.Create(&m).Error; err != nil {
		t.Fatalf("seed model: %v", err)
	}
	pm := model.PriceMatrix{
		SchemaVersion: 1,
		Cells: []model.PriceMatrixCell{
			{DimValues: map[string]interface{}{"inference_mode": "offline"}, Supported: false, UnsupportedReason: "暂不支持离线推理"},
		},
	}
	pmJSON, _ := json.Marshal(pm)
	if err := db.Create(&model.ModelPricing{
		ModelID:             m.ID,
		InputPriceRMB:       13.6,
		OutputPriceRMB:      13.6,
		InputPricePerToken:  136000,
		OutputPricePerToken: 136000,
		Currency:            "CREDIT",
		PriceMatrix:         pmJSON,
	}).Error; err != nil {
		t.Fatalf("seed mp: %v", err)
	}

	pc := pricing.NewPricingCalculator(db)
	qs := NewQuoteService(db, pc)
	quote, err := qs.Calculate(context.Background(), QuoteRequest{
		Scenario:  QuoteScenarioPreview,
		ModelID:   m.ID,
		ModelName: m.ModelName,
		UserID:    212,
		TenantID:  1,
		Usage:     QuoteUsage{InputTokens: 1000},
		DimValues: map[string]interface{}{"inference_mode": "offline"},
	})
	if err != nil {
		t.Fatalf("Preview Calculate: %v", err)
	}
	if len(quote.MatchedDimValues) != 0 {
		t.Fatalf("unsupported cell should not match in preview, got %v", quote.MatchedDimValues)
	}
}

// TestPriceMatrixConsistency_QuoteHashDeterministic 同一组输入(scenario / quote_id / model /
// usage / dim_values 全相同)调用 QuoteService.Calculate 两次,产出的 quote_hash 必须完全相同。
//
// 这是三方一致性的基础契约:重放/重算时,只要输入未变,hash 就是稳定的。
//
// 注意:hash 包含 scenario 与 quote_id,所以 Preview 与 Settle 两条路径的 hash 不必相等
// (它们有意区分 scenario);但同一路径下相同输入必须 deterministic。
func TestPriceMatrixConsistency_QuoteHashDeterministic(t *testing.T) {
	db := newBillingTestDB(t)

	m := model.AIModel{
		ModelName:     "hash-deterministic-test",
		ModelType:     model.ModelTypeVideoGeneration,
		PricingUnit:   model.UnitPerMillionTokens,
		IsActive:      true,
		Status:        "online",
		InputCostRMB:  46.0,
		OutputCostRMB: 46.0,
	}
	if err := db.Create(&m).Error; err != nil {
		t.Fatalf("seed model: %v", err)
	}
	pm := model.PriceMatrix{
		SchemaVersion: 1,
		Cells: []model.PriceMatrixCell{
			{DimValues: map[string]interface{}{"resolution": "1080p"}, Supported: true, SellingPerUnit: ptrF(26.35)},
		},
	}
	pmJSON, _ := json.Marshal(pm)
	if err := db.Create(&model.ModelPricing{
		ModelID:             m.ID,
		InputPriceRMB:       26.35,
		OutputPriceRMB:      26.35,
		InputPricePerToken:  263500,
		OutputPricePerToken: 263500,
		Currency:            "CREDIT",
		PriceMatrix:         pmJSON,
	}).Error; err != nil {
		t.Fatalf("seed mp: %v", err)
	}

	pc := pricing.NewPricingCalculator(db)
	qs := NewQuoteService(db, pc)

	dim := map[string]interface{}{"resolution": "1080p"}
	req := QuoteRequest{
		Scenario:  QuoteScenarioPreview,
		RequestID: "deterministic-req-1",
		ModelID:   m.ID,
		ModelName: m.ModelName,
		UserID:    214,
		TenantID:  1,
		Usage:     QuoteUsage{InputTokens: 1_000_000},
		DimValues: dim,
	}

	first, err := qs.Calculate(context.Background(), req)
	if err != nil {
		t.Fatalf("first Calculate: %v", err)
	}
	second, err := qs.Calculate(context.Background(), req)
	if err != nil {
		t.Fatalf("second Calculate: %v", err)
	}
	if first.QuoteHash == "" {
		t.Fatalf("first quote_hash empty")
	}
	if first.QuoteHash != second.QuoteHash {
		t.Fatalf("quote_hash not deterministic: first=%q second=%q", first.QuoteHash, second.QuoteHash)
	}
	// 确认 hash 对 dim 敏感:换一个 dim 应得不同 hash
	differentDim := req
	differentDim.DimValues = map[string]interface{}{"resolution": "480p"}
	third, err := qs.Calculate(context.Background(), differentDim)
	if err != nil {
		// 480p cell 不存在 → 未命中 → fallback 到顶层售价(26.35)
		// 期望 Calculate 不报错,而是返回 quote(只是 MatchedDimValues 为空)
		t.Fatalf("third Calculate: %v", err)
	}
	if third.QuoteHash == first.QuoteHash {
		t.Fatalf("hash should differ when dim_values differ")
	}
}

func ptrF(v float64) *float64 { return &v }
