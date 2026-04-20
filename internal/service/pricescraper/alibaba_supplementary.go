package pricescraper

import (
	"tokenhub-server/internal/model"
)

// =====================================================
// 阿里云百炼补充价格数据（v3.5）
// 来源：https://help.aliyun.com/zh/model-studio/model-pricing（2026-04 抓取）
//
// 用途：
//   1. 当 DashScope API 未返回某模型的价格数据时兜底（API 优先）
//   2. 覆盖按张/按秒/按万字符/按小时等非 Token 计费模型
//   3. 覆盖 Embedding/Rerank 等专用模型
//
// 维护原则：
//   - 数据仅作 fallback，不覆盖 API 正常返回的价格
//   - 价格定期手动与官网对齐（每季度）
// =====================================================

// getAlibabaSupplementaryPrices 返回阿里云补充价格列表
func getAlibabaSupplementaryPrices() []ScrapedModel {
	i64 := func(v int64) *int64 { return &v }
	_ = i64 // 保留指针辅助，后续扩展备用

	return []ScrapedModel{
		// ============ 通义万相 文生图 / 图生图 — 元/张 ============
		{
			ModelName: "qwen-image-2.0-pro", DisplayName: "通义千问图像 2.0 Pro",
			InputPrice: 0.5, Currency: "CNY",
			ModelType: "ImageGeneration", PricingUnit: PricingUnitPerImage,
		},
		{
			ModelName: "qwen-image-2.0", DisplayName: "通义千问图像 2.0",
			InputPrice: 0.2, Currency: "CNY",
			ModelType: "ImageGeneration", PricingUnit: PricingUnitPerImage,
		},
		{
			ModelName: "wanx2.1-t2i-plus", DisplayName: "通义万相 2.1 文生图 Plus",
			InputPrice: 0.2, Currency: "CNY",
			ModelType: "ImageGeneration", PricingUnit: PricingUnitPerImage,
		},
		{
			ModelName: "wanx2.1-t2i-turbo", DisplayName: "通义万相 2.1 文生图 Turbo",
			InputPrice: 0.14, Currency: "CNY",
			ModelType: "ImageGeneration", PricingUnit: PricingUnitPerImage,
		},
		{
			ModelName: "wan2.6-t2i", DisplayName: "通义万相 2.6 文生图",
			InputPrice: 0.2, Currency: "CNY",
			ModelType: "ImageGeneration", PricingUnit: PricingUnitPerImage,
		},
		{
			ModelName: "wanx2.0-t2i-turbo", DisplayName: "通义万相 2.0 文生图 Turbo",
			InputPrice: 0.14, Currency: "CNY",
			ModelType: "ImageGeneration", PricingUnit: PricingUnitPerImage,
		},

		// ============ 通义万相 视频生成 — 元/秒（按输出时长） ============
		// 参考定价：720P / 1080P 差异化，用 Variant 区分
		{
			ModelName: "wan2.7-t2v", DisplayName: "通义万相 2.7 文生视频 720P",
			InputPrice: 0.6, Currency: "CNY", Variant: "720P",
			ModelType: "VideoGeneration", PricingUnit: PricingUnitPerSecond,
			PriceTiers: []model.PriceTier{
				{Name: "720P", Variant: "720P", InputPrice: 0.6},
				{Name: "1080P", Variant: "1080P", InputPrice: 1.0},
			},
		},
		{
			ModelName: "wan2.6-t2v", DisplayName: "通义万相 2.6 文生视频 720P",
			InputPrice: 0.6, Currency: "CNY", Variant: "720P",
			ModelType: "VideoGeneration", PricingUnit: PricingUnitPerSecond,
			PriceTiers: []model.PriceTier{
				{Name: "720P", Variant: "720P", InputPrice: 0.6},
				{Name: "1080P", Variant: "1080P", InputPrice: 1.0},
			},
		},
		{
			ModelName: "wanx2.1-t2v-turbo", DisplayName: "通义万相 2.1 文生视频 Turbo",
			InputPrice: 0.24, Currency: "CNY",
			ModelType: "VideoGeneration", PricingUnit: PricingUnitPerSecond,
		},
		{
			ModelName: "wanx2.1-t2v-plus", DisplayName: "通义万相 2.1 文生视频 Plus",
			InputPrice: 0.7, Currency: "CNY",
			ModelType: "VideoGeneration", PricingUnit: PricingUnitPerSecond,
		},

		// ============ 通义千问 TTS 语音合成 — 元/万字符 ============
		{
			ModelName: "qwen3-tts-flash", DisplayName: "Qwen3 TTS Flash",
			InputPrice: 0.8, Currency: "CNY",
			ModelType: "TTS", PricingUnit: PricingUnitPer10kCharacters,
		},
		{
			ModelName: "cosyvoice-v3.5-plus", DisplayName: "CosyVoice V3.5 Plus",
			InputPrice: 1.5, Currency: "CNY",
			ModelType: "TTS", PricingUnit: PricingUnitPer10kCharacters,
		},
		{
			ModelName: "cosyvoice-v3-flash", DisplayName: "CosyVoice V3 Flash",
			InputPrice: 0.8, Currency: "CNY",
			ModelType: "TTS", PricingUnit: PricingUnitPer10kCharacters,
		},
		{
			ModelName: "cosyvoice-v2", DisplayName: "CosyVoice V2",
			InputPrice: 2.0, Currency: "CNY",
			ModelType: "TTS", PricingUnit: PricingUnitPer10kCharacters,
		},
		{
			ModelName: "cosyvoice-v1", DisplayName: "CosyVoice V1",
			InputPrice: 2.0, Currency: "CNY",
			ModelType: "TTS", PricingUnit: PricingUnitPer10kCharacters,
		},

		// ============ 通义千问 ASR 语音识别 — 元/秒 ============
		// 阿里云官方单位为元/秒，但大多在 0.00008-0.00022 级别
		{
			ModelName: "qwen3-asr-flash", DisplayName: "Qwen3 ASR Flash",
			InputPrice: 0.00022, Currency: "CNY",
			ModelType: "ASR", PricingUnit: PricingUnitPerSecond,
		},
		{
			ModelName: "fun-asr", DisplayName: "FunASR",
			InputPrice: 0.00022, Currency: "CNY",
			ModelType: "ASR", PricingUnit: PricingUnitPerSecond,
		},
		{
			ModelName: "paraformer-v2", DisplayName: "Paraformer V2",
			InputPrice: 0.00008, Currency: "CNY",
			ModelType: "ASR", PricingUnit: PricingUnitPerSecond,
		},
		{
			ModelName: "paraformer-v1", DisplayName: "Paraformer V1",
			InputPrice: 0.00008, Currency: "CNY",
			ModelType: "ASR", PricingUnit: PricingUnitPerSecond,
		},
		{
			ModelName: "sensevoice-v1", DisplayName: "SenseVoice V1",
			InputPrice: 0.00022, Currency: "CNY",
			ModelType: "ASR", PricingUnit: PricingUnitPerSecond,
		},

		// ============ 文本嵌入 Embedding — 元/百万 tokens ============
		{
			ModelName: "text-embedding-v4", DisplayName: "Text Embedding V4",
			InputPrice: 0.7, Currency: "CNY",
			ModelType: "Embedding", PricingUnit: PricingUnitPerMillionTokens,
		},
		{
			ModelName: "text-embedding-v3", DisplayName: "Text Embedding V3",
			InputPrice: 0.7, Currency: "CNY",
			ModelType: "Embedding", PricingUnit: PricingUnitPerMillionTokens,
		},
		{
			ModelName: "text-embedding-v2", DisplayName: "Text Embedding V2",
			InputPrice: 0.7, Currency: "CNY",
			ModelType: "Embedding", PricingUnit: PricingUnitPerMillionTokens,
		},
		{
			ModelName: "text-embedding-v1", DisplayName: "Text Embedding V1",
			InputPrice: 0.7, Currency: "CNY",
			ModelType: "Embedding", PricingUnit: PricingUnitPerMillionTokens,
		},
		{
			ModelName: "text-embedding-async-v2", DisplayName: "Text Embedding Async V2",
			InputPrice: 0.5, Currency: "CNY",
			ModelType: "Embedding", PricingUnit: PricingUnitPerMillionTokens,
		},

		// ============ Rerank 重排序 — 元/百万 tokens ============
		// 阿里云 gte-rerank 按 token 计费，无缓存
		{
			ModelName: "gte-rerank-v2", DisplayName: "GTE Rerank V2",
			InputPrice: 0.8, Currency: "CNY",
			ModelType: "Rerank", PricingUnit: PricingUnitPerMillionTokens,
		},
		{
			ModelName: "gte-rerank", DisplayName: "GTE Rerank",
			InputPrice: 0.8, Currency: "CNY",
			ModelType: "Rerank", PricingUnit: PricingUnitPerMillionTokens,
		},
	}
}

// mergeAlibabaWithSupplementary API 结果 + 补充价格合并
// API 优先：仅在 API 结果中未包含或价格为 0 时，使用补充数据兜底
//
// v3.5 增强：
//   1. 精确命中 → 填充空字段（已有逻辑）
//   2. 前缀匹配 → 将 supp.ModelName 作为前缀匹配所有零价的 API 模型
//      例：supp "cosyvoice" → 匹配 API 的 "cosyvoice-v3", "cosyvoice-v2-2026-01" 等
//   3. 完全无匹配 → append 补充条目
func mergeAlibabaWithSupplementary(apiModels []ScrapedModel, supplementary []ScrapedModel) []ScrapedModel {
	// 建立 API 模型索引（按小写模型名）
	apiIdx := make(map[string]int, len(apiModels))
	for i, m := range apiModels {
		apiIdx[normalizeModelID(m.ModelName)] = i
	}

	result := make([]ScrapedModel, len(apiModels))
	copy(result, apiModels)

	// 辅助函数：用 supp 填充 m 的空字段
	fillEmpty := func(m *ScrapedModel, supp ScrapedModel) {
		if m.InputPrice == 0 && supp.InputPrice > 0 {
			m.InputPrice = supp.InputPrice
		}
		if m.OutputPrice == 0 && supp.OutputPrice > 0 {
			m.OutputPrice = supp.OutputPrice
		}
		if m.ModelType == "" {
			m.ModelType = supp.ModelType
		}
		if m.PricingUnit == "" {
			m.PricingUnit = supp.PricingUnit
		}
		if m.Variant == "" {
			m.Variant = supp.Variant
		}
		if len(m.PriceTiers) == 0 && len(supp.PriceTiers) > 0 {
			m.PriceTiers = supp.PriceTiers
		}
	}

	for _, supp := range supplementary {
		key := normalizeModelID(supp.ModelName)

		// 步骤 1：精确命中
		if idx, ok := apiIdx[key]; ok {
			fillEmpty(&result[idx], supp)
			continue
		}

		// 步骤 2：前缀匹配（supp 名作为 API 模型名的前缀）
		// 仅对价格类字段（InputPrice/OutputPrice）做兜底，避免覆盖 API 的类型/单位推断
		prefixMatched := false
		for i := range result {
			apiKey := normalizeModelID(result[i].ModelName)
			// 精确前缀匹配（必须以 - 分隔，避免 "qwen" 误匹配 "qwen3"）
			if len(apiKey) > len(key) && apiKey[:len(key)] == key && apiKey[len(key)] == '-' {
				// 只在 API 模型价格为 0 时填充
				if result[i].InputPrice == 0 && supp.InputPrice > 0 {
					result[i].InputPrice = supp.InputPrice
					result[i].OutputPrice = supp.OutputPrice
					// ModelType / PricingUnit / PriceTiers 仅在 API 未推断时补充
					if result[i].ModelType == "" {
						result[i].ModelType = supp.ModelType
					}
					if result[i].PricingUnit == "" {
						result[i].PricingUnit = supp.PricingUnit
					}
					if len(result[i].PriceTiers) == 0 && len(supp.PriceTiers) > 0 {
						result[i].PriceTiers = supp.PriceTiers
					}
					prefixMatched = true
				}
			}
		}

		// 步骤 3：如果既无精确也无前缀匹配 → append 补充条目本身
		if !prefixMatched {
			result = append(result, supp)
		}
	}

	return result
}
