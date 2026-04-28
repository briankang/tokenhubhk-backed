package pricing

import (
	"testing"

	"tokenhub-server/internal/model"
)

func float64Ptr(v float64) *float64 { return &v }

// TestBuildTierSelectionFromMatrixCell_FullPrice 验证完整 cell 转换
func TestBuildTierSelectionFromMatrixCell_FullPrice(t *testing.T) {
	cell := &model.PriceMatrixCell{
		DimValues:      map[string]interface{}{"resolution": "1080p", "input_has_video": "true"},
		OfficialInput:  float64Ptr(31),
		OfficialOutput: float64Ptr(31),
		SellingInput:   float64Ptr(46.5),
		SellingOutput:  float64Ptr(46.5),
		Supported:      true,
		Note:           "1080p × 含视频",
	}
	aiModel := &model.AIModel{InputCostRMB: 31, OutputCostRMB: 31}

	sel := buildTierSelectionFromMatrixCell(cell, aiModel)
	if sel == nil {
		t.Fatal("expected non-nil selection")
	}
	if sel.InputPriceRMB != 46.5 || sel.OutputPriceRMB != 46.5 {
		t.Errorf("selling: input=%v output=%v, want both 46.5", sel.InputPriceRMB, sel.OutputPriceRMB)
	}
	if sel.PlatformInputPriceRMB != 31 || sel.PlatformOutputPriceRMB != 31 {
		t.Errorf("cost: input=%v output=%v, want both 31", sel.PlatformInputPriceRMB, sel.PlatformOutputPriceRMB)
	}
	if !sel.FromTier || !sel.SellingOverride || !sel.FromMatrix {
		t.Errorf("flags: FromTier=%v SellingOverride=%v FromMatrix=%v, want all true", sel.FromTier, sel.SellingOverride, sel.FromMatrix)
	}
	if sel.MatrixCellNote != "1080p × 含视频" {
		t.Errorf("MatrixCellNote=%q, want '1080p × 含视频'", sel.MatrixCellNote)
	}
	if len(sel.MatrixCellDimValues) != 2 {
		t.Errorf("MatrixCellDimValues=%v, want 2 entries", sel.MatrixCellDimValues)
	}
}

// TestBuildTierSelectionFromMatrixCell_PerUnitOnly 验证单价模型 (per_unit)
func TestBuildTierSelectionFromMatrixCell_PerUnitOnly(t *testing.T) {
	cell := &model.PriceMatrixCell{
		DimValues:       map[string]interface{}{"image_size": "1024x1024"},
		OfficialPerUnit: float64Ptr(0.2),
		SellingPerUnit:  float64Ptr(0.3),
		Supported:       true,
	}
	sel := buildTierSelectionFromMatrixCell(cell, nil)
	if sel == nil {
		t.Fatal("expected non-nil selection")
	}
	// per_unit fills both input and output
	if sel.InputPriceRMB != 0.3 || sel.OutputPriceRMB != 0.3 {
		t.Errorf("per_unit: input=%v output=%v, want both 0.3", sel.InputPriceRMB, sel.OutputPriceRMB)
	}
	if sel.PlatformInputPriceRMB != 0.2 || sel.PlatformOutputPriceRMB != 0.2 {
		t.Errorf("cost per_unit: input=%v output=%v, want both 0.2", sel.PlatformInputPriceRMB, sel.PlatformOutputPriceRMB)
	}
}

// TestBuildTierSelectionFromMatrixCell_UnsupportedReturnsNil 验证 unsupported cell 不命中
func TestBuildTierSelectionFromMatrixCell_UnsupportedReturnsNil(t *testing.T) {
	cell := &model.PriceMatrixCell{
		DimValues:    map[string]interface{}{"x": "y"},
		SellingInput: float64Ptr(10),
		Supported:    false,
	}
	if sel := buildTierSelectionFromMatrixCell(cell, nil); sel != nil {
		t.Error("unsupported cell 应返回 nil")
	}
}

// TestBuildTierSelectionFromMatrixCell_NoPriceFields 验证缺售价字段时不命中
func TestBuildTierSelectionFromMatrixCell_NoPriceFields(t *testing.T) {
	cell := &model.PriceMatrixCell{
		DimValues: map[string]interface{}{"x": "y"},
		// 全部 selling 字段 nil
		Supported: true,
	}
	if sel := buildTierSelectionFromMatrixCell(cell, nil); sel != nil {
		t.Error("缺售价字段应返回 nil（不能用 0 价计费）")
	}
}

// TestBuildTierSelectionFromMatrixCell_NilCell 验证 nil cell 安全
func TestBuildTierSelectionFromMatrixCell_NilCell(t *testing.T) {
	if sel := buildTierSelectionFromMatrixCell(nil, nil); sel != nil {
		t.Error("nil cell 应返回 nil")
	}
}

// TestBuildTierSelectionFromMatrixCell_InputOnlyForEmbedding 验证 Embedding 类只有 input 价
func TestBuildTierSelectionFromMatrixCell_InputOnlyForEmbedding(t *testing.T) {
	cell := &model.PriceMatrixCell{
		DimValues:     map[string]interface{}{"context_tier": "default"},
		OfficialInput: float64Ptr(0.5),
		SellingInput:  float64Ptr(0.7),
		Supported:     true,
	}
	sel := buildTierSelectionFromMatrixCell(cell, nil)
	if sel == nil {
		t.Fatal("expected non-nil selection (input only)")
	}
	if sel.InputPriceRMB != 0.7 {
		t.Errorf("input: %v, want 0.7", sel.InputPriceRMB)
	}
	if sel.OutputPriceRMB != 0 {
		t.Errorf("output should be 0 for embedding, got %v", sel.OutputPriceRMB)
	}
}

// TestBuildTierSelectionFromMatrixCell_FallbackToAiModelCost 验证 cell 缺成本时 fallback aiModel
func TestBuildTierSelectionFromMatrixCell_FallbackToAiModelCost(t *testing.T) {
	cell := &model.PriceMatrixCell{
		DimValues:     map[string]interface{}{"x": "y"},
		SellingInput:  float64Ptr(2.0),
		SellingOutput: float64Ptr(8.0),
		// OfficialInput / OfficialOutput nil → 走 fallback
		Supported: true,
	}
	aiModel := &model.AIModel{InputCostRMB: 1, OutputCostRMB: 4}
	sel := buildTierSelectionFromMatrixCell(cell, aiModel)
	if sel == nil {
		t.Fatal("expected non-nil")
	}
	if sel.PlatformInputPriceRMB != 1 || sel.PlatformOutputPriceRMB != 4 {
		t.Errorf("cost fallback: input=%v output=%v, want 1/4 from aiModel",
			sel.PlatformInputPriceRMB, sel.PlatformOutputPriceRMB)
	}
}

// TestBuildTierSelectionFromMatrixCell_NoteFallback 验证 Note 缺失时拼 dim_values
func TestBuildTierSelectionFromMatrixCell_NoteFallback(t *testing.T) {
	cell := &model.PriceMatrixCell{
		DimValues:     map[string]interface{}{"resolution": "1080p"},
		SellingInput:  float64Ptr(10),
		SellingOutput: float64Ptr(10),
		Supported:     true,
		// Note 留空
	}
	sel := buildTierSelectionFromMatrixCell(cell, nil)
	if sel == nil {
		t.Fatal("expected non-nil")
	}
	// MatchedTier 应包含 dim_values 摘要
	if sel.MatchedTier == "" {
		t.Error("MatchedTier 不应为空")
	}
}

// TestJoinStrings 简单验证 helper
func TestJoinStrings(t *testing.T) {
	if got := joinStrings(nil, ","); got != "" {
		t.Errorf("nil → %q, want empty", got)
	}
	if got := joinStrings([]string{"a"}, ","); got != "a" {
		t.Errorf("single → %q, want 'a'", got)
	}
	if got := joinStrings([]string{"a", "b", "c"}, "-"); got != "a-b-c" {
		t.Errorf("multi → %q, want 'a-b-c'", got)
	}
}
