package model

import (
	"testing"
)

func int64Ptr(v int64) *int64 { return &v }

func TestMatches_Boundary(t *testing.T) {
	// 阶梯：(0, 32k] × (0, +∞]
	tier := PriceTier{
		InputMin:           0,
		InputMinExclusive:  true,
		InputMax:           int64Ptr(32000),
		InputMaxExclusive:  false,
		OutputMin:          0,
		OutputMinExclusive: true,
	}

	cases := []struct {
		in, out int64
		want    bool
		desc    string
	}{
		{1, 1, true, "inside"},
		{0, 1, false, "input lower exclusive boundary"},
		{32000, 1, true, "input upper closed boundary"},
		{32001, 1, false, "input above upper"},
		{1, 0, false, "output lower exclusive boundary"},
	}

	for _, c := range cases {
		if got := tier.Matches(c.in, c.out); got != c.want {
			t.Errorf("%s: Matches(%d,%d)=%v want %v", c.desc, c.in, c.out, got, c.want)
		}
	}
}

func TestMatches_NoUpperBound(t *testing.T) {
	// (0, +∞] × (0, +∞]
	tier := DefaultTier(0.5, 1.0)
	if !tier.Matches(1, 1) {
		t.Error("default tier should match (1,1)")
	}
	if !tier.Matches(1_000_000, 1_000_000) {
		t.Error("default tier should match large values")
	}
	if tier.Matches(0, 1) {
		t.Error("default tier should exclude input=0")
	}
}

func TestSelectTier_FirstMatch(t *testing.T) {
	tiers := []PriceTier{
		{InputMin: 0, InputMinExclusive: true, InputMax: int64Ptr(32000), Name: "t1"},
		{InputMin: 32000, InputMinExclusive: true, InputMax: int64Ptr(128000), Name: "t2"},
		{InputMin: 128000, InputMinExclusive: true, Name: "t3"},
	}

	idx, tier := SelectTier(tiers, 20000, 500)
	if idx != 0 || tier.Name != "t1" {
		t.Errorf("expected t1, got idx=%d tier=%v", idx, tier)
	}

	idx, tier = SelectTier(tiers, 100000, 500)
	if idx != 1 || tier.Name != "t2" {
		t.Errorf("expected t2, got idx=%d tier=%v", idx, tier)
	}

	idx, tier = SelectTier(tiers, 500000, 500)
	if idx != 2 || tier.Name != "t3" {
		t.Errorf("expected t3, got idx=%d tier=%v", idx, tier)
	}
}

func TestSelectTier_Miss(t *testing.T) {
	tiers := []PriceTier{
		{InputMin: 0, InputMinExclusive: true, InputMax: int64Ptr(32000)},
	}
	idx, tier := SelectTier(tiers, 100000, 500)
	if idx != -1 || tier != nil {
		t.Errorf("expected miss, got idx=%d tier=%v", idx, tier)
	}
}

func TestParseRangeExpression(t *testing.T) {
	cases := []struct {
		expr         string
		wantMin      int64
		wantMinExcl  bool
		wantMax      *int64
		wantMaxExcl  bool
		wantErr      bool
	}{
		{"[0, 32]", 0, false, int64Ptr(32), false, false},
		{"(32, 128]", 32, true, int64Ptr(128), false, false},
		{"[32, 128)", 32, false, int64Ptr(128), true, false},
		{"(32, 128)", 32, true, int64Ptr(128), true, false},
		{"[0, 32k]", 0, false, int64Ptr(32000), false, false},
		{"(32k, 128k]", 32000, true, int64Ptr(128000), false, false},
		{"[0, 1M]", 0, false, int64Ptr(1_000_000), false, false},
		{"[0, +∞)", 0, false, nil, false, false},
		{"32k<input<=128k", 32000, true, int64Ptr(128000), false, false},
		{"input<=128k", 0, true, int64Ptr(128000), false, false},
		{"input>=32k", 32000, false, nil, false, false},
		{"0-1M", 0, false, int64Ptr(1_000_000), false, false},
		{"", 0, false, nil, false, true},
	}

	for _, c := range cases {
		min, minExcl, max, maxExcl, err := ParseRangeExpression(c.expr)
		if (err != nil) != c.wantErr {
			t.Errorf("%q: err=%v wantErr=%v", c.expr, err, c.wantErr)
			continue
		}
		if c.wantErr {
			continue
		}
		if min != c.wantMin || minExcl != c.wantMinExcl || maxExcl != c.wantMaxExcl {
			t.Errorf("%q: min=%d/%v max=%v/%v want min=%d/%v max=%v/%v",
				c.expr, min, minExcl, derefInt64(max), maxExcl,
				c.wantMin, c.wantMinExcl, derefInt64(c.wantMax), c.wantMaxExcl)
		}
		if (max == nil) != (c.wantMax == nil) {
			t.Errorf("%q: max nullness mismatch", c.expr)
		} else if max != nil && *max != *c.wantMax {
			t.Errorf("%q: max=%d want %d", c.expr, *max, *c.wantMax)
		}
	}
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return -1
	}
	return *p
}

func TestNormalize_LegacyFields(t *testing.T) {
	// 旧数据：只有 MinTokens/MaxTokens
	tier := PriceTier{
		MinTokens: 32000,
		MaxTokens: int64Ptr(128000),
	}
	tier.Normalize()

	if tier.InputMin != 32000 {
		t.Errorf("InputMin not backfilled: %d", tier.InputMin)
	}
	if tier.InputMax == nil || *tier.InputMax != 128000 {
		t.Errorf("InputMax not backfilled: %v", tier.InputMax)
	}
	// 旧字段应回写同步
	if tier.MinTokens != tier.InputMin {
		t.Error("MinTokens/InputMin out of sync")
	}
	// Output 应默认 (0, +∞]
	if !tier.OutputMinExclusive {
		t.Error("Output defaults not applied")
	}
}

func TestNormalize_NewFieldsPreserved(t *testing.T) {
	// 新字段已设置，不应被旧字段覆盖
	tier := PriceTier{
		InputMin:          1000,
		InputMinExclusive: true,
		InputMax:          int64Ptr(5000),
		MinTokens:         9999, // 故意冲突
	}
	tier.Normalize()
	if tier.InputMin != 1000 {
		t.Errorf("new InputMin overwritten by legacy: %d", tier.InputMin)
	}
}

// TestAutoName_UserRequirement 对应 2026-04-19 用户验收项 V1：
// "如果只有一个阶梯的，默认显示 0-无限"。
// 同时验证多阶梯的 (lo, hi] 格式与 k/M 后缀。
func TestAutoName_UserRequirement(t *testing.T) {
	cases := []struct {
		tier PriceTier
		want string
		desc string
	}{
		{PriceTier{InputMin: 0, InputMinExclusive: true, InputMax: nil}, "0-无限", "默认全覆盖 (0, +∞]"},
		{PriceTier{InputMin: 0, InputMinExclusive: false, InputMax: nil}, "0-无限", "[0, +∞]"},
		{PriceTier{InputMin: 0, InputMax: int64Ptr(32000)}, "[0, 32k]", "[0, 32k]"},
		{PriceTier{InputMin: 32000, InputMinExclusive: true, InputMax: int64Ptr(128000)}, "(32k, 128k]", "(32k, 128k]"},
		{PriceTier{InputMin: 32000, InputMinExclusive: true, InputMax: nil}, "(32k, +∞)", "(32k, +∞)"},
		{PriceTier{InputMin: 1000000, InputMinExclusive: true, InputMax: int64Ptr(10000000)}, "(1M, 10M]", "百万量级单位"},
		{PriceTier{InputMin: 100, InputMinExclusive: true, InputMax: int64Ptr(500)}, "(100, 500]", "非 k/M 整数"},
	}
	for _, c := range cases {
		got := c.tier.AutoName()
		if got != c.want {
			t.Errorf("%s: AutoName()=%q, want %q", c.desc, got, c.want)
		}
	}
}

// TestNormalize_AutoFillsName 验证 Normalize() 会自动为空 Name 的阶梯填充
func TestNormalize_AutoFillsName(t *testing.T) {
	tier := PriceTier{
		InputMin:          0,
		InputMinExclusive: true,
		InputMax:          nil,
		InputPrice:        1.5,
		OutputPrice:       3.0,
	}
	tier.Normalize()
	if tier.Name != "0-无限" {
		t.Errorf("Normalize should auto-generate Name='0-无限', got %q", tier.Name)
	}

	// 用户手填过 Name 不应被覆盖
	tier2 := PriceTier{Name: "档 1", InputMin: 0, InputMax: int64Ptr(32000)}
	tier2.Normalize()
	if tier2.Name != "档 1" {
		t.Errorf("custom Name should be preserved, got %q", tier2.Name)
	}
}

func TestDefaultTier_Semantics(t *testing.T) {
	tier := DefaultTier(0.5, 1.0)
	if !tier.IsDefaultTier() {
		t.Error("DefaultTier should be IsDefaultTier=true")
	}
	if !tier.Matches(1, 1) {
		t.Error("default tier should match (1, 1)")
	}
	if tier.Matches(0, 1) {
		t.Error("default tier should not match input=0")
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		tier    PriceTier
		wantErr bool
		desc    string
	}{
		{PriceTier{InputMin: 0, InputMax: int64Ptr(100), InputPrice: 1, OutputPrice: 2}, false, "valid"},
		{PriceTier{InputMin: -1}, true, "negative InputMin"},
		{PriceTier{InputMin: 100, InputMax: int64Ptr(50)}, true, "InputMax < InputMin"},
		{PriceTier{InputPrice: -1}, true, "negative price"},
	}
	for _, c := range cases {
		err := c.tier.Validate()
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", c.desc, err, c.wantErr)
		}
	}
}

func TestEnsureDefaultTier(t *testing.T) {
	data := &PriceTiersData{}
	EnsureDefaultTier(data, 0.5, 1.0)
	if len(data.Tiers) != 1 || !data.Tiers[0].IsDefaultTier() {
		t.Errorf("EnsureDefaultTier failed: %+v", data)
	}
	// 非空时不应改动
	before := len(data.Tiers)
	EnsureDefaultTier(data, 0.1, 0.2)
	if len(data.Tiers) != before {
		t.Error("EnsureDefaultTier should not overwrite existing tiers")
	}
}

func TestSortTiers(t *testing.T) {
	tiers := []PriceTier{
		{InputMin: 128000},
		{InputMin: 0},
		{InputMin: 32000},
	}
	SortTiers(tiers)
	if tiers[0].InputMin != 0 || tiers[1].InputMin != 32000 || tiers[2].InputMin != 128000 {
		t.Errorf("sort failed: %+v", tiers)
	}
}
