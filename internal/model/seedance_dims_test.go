package model

import "testing"

// =============================================================================
// Seedance 全系列 DimValues 选档准确性测试 (F5 主体)
//
// 锁定 S4 + F1 + F2(A) 数据迁移后的扣费选档正确性：
//   - Seedance 2.0 / 2.0-fast：resolution × input_has_video 共 4 档
//   - Seedance 1.5-pro：inference_mode × audio_mode 共 4 档（+ Draft 折算系数）
//   - Seedance 1.0 系列：inference_mode 共 2 档
//
// 这些测试用纯 PriceTier 数组（无 DB），验证 model.SelectTierByDims 在 Seedance
// 业务维度下的全档命中。前置：S1 PriceTier.DimValues + ModelDimensionConfig 必须就位。
// =============================================================================

// makeSeedance20Tiers 构造 Seedance 2.0 (Pro) 的 4 档 DimValues 阶梯
//
// 火山引擎官网 2026-04 定价：
//
//	480p/720p · 不含视频: 46 ¥/M
//	480p/720p · 含视频:   28 ¥/M
//	1080p · 不含视频:     51 ¥/M
//	1080p · 含视频:       31 ¥/M
//
// 注：720p / 480p 共用同一档（火山未对 480p 单独定价）。
func makeSeedance20Tiers() []PriceTier {
	return []PriceTier{
		{
			Name:        "480p/720p · 不含视频",
			DimValues:   map[string]string{DimKeyResolution: "720p", DimKeyInputHasVideo: "false"},
			OutputPrice: 46,
		},
		{
			Name:        "480p/720p · 含视频",
			DimValues:   map[string]string{DimKeyResolution: "720p", DimKeyInputHasVideo: "true"},
			OutputPrice: 28,
		},
		{
			Name:        "1080p · 不含视频",
			DimValues:   map[string]string{DimKeyResolution: "1080p", DimKeyInputHasVideo: "false"},
			OutputPrice: 51,
		},
		{
			Name:        "1080p · 含视频",
			DimValues:   map[string]string{DimKeyResolution: "1080p", DimKeyInputHasVideo: "true"},
			OutputPrice: 31,
		},
	}
}

// makeSeedance20FastTiers 构造 Seedance 2.0 Fast 的 2 档（无 1080p）
//
// 火山引擎官网 2026-04 定价：
//
//	不含视频: 37 ¥/M
//	含视频:   22 ¥/M
func makeSeedance20FastTiers() []PriceTier {
	return []PriceTier{
		{
			Name:        "不含视频",
			DimValues:   map[string]string{DimKeyInputHasVideo: "false"},
			OutputPrice: 37,
		},
		{
			Name:        "含视频",
			DimValues:   map[string]string{DimKeyInputHasVideo: "true"},
			OutputPrice: 22,
		},
	}
}

// makeSeedance15ProTiers 构造 Seedance 1.5 Pro 的 4 档
//
// 火山引擎官网 2026-04 定价：
//
//	在线 + 有声: 16 ¥/M
//	在线 + 无声: 8 ¥/M
//	离线 + 有声: 8 ¥/M
//	离线 + 无声: 4 ¥/M
func makeSeedance15ProTiers() []PriceTier {
	return []PriceTier{
		{
			Name:        "在线推理 · 有声视频",
			DimValues:   map[string]string{DimKeyInferenceMode: "online", DimKeyAudioMode: "true"},
			OutputPrice: 16,
		},
		{
			Name:        "在线推理 · 无声视频",
			DimValues:   map[string]string{DimKeyInferenceMode: "online", DimKeyAudioMode: "false"},
			OutputPrice: 8,
		},
		{
			Name:        "离线推理 · 有声视频",
			DimValues:   map[string]string{DimKeyInferenceMode: "offline", DimKeyAudioMode: "true"},
			OutputPrice: 8,
		},
		{
			Name:        "离线推理 · 无声视频",
			DimValues:   map[string]string{DimKeyInferenceMode: "offline", DimKeyAudioMode: "false"},
			OutputPrice: 4,
		},
	}
}

// makeSeedance10OnOffTiers 构造 1.0 系列的 2 档
//
// 火山引擎官网 2026-04 定价（onlinePrice 参数化，离线 = 在线 × 0.5）：
//
//	1.0 Pro:      online=15, offline=7.5
//	1.0 Pro Fast: online=4.2, offline=2.1
//	1.0 Lite:     online=10,  offline=5.0
func makeSeedance10OnOffTiers(onlinePrice float64) []PriceTier {
	return []PriceTier{
		{
			Name:        "在线推理",
			DimValues:   map[string]string{DimKeyInferenceMode: "online"},
			OutputPrice: onlinePrice,
		},
		{
			Name:        "离线推理",
			DimValues:   map[string]string{DimKeyInferenceMode: "offline"},
			OutputPrice: onlinePrice * 0.5,
		},
	}
}

// TestSeedance20_AllFourCells Seedance 2.0 全 4 档命中
func TestSeedance20_AllFourCells(t *testing.T) {
	tiers := makeSeedance20Tiers()

	cases := []struct {
		name      string
		dims      map[string]string
		wantPrice float64
		wantTier  string
	}{
		{"720p no-video", map[string]string{DimKeyResolution: "720p", DimKeyInputHasVideo: "false"}, 46, "480p/720p · 不含视频"},
		{"720p with-video", map[string]string{DimKeyResolution: "720p", DimKeyInputHasVideo: "true"}, 28, "480p/720p · 含视频"},
		{"1080p no-video", map[string]string{DimKeyResolution: "1080p", DimKeyInputHasVideo: "false"}, 51, "1080p · 不含视频"},
		{"1080p with-video", map[string]string{DimKeyResolution: "1080p", DimKeyInputHasVideo: "true"}, 31, "1080p · 含视频"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, tier := SelectTierByDims(tiers, c.dims)
			if tier == nil {
				t.Fatalf("dims=%v: 未命中", c.dims)
			}
			if tier.OutputPrice != c.wantPrice {
				t.Errorf("dims=%v: price=%v, want %v", c.dims, tier.OutputPrice, c.wantPrice)
			}
			if tier.Name != c.wantTier {
				t.Errorf("dims=%v: tier=%s, want %s", c.dims, tier.Name, c.wantTier)
			}
		})
	}
}

// TestSeedance20Fast_TwoCells Seedance 2.0 Fast 2 档（含/不含视频）
func TestSeedance20Fast_TwoCells(t *testing.T) {
	tiers := makeSeedance20FastTiers()

	cases := []struct {
		name string
		dims map[string]string
		want float64
	}{
		{"no-video", map[string]string{DimKeyInputHasVideo: "false"}, 37},
		{"with-video", map[string]string{DimKeyInputHasVideo: "true"}, 22},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, tier := SelectTierByDims(tiers, c.dims)
			if tier == nil || tier.OutputPrice != c.want {
				t.Errorf("dims=%v: got tier=%+v, want price=%v", c.dims, tier, c.want)
			}
		})
	}
}

// TestSeedance15Pro_FourCells Seedance 1.5 Pro 全 4 档
//
// 这是历史上"漏扣 75%"最严重的模型 —— 旧路径 fallback 到最便宜档（4 元）
// 现在通过 DimValues 4 档显式选档，每档应严格命中
func TestSeedance15Pro_FourCells(t *testing.T) {
	tiers := makeSeedance15ProTiers()

	cases := []struct {
		name string
		dims map[string]string
		want float64
	}{
		{"online + audio", map[string]string{DimKeyInferenceMode: "online", DimKeyAudioMode: "true"}, 16},
		{"online + silent", map[string]string{DimKeyInferenceMode: "online", DimKeyAudioMode: "false"}, 8},
		{"offline + audio", map[string]string{DimKeyInferenceMode: "offline", DimKeyAudioMode: "true"}, 8},
		{"offline + silent", map[string]string{DimKeyInferenceMode: "offline", DimKeyAudioMode: "false"}, 4},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, tier := SelectTierByDims(tiers, c.dims)
			if tier == nil {
				t.Fatalf("dims=%v: 未命中（漏扣回归！）", c.dims)
			}
			if tier.OutputPrice != c.want {
				t.Errorf("dims=%v: price=%v, want %v", c.dims, tier.OutputPrice, c.want)
			}
		})
	}
}

// TestSeedance10Series_OnlineOffline 1.0 Pro / Lite / Fast 系列 2 档
func TestSeedance10Series_OnlineOffline(t *testing.T) {
	cases := []struct {
		modelName    string
		onlinePrice  float64
		offlinePrice float64
	}{
		{"doubao-seedance-1.0-pro", 15, 7.5},
		{"doubao-seedance-1.0-pro-fast", 4.2, 2.1},
		{"doubao-seedance-1.0-lite", 10, 5},
	}

	for _, m := range cases {
		t.Run(m.modelName, func(t *testing.T) {
			tiers := makeSeedance10OnOffTiers(m.onlinePrice)

			_, online := SelectTierByDims(tiers, map[string]string{DimKeyInferenceMode: "online"})
			if online == nil || online.OutputPrice != m.onlinePrice {
				t.Errorf("online: got %+v, want %v", online, m.onlinePrice)
			}

			_, offline := SelectTierByDims(tiers, map[string]string{DimKeyInferenceMode: "offline"})
			if offline == nil || offline.OutputPrice != m.offlinePrice {
				t.Errorf("offline: got %+v, want %v", offline, m.offlinePrice)
			}
		})
	}
}

// TestSeedance_DefaultDimsViaConfig 验证 ApplyDefaults 与 SelectTierByDims 的协作
//
// 场景：用户只传 resolution，audio_mode 走默认 → 应能命中
func TestSeedance_DefaultDimsViaConfig(t *testing.T) {
	tiers := makeSeedance15ProTiers()
	config := &ModelDimensionConfig{
		Dimensions: VideoGenerationDefaultDimensions(),
	}

	// 用户只传 inference_mode，audio_mode 应被 default 填充为 true（"true"=有声）
	dims := map[string]string{DimKeyInferenceMode: "online"}
	dims = config.ApplyDefaults(dims)

	if dims[DimKeyAudioMode] != "true" {
		t.Errorf("audio_mode 默认应为 true, got %q", dims[DimKeyAudioMode])
	}

	_, tier := SelectTierByDims(tiers, dims)
	if tier == nil {
		t.Fatal("默认值填充后应能命中 tier")
	}
	if tier.OutputPrice != 16 {
		t.Errorf("default-filled dims 应命中 online+audio=16, got %v", tier.OutputPrice)
	}
}

// TestSeedance_MissingRequiredDim_FallsThrough 验证维度缺失时不误命中
//
// 场景：只传 resolution=720p（没传 input_has_video）→ 应 NOT 命中（因为所有 tier 都要求两个键都对）
func TestSeedance_MissingRequiredDim_FallsThrough(t *testing.T) {
	tiers := makeSeedance20Tiers()

	// 只传 resolution 不传 input_has_video
	idx, tier := SelectTierByDims(tiers, map[string]string{DimKeyResolution: "720p"})
	if idx != -1 || tier != nil {
		t.Fatalf("缺维度应不命中，got idx=%d tier=%+v", idx, tier)
	}
}

// TestSeedance_LegacyTokenTier_StillWorks 验证旧无 DimValues 阶梯仍走 token 区间路径
//
// 重要：S2 升级后旧数据（DimValues=nil）必须保持原行为，否则会破坏未迁移的模型
func TestSeedance_LegacyTokenTier_StillWorks(t *testing.T) {
	max := int64(32000)
	legacyTiers := []PriceTier{
		{Name: "0-32k", InputMin: 0, InputMinExclusive: true, InputMax: &max, OutputPrice: 8},
		{Name: "32k+", InputMin: 32000, InputMinExclusive: true, OutputPrice: 16},
	}

	// SelectTierByDims 应直接返回 -1（无 tier 声明 dims）
	idx, tier := SelectTierByDims(legacyTiers, map[string]string{DimKeyResolution: "1080p"})
	if idx != -1 || tier != nil {
		t.Fatal("无 DimValues 的 tier 数组不应命中 dims，应让调用方走 token 路径")
	}

	// SelectTierOrLargest 仍按 token 命中
	idx2, t2, matched := SelectTierOrLargest(legacyTiers, 100, 0)
	if !matched || t2 == nil || t2.Name != "0-32k" || idx2 != 0 {
		t.Errorf("legacy token tier 仍应命中 0-32k, got matched=%v tier=%+v", matched, t2)
	}
}

// TestSeedance_MixedDimsAndTokenTiers 验证带 DimValues 的 tier 优先于 token tier
//
// 模拟未来场景：阶梯 mixed in PriceTier 数组中（部分有 dims, 部分无）
// 当请求 dims 命中时不应被 token tier 干扰
func TestSeedance_MixedDimsAndTokenTiers(t *testing.T) {
	max := int64(1_000_000)
	tiers := []PriceTier{
		// 旧 token tier（无 dims）
		{Name: "fallback", InputMin: 0, InputMinExclusive: true, InputMax: &max, OutputPrice: 999},
		// 新 dim tier
		{Name: "1080p with video", DimValues: map[string]string{DimKeyResolution: "1080p", DimKeyInputHasVideo: "true"}, OutputPrice: 31},
	}

	// 命中 dims 应选 dim tier，不是 fallback
	_, tier := SelectTierByDims(tiers, map[string]string{
		DimKeyResolution:    "1080p",
		DimKeyInputHasVideo: "true",
	})
	if tier == nil || tier.Name != "1080p with video" || tier.OutputPrice != 31 {
		t.Errorf("应优先命中 dim tier, got %+v", tier)
	}
}
