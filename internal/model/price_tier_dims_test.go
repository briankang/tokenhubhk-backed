package model

import "testing"

// TestMatchesDims_FullMatch 验证全字段命中
func TestMatchesDims_FullMatch(t *testing.T) {
	tier := PriceTier{
		Name: "1080p+video",
		DimValues: map[string]string{
			"resolution":      "1080p",
			"input_has_video": "true",
		},
	}
	declared, matched := tier.MatchesDims(map[string]string{
		"resolution":      "1080p",
		"input_has_video": "true",
		"extra_dim":       "ignored", // 多余维度不影响
	})
	if !declared || !matched {
		t.Fatalf("expected declared && matched, got declared=%v matched=%v", declared, matched)
	}
}

// TestMatchesDims_Mismatch 验证一个键不匹配整体不匹配
func TestMatchesDims_Mismatch(t *testing.T) {
	tier := PriceTier{
		DimValues: map[string]string{
			"resolution":      "1080p",
			"input_has_video": "true",
		},
	}
	declared, matched := tier.MatchesDims(map[string]string{
		"resolution":      "1080p",
		"input_has_video": "false", // 不匹配
	})
	if !declared {
		t.Fatal("expected declared=true (tier 声明了 dims)")
	}
	if matched {
		t.Fatal("expected matched=false (一个键不匹配)")
	}
}

// TestMatchesDims_MissingKey 验证 dims 中缺失键算不匹配
func TestMatchesDims_MissingKey(t *testing.T) {
	tier := PriceTier{
		DimValues: map[string]string{"resolution": "1080p"},
	}
	declared, matched := tier.MatchesDims(map[string]string{
		// resolution 缺失
		"other": "x",
	})
	if !declared || matched {
		t.Fatalf("expected declared=true matched=false, got declared=%v matched=%v", declared, matched)
	}
}

// TestMatchesDims_NoDimValues_FallsThrough 验证 tier 无 DimValues → declared=false
func TestMatchesDims_NoDimValues_FallsThrough(t *testing.T) {
	tier := PriceTier{Name: "Default"}
	declared, _ := tier.MatchesDims(map[string]string{"resolution": "1080p"})
	if declared {
		t.Fatal("tier 没声明 DimValues 时 declared 应为 false")
	}
}

// TestMatchesDims_EmptyValueIsWildcard 验证空字符串值视为通配
func TestMatchesDims_EmptyValueIsWildcard(t *testing.T) {
	tier := PriceTier{
		DimValues: map[string]string{
			"resolution":      "",     // 通配
			"input_has_video": "true", // 必须 = true
		},
	}
	// 任意 resolution 都行，只要 input_has_video=true
	declared, matched := tier.MatchesDims(map[string]string{
		"resolution":      "anything",
		"input_has_video": "true",
	})
	if !declared || !matched {
		t.Fatalf("expected declared && matched, got declared=%v matched=%v", declared, matched)
	}
}

// TestMatchesDims_AllEmptyValues_NotDeclared 验证全空值等价于未声明
func TestMatchesDims_AllEmptyValues_NotDeclared(t *testing.T) {
	tier := PriceTier{
		DimValues: map[string]string{"resolution": "", "audio": ""},
	}
	declared, _ := tier.MatchesDims(map[string]string{"resolution": "1080p"})
	if declared {
		t.Fatal("全空值应等价于未声明 dims")
	}
}

// TestSelectTierByDims_FirstMatch 验证选第一个命中
func TestSelectTierByDims_FirstMatch(t *testing.T) {
	tiers := []PriceTier{
		{Name: "tier1", DimValues: map[string]string{"resolution": "720p"}, OutputPrice: 46},
		{Name: "tier2", DimValues: map[string]string{"resolution": "1080p"}, OutputPrice: 51},
	}
	idx, tier := SelectTierByDims(tiers, map[string]string{"resolution": "1080p"})
	if idx != 1 || tier == nil || tier.Name != "tier2" {
		t.Fatalf("expected tier2 (idx=1), got idx=%d tier=%+v", idx, tier)
	}
}

// TestSelectTierByDims_NoMatch_ReturnsMinusOne 验证未命中返回 -1
func TestSelectTierByDims_NoMatch_ReturnsMinusOne(t *testing.T) {
	tiers := []PriceTier{
		{DimValues: map[string]string{"resolution": "720p"}},
		{DimValues: map[string]string{"resolution": "1080p"}},
	}
	idx, tier := SelectTierByDims(tiers, map[string]string{"resolution": "4k"})
	if idx != -1 || tier != nil {
		t.Fatalf("expected -1/nil for no-match, got idx=%d tier=%+v", idx, tier)
	}
}

// TestSelectTierByDims_NoTierDeclaresDims_ReturnsMinusOne 验证无 tier 声明 dims 时返回 -1
//
// 重要：调用方应当把 -1 解释为"该 tier 数组不支持 dim 匹配，回退到 token 区间"
func TestSelectTierByDims_NoTierDeclaresDims_ReturnsMinusOne(t *testing.T) {
	tiers := []PriceTier{
		{Name: "Default", InputMin: 0, OutputPrice: 16},
		{Name: "32k+", InputMin: 32000, OutputPrice: 24},
	}
	idx, tier := SelectTierByDims(tiers, map[string]string{"resolution": "1080p"})
	if idx != -1 || tier != nil {
		t.Fatalf("expected -1/nil when no tier declares dims, got idx=%d tier=%+v", idx, tier)
	}
}

// TestSelectTierByDims_EmptyDims_ReturnsMinusOne 验证 dims 空时直接返回 -1
func TestSelectTierByDims_EmptyDims_ReturnsMinusOne(t *testing.T) {
	tiers := []PriceTier{
		{DimValues: map[string]string{"resolution": "1080p"}},
	}
	idx, tier := SelectTierByDims(tiers, nil)
	if idx != -1 || tier != nil {
		t.Fatalf("expected -1/nil for empty dims, got idx=%d", idx)
	}
}

// TestSelectTierByDims_Seedance20FourCells 模拟 Seedance 2.0 完整 4 档
func TestSelectTierByDims_Seedance20FourCells(t *testing.T) {
	tiers := []PriceTier{
		{Name: "720p · 不含视频", DimValues: map[string]string{"resolution": "720p", "input_has_video": "false"}, OutputPrice: 46},
		{Name: "720p · 含视频", DimValues: map[string]string{"resolution": "720p", "input_has_video": "true"}, OutputPrice: 28},
		{Name: "1080p · 不含视频", DimValues: map[string]string{"resolution": "1080p", "input_has_video": "false"}, OutputPrice: 51},
		{Name: "1080p · 含视频", DimValues: map[string]string{"resolution": "1080p", "input_has_video": "true"}, OutputPrice: 31},
	}

	cases := []struct {
		name      string
		dims      map[string]string
		wantIdx   int
		wantPrice float64
	}{
		{"720p no-video", map[string]string{"resolution": "720p", "input_has_video": "false"}, 0, 46},
		{"720p with-video", map[string]string{"resolution": "720p", "input_has_video": "true"}, 1, 28},
		{"1080p no-video", map[string]string{"resolution": "1080p", "input_has_video": "false"}, 2, 51},
		{"1080p with-video", map[string]string{"resolution": "1080p", "input_has_video": "true"}, 3, 31},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			idx, tier := SelectTierByDims(tiers, c.dims)
			if idx != c.wantIdx || tier == nil || tier.OutputPrice != c.wantPrice {
				t.Fatalf("dims=%v: got idx=%d price=%v, want idx=%d price=%v",
					c.dims, idx, tierPrice(tier), c.wantIdx, c.wantPrice)
			}
		})
	}
}

func tierPrice(t *PriceTier) float64 {
	if t == nil {
		return -1
	}
	return t.OutputPrice
}

// TestSelectTierOrLargest_BackwardCompatible 验证 SelectTierOrLargest 行为不变（旧路径不受影响）
func TestSelectTierOrLargest_BackwardCompatible(t *testing.T) {
	max32k := int64(32000)
	tiers := []PriceTier{
		{Name: "0-32k", InputMin: 0, InputMax: &max32k, InputMinExclusive: true, OutputPrice: 4},
		{Name: "32k+", InputMin: 32000, InputMinExclusive: true, OutputPrice: 16},
	}
	idx, tier, matched := SelectTierOrLargest(tiers, 100, 200)
	if !matched || idx != 0 || tier.Name != "0-32k" {
		t.Fatalf("expected 0-32k tier matched, got idx=%d matched=%v", idx, matched)
	}
}
