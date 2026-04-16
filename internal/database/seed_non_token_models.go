package database

import (
	"errors"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// RunSeedNonTokenModels 幂等种子：预置非 Token 计费的 AI 模型
//
// 设计原则：
//   - 使用 FirstOrCreate（按 supplier_code + model_name 查询），不覆盖已存在模型
//   - 如模型已存在但 pricing_unit 为空或 per_million_tokens，则回填正确单位和类型
//   - 分类不存在时自动创建（CategoryCode 由函数内推断）
//   - 供应商不存在（如未初始化或 access_type 不同），则跳过并记录日志
//
// 数据来源：2026-04 各供应商官网定价
func RunSeedNonTokenModels(db *gorm.DB) {
	if db == nil {
		return
	}

	totalCreated := 0
	totalUpdated := 0
	totalSkipped := 0

	for _, def := range nonTokenModelDefs {
		// 查找供应商（仅 api 类型接入点）
		var sup model.Supplier
		if err := db.Where("code = ? AND access_type = ?", def.SupplierCode, "api").First(&sup).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				logger.L.Debug("seed non-token: supplier not found, skip",
					zap.String("supplier", def.SupplierCode),
					zap.String("model", def.ModelName),
				)
				totalSkipped++
				continue
			}
			logger.L.Warn("seed non-token: query supplier failed",
				zap.String("supplier", def.SupplierCode),
				zap.Error(err),
			)
			totalSkipped++
			continue
		}

		// 查找或创建分类（使用 SupplierCode + ModelType 组合作为分类 code）
		cat, err := ensureCategoryForSupplier(db, sup.ID, def.SupplierCode, def.ModelType, def.CategoryDisplayName)
		if err != nil {
			logger.L.Warn("seed non-token: ensure category failed",
				zap.String("supplier", def.SupplierCode),
				zap.String("model", def.ModelName),
				zap.Error(err),
			)
			totalSkipped++
			continue
		}

		// 查询是否已存在此模型
		var existing model.AIModel
		result := db.Where("supplier_id = ? AND model_name = ?", sup.ID, def.ModelName).First(&existing)

		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			// 新建
			aim := model.AIModel{
				CategoryID:    cat.ID,
				SupplierID:    sup.ID,
				ModelName:     def.ModelName,
				DisplayName:   def.DisplayName,
				Description:   def.Description,
				IsActive:      true,
				Status:        "offline", // 默认离线，由管理员手动启用或一键检测
				MaxTokens:     def.MaxTokens,
				ContextWindow: def.ContextWindow,
				InputCostRMB:  def.InputCostRMB,  // 非 Token 单位：此字段代表"元/单位"
				OutputCostRMB: 0,                 // 非 Token 单位下不使用 output 价
				Currency:      "CREDIT",
				Source:        "seed",
				ModelType:     def.ModelType,
				PricingUnit:   def.PricingUnit,
				Variant:       def.Variant,
				Domain:        def.Domain,
			}
			if err := db.Create(&aim).Error; err != nil {
				logger.L.Warn("seed non-token: create model failed",
					zap.String("model", def.ModelName),
					zap.Error(err),
				)
				totalSkipped++
				continue
			}
			totalCreated++
			continue
		}
		if result.Error != nil {
			logger.L.Warn("seed non-token: query model failed",
				zap.String("model", def.ModelName),
				zap.Error(result.Error),
			)
			totalSkipped++
			continue
		}

		// 已存在：仅回填 pricing_unit / model_type / variant（不覆盖管理员已配置的价格）
		updates := map[string]interface{}{}
		if existing.PricingUnit == "" || existing.PricingUnit == model.UnitPerMillionTokens {
			updates["pricing_unit"] = def.PricingUnit
		}
		if existing.ModelType == "" || existing.ModelType == model.ModelTypeLLM {
			updates["model_type"] = def.ModelType
		}
		if existing.Variant == "" && def.Variant != "" {
			updates["variant"] = def.Variant
		}
		// 若价格未配置（InputCostRMB=0），则填入默认价格（管理员可事后手动覆盖）
		if existing.InputCostRMB == 0 && def.InputCostRMB > 0 {
			updates["input_cost_rmb"] = def.InputCostRMB
		}
		if len(updates) > 0 {
			if err := db.Model(&model.AIModel{}).Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
				logger.L.Warn("seed non-token: update model failed",
					zap.String("model", def.ModelName),
					zap.Error(err),
				)
				totalSkipped++
				continue
			}
			totalUpdated++
		}
	}

	logger.L.Info("seed non-token: complete",
		zap.Int("created", totalCreated),
		zap.Int("updated", totalUpdated),
		zap.Int("skipped", totalSkipped),
	)
}

// ensureCategoryForSupplier 查找或创建分类
// CategoryCode 格式：{supplier_code}_{model_type_lower}，如 volcengine_imagegeneration
func ensureCategoryForSupplier(db *gorm.DB, supplierID uint, supplierCode, modelType, displayName string) (*model.ModelCategory, error) {
	code := supplierCode + "_" + lowerModelType(modelType)

	var cat model.ModelCategory
	err := db.Where("code = ?", code).First(&cat).Error
	if err == nil {
		return &cat, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	// 创建
	cat = model.ModelCategory{
		SupplierID: supplierID,
		Name:       displayName,
		Code:       code,
		SortOrder:  100,
	}
	if err := db.Create(&cat).Error; err != nil {
		return nil, err
	}
	return &cat, nil
}

func lowerModelType(t string) string {
	switch t {
	case model.ModelTypeImageGeneration:
		return "image_generation"
	case model.ModelTypeVideoGeneration:
		return "video_generation"
	case model.ModelTypeTTS:
		return "tts"
	case model.ModelTypeASR:
		return "asr"
	case model.ModelTypeEmbedding:
		return "embedding"
	case model.ModelTypeRerank:
		return "rerank"
	case model.ModelTypeVision:
		return "vision"
	default:
		return "llm"
	}
}

// nonTokenModelDef 非 Token 模型定义
type nonTokenModelDef struct {
	SupplierCode        string  // 对应 suppliers.code（如 volcengine / aliyun_dashscope / zhipu / openai）
	CategoryDisplayName string  // 分类展示名（如 "图像生成"）
	ModelName           string  // 模型标识
	DisplayName         string  // 展示名
	Description         string  // 描述
	ModelType           string  // ImageGeneration / VideoGeneration / TTS / ASR / Embedding / Rerank
	PricingUnit         string  // per_image / per_second / per_10k_characters / ...
	InputCostRMB        float64 // 价格（单位由 PricingUnit 决定）
	Variant             string  // 变体/质量档（如 "1024x1024"/"720p"）
	Domain              string  // 模型领域（选填）
	MaxTokens           int     // 对非 Token 模型可填 0
	ContextWindow       int     // 对非 Token 模型可填 0
}

// nonTokenModelDefs 预置约 20 条非 Token 模型（2026-04 官网定价）
var nonTokenModelDefs = []nonTokenModelDef{
	// ── 火山引擎 Seedream 图像生成（元/张）─────────────────────────
	// 注意：doubao-seedream-3-0-t2i-250415 已于 2026-05-11 EOS 下线，替换为 doubao-seedream-5-0-lite-260128
	// 参考：https://www.volcengine.com/docs/82379/1350667（第八批下线公告）
	{
		SupplierCode: "volcengine", CategoryDisplayName: "图像生成",
		ModelName: "doubao-seedream-5-0-lite-260128", DisplayName: "Seedream 5.0 Lite",
		Description: "豆包 Seedream 5.0 Lite 图像生成模型（替代 Seedream 3.0）",
		ModelType:   model.ModelTypeImageGeneration, PricingUnit: model.UnitPerImage,
		InputCostRMB: 0.22, Variant: "1024x1024",
	},
	{
		SupplierCode: "volcengine", CategoryDisplayName: "图像生成",
		ModelName: "doubao-seedream-5-0-lite", DisplayName: "Seedream 5.0 Lite（别名）",
		Description: "豆包 Seedream 5.0 Lite 图像生成模型（无版本日期别名）",
		ModelType:   model.ModelTypeImageGeneration, PricingUnit: model.UnitPerImage,
		InputCostRMB: 0.22, Variant: "1024x1024",
	},
	{
		SupplierCode: "volcengine", CategoryDisplayName: "图像生成",
		ModelName: "doubao-seedream-4-5", DisplayName: "Seedream 4.5",
		Description: "豆包 Seedream 4.5 图像生成模型",
		ModelType:   model.ModelTypeImageGeneration, PricingUnit: model.UnitPerImage,
		InputCostRMB: 0.25, Variant: "1024x1024",
	},
	// ── 火山引擎 Seedance 视频生成（元/百万 Token，走 Token 路径不纳入本种子）─
	// Seedance 按 Token 计费，已由常规 scraper 覆盖，本表不重复。

	// ── 火山引擎 豆包 TTS（元/万字符）──────────────────────────
	{
		SupplierCode: "volcengine", CategoryDisplayName: "语音合成",
		ModelName: "doubao-tts-2-0", DisplayName: "豆包语音合成 2.0",
		Description: "豆包 TTS 2.0，按万字符计费",
		ModelType:   model.ModelTypeTTS, PricingUnit: model.UnitPer10kCharacters,
		InputCostRMB: 2.8,
	},
	{
		SupplierCode: "volcengine", CategoryDisplayName: "语音合成",
		ModelName: "doubao-tts-lm", DisplayName: "大模型语音合成",
		Description: "豆包大模型 TTS，按万字符计费",
		ModelType:   model.ModelTypeTTS, PricingUnit: model.UnitPer10kCharacters,
		InputCostRMB: 4.5,
	},

	// ── 火山引擎 豆包 ASR（元/小时）────────────────────────────
	{
		SupplierCode: "volcengine", CategoryDisplayName: "语音识别",
		ModelName: "doubao-asr-stream-2-0", DisplayName: "豆包流式语音识别 2.0",
		Description: "豆包流式 ASR 2.0，按小时计费",
		ModelType:   model.ModelTypeASR, PricingUnit: model.UnitPerHour,
		InputCostRMB: 0.9,
	},
	{
		SupplierCode: "volcengine", CategoryDisplayName: "语音识别",
		ModelName: "doubao-asr-stream-lm", DisplayName: "大模型流式语音识别",
		Description: "大模型流式 ASR，按小时计费",
		ModelType:   model.ModelTypeASR, PricingUnit: model.UnitPerHour,
		InputCostRMB: 4.0,
	},
	{
		SupplierCode: "volcengine", CategoryDisplayName: "语音识别",
		ModelName: "doubao-asr-file", DisplayName: "录音文件识别（标准版）",
		Description: "录音文件识别标准版，按小时计费",
		ModelType:   model.ModelTypeASR, PricingUnit: model.UnitPerHour,
		InputCostRMB: 2.0,
	},

	// ── 阿里云百炼 wanx 图像生成（元/张）──────────────────────
	{
		SupplierCode: "aliyun_dashscope", CategoryDisplayName: "图像生成",
		ModelName: "wanx-v1", DisplayName: "通义万相 v1",
		Description: "阿里云通义万相文生图 v1",
		ModelType:   model.ModelTypeImageGeneration, PricingUnit: model.UnitPerImage,
		InputCostRMB: 0.16, Variant: "1024x1024",
	},
	// ── 阿里云百炼 wanx-video 视频生成（元/秒）──────────────────
	{
		SupplierCode: "aliyun_dashscope", CategoryDisplayName: "视频生成",
		ModelName: "wanx2-t2v-turbo", DisplayName: "通义万相视频 Turbo",
		Description: "阿里云通义万相文生视频，按秒计费",
		ModelType:   model.ModelTypeVideoGeneration, PricingUnit: model.UnitPerSecond,
		InputCostRMB: 0.24, Variant: "720p",
	},
	// ── 阿里云百炼 qwen-tts（元/百万字符）─────────────────────
	{
		SupplierCode: "aliyun_dashscope", CategoryDisplayName: "语音合成",
		ModelName: "qwen-tts", DisplayName: "通义语音合成",
		Description: "阿里云通义 TTS，按百万字符计费",
		ModelType:   model.ModelTypeTTS, PricingUnit: model.UnitPerMillionCharacters,
		InputCostRMB: 300,
	},
	// ── 阿里云百炼 paraformer ASR（元/小时）────────────────────
	{
		SupplierCode: "aliyun_dashscope", CategoryDisplayName: "语音识别",
		ModelName: "paraformer-v2", DisplayName: "Paraformer v2 语音识别",
		Description: "阿里云 Paraformer 实时语音识别",
		ModelType:   model.ModelTypeASR, PricingUnit: model.UnitPerHour,
		InputCostRMB: 2.52,
	},

	// ── 智谱 CogView 图像生成（元/张）──────────────────────────
	{
		SupplierCode: "zhipu", CategoryDisplayName: "图像生成",
		ModelName: "cogview-4", DisplayName: "CogView-4",
		Description: "智谱 CogView-4 文生图",
		ModelType:   model.ModelTypeImageGeneration, PricingUnit: model.UnitPerImage,
		InputCostRMB: 0.25, Variant: "1024x1024",
	},
	// ── 智谱 CogVideo 视频生成（元/秒）──────────────────────────
	{
		SupplierCode: "zhipu", CategoryDisplayName: "视频生成",
		ModelName: "cogvideox", DisplayName: "CogVideoX",
		Description: "智谱 CogVideoX 文生视频",
		ModelType:   model.ModelTypeVideoGeneration, PricingUnit: model.UnitPerSecond,
		InputCostRMB: 0.50, Variant: "720p",
	},
	// ── 智谱 Rerank（元/次）────────────────────────────────
	{
		SupplierCode: "zhipu", CategoryDisplayName: "Rerank",
		ModelName: "glm-rerank", DisplayName: "GLM Rerank",
		Description: "智谱 GLM 重排序，按次计费",
		ModelType:   model.ModelTypeRerank, PricingUnit: model.UnitPerCall,
		InputCostRMB: 0.0001,
	},

	// ── OpenAI dall-e-3 图像生成（元/张，按官网价约 0.29 元/张 1024x1024 standard）
	{
		SupplierCode: "openai", CategoryDisplayName: "图像生成",
		ModelName: "dall-e-3", DisplayName: "DALL-E 3",
		Description: "OpenAI DALL-E 3 图像生成",
		ModelType:   model.ModelTypeImageGeneration, PricingUnit: model.UnitPerImage,
		InputCostRMB: 0.29, Variant: "1024x1024",
	},
	{
		SupplierCode: "openai", CategoryDisplayName: "图像生成",
		ModelName: "gpt-image-1", DisplayName: "GPT Image 1",
		Description: "OpenAI GPT Image 1 图像生成",
		ModelType:   model.ModelTypeImageGeneration, PricingUnit: model.UnitPerImage,
		InputCostRMB: 0.29, Variant: "1024x1024",
	},
	// ── OpenAI TTS（元/百万字符，约 $15/1M chars ≈ 108 元）
	{
		SupplierCode: "openai", CategoryDisplayName: "语音合成",
		ModelName: "tts-1", DisplayName: "OpenAI TTS-1",
		Description: "OpenAI 基础 TTS，按百万字符计费",
		ModelType:   model.ModelTypeTTS, PricingUnit: model.UnitPerMillionCharacters,
		InputCostRMB: 108,
	},
	{
		SupplierCode: "openai", CategoryDisplayName: "语音合成",
		ModelName: "tts-1-hd", DisplayName: "OpenAI TTS-1 HD",
		Description: "OpenAI 高清 TTS，按百万字符计费",
		ModelType:   model.ModelTypeTTS, PricingUnit: model.UnitPerMillionCharacters,
		InputCostRMB: 216,
	},
	// ── OpenAI Whisper ASR（元/分钟，$0.006/min ≈ 0.043 元）
	{
		SupplierCode: "openai", CategoryDisplayName: "语音识别",
		ModelName: "whisper-1", DisplayName: "OpenAI Whisper",
		Description: "OpenAI Whisper 语音识别，按分钟计费",
		ModelType:   model.ModelTypeASR, PricingUnit: model.UnitPerMinute,
		InputCostRMB: 0.043,
	},
}
