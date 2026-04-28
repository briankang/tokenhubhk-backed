package database

import (
	"encoding/json"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// RunSeedTalkingData 种子：TalkingData 平台（以 "TD-火山引擎" 命名，表明 doubao 源自火山）
//
// 设计原则：
//   - 供应商、分类、渠道：存在则更新关键配置，不存在则创建
//   - 模型：存在则 UPDATE 价格/阶梯/缓存/折扣（数据可随 xlsx 更新），不存在则 INSERT
//   - 价格来源：C:\Users\a1762\Desktop\模型成本方案 (1.1).xlsx（仅 doubao 相关）
//   - API 协议：OpenAI 兼容，Bearer <APPCODE>
//   - 文档: https://doc.talkingdata.com/posts/1174
func RunSeedTalkingData(db *gorm.DB) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}
	if db == nil {
		log.Warn("seed_talkingdata: db is nil, skip")
		return
	}

	log.Info("seed_talkingdata: 开始初始化 TD-火山引擎（TalkingData 入口）数据...")

	// ---- 1. 供应商（upsert Name/BaseURL/Description/Discount） ----
	var supplier model.Supplier
	err := db.Where("code = ? AND access_type = ?", "talkingdata", "api").First(&supplier).Error
	if err != nil {
		supplier = model.Supplier{
			Name:        "TD-火山引擎",
			Code:        "talkingdata",
			BaseURL:     "https://REPLACE_WITH_TALKINGDATA_BASE_URL/v1",
			Description: "TalkingData（TD云牍/灵犀平台）入口，转发至火山引擎 doubao 系列；OpenAI 兼容，Bearer <APPCODE>",
			IsActive:    true,
			SortOrder:   150,
			AccessType:  "api",
			Discount:    0.9,
			Status:      "active",
		}
		if createErr := db.Create(&supplier).Error; createErr != nil {
			log.Error("seed_talkingdata: 创建供应商失败", zap.Error(createErr))
			return
		}
		log.Info("seed_talkingdata: 创建供应商成功", zap.Uint("id", supplier.ID))
	} else {
		// 已存在 → 更新展示字段（Name/Description/Discount），保留管理员已填的 BaseURL
		updates := map[string]interface{}{
			"name":        "TD-火山引擎",
			"description": "TalkingData（TD云牍/灵犀平台）入口，转发至火山引擎 doubao 系列；OpenAI 兼容，Bearer <APPCODE>",
			"discount":    0.9,
		}
		db.Model(&model.Supplier{}).Where("id = ?", supplier.ID).Updates(updates)
		log.Info("seed_talkingdata: 供应商已存在，更新展示字段", zap.Uint("id", supplier.ID))
	}

	// ---- 2. 分类 ----
	type catDef struct {
		Code      string
		Name      string
		Desc      string
		SortOrder int
	}
	catDefs := []catDef{
		{"talkingdata_doubao_chat", "TalkingData-豆包对话", "doubao 系列文本对话模型", 10},
		{"talkingdata_doubao_vision", "TalkingData-豆包视觉", "doubao 系列多模态视觉模型", 20},
		{"talkingdata_doubao_image", "TalkingData-豆包图片生成", "doubao Seedream 图片生成系列", 30},
		{"talkingdata_doubao_video", "TalkingData-豆包视频生成", "doubao Seedance 视频生成系列", 40},
	}
	catIDMap := make(map[string]uint, len(catDefs))
	for _, cd := range catDefs {
		var cat model.ModelCategory
		if catErr := db.Where("code = ?", cd.Code).First(&cat).Error; catErr != nil {
			cat = model.ModelCategory{
				SupplierID:  supplier.ID,
				Name:        cd.Name,
				Code:        cd.Code,
				Description: cd.Desc,
				SortOrder:   cd.SortOrder,
			}
			if createErr := db.Create(&cat).Error; createErr != nil {
				log.Error("seed_talkingdata: 创建分类失败", zap.String("code", cd.Code), zap.Error(createErr))
				continue
			}
		}
		catIDMap[cd.Code] = cat.ID
	}

	// ---- 3. 模型（存在则更新价格/阶梯/缓存/折扣，不存在则创建） ----
	createdCount := 0
	updatedCount := 0
	for _, md := range talkingDataModelDefs() {
		catID, ok := catIDMap[md.CategoryCode]
		if !ok {
			log.Warn("seed_talkingdata: 分类未找到，跳过模型", zap.String("model", md.ModelName), zap.String("cat", md.CategoryCode))
			continue
		}

		// 序列化阶梯价格
		var priceTiersJSON model.JSON
		if len(md.PriceTiers) > 0 {
			tiers := make([]model.PriceTier, len(md.PriceTiers))
			copy(tiers, md.PriceTiers)
			for i := range tiers {
				tiers[i].Normalize()
			}
			model.SortTiers(tiers)
			wrapped := model.PriceTiersData{
				Tiers:     tiers,
				Currency:  "CNY",
				UnitLabel: "元/百万token",
				UpdatedAt: time.Now(),
				SourceURL: "模型成本方案 (1.1).xlsx",
			}
			if b, err := json.Marshal(wrapped); err == nil {
				priceTiersJSON = model.JSON(b)
			}
		}

		// 序列化 features
		var featuresJSON model.JSON
		if md.Features != nil {
			if b, err := json.Marshal(md.Features); err == nil {
				featuresJSON = model.JSON(b)
			}
		}

		// 存在 → UPDATE 价格相关字段（保留管理员已填的 description/max_tokens/context_window 等）
		var existing model.AIModel
		if db.Where("model_name = ? AND supplier_id = ?", md.ModelName, supplier.ID).First(&existing).Error == nil {
			priceUpdates := map[string]interface{}{
				"display_name":            md.DisplayName,
				"input_cost_rmb":          md.InputCostRMB,
				"output_cost_rmb":         md.OutputCostRMB,
				"input_price_per_token":   int64(md.InputCostRMB * 10000),
				"output_price_per_token":  int64(md.OutputCostRMB * 10000),
				"pricing_unit":            md.PricingUnit,
				"model_type":              md.ModelType,
				"variant":                 md.Variant,
				"supports_cache":          md.SupportsCache,
				"cache_mechanism":         md.CacheMechanism,
				"cache_min_tokens":        md.CacheMinTokens,
				"cache_input_price_rmb":   md.CacheInputPriceRMB,
				"cache_storage_price_rmb": md.CacheStoragePriceRMB,
				"discount":                md.Discount,
				"tags":                    md.Tags,
			}
			if priceTiersJSON != nil {
				priceUpdates["price_tiers"] = priceTiersJSON
			}
			if featuresJSON != nil {
				priceUpdates["features"] = featuresJSON
			}
			if err := db.Model(&model.AIModel{}).Where("id = ?", existing.ID).Updates(priceUpdates).Error; err != nil {
				log.Error("seed_talkingdata: 更新模型失败", zap.String("model", md.ModelName), zap.Error(err))
				continue
			}
			updatedCount++
			continue
		}

		// 不存在 → INSERT
		m := model.AIModel{
			CategoryID:           catID,
			SupplierID:           supplier.ID,
			ModelName:            md.ModelName,
			DisplayName:          md.DisplayName,
			Description:          md.Description,
			IsActive:             true,
			Status:               "offline", // 等待管理员配 APIKey 后手动 Verify
			MaxTokens:            md.MaxTokens,
			ContextWindow:        md.ContextWindow,
			MaxInputTokens:       md.MaxInputTokens,
			MaxOutputTokens:      md.MaxOutputTokens,
			InputCostRMB:         md.InputCostRMB,
			OutputCostRMB:        md.OutputCostRMB,
			InputPricePerToken:   int64(md.InputCostRMB * 10000),
			OutputPricePerToken:  int64(md.OutputCostRMB * 10000),
			Currency:             "CREDIT",
			Source:               "manual",
			ModelType:            md.ModelType,
			PricingUnit:          md.PricingUnit,
			Variant:              md.Variant,
			SupportsCache:        md.SupportsCache,
			CacheMechanism:       md.CacheMechanism,
			CacheMinTokens:       md.CacheMinTokens,
			CacheInputPriceRMB:   md.CacheInputPriceRMB,
			CacheStoragePriceRMB: md.CacheStoragePriceRMB,
			Tags:                 md.Tags,
			Discount:             md.Discount,
			SupplierStatus:       "Active",
		}

		// 阶梯/features 已在上文序列化
		if priceTiersJSON != nil {
			m.PriceTiers = priceTiersJSON
		}
		if featuresJSON != nil {
			m.Features = featuresJSON
		}

		if createErr := db.Create(&m).Error; createErr != nil {
			log.Error("seed_talkingdata: 创建模型失败", zap.String("model", md.ModelName), zap.Error(createErr))
			continue
		}
		createdCount++
	}
	log.Info("seed_talkingdata: 模型同步完成",
		zap.Int("created", createdCount),
		zap.Int("updated", updatedCount))

	// ---- 4. 渠道（不存在则创建；存在则更新展示字段，保留管理员已填的 endpoint/api_key） ----
	var ch model.Channel
	chName := "TD-火山引擎-主渠道"
	if db.Where("name = ? OR name = ?", chName, "TalkingData-主渠道").First(&ch).Error != nil {
		ch = model.Channel{
			Name:                  chName,
			SupplierID:            supplier.ID,
			Type:                  "openai",
			ChannelType:           "CHAT",
			SupportedCapabilities: "chat,image,video",
			Endpoint:              "https://REPLACE_WITH_TALKINGDATA_BASE_URL/v1/chat/completions",
			APIKey:                "",
			ApiProtocol:           "openai_chat",
			AuthMethod:            "bearer",
			Weight:                10,
			Priority:              10,
			Status:                "unverified",
			MaxConcurrency:        100,
			QPM:                   60,
		}
		if createErr := db.Create(&ch).Error; createErr != nil {
			log.Error("seed_talkingdata: 创建渠道失败", zap.Error(createErr))
		} else {
			log.Info("seed_talkingdata: 创建渠道成功（占位 endpoint，待管理员替换）", zap.Uint("id", ch.ID))
		}
	} else {
		// 存在 → 更新 name/supported_capabilities（不覆盖 endpoint/api_key）
		db.Model(&model.Channel{}).Where("id = ?", ch.ID).Updates(map[string]interface{}{
			"name":                   chName,
			"supported_capabilities": "chat,image,video",
		})
		log.Info("seed_talkingdata: 渠道已存在，刷新展示字段", zap.Uint("id", ch.ID))
	}

	log.Info("seed_talkingdata: TD-火山引擎 种子同步完成")
}

// --- 模型定义 ---

type talkingDataModelDef struct {
	CategoryCode    string
	ModelName       string
	DisplayName     string
	Description     string
	ModelType       string
	PricingUnit     string
	Variant         string
	MaxTokens       int
	ContextWindow   int
	MaxInputTokens  int
	MaxOutputTokens int
	// 默认价（阶梯第一档）
	InputCostRMB  float64
	OutputCostRMB float64
	// 阶梯（可选）
	PriceTiers []model.PriceTier
	// 缓存
	SupportsCache        bool
	CacheMechanism       string
	CacheMinTokens       int
	CacheInputPriceRMB   float64
	CacheStoragePriceRMB float64
	// 其他
	Tags     string
	Discount float64
	Features map[string]interface{}
}

// 辅助：构造输入阶梯
// tiers = [[inputMin, inputMax, inputPrice, outputPrice, cacheInPrice], ...]
// inputMax=0 表示 +∞
func buildInputTiers(rows [][5]float64) []model.PriceTier {
	tiers := make([]model.PriceTier, 0, len(rows))
	for _, r := range rows {
		tier := model.PriceTier{
			InputMin:          int64(r[0]),
			InputMinExclusive: r[0] > 0, // 0 时用闭区间 [0, inputMax]；其他用开区间 (min, max]
			InputPrice:        r[2],
			OutputPrice:       r[3],
			CacheInputPrice:   r[4],
		}
		if r[1] > 0 {
			max := int64(r[1])
			tier.InputMax = &max
			tier.InputMaxExclusive = false // 闭区间
		}
		tiers = append(tiers, tier)
	}
	return tiers
}

// talkingDataModelDefs 返回所有 TalkingData 接入的 doubao 模型定义（源：xlsx）
func talkingDataModelDefs() []talkingDataModelDef {
	const cacheStorage = 0.017 // ¥/M/h，文本模型统一

	return []talkingDataModelDef{
		// ============ 文本模型（LLM） ============
		{
			CategoryCode: "talkingdata_doubao_chat", ModelName: "doubao-seed-2.0-pro", DisplayName: "Doubao Seed 2.0 Pro",
			ModelType: model.ModelTypeLLM, PricingUnit: model.UnitPerMillionTokens,
			MaxTokens: 4096, ContextWindow: 256000, MaxInputTokens: 256000, MaxOutputTokens: 4096,
			InputCostRMB: 3.2, OutputCostRMB: 16,
			PriceTiers: buildInputTiers([][5]float64{
				{0, 32000, 3.2, 16, 0.64},
				{32000, 128000, 4.8, 24, 0.96},
				{128000, 256000, 9.6, 48, 1.92},
			}),
			SupportsCache: true, CacheMechanism: "auto", CacheMinTokens: 0,
			CacheInputPriceRMB: 0.64, CacheStoragePriceRMB: cacheStorage,
			Tags: "Doubao,豆包,TalkingData", Discount: 0.9,
		},
		{
			CategoryCode: "talkingdata_doubao_chat", ModelName: "doubao-seed-2.0-lite", DisplayName: "Doubao Seed 2.0 Lite",
			ModelType: model.ModelTypeLLM, PricingUnit: model.UnitPerMillionTokens,
			MaxTokens: 4096, ContextWindow: 256000,
			InputCostRMB: 0.6, OutputCostRMB: 3.6,
			PriceTiers: buildInputTiers([][5]float64{
				{0, 32000, 0.6, 3.6, 0.12},
				{32000, 128000, 0.9, 5.4, 0.18},
				{128000, 256000, 1.8, 10.8, 0.36},
			}),
			SupportsCache: true, CacheMechanism: "auto",
			CacheInputPriceRMB: 0.12, CacheStoragePriceRMB: cacheStorage,
			Tags: "Doubao,豆包,TalkingData", Discount: 0.75,
		},
		{
			CategoryCode: "talkingdata_doubao_chat", ModelName: "doubao-seed-2.0-mini", DisplayName: "Doubao Seed 2.0 Mini",
			ModelType: model.ModelTypeLLM, PricingUnit: model.UnitPerMillionTokens,
			MaxTokens: 4096, ContextWindow: 256000,
			InputCostRMB: 0.2, OutputCostRMB: 2,
			PriceTiers: buildInputTiers([][5]float64{
				{0, 32000, 0.2, 2, 0.04},
				{32000, 128000, 0.4, 4, 0.08},
				{128000, 256000, 0.8, 8, 0.16},
			}),
			SupportsCache: true, CacheMechanism: "auto",
			CacheInputPriceRMB: 0.04, CacheStoragePriceRMB: cacheStorage,
			Tags: "Doubao,豆包,TalkingData", Discount: 0.75,
		},
		{
			CategoryCode: "talkingdata_doubao_chat", ModelName: "doubao-seed-2.0-code", DisplayName: "Doubao Seed 2.0 Code",
			ModelType: model.ModelTypeLLM, PricingUnit: model.UnitPerMillionTokens,
			MaxTokens: 4096, ContextWindow: 256000,
			InputCostRMB: 3.2, OutputCostRMB: 16,
			PriceTiers: buildInputTiers([][5]float64{
				{0, 32000, 3.2, 16, 0.64},
				{32000, 128000, 4.8, 24, 0.96},
				{128000, 256000, 9.6, 48, 1.92},
			}),
			SupportsCache: true, CacheMechanism: "auto",
			CacheInputPriceRMB: 0.64, CacheStoragePriceRMB: cacheStorage,
			Tags: "Doubao,豆包,TalkingData,Coding", Discount: 0.75,
		},
		// 1.8: xlsx 有 input×output 嵌套，按决策取 output 低档（保守计费）
		{
			CategoryCode: "talkingdata_doubao_chat", ModelName: "doubao-seed-1.8", DisplayName: "Doubao Seed 1.8",
			ModelType: model.ModelTypeLLM, PricingUnit: model.UnitPerMillionTokens,
			MaxTokens: 4096, ContextWindow: 256000,
			InputCostRMB: 0.8, OutputCostRMB: 2,
			PriceTiers: buildInputTiers([][5]float64{
				{0, 32000, 0.8, 2, 0.16},
				{32000, 128000, 1.2, 16, 0.16},
				{128000, 256000, 2.4, 24, 0.16},
			}),
			SupportsCache: true, CacheMechanism: "auto",
			CacheInputPriceRMB: 0.16, CacheStoragePriceRMB: cacheStorage,
			Tags: "Doubao,豆包,TalkingData", Discount: 0.9,
		},
		{
			CategoryCode: "talkingdata_doubao_chat", ModelName: "doubao-seed-1.6", DisplayName: "Doubao Seed 1.6",
			ModelType: model.ModelTypeLLM, PricingUnit: model.UnitPerMillionTokens,
			MaxTokens: 4096, ContextWindow: 256000,
			InputCostRMB: 0.8, OutputCostRMB: 2,
			PriceTiers: buildInputTiers([][5]float64{
				{0, 32000, 0.8, 2, 0.16},
				{32000, 128000, 1.2, 16, 0.16},
				{128000, 256000, 2.4, 24, 0.16},
			}),
			SupportsCache: true, CacheMechanism: "auto",
			CacheInputPriceRMB: 0.16, CacheStoragePriceRMB: cacheStorage,
			Tags: "Doubao,豆包,TalkingData", Discount: 0.7,
		},
		{
			CategoryCode: "talkingdata_doubao_chat", ModelName: "doubao-seed-1.6-flash", DisplayName: "Doubao Seed 1.6 Flash",
			ModelType: model.ModelTypeLLM, PricingUnit: model.UnitPerMillionTokens,
			MaxTokens: 4096, ContextWindow: 256000,
			InputCostRMB: 0.15, OutputCostRMB: 1.5,
			PriceTiers: buildInputTiers([][5]float64{
				{0, 32000, 0.15, 1.5, 0.03},
				{32000, 128000, 0.3, 3, 0.03},
				{128000, 256000, 0.6, 6, 0.03},
			}),
			SupportsCache: true, CacheMechanism: "auto",
			CacheInputPriceRMB: 0.03, CacheStoragePriceRMB: cacheStorage,
			Tags: "Doubao,豆包,TalkingData,Fast", Discount: 0.7,
		},
		{
			CategoryCode: "talkingdata_doubao_vision", ModelName: "doubao-seed-1.6-vision", DisplayName: "Doubao Seed 1.6 Vision",
			ModelType: model.ModelTypeVision, PricingUnit: model.UnitPerMillionTokens,
			MaxTokens: 4096, ContextWindow: 256000,
			InputCostRMB: 0.8, OutputCostRMB: 8,
			PriceTiers: buildInputTiers([][5]float64{
				{0, 32000, 0.8, 8, 0.16},
				{32000, 128000, 1.2, 16, 0.16},
				{128000, 256000, 2.4, 24, 0.16},
			}),
			SupportsCache: true, CacheMechanism: "auto",
			CacheInputPriceRMB: 0.16, CacheStoragePriceRMB: cacheStorage,
			Tags: "Doubao,豆包,TalkingData,Vision", Discount: 0.7,
		},
		{
			CategoryCode: "talkingdata_doubao_chat", ModelName: "doubao-seed-1.6-thinking", DisplayName: "Doubao Seed 1.6 Thinking",
			ModelType: model.ModelTypeLLM, PricingUnit: model.UnitPerMillionTokens,
			MaxTokens: 4096, ContextWindow: 256000,
			InputCostRMB: 0.8, OutputCostRMB: 8,
			PriceTiers: buildInputTiers([][5]float64{
				{0, 32000, 0.8, 8, 0.16},
				{32000, 128000, 1.2, 16, 0.16},
				{128000, 256000, 2.4, 24, 0.16},
			}),
			SupportsCache: true, CacheMechanism: "auto",
			CacheInputPriceRMB: 0.16, CacheStoragePriceRMB: cacheStorage,
			Tags:     "Doubao,豆包,TalkingData,Reasoning",
			Discount: 0.7,
			Features: map[string]interface{}{"supports_thinking": true},
		},
		{
			CategoryCode: "talkingdata_doubao_chat", ModelName: "doubao-seed-1.6-lite", DisplayName: "Doubao Seed 1.6 Lite",
			ModelType: model.ModelTypeLLM, PricingUnit: model.UnitPerMillionTokens,
			MaxTokens: 4096, ContextWindow: 256000,
			InputCostRMB: 0.3, OutputCostRMB: 0.6,
			PriceTiers: buildInputTiers([][5]float64{
				{0, 32000, 0.3, 0.6, 0.06},
				{32000, 128000, 0.6, 4, 0.06},
				{128000, 256000, 1.2, 12, 0.06},
			}),
			SupportsCache: true, CacheMechanism: "auto",
			CacheInputPriceRMB: 0.06, CacheStoragePriceRMB: cacheStorage,
			Tags: "Doubao,豆包,TalkingData", Discount: 0.7,
		},
		{
			CategoryCode: "talkingdata_doubao_chat", ModelName: "doubao-1.5-pro-32k", DisplayName: "Doubao 1.5 Pro 32K",
			ModelType: model.ModelTypeLLM, PricingUnit: model.UnitPerMillionTokens,
			MaxTokens: 4096, ContextWindow: 32000,
			InputCostRMB: 0.8, OutputCostRMB: 2,
			SupportsCache: true, CacheMechanism: "auto",
			CacheInputPriceRMB: 0.16, CacheStoragePriceRMB: cacheStorage,
			Tags: "Doubao,豆包,TalkingData", Discount: 0.7,
		},

		// ============ 图片模型（ImageGeneration） ============
		{
			CategoryCode: "talkingdata_doubao_image", ModelName: "doubao-seedream-5.0-lite", DisplayName: "Doubao Seedream 5.0 Lite",
			ModelType: model.ModelTypeImageGeneration, PricingUnit: model.UnitPerImage,
			InputCostRMB: 0.22, OutputCostRMB: 0.22,
			Tags: "Doubao,豆包,TalkingData,ImageGen", Discount: 0.9,
		},
		{
			CategoryCode: "talkingdata_doubao_image", ModelName: "doubao-seedream-5-0-260128", DisplayName: "Doubao Seedream 5.0",
			ModelType: model.ModelTypeImageGeneration, PricingUnit: model.UnitPerImage,
			InputCostRMB: 0.22, OutputCostRMB: 0.22,
			Tags: "Doubao,TalkingData,ImageGen", Discount: 0.9,
		},
		{
			CategoryCode: "talkingdata_doubao_image", ModelName: "doubao-seedream-4.5", DisplayName: "Doubao Seedream 4.5",
			ModelType: model.ModelTypeImageGeneration, PricingUnit: model.UnitPerImage,
			InputCostRMB: 0.25, OutputCostRMB: 0.25,
			Tags: "Doubao,豆包,TalkingData,ImageGen", Discount: 0.9,
		},
		{
			CategoryCode: "talkingdata_doubao_image", ModelName: "doubao-seedream-4.0", DisplayName: "Doubao Seedream 4.0",
			ModelType: model.ModelTypeImageGeneration, PricingUnit: model.UnitPerImage,
			InputCostRMB: 0.20, OutputCostRMB: 0.20,
			Tags: "Doubao,豆包,TalkingData,ImageGen", Discount: 0.8,
		},
		{
			CategoryCode: "talkingdata_doubao_image", ModelName: "doubao-seedream-3.0", DisplayName: "Doubao Seedream 3.0",
			ModelType: model.ModelTypeImageGeneration, PricingUnit: model.UnitPerImage,
			InputCostRMB: 0.259, OutputCostRMB: 0.259,
			Tags: "Doubao,豆包,TalkingData,ImageGen", Discount: 0.2,
		},

		// ============ 视频模型（VideoGeneration） ============
		{
			CategoryCode: "talkingdata_doubao_video", ModelName: "doubao-seedance-2.0-720p", DisplayName: "Doubao Seedance 2.0 (720p)",
			ModelType: model.ModelTypeVideoGeneration, PricingUnit: model.UnitPerMillionTokens,
			Variant:      "720p",
			InputCostRMB: 0, OutputCostRMB: 46,
			PriceTiers: talkingDataSeedance20Tiers("480p/720p", 46, 28),
			Tags:       "Doubao,豆包,TalkingData,VideoGen", Discount: 1.0,
		},
		{
			CategoryCode: "talkingdata_doubao_video", ModelName: "doubao-seedance-2-0-fast-260128", DisplayName: "Doubao Seedance 2.0 Fast",
			ModelType: model.ModelTypeVideoGeneration, PricingUnit: model.UnitPerMillionTokens,
			Variant:      "720p",
			InputCostRMB: 0, OutputCostRMB: 37,
			PriceTiers: talkingDataSeedance20Tiers("", 37, 22),
			Tags:       "Doubao,TalkingData,VideoGen", Discount: 1.0,
		},
		{
			CategoryCode: "talkingdata_doubao_video", ModelName: "doubao-seedance-2.0-1080p", DisplayName: "Doubao Seedance 2.0 (1080p)",
			ModelType: model.ModelTypeVideoGeneration, PricingUnit: model.UnitPerMillionTokens,
			Variant:      "1080p",
			InputCostRMB: 0, OutputCostRMB: 51,
			PriceTiers: talkingDataSeedance20Tiers("1080p", 51, 31),
			Tags:       "Doubao,豆包,TalkingData,VideoGen", Discount: 1.0,
		},
		{
			CategoryCode: "talkingdata_doubao_video", ModelName: "doubao-seedance-1.5-pro", DisplayName: "Doubao Seedance 1.5 Pro",
			ModelType: model.ModelTypeVideoGeneration, PricingUnit: model.UnitPerMillionTokens,
			InputCostRMB: 0, OutputCostRMB: 16,
			PriceTiers: talkingDataSeedance15Tiers(),
			Tags:       "Doubao,豆包,TalkingData,VideoGen", Discount: 0.9,
		},
	}
}

func talkingDataSeedance15Tiers() []model.PriceTier {
	return []model.PriceTier{
		{Name: "在线推理 · 有声视频", InputMin: 0, InputMinExclusive: true, OutputMin: 0, OutputMinExclusive: true, InputPrice: 0, OutputPrice: 16},
		{Name: "在线推理 · 无声视频", InputMin: 1, OutputMin: 0, OutputMinExclusive: true, InputPrice: 0, OutputPrice: 8},
		{Name: "离线推理 · 有声视频", InputMin: 2, OutputMin: 0, OutputMinExclusive: true, InputPrice: 0, OutputPrice: 8},
		{Name: "离线推理 · 无声视频", InputMin: 3, OutputMin: 0, OutputMinExclusive: true, InputPrice: 0, OutputPrice: 4},
	}
}

func talkingDataSeedance20Tiers(resolution string, noVideoPrice, withVideoPrice float64) []model.PriceTier {
	prefix := "在线推理"
	if resolution != "" {
		prefix = resolution + " 在线推理"
	}
	return []model.PriceTier{
		{Name: prefix + " · 输入不含视频", InputMin: 0, InputMinExclusive: true, OutputMin: 0, OutputMinExclusive: true, InputPrice: 0, OutputPrice: noVideoPrice},
		{Name: prefix + " · 输入包含视频", InputMin: 1, OutputMin: 0, OutputMinExclusive: true, InputPrice: 0, OutputPrice: withVideoPrice},
	}
}
