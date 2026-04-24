package pricescraper

import (
	"context"
	"time"

	"tokenhub-server/internal/model"
)

// =====================================================
// TalkingData 灵犀（TD 云牍）价格爬虫
//
// 策略：纯文件源（价格取自 "模型成本方案 (1.1).xlsx"），不发 HTTP。
// 更新价格时直接修改 talkingDataScrapedModels() 返回值。
//
// 与 seed_talkingdata.go 的模型定义保持一致，用于：
//   - 管理后台「价格抓取」预览：对比 DB 中现有价格 vs 文件价格，展示差异
//   - 未来如需批量应用最新价格，可走 ApplyPrices 路径
// =====================================================

const talkingDataSourceURL = "file:模型成本方案 (1.1).xlsx"

// TalkingDataScraper 文件源价格爬虫，实现 Scraper + KeySetter 接口
type TalkingDataScraper struct{}

// NewTalkingDataScraper 创建 TalkingData 爬虫
func NewTalkingDataScraper() *TalkingDataScraper {
	return &TalkingDataScraper{}
}

// SetAPIKey 文件源不需要 Key，空实现以满足 KeySetter 接口
func (s *TalkingDataScraper) SetAPIKey(_ string) {}

// ScrapePrices 返回 xlsx 中 doubao 模型的完整价格数据
func (s *TalkingDataScraper) ScrapePrices(ctx context.Context) (*ScrapedPriceData, error) {
	_ = ctx
	return &ScrapedPriceData{
		SupplierName: "TalkingData灵犀",
		FetchedAt:    time.Now(),
		SourceURL:    talkingDataSourceURL,
		Models:       talkingDataScrapedModels(),
	}, nil
}

// talkingDataScrapedModels 所有 doubao 模型的 ScrapedModel，源 = xlsx
// 字段与 seed_talkingdata.go 中的 talkingDataModelDefs 一一对应
func talkingDataScrapedModels() []ScrapedModel {
	const cacheStorage = 0.017

	newTier := func(inMin, inMax int64, inP, outP, cacheIn float64) model.PriceTier {
		t := model.PriceTier{
			InputMin:          inMin,
			InputMinExclusive: inMin > 0,
			InputPrice:        inP,
			OutputPrice:       outP,
			CacheInputPrice:   cacheIn,
		}
		if inMax > 0 {
			max := inMax
			t.InputMax = &max
			t.InputMaxExclusive = false
		}
		t.Normalize()
		return t
	}

	textModel := func(name, display string, modelType string, tiers []model.PriceTier, cacheIn float64) ScrapedModel {
		m := ScrapedModel{
			ModelName:            name,
			DisplayName:          display,
			Currency:             "CNY",
			PricingUnit:          PricingUnitPerMillionTokens,
			ModelType:            modelType,
			PriceTiers:           tiers,
			SupportsCache:        true,
			CacheMechanism:       "auto",
			CacheMinTokens:       0,
			CacheInputPrice:      cacheIn,
			CacheStoragePrice:    cacheStorage,
			CacheSource:          "file",
		}
		if len(tiers) > 0 {
			m.InputPrice = tiers[0].InputPrice
			m.OutputPrice = tiers[0].OutputPrice
		}
		return m
	}

	singleTextModel := func(name, display string, input, output, cacheIn float64) ScrapedModel {
		return ScrapedModel{
			ModelName:            name,
			DisplayName:          display,
			Currency:             "CNY",
			PricingUnit:          PricingUnitPerMillionTokens,
			ModelType:            "LLM",
			InputPrice:           input,
			OutputPrice:          output,
			SupportsCache:        true,
			CacheMechanism:       "auto",
			CacheInputPrice:      cacheIn,
			CacheStoragePrice:    cacheStorage,
			CacheSource:          "file",
		}
	}

	imageModel := func(name, display string, price float64) ScrapedModel {
		return ScrapedModel{
			ModelName:   name,
			DisplayName: display,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerImage,
			ModelType:   "ImageGeneration",
			InputPrice:  price,
			OutputPrice: price,
			CacheSource: "file",
		}
	}

	videoModel := func(name, display, variant string, input, output float64) ScrapedModel {
		return ScrapedModel{
			ModelName:   name,
			DisplayName: display,
			Variant:     variant,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "VideoGeneration",
			InputPrice:  input,
			OutputPrice: output,
			CacheSource: "file",
		}
	}

	return []ScrapedModel{
		// 文本模型（3 档阶梯）
		textModel("doubao-seed-2.0-pro", "Doubao Seed 2.0 Pro", "LLM", []model.PriceTier{
			newTier(0, 32000, 3.2, 16, 0.64),
			newTier(32000, 128000, 4.8, 24, 0.96),
			newTier(128000, 256000, 9.6, 48, 1.92),
		}, 0.64),
		textModel("doubao-seed-2.0-lite", "Doubao Seed 2.0 Lite", "LLM", []model.PriceTier{
			newTier(0, 32000, 0.6, 3.6, 0.12),
			newTier(32000, 128000, 0.9, 5.4, 0.18),
			newTier(128000, 256000, 1.8, 10.8, 0.36),
		}, 0.12),
		textModel("doubao-seed-2.0-mini", "Doubao Seed 2.0 Mini", "LLM", []model.PriceTier{
			newTier(0, 32000, 0.2, 2, 0.04),
			newTier(32000, 128000, 0.4, 4, 0.08),
			newTier(128000, 256000, 0.8, 8, 0.16),
		}, 0.04),
		textModel("doubao-seed-2.0-code", "Doubao Seed 2.0 Code", "LLM", []model.PriceTier{
			newTier(0, 32000, 3.2, 16, 0.64),
			newTier(32000, 128000, 4.8, 24, 0.96),
			newTier(128000, 256000, 9.6, 48, 1.92),
		}, 0.64),
		textModel("doubao-seed-1.8", "Doubao Seed 1.8", "LLM", []model.PriceTier{
			newTier(0, 32000, 0.8, 2, 0.16),
			newTier(32000, 128000, 1.2, 16, 0.16),
			newTier(128000, 256000, 2.4, 24, 0.16),
		}, 0.16),
		textModel("doubao-seed-1.6", "Doubao Seed 1.6", "LLM", []model.PriceTier{
			newTier(0, 32000, 0.8, 2, 0.16),
			newTier(32000, 128000, 1.2, 16, 0.16),
			newTier(128000, 256000, 2.4, 24, 0.16),
		}, 0.16),
		textModel("doubao-seed-1.6-flash", "Doubao Seed 1.6 Flash", "LLM", []model.PriceTier{
			newTier(0, 32000, 0.15, 1.5, 0.03),
			newTier(32000, 128000, 0.3, 3, 0.03),
			newTier(128000, 256000, 0.6, 6, 0.03),
		}, 0.03),
		textModel("doubao-seed-1.6-vision", "Doubao Seed 1.6 Vision", "Vision", []model.PriceTier{
			newTier(0, 32000, 0.8, 8, 0.16),
			newTier(32000, 128000, 1.2, 16, 0.16),
			newTier(128000, 256000, 2.4, 24, 0.16),
		}, 0.16),
		textModel("doubao-seed-1.6-thinking", "Doubao Seed 1.6 Thinking", "LLM", []model.PriceTier{
			newTier(0, 32000, 0.8, 8, 0.16),
			newTier(32000, 128000, 1.2, 16, 0.16),
			newTier(128000, 256000, 2.4, 24, 0.16),
		}, 0.16),
		textModel("doubao-seed-1.6-lite", "Doubao Seed 1.6 Lite", "LLM", []model.PriceTier{
			newTier(0, 32000, 0.3, 0.6, 0.06),
			newTier(32000, 128000, 0.6, 4, 0.06),
			newTier(128000, 256000, 1.2, 12, 0.06),
		}, 0.06),
		singleTextModel("doubao-1.5-pro-32k", "Doubao 1.5 Pro 32K", 0.8, 2, 0.16),

		// 图片模型
		imageModel("doubao-seedream-5.0-lite", "Doubao Seedream 5.0 Lite", 0.22),
		imageModel("doubao-seedream-4.5", "Doubao Seedream 4.5", 0.25),
		imageModel("doubao-seedream-4.0", "Doubao Seedream 4.0", 0.20),
		imageModel("doubao-seedream-3.0", "Doubao Seedream 3.0", 0.259),

		// 视频模型
		videoModel("doubao-seedance-2.0-720p", "Doubao Seedance 2.0 (720p)", "720p", 28, 46),
		videoModel("doubao-seedance-2.0-1080p", "Doubao Seedance 2.0 (1080p)", "1080p", 31, 51),
		videoModel("doubao-seedance-1.5-pro", "Doubao Seedance 1.5 Pro", "", 8, 16),
	}
}
