package admin

import (
	"math"
	"strings"
	"testing"
)

// TestParseSupplierCSV_Standard 标准 CSV 解析
func TestParseSupplierCSV_Standard(t *testing.T) {
	csv := `model_name,input_tokens,output_tokens,cost_rmb
gpt-4o,1000000,500000,15.50
claude-sonnet,800000,200000,8.20`
	items, err := parseSupplierCSV(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].ModelName != "gpt-4o" || items[0].SupplierCostRMB != 15.50 {
		t.Errorf("first row mismatch: %+v", items[0])
	}
	if items[1].ModelName != "claude-sonnet" || items[1].InputTokens != 800000 {
		t.Errorf("second row mismatch: %+v", items[1])
	}
}

// TestParseSupplierCSV_BOM BOM 头需要被剥离
func TestParseSupplierCSV_BOM(t *testing.T) {
	csv := "\uFEFFmodel_name,cost_rmb\ngpt-4o,12.34"
	items, err := parseSupplierCSV(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(items) != 1 || items[0].ModelName != "gpt-4o" {
		t.Errorf("BOM not stripped: %+v", items)
	}
}

// TestParseSupplierCSV_MissingCostColumn 缺 cost_rmb 列必须报错
func TestParseSupplierCSV_MissingCostColumn(t *testing.T) {
	csv := "model_name,input_tokens\ngpt-4o,1000000"
	_, err := parseSupplierCSV(strings.NewReader(csv))
	if err == nil {
		t.Error("expected error for missing cost_rmb column")
	}
}

// TestMergeReconcile_PerfectMatch 双方完全一致 → 无超阈
func TestMergeReconcile_PerfectMatch(t *testing.T) {
	platform := []platformAgg{
		{ModelName: "gpt-4o", InputTokens: 1000000, OutputTokens: 500000, PlatformCostRMB: 10.0},
	}
	supplier := []supplierAgg{
		{ModelName: "gpt-4o", InputTokens: 1000000, OutputTokens: 500000, SupplierCostRMB: 10.0},
	}
	res := mergeReconcile(platform, supplier)
	if res.OverThresholdHits != 0 {
		t.Errorf("perfect match: expected 0 hits, got %d", res.OverThresholdHits)
	}
	if math.Abs(res.OverallDiffRMB) > 1e-6 {
		t.Errorf("perfect match: expected 0 diff, got %f", res.OverallDiffRMB)
	}
}

// TestMergeReconcile_OverThreshold 偏差 > 3% → 标记 over_threshold
func TestMergeReconcile_OverThreshold(t *testing.T) {
	platform := []platformAgg{
		{ModelName: "gpt-4o", PlatformCostRMB: 10.5}, // +5%
	}
	supplier := []supplierAgg{
		{ModelName: "gpt-4o", SupplierCostRMB: 10.0},
	}
	res := mergeReconcile(platform, supplier)
	if res.OverThresholdHits != 1 {
		t.Errorf("5%% diff: expected 1 over-threshold hit, got %d", res.OverThresholdHits)
	}
	if !res.Items[0].OverThreshold {
		t.Error("first item should be over threshold")
	}
}

// TestMergeReconcile_BelowThreshold 偏差 < 3% → 不标记
func TestMergeReconcile_BelowThreshold(t *testing.T) {
	platform := []platformAgg{
		{ModelName: "gpt-4o", PlatformCostRMB: 10.2}, // +2%
	}
	supplier := []supplierAgg{
		{ModelName: "gpt-4o", SupplierCostRMB: 10.0},
	}
	res := mergeReconcile(platform, supplier)
	if res.OverThresholdHits != 0 {
		t.Errorf("2%% diff: expected 0 hits, got %d", res.OverThresholdHits)
	}
}

// TestMergeReconcile_PlatformOnly 平台有，供应商账单无 → 100% 偏差
func TestMergeReconcile_PlatformOnly(t *testing.T) {
	platform := []platformAgg{
		{ModelName: "ghost-model", PlatformCostRMB: 5.0},
	}
	supplier := []supplierAgg{}
	res := mergeReconcile(platform, supplier)
	if res.OverThresholdHits != 1 {
		t.Errorf("platform-only: expected 1 hit, got %d", res.OverThresholdHits)
	}
	if math.Abs(res.Items[0].DiffPct-1.0) > 1e-6 {
		t.Errorf("platform-only: expected DiffPct=1.0, got %f", res.Items[0].DiffPct)
	}
}

// TestMergeReconcile_SupplierOnly 供应商账单有，平台无 → 应出现差额
func TestMergeReconcile_SupplierOnly(t *testing.T) {
	supplier := []supplierAgg{
		{ModelName: "stale-model", SupplierCostRMB: 7.5},
	}
	res := mergeReconcile(nil, supplier)
	if len(res.Items) != 1 {
		t.Fatalf("supplier-only: expected 1 item, got %d", len(res.Items))
	}
	// platform = 0, supplier = 7.5 → diff = -7.5, pct = -1.0
	if math.Abs(res.Items[0].DiffPct-(-1.0)) > 1e-6 {
		t.Errorf("supplier-only: expected DiffPct=-1.0, got %f", res.Items[0].DiffPct)
	}
}

// TestMergeReconcile_CaseInsensitiveModelName 模型名归一化（大小写不敏感）
func TestMergeReconcile_CaseInsensitiveModelName(t *testing.T) {
	platform := []platformAgg{{ModelName: "GPT-4o", PlatformCostRMB: 10.0}}
	supplier := []supplierAgg{{ModelName: "gpt-4o", SupplierCostRMB: 10.0}}
	res := mergeReconcile(platform, supplier)
	if len(res.Items) != 1 {
		t.Fatalf("case insensitive: expected 1 merged item, got %d", len(res.Items))
	}
	if res.OverThresholdHits != 0 {
		t.Errorf("case insensitive: expected 0 hits, got %d", res.OverThresholdHits)
	}
}

// TestParsePeriod 月份解析
func TestParsePeriod(t *testing.T) {
	start, end, err := parsePeriod("2026-04")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if start.Year() != 2026 || start.Month() != 4 || start.Day() != 1 {
		t.Errorf("start mismatch: %v", start)
	}
	if end.Month() != 5 || end.Day() != 1 {
		t.Errorf("end should be next month 1st: %v", end)
	}
}

// TestParsePeriod_Invalid 非法格式
func TestParsePeriod_Invalid(t *testing.T) {
	_, _, err := parsePeriod("2026/04")
	if err == nil {
		t.Error("expected error for invalid period format")
	}
	_, _, err = parsePeriod("not-a-date")
	if err == nil {
		t.Error("expected error for non-date string")
	}
}
