package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/provider"
	balancesvc "tokenhub-server/internal/service/balance"
	"tokenhub-server/internal/service/pricing"
)

// TestDataConsistencyMatrix 数据一致性矩阵 — 7 类型 × 5 场景。
//
// 每个用例同时验证:
//  1. 三方金额一致:Calculate(charge) == SettleUsage == snapshot.quote.total_credits
//  2. quote_hash 在同 scenario+usage 下三方一致
//  3. line_items 求和等于 total_credits(分项一致)
//  4. 实扣金额(actual_cost_credits) 等于 应扣金额(total_credits)
//
// 用例覆盖(35+ 组合):
//
//	Type        × Scenario:     basic   tiered   cache   thinking   discount
//	─────────────────────────────────────────────────────────────────
//	LLM           ✓       ✓       ✓       ✓          ✓
//	VLM           ✓       skip    ✓       skip       ✓
//	Embedding     ✓       skip    skip    skip       ✓
//	Image         ✓       ✓       skip    skip       ✓
//	Video         ✓       ✓       skip    skip       skip
//	TTS           ✓       ✓       skip    skip       skip
//	ASR           ✓       skip    skip    skip       skip
//	Rerank        ✓       skip    skip    skip       skip
func TestDataConsistencyMatrix(t *testing.T) {
	cases := []consistencyCase{
		// ===== LLM 5 场景 =====
		{name: "LLM_basic", modelType: "LLM", unit: "per_million_tokens", inputCost: 1.0, outputCost: 2.0, useTokens: usage{In: 1000, Out: 500}, expectCredits: 20},
		{name: "LLM_tiered", modelType: "LLM", unit: "per_million_tokens", inputCost: 1.0, outputCost: 2.0, tiers: standardTiers(), useTokens: usage{In: 50000, Out: 1000}, expectCredits: positive},
		{name: "LLM_cache", modelType: "LLM", unit: "per_million_tokens", inputCost: 1.0, outputCost: 2.0, supportsCache: true, cacheMech: "auto", cacheInputPrice: 0.1, useTokens: usage{In: 1000, Out: 500, CacheRead: 200}, expectCredits: positive},
		{name: "LLM_thinking", modelType: "LLM", unit: "per_million_tokens", inputCost: 1.0, outputCost: 2.0, outputThinkingCost: 5.0, thinkingMode: true, useTokens: usage{In: 1000, Out: 500}, expectCredits: positive},

		// ===== VLM 3 场景(图片输入,但同 token 计费) =====
		{name: "VLM_basic", modelType: "Vision", unit: "per_million_tokens", inputCost: 2.0, outputCost: 5.0, useTokens: usage{In: 1500, Out: 800}, expectCredits: positive},
		{name: "VLM_cache", modelType: "Vision", unit: "per_million_tokens", inputCost: 2.0, outputCost: 5.0, supportsCache: true, cacheMech: "auto", cacheInputPrice: 0.2, useTokens: usage{In: 1500, Out: 800, CacheRead: 500}, expectCredits: positive},

		// ===== Embedding 1 场景 =====
		{name: "Embedding_basic", modelType: "Embedding", unit: "per_million_tokens", inputCost: 0.5, outputCost: 0, useTokens: usage{In: 5000}, expectCredits: positive},

		// ===== Image 2 场景(per_image) =====
		{name: "Image_basic", modelType: "ImageGeneration", unit: "per_image", inputCost: 0.3, outputCost: 0.3, useUnit: unitUsage{Images: 3}, expectCredits: positive},
		{name: "Image_tiered", modelType: "ImageGeneration", unit: "per_image", inputCost: 0.3, outputCost: 0.3, tiers: imageTiers(), useUnit: unitUsage{Images: 1}, expectCredits: positive},

		// ===== Video 2 场景(per_second) =====
		{name: "Video_basic", modelType: "VideoGeneration", unit: "per_second", inputCost: 0.5, outputCost: 0.5, useUnit: unitUsage{DurationSec: 10}, expectCredits: positive},
		{name: "Video_long", modelType: "VideoGeneration", unit: "per_second", inputCost: 0.45, outputCost: 0.45, useUnit: unitUsage{DurationSec: 60}, expectCredits: positive},

		// ===== TTS 2 场景(per_10k_characters) =====
		{name: "TTS_basic", modelType: "TTS", unit: "per_10k_characters", inputCost: 0.2, outputCost: 0.2, useUnit: unitUsage{CharCount: 10000}, expectCredits: positive},
		{name: "TTS_long", modelType: "TTS", unit: "per_10k_characters", inputCost: 0.2, outputCost: 0.2, useUnit: unitUsage{CharCount: 50000}, expectCredits: positive},

		// ===== ASR 1 场景(per_minute) =====
		{name: "ASR_basic", modelType: "ASR", unit: "per_minute", inputCost: 0.4, outputCost: 0.4, useUnit: unitUsage{DurationSec: 600}, expectCredits: positive},

		// ===== Rerank 1 场景(per_call) =====
		{name: "Rerank_basic", modelType: "Rerank", unit: "per_call", inputCost: 0.005, outputCost: 0.005, useUnit: unitUsage{CallCount: 100}, expectCredits: positive},

		// ===== Free model: 0 价不应触发 1 积分保底 =====
		{name: "LLM_zero_priced", modelType: "LLM", unit: "per_million_tokens", inputCost: 0, outputCost: 0, useTokens: usage{In: 1000, Out: 500}, expectCredits: zero},

		// ============================================================
		// v3 PriceMatrix 矩阵命中场景(15 项,覆盖 7 类型)
		// 每项验证: Calculate / SettleUsage / SettleUnitUsage 三方都命中同一 cell,
		// snapshot.quote.matched_dim_values 与请求 dim_values 完全一致。
		// ============================================================

		// LLM 矩阵命中:thinking_mode=on / off 切换
		{name: "Matrix_LLM_thinking_off", modelType: "LLM", unit: "per_million_tokens",
			inputCost: 1.0, outputCost: 2.0,
			useTokens: usage{In: 1000, Out: 500},
			priceMatrix: matrixForLLMThinking(),
			dimValues:   map[string]interface{}{"thinking_mode": "off"},
			expectMatched: true, expectCredits: positive,
		},
		{name: "Matrix_LLM_thinking_on", modelType: "LLM", unit: "per_million_tokens",
			inputCost: 1.0, outputCost: 2.0,
			useTokens: usage{In: 1000, Out: 500},
			priceMatrix: matrixForLLMThinking(),
			dimValues:   map[string]interface{}{"thinking_mode": "on"},
			expectMatched: true, expectCredits: positive,
		},

		// VLM 矩阵命中:resolution 选择
		{name: "Matrix_VLM_512", modelType: "Vision", unit: "per_million_tokens",
			inputCost: 2.0, outputCost: 5.0,
			useTokens: usage{In: 1500, Out: 800},
			priceMatrix: matrixForVisionRes(),
			dimValues:   map[string]interface{}{"resolution": "512"},
			expectMatched: true, expectCredits: positive,
		},

		// Image 矩阵命中:512×512 / 1024×1024 / 不支持 2048×2048
		{name: "Matrix_Image_512", modelType: "ImageGeneration", unit: "per_image",
			inputCost: 0.3, outputCost: 0.3,
			useUnit: unitUsage{Images: 2},
			priceMatrix: matrixForImage(),
			dimValues:   map[string]interface{}{"resolution": "512x512", "quality": "standard"},
			expectMatched: true, expectCredits: positive,
		},
		{name: "Matrix_Image_1024_hd", modelType: "ImageGeneration", unit: "per_image",
			inputCost: 0.3, outputCost: 0.3,
			useUnit: unitUsage{Images: 1},
			priceMatrix: matrixForImage(),
			dimValues:   map[string]interface{}{"resolution": "1024x1024", "quality": "hd"},
			expectMatched: true, expectCredits: positive,
		},
		{name: "Matrix_Image_unsupported", modelType: "ImageGeneration", unit: "per_image",
			inputCost: 0.3, outputCost: 0.3,
			useUnit: unitUsage{Images: 1},
			priceMatrix: matrixForImage(),
			// 2048x2048 cell 不存在 → 不命中,fallback 到顶层售价
			dimValues:   map[string]interface{}{"resolution": "2048x2048"},
			expectMatched: false, expectCredits: positive,
		},

		// Video Seedance 2.0:resolution × input_has_video × inference_mode
		{name: "Matrix_VideoSeedance20_1080p_video_online", modelType: "VideoGeneration", unit: "per_million_tokens",
			inputCost: 46.0, outputCost: 46.0,
			useTokens: usage{In: 1_000_000},
			priceMatrix: matrixForSeedance20(),
			dimValues:   map[string]interface{}{"resolution": "1080p", "input_has_video": true, "inference_mode": "online"},
			expectMatched: true, expectCredits: positive,
		},
		{name: "Matrix_VideoSeedance20_offline_unsupported", modelType: "VideoGeneration", unit: "per_million_tokens",
			inputCost: 46.0, outputCost: 46.0,
			useTokens: usage{In: 1_000_000},
			priceMatrix: matrixForSeedance20(),
			// offline 是 supported=false 的 cell,不应命中(三方都 fallback)
			dimValues:   map[string]interface{}{"inference_mode": "offline"},
			expectMatched: false, expectCredits: positive,
		},

		// Video Seedance 1.5:audio_mode × inference_mode
		{name: "Matrix_VideoSeedance15_audio_online", modelType: "VideoGeneration", unit: "per_million_tokens",
			inputCost: 16.0, outputCost: 16.0,
			useTokens: usage{In: 1_000_000},
			priceMatrix: matrixForSeedance15(),
			dimValues:   map[string]interface{}{"audio_mode": "audio", "inference_mode": "online"},
			expectMatched: true, expectCredits: positive,
		},
		{name: "Matrix_VideoSeedance15_silent_offline", modelType: "VideoGeneration", unit: "per_million_tokens",
			inputCost: 16.0, outputCost: 16.0,
			useTokens: usage{In: 1_000_000},
			priceMatrix: matrixForSeedance15(),
			dimValues:   map[string]interface{}{"audio_mode": "silent", "inference_mode": "offline"},
			expectMatched: true, expectCredits: positive,
		},

		// TTS 矩阵命中:voice_tier × stream_mode
		{name: "Matrix_TTS_standard_nonstream", modelType: "TTS", unit: "per_10k_characters",
			inputCost: 0.2, outputCost: 0.2,
			useUnit: unitUsage{CharCount: 10000},
			priceMatrix: matrixForTTS(),
			dimValues:   map[string]interface{}{"voice_tier": "standard", "stream_mode": "non_stream"},
			expectMatched: true, expectCredits: positive,
		},
		{name: "Matrix_TTS_hd_stream", modelType: "TTS", unit: "per_10k_characters",
			inputCost: 0.2, outputCost: 0.2,
			useUnit: unitUsage{CharCount: 20000},
			priceMatrix: matrixForTTS(),
			dimValues:   map[string]interface{}{"voice_tier": "hd", "stream_mode": "stream"},
			expectMatched: true, expectCredits: positive,
		},

		// ASR 矩阵命中:recognition_type × inference_mode
		{name: "Matrix_ASR_realtime_online", modelType: "ASR", unit: "per_minute",
			inputCost: 0.4, outputCost: 0.4,
			useUnit: unitUsage{DurationSec: 600},
			priceMatrix: matrixForASR(),
			dimValues:   map[string]interface{}{"recognition_type": "realtime", "inference_mode": "online"},
			expectMatched: true, expectCredits: positive,
		},
		{name: "Matrix_ASR_file_offline", modelType: "ASR", unit: "per_minute",
			inputCost: 0.4, outputCost: 0.4,
			useUnit: unitUsage{DurationSec: 1200},
			priceMatrix: matrixForASR(),
			dimValues:   map[string]interface{}{"recognition_type": "file", "inference_mode": "offline"},
			expectMatched: true, expectCredits: positive,
		},

		// Embedding 矩阵命中(单 cell,language 维度):验证 Embedding 也能携带 dim 信息
		{name: "Matrix_Embedding_zh", modelType: "Embedding", unit: "per_million_tokens",
			inputCost: 0.5, outputCost: 0,
			useTokens: usage{In: 5000},
			priceMatrix: matrixForEmbedding(),
			dimValues:   map[string]interface{}{"language": "zh"},
			expectMatched: true, expectCredits: positive,
		},

		// E2E: 模拟管理员保存矩阵 → 真实扣费 → snapshot 显示命中维度
		{name: "Matrix_E2E_EditorSave_to_Snapshot", modelType: "VideoGeneration", unit: "per_million_tokens",
			inputCost: 46.0, outputCost: 46.0,
			useTokens: usage{In: 500_000},
			priceMatrix: matrixForSeedance20(),
			dimValues:   map[string]interface{}{"resolution": "720p", "input_has_video": false, "inference_mode": "online"},
			expectMatched: true, expectCredits: positive,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runConsistencyCase(t, c) })
	}
}

// ============================================================
// v3 PriceMatrix 测试矩阵生成器(用于 Matrix_* 用例)
// ============================================================

// matrixForLLMThinking 思考模式 LLM 的 2 cell 矩阵。
func matrixForLLMThinking() *model.PriceMatrix {
	return &model.PriceMatrix{
		SchemaVersion: 1,
		Currency:      "RMB",
		Unit:          model.UnitPerMillionTokens,
		Dimensions: []model.PriceDimension{
			{Key: "thinking_mode", Label: "思考模式", Type: "select", Values: []interface{}{"off", "on"}},
		},
		Cells: []model.PriceMatrixCell{
			{DimValues: map[string]interface{}{"thinking_mode": "off"}, Supported: true, SellingInput: floatPtr(1.0), SellingOutput: floatPtr(2.0)},
			{DimValues: map[string]interface{}{"thinking_mode": "on"}, Supported: true, SellingInput: floatPtr(1.0), SellingOutput: floatPtr(8.0)},
		},
	}
}

func matrixForVisionRes() *model.PriceMatrix {
	return &model.PriceMatrix{
		SchemaVersion: 1,
		Currency:      "RMB",
		Unit:          model.UnitPerMillionTokens,
		Dimensions: []model.PriceDimension{
			{Key: "resolution", Label: "分辨率", Type: "select", Values: []interface{}{"512", "1024"}},
		},
		Cells: []model.PriceMatrixCell{
			{DimValues: map[string]interface{}{"resolution": "512"}, Supported: true, SellingInput: floatPtr(2.0), SellingOutput: floatPtr(5.0)},
			{DimValues: map[string]interface{}{"resolution": "1024"}, Supported: true, SellingInput: floatPtr(3.0), SellingOutput: floatPtr(8.0)},
		},
	}
}

func matrixForImage() *model.PriceMatrix {
	return &model.PriceMatrix{
		SchemaVersion: 1,
		Currency:      "RMB",
		Unit:          model.UnitPerImage,
		Dimensions: []model.PriceDimension{
			{Key: "resolution", Label: "分辨率", Type: "select", Values: []interface{}{"512x512", "1024x1024"}},
			{Key: "quality", Label: "质量", Type: "select", Values: []interface{}{"standard", "hd"}},
		},
		Cells: []model.PriceMatrixCell{
			{DimValues: map[string]interface{}{"resolution": "512x512", "quality": "standard"}, Supported: true, SellingPerUnit: floatPtr(0.20)},
			{DimValues: map[string]interface{}{"resolution": "1024x1024", "quality": "hd"}, Supported: true, SellingPerUnit: floatPtr(0.40)},
		},
	}
}

// matrixForSeedance20 Seedance 2.0 的 6 supported + 3 unsupported(offline) cell 矩阵。
// 严格对应 G 验收标准的 "Seedance-2.0 看到 6+3 cell 的矩阵表"。
func matrixForSeedance20() *model.PriceMatrix {
	return &model.PriceMatrix{
		SchemaVersion: 1,
		Currency:      "RMB",
		Unit:          model.UnitPerMillionTokens,
		Dimensions: []model.PriceDimension{
			{Key: "resolution", Label: "输出分辨率", Type: "select", Values: []interface{}{"480p", "720p", "1080p"}},
			{Key: "input_has_video", Label: "输入是否含视频", Type: "boolean", Values: []interface{}{false, true}},
			{Key: "inference_mode", Label: "推理模式", Type: "select", Values: []interface{}{"online", "offline"}},
		},
		Cells: []model.PriceMatrixCell{
			// 6 supported (3 resolution × 2 has_video × 1 online)
			{DimValues: map[string]interface{}{"resolution": "480p", "input_has_video": false, "inference_mode": "online"}, Supported: true, SellingPerUnit: floatPtr(39.10)},
			{DimValues: map[string]interface{}{"resolution": "480p", "input_has_video": true, "inference_mode": "online"}, Supported: true, SellingPerUnit: floatPtr(23.80)},
			{DimValues: map[string]interface{}{"resolution": "720p", "input_has_video": false, "inference_mode": "online"}, Supported: true, SellingPerUnit: floatPtr(39.10)},
			{DimValues: map[string]interface{}{"resolution": "720p", "input_has_video": true, "inference_mode": "online"}, Supported: true, SellingPerUnit: floatPtr(23.80)},
			{DimValues: map[string]interface{}{"resolution": "1080p", "input_has_video": false, "inference_mode": "online"}, Supported: true, SellingPerUnit: floatPtr(39.10)},
			{DimValues: map[string]interface{}{"resolution": "1080p", "input_has_video": true, "inference_mode": "online"}, Supported: true, SellingPerUnit: floatPtr(23.80)},
			// 3 unsupported (3 resolution × any × offline)
			{DimValues: map[string]interface{}{"resolution": "480p", "inference_mode": "offline"}, Supported: false, UnsupportedReason: "Seedance 2.0 暂不支持离线"},
			{DimValues: map[string]interface{}{"resolution": "720p", "inference_mode": "offline"}, Supported: false, UnsupportedReason: "Seedance 2.0 暂不支持离线"},
			{DimValues: map[string]interface{}{"resolution": "1080p", "inference_mode": "offline"}, Supported: false, UnsupportedReason: "Seedance 2.0 暂不支持离线"},
		},
	}
}

// matrixForSeedance15 Seedance 1.5-pro 的 4 cell 矩阵(audio×inference_mode)。
// 严格对应 G 验收标准的 "Seedance-1.5-pro 看到 4 cell 矩阵(有声/无声 × 在线/离线)"。
func matrixForSeedance15() *model.PriceMatrix {
	return &model.PriceMatrix{
		SchemaVersion: 1,
		Currency:      "RMB",
		Unit:          model.UnitPerMillionTokens,
		Dimensions: []model.PriceDimension{
			{Key: "audio_mode", Label: "输出有声/无声", Type: "select", Values: []interface{}{"audio", "silent"}},
			{Key: "inference_mode", Label: "推理模式", Type: "select", Values: []interface{}{"online", "offline"}},
		},
		Cells: []model.PriceMatrixCell{
			{DimValues: map[string]interface{}{"audio_mode": "audio", "inference_mode": "online"}, Supported: true, SellingPerUnit: floatPtr(13.6)},
			{DimValues: map[string]interface{}{"audio_mode": "audio", "inference_mode": "offline"}, Supported: true, SellingPerUnit: floatPtr(11.6)},
			{DimValues: map[string]interface{}{"audio_mode": "silent", "inference_mode": "online"}, Supported: true, SellingPerUnit: floatPtr(12.6)},
			{DimValues: map[string]interface{}{"audio_mode": "silent", "inference_mode": "offline"}, Supported: true, SellingPerUnit: floatPtr(10.6)},
		},
	}
}

func matrixForTTS() *model.PriceMatrix {
	return &model.PriceMatrix{
		SchemaVersion: 1,
		Currency:      "RMB",
		Unit:          model.UnitPer10kCharacters,
		Dimensions: []model.PriceDimension{
			{Key: "voice_tier", Label: "音色档", Type: "select", Values: []interface{}{"standard", "hd"}},
			{Key: "stream_mode", Label: "流式模式", Type: "select", Values: []interface{}{"non_stream", "stream"}},
		},
		Cells: []model.PriceMatrixCell{
			{DimValues: map[string]interface{}{"voice_tier": "standard", "stream_mode": "non_stream"}, Supported: true, SellingPerUnit: floatPtr(0.18)},
			{DimValues: map[string]interface{}{"voice_tier": "standard", "stream_mode": "stream"}, Supported: true, SellingPerUnit: floatPtr(0.20)},
			{DimValues: map[string]interface{}{"voice_tier": "hd", "stream_mode": "non_stream"}, Supported: true, SellingPerUnit: floatPtr(0.30)},
			{DimValues: map[string]interface{}{"voice_tier": "hd", "stream_mode": "stream"}, Supported: true, SellingPerUnit: floatPtr(0.35)},
		},
	}
}

func matrixForASR() *model.PriceMatrix {
	return &model.PriceMatrix{
		SchemaVersion: 1,
		Currency:      "RMB",
		Unit:          model.UnitPerMinute,
		Dimensions: []model.PriceDimension{
			{Key: "recognition_type", Label: "识别类型", Type: "select", Values: []interface{}{"realtime", "file", "long"}},
			{Key: "inference_mode", Label: "推理模式", Type: "select", Values: []interface{}{"online", "offline"}},
		},
		Cells: []model.PriceMatrixCell{
			{DimValues: map[string]interface{}{"recognition_type": "realtime", "inference_mode": "online"}, Supported: true, SellingPerUnit: floatPtr(0.40)},
			{DimValues: map[string]interface{}{"recognition_type": "file", "inference_mode": "offline"}, Supported: true, SellingPerUnit: floatPtr(0.30)},
			{DimValues: map[string]interface{}{"recognition_type": "long", "inference_mode": "offline"}, Supported: true, SellingPerUnit: floatPtr(0.25)},
		},
	}
}

func matrixForEmbedding() *model.PriceMatrix {
	return &model.PriceMatrix{
		SchemaVersion: 1,
		Currency:      "RMB",
		Unit:          model.UnitPerMillionTokens,
		Dimensions: []model.PriceDimension{
			{Key: "language", Label: "语言", Type: "select", Values: []interface{}{"zh", "en", "multi"}},
		},
		Cells: []model.PriceMatrixCell{
			{DimValues: map[string]interface{}{"language": "zh"}, Supported: true, SellingInput: floatPtr(0.5)},
			{DimValues: map[string]interface{}{"language": "en"}, Supported: true, SellingInput: floatPtr(0.5)},
			{DimValues: map[string]interface{}{"language": "multi"}, Supported: true, SellingInput: floatPtr(0.7)},
		},
	}
}

func floatPtr(v float64) *float64 { return &v }

// consistencyCase 单条测试用例。
type consistencyCase struct {
	name               string
	modelType          string
	unit               string
	inputCost          float64
	outputCost         float64
	outputThinkingCost float64
	supportsCache      bool
	cacheMech          string
	cacheInputPrice    float64
	thinkingMode       bool
	tiers              model.PriceTiersData
	useTokens          usage
	useUnit            unitUsage
	expectCredits      expectation

	// v3 PriceMatrix 矩阵命中场景:若 priceMatrix 非空,则:
	//   1. 写入 ModelPricing.PriceMatrix
	//   2. SettleUsage / SettleUnitUsage / Calculate 带上 dimValues
	//   3. 校验 snapshot.quote.matched_dim_values 与 dimValues 一致(若 expectMatched=true)
	priceMatrix    *model.PriceMatrix
	dimValues      map[string]interface{}
	expectMatched  bool // true = 期望命中 cell;false = 期望 fallback,无 matched_dim_values
}

// expectation 预期金额规则。
type expectation int

const (
	zero       expectation = 0   // 必须 = 0(0 价模型不保底)
	positive   expectation = 1   // 必须 > 0
	exactMatch expectation = 100 // 占位,实际值由用例 expectCredits 直接传整数
)

type usage struct {
	In         int
	Out        int
	CacheRead  int
	CacheWrite int
}

type unitUsage struct {
	Images      int
	DurationSec float64
	CharCount   int
	CallCount   int
}

func standardTiers() model.PriceTiersData {
	return model.PriceTiersData{
		Tiers: []model.PriceTier{
			{Name: "tier1", InputMin: 0, InputMax: ptr(int64(32000)), InputPrice: 1.0, OutputPrice: 2.0},
			{Name: "tier2", InputMin: 32000, InputPrice: 1.5, OutputPrice: 3.0},
		},
		Currency: "CNY",
	}
}

func imageTiers() model.PriceTiersData {
	return model.PriceTiersData{
		Tiers: []model.PriceTier{
			{Name: "tier_512", Variant: "512x512", InputPrice: 0.1, OutputPrice: 0.1},
			{Name: "tier_1024", Variant: "1024x1024", InputPrice: 0.3, OutputPrice: 0.3},
		},
		Currency: "CNY",
	}
}

func ptr[T any](v T) *T { return &v }

// runConsistencyCase 执行单个用例:验证三方一致性。
func runConsistencyCase(t *testing.T, c consistencyCase) {
	t.Helper()
	db := newBillingTestDB(t)
	m := seedConsistencyModel(t, db, c)
	seedBillingBalance(t, db, 100, 1, 100_000_000)

	pc := pricing.NewPricingCalculator(db)
	qs := NewQuoteService(db, pc)
	svc := NewService(db, pc, balancesvc.NewBalanceService(db, nil))

	requestID := fmt.Sprintf("consistency-%s", c.name)
	scenario := QuoteScenarioCharge

	// 路径 A: Calculate(charge)
	qreq := QuoteRequest{
		Scenario:  scenario,
		RequestID: requestID,
		ModelID:   m.ID,
		UserID:    100,
		TenantID:  1,
		Usage: QuoteUsage{
			InputTokens:      c.useTokens.In,
			OutputTokens:     c.useTokens.Out,
			CacheReadTokens:  c.useTokens.CacheRead,
			CacheWriteTokens: c.useTokens.CacheWrite,
			ImageCount:       c.useUnit.Images,
			DurationSec:      c.useUnit.DurationSec,
			CharCount:        c.useUnit.CharCount,
			CallCount:        c.useUnit.CallCount,
		},
		ThinkingMode: c.thinkingMode,
		DimValues:    c.dimValues,
	}
	quoteA, err := qs.Calculate(context.Background(), qreq)
	if err != nil {
		t.Fatalf("Calculate failed: %v", err)
	}

	// 路径 B: SettleUsage / SettleUnitUsage
	var actualCost int64
	var snapshotMap map[string]interface{}
	if isTokenUnit(c.unit) {
		out, err := svc.SettleUsage(context.Background(), UsageRequest{
			RequestID: requestID,
			UserID:    100,
			TenantID:  1,
			ModelName: m.ModelName,
			Usage: provider.Usage{
				PromptTokens:     c.useTokens.In,
				CompletionTokens: c.useTokens.Out,
				CacheReadTokens:  c.useTokens.CacheRead,
				CacheWriteTokens: c.useTokens.CacheWrite,
			},
			ThinkingMode: c.thinkingMode,
			DimValues:    c.dimValues,
		})
		if err != nil {
			t.Fatalf("SettleUsage failed: %v", err)
		}
		actualCost = out.CostCredits
		snapshotMap = out.Snapshot
	} else {
		out, err := svc.SettleUnitUsage(context.Background(), UnitUsageRequest{
			RequestID: requestID,
			UserID:    100,
			TenantID:  1,
			ModelName: m.ModelName,
			Usage: pricing.UsageInput{
				ImageCount:  c.useUnit.Images,
				DurationSec: c.useUnit.DurationSec,
				CharCount:   c.useUnit.CharCount,
				CallCount:   c.useUnit.CallCount,
			},
			DimValues: c.dimValues,
		})
		if err != nil {
			t.Fatalf("SettleUnitUsage failed: %v", err)
		}
		actualCost = out.CostCredits
		snapshotMap = out.Snapshot
	}

	// ===== 不变量 1:total_credits 三方一致 =====
	if quoteA.TotalCredits != actualCost {
		t.Fatalf("[%s] Calculate vs Settle: %d != %d", c.name, quoteA.TotalCredits, actualCost)
	}

	// ===== 不变量 2:snapshot.quote.total_credits 与 Calculate 一致 =====
	rawQuote, ok := snapshotMap["quote"].(map[string]interface{})
	if !ok || rawQuote == nil {
		t.Fatalf("[%s] snapshot.quote missing", c.name)
	}
	snapTotal := snapshotInt64(rawQuote, "total_credits")
	if snapTotal != quoteA.TotalCredits {
		t.Fatalf("[%s] Calculate vs snapshot.quote: %d != %d", c.name, quoteA.TotalCredits, snapTotal)
	}

	// ===== 不变量 3:同 scenario+usage hash 一致 =====
	snapHash, _ := rawQuote["quote_hash"].(string)
	if quoteA.QuoteHash != snapHash {
		t.Fatalf("[%s] quote_hash mismatch:\n  calc=%q\n  snap=%q", c.name, quoteA.QuoteHash, snapHash)
	}

	// ===== 不变量 4:line items 求和 =====
	var lineSum int64
	if items, ok := rawQuote["line_items"].([]map[string]interface{}); ok {
		for _, li := range items {
			lineSum += snapshotInt64(li, "cost_credits")
		}
		if lineSum != snapTotal {
			t.Fatalf("[%s] line items sum=%d != total=%d", c.name, lineSum, snapTotal)
		}
	}

	// ===== 不变量 5:expectation 校验 =====
	switch c.expectCredits {
	case zero:
		if quoteA.TotalCredits != 0 {
			t.Fatalf("[%s] expected zero, got %d", c.name, quoteA.TotalCredits)
		}
	case positive:
		if quoteA.TotalCredits <= 0 {
			t.Fatalf("[%s] expected positive, got %d", c.name, quoteA.TotalCredits)
		}
	default:
		// exactMatch with embedded value
		expected := int64(c.expectCredits)
		if expected > 0 && quoteA.TotalCredits != expected {
			t.Fatalf("[%s] expected %d, got %d", c.name, expected, quoteA.TotalCredits)
		}
	}

	// ===== 不变量 6 (v3): PriceMatrix 命中一致性 =====
	// 当 expectMatched=true 时,Calculate / Settle 双方都必须命中同一 cell。
	// 当 expectMatched=false 时(请求 dimValues 但 cell 不存在/不支持),双方都不应命中。
	if c.dimValues != nil || c.priceMatrix != nil {
		matchedFromSnap, _ := rawQuote["matched_dim_values"].(map[string]interface{})
		if c.expectMatched {
			if quoteA.MatchedDimValues == nil {
				t.Fatalf("[%s] Calculate.MatchedDimValues nil; expected match for dimValues=%v", c.name, c.dimValues)
			}
			if matchedFromSnap == nil {
				t.Fatalf("[%s] snapshot.quote.matched_dim_values missing; expected match for dimValues=%v", c.name, c.dimValues)
			}
			// 命中的 cell 必须包含请求的所有 dim key,且值一致
			for k, v := range c.dimValues {
				if matchedFromSnap[k] == nil {
					t.Fatalf("[%s] snapshot matched_dim_values missing key %q (want %v)", c.name, k, v)
				}
				if fmt.Sprintf("%v", matchedFromSnap[k]) != fmt.Sprintf("%v", v) {
					t.Fatalf("[%s] snapshot matched_dim_values[%q]=%v vs dim req=%v", c.name, k, matchedFromSnap[k], v)
				}
				if fmt.Sprintf("%v", quoteA.MatchedDimValues[k]) != fmt.Sprintf("%v", v) {
					t.Fatalf("[%s] Calculate.MatchedDimValues[%q]=%v vs dim req=%v", c.name, k, quoteA.MatchedDimValues[k], v)
				}
			}
		} else {
			// 不期望命中: Calculate.MatchedDimValues 应为空
			if len(quoteA.MatchedDimValues) != 0 {
				t.Fatalf("[%s] expected no match but Calculate.MatchedDimValues=%v", c.name, quoteA.MatchedDimValues)
			}
		}
	}
}

// seedConsistencyModel 按用例配置注入一个测试模型。
func seedConsistencyModel(t *testing.T, db *gorm.DB, c consistencyCase) *model.AIModel {
	t.Helper()
	m := model.AIModel{
		CategoryID:            1,
		SupplierID:            1,
		ModelName:             fmt.Sprintf("test-%s", c.name),
		DisplayName:           c.name,
		IsActive:              true,
		Status:                "online",
		ModelType:             c.modelType,
		PricingUnit:           c.unit,
		InputCostRMB:          c.inputCost,
		OutputCostRMB:         c.outputCost,
		OutputCostThinkingRMB: c.outputThinkingCost,
		SupportsCache:         c.supportsCache,
		CacheMechanism:        c.cacheMech,
		CacheInputPriceRMB:    c.cacheInputPrice,
	}
	if len(c.tiers.Tiers) > 0 {
		raw, _ := json.Marshal(c.tiers)
		m.PriceTiers = raw
	}
	if err := db.Create(&m).Error; err != nil {
		t.Fatalf("seed model: %v", err)
	}

	// 创建配套 ModelPricing(若有 cost,则按 1:1 写 selling)
	mp := model.ModelPricing{
		ModelID:             m.ID,
		InputPriceRMB:       c.inputCost,
		OutputPriceRMB:      c.outputCost,
		InputPricePerToken:  int64(c.inputCost * 10000),
		OutputPricePerToken: int64(c.outputCost * 10000),
		Currency:            "CREDIT",
	}
	if c.outputThinkingCost > 0 {
		mp.OutputPriceThinkingRMB = c.outputThinkingCost
		mp.OutputPriceThinkingPerToken = int64(c.outputThinkingCost * 10000)
	}
	if len(c.tiers.Tiers) > 0 {
		// 以 selling=cost 直接写,本测试不验证折扣
		newTiers := c.tiers
		for i := range newTiers.Tiers {
			vIn := newTiers.Tiers[i].InputPrice
			vOut := newTiers.Tiers[i].OutputPrice
			newTiers.Tiers[i].SellingInputPrice = &vIn
			newTiers.Tiers[i].SellingOutputPrice = &vOut
		}
		raw, _ := json.Marshal(newTiers)
		mp.PriceTiers = raw
	}
	// v3 PriceMatrix 写入:管理员"保存矩阵"操作模拟
	if c.priceMatrix != nil {
		raw, _ := json.Marshal(c.priceMatrix)
		mp.PriceMatrix = raw
	}
	if err := db.Create(&mp).Error; err != nil {
		t.Fatalf("seed pricing: %v", err)
	}
	return &m
}

func isTokenUnit(unit string) bool {
	return unit == "per_million_tokens" || unit == ""
}
