package model

import "testing"

func int64Ptr(v int64) *int64 { return &v }

func TestMatchesBoundary(t *testing.T) {
	tier := PriceTier{InputMin: 0, InputMinExclusive: true, InputMax: int64Ptr(32000), OutputMin: 0, OutputMinExclusive: true}
	cases := []struct {
		in, out int64
		want    bool
	}{
		{1, 1, true},
		{0, 1, false},
		{32000, 1, true},
		{32001, 1, false},
		{1, 0, false},
	}
	for _, tc := range cases {
		if got := tier.Matches(tc.in, tc.out); got != tc.want {
			t.Fatalf("Matches(%d,%d)=%v want %v", tc.in, tc.out, got, tc.want)
		}
	}
}

func TestSelectTier(t *testing.T) {
	tiers := []PriceTier{
		{InputMin: 0, InputMinExclusive: true, InputMax: int64Ptr(32000), OutputMin: 0, OutputMinExclusive: true, Name: "t1"},
		{InputMin: 32000, InputMinExclusive: true, InputMax: int64Ptr(128000), OutputMin: 0, OutputMinExclusive: true, Name: "t2"},
		{InputMin: 128000, InputMinExclusive: true, OutputMin: 0, OutputMinExclusive: true, Name: "t3"},
	}
	idx, tier := SelectTier(tiers, 100000, 500)
	if idx != 1 || tier == nil || tier.Name != "t2" {
		t.Fatalf("expected t2, got idx=%d tier=%v", idx, tier)
	}
	idx, tier = SelectTier(tiers, 999999, 500)
	if idx != 2 || tier == nil || tier.Name != "t3" {
		t.Fatalf("expected t3, got idx=%d tier=%v", idx, tier)
	}
}

func TestSelectTierOrLargestFallback(t *testing.T) {
	max32k := int64(32000)
	max128k := int64(128000)
	tiers := []PriceTier{
		{InputMin: 32000, InputMinExclusive: true, InputMax: &max128k, OutputMin: 0, OutputMinExclusive: true, Name: "middle"},
		{InputMin: 0, InputMinExclusive: true, InputMax: &max32k, OutputMin: 0, OutputMinExclusive: true, Name: "small"},
	}

	idx, tier, matched := SelectTierOrLargest(tiers, 1_000_000, 2_000)
	if matched {
		t.Fatal("expected fallback when tokens exceed all configured tiers")
	}
	if idx != 0 || tier == nil || tier.Name != "middle" {
		t.Fatalf("expected largest tier fallback to middle, got idx=%d tier=%+v", idx, tier)
	}
}

func TestNormalizeCurrentSchemaOnly(t *testing.T) {
	tier := PriceTier{InputMin: 1000, InputMinExclusive: true, InputMax: int64Ptr(5000)}
	tier.Normalize()
	if tier.InputMin != 1000 || tier.InputMax == nil || *tier.InputMax != 5000 {
		t.Fatalf("current fields changed unexpectedly: %+v", tier)
	}
	if !tier.OutputMinExclusive {
		t.Fatal("default output lower bound should be exclusive")
	}
	if tier.Name == "" {
		t.Fatal("Normalize should fill an auto name")
	}
}

func TestAutoName(t *testing.T) {
	cases := []struct {
		tier PriceTier
		want string
	}{
		{PriceTier{InputMin: 0, InputMinExclusive: true}, "0-inf"},
		{PriceTier{InputMin: 0, InputMax: int64Ptr(32000)}, "[0, 32k]"},
		{PriceTier{InputMin: 32000, InputMinExclusive: true, InputMax: int64Ptr(128000)}, "(32k, 128k]"},
		{PriceTier{InputMin: 32000, InputMinExclusive: true}, "(32k, +inf)"},
	}
	for _, tc := range cases {
		if got := tc.tier.AutoName(); got != tc.want {
			t.Fatalf("AutoName()=%q want %q", got, tc.want)
		}
	}
}

func TestValidateAndSort(t *testing.T) {
	if err := (PriceTier{InputMin: -1}).Validate(); err == nil {
		t.Fatal("negative input_min should fail")
	}
	tiers := []PriceTier{{InputMin: 128000}, {InputMin: 0}, {InputMin: 32000}}
	SortTiers(tiers)
	if tiers[0].InputMin != 0 || tiers[1].InputMin != 32000 || tiers[2].InputMin != 128000 {
		t.Fatalf("sort failed: %+v", tiers)
	}
}
