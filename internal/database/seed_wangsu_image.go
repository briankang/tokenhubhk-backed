package database

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
	"tokenhub-server/internal/pkg/logger"
)

const (
	wangsuImageGatewayEndpoint = "https://aigateway.edgecloudapp.com/v1/be98584eecab40826dceef13355d4392/coze-gpt-image/images/generations"
	wangsuImageGatewayID       = "rg66wsl2"
	wangsuImageGatewayName     = "coze-gpt-image"
	wangsuImageDefaultKey      = "64250d1bdd0a484dadba120b0ab70c9c"
	wangsuImageCostDiscount    = 0.80
)

type wangsuImageModelDef struct {
	ModelName       string
	DisplayName     string
	OfficialUSD     float64
	Variant         string
	OfficialSource  string
	PricingNote     string
	ContextWindow   int
	MaxOutputTokens int
}

var wangsuImageModels = []wangsuImageModelDef{
	{
		ModelName:       "gpt-image-2",
		DisplayName:     "GPT Image 2",
		OfficialUSD:     0.03168,
		Variant:         "1024x1024 medium",
		OfficialSource:  "https://openai.com/api/pricing/ + https://platform.openai.com/docs/guides/image-generation/",
		PricingNote:     "OpenAI token price: image output $30/1M tokens; 1024x1024 medium estimate 1056 output image tokens.",
		MaxOutputTokens: 1056,
	},
	{
		ModelName:       "dall-e-3",
		DisplayName:     "DALL-E 3",
		OfficialUSD:     0.04,
		Variant:         "1024x1024 standard",
		OfficialSource:  "https://developers.openai.com/api/docs/models/dall-e-3",
		PricingNote:     "OpenAI official per-image price for standard 1024x1024 generation.",
		MaxOutputTokens: 1,
	},
	{
		ModelName:       "flux.1-schnell",
		DisplayName:     "FLUX.1 [schnell]",
		OfficialUSD:     0.002831,
		Variant:         "1024x1024 (~1.05MP)",
		OfficialSource:  "https://www.together.ai/pricing",
		PricingNote:     "Together AI official price is $0.0027/MP; 1024x1024 is about 1.048576MP.",
		MaxOutputTokens: 1,
	},
	{
		ModelName:       "gpt-image-1.5",
		DisplayName:     "GPT Image 1.5",
		OfficialUSD:     0.034,
		Variant:         "1024x1024 medium",
		OfficialSource:  "https://developers.openai.com/api/docs/models/gpt-image-1.5",
		PricingNote:     "OpenAI official per-image price for medium 1024x1024 generation.",
		MaxOutputTokens: 1,
	},
}

// RunSeedWangsuImageGateway 幂等写入网宿 AI 网关图片生成通道与模型。
//
// 规格片段：
//   - POST /v1/images/generations
//   - Auth: Authorization: Bearer <WANGSU_IMAGE_KEY>
//   - Endpoint: 网宿 AI 网关 coze-gpt-image 完整 images/generations URL
//   - Models: gpt-image-2 / dall-e-3 / flux.1-schnell / gpt-image-1.5
//   - Cost: 官方价 * 0.8；Sale: 官方价
func RunSeedWangsuImageGateway(db *gorm.DB) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}
	if db == nil {
		log.Warn("seed_wangsu_image: db is nil, skip")
		return
	}

	sup, err := ensureWangsuImageSupplier(db)
	if err != nil {
		log.Warn("seed_wangsu_image: ensure supplier failed", zap.Error(err))
		return
	}
	cat, err := ensureWangsuImageCategory(db, sup.ID)
	if err != nil {
		log.Warn("seed_wangsu_image: ensure category failed", zap.Error(err))
		return
	}
	ch, err := ensureWangsuImageChannel(db, sup.ID)
	if err != nil {
		log.Warn("seed_wangsu_image: ensure channel failed", zap.Error(err))
		return
	}

	created := 0
	updated := 0
	for _, def := range wangsuImageModels {
		ai, wasCreated, err := upsertWangsuImageModel(db, sup.ID, cat.ID, def)
		if err != nil {
			log.Warn("seed_wangsu_image: upsert model failed",
				zap.String("model", def.ModelName), zap.Error(err))
			continue
		}
		if wasCreated {
			created++
		} else {
			updated++
		}
		if err := upsertWangsuImagePricing(db, ai.ID, def); err != nil {
			log.Warn("seed_wangsu_image: upsert pricing failed",
				zap.String("model", def.ModelName), zap.Error(err))
		}
		if err := upsertWangsuImageChannelModel(db, ch.ID, def.ModelName); err != nil {
			log.Warn("seed_wangsu_image: upsert channel model failed",
				zap.String("model", def.ModelName), zap.Error(err))
		}
	}

	log.Info("seed_wangsu_image: complete",
		zap.Int("created", created),
		zap.Int("updated", updated),
		zap.Int("total", len(wangsuImageModels)))
}

func ensureWangsuImageSupplier(db *gorm.DB) (*model.Supplier, error) {
	var sup model.Supplier
	err := db.Where("code = ? AND access_type = ?", "wangsu_aigw", "api").First(&sup).Error
	if err == nil {
		return &sup, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	sup = model.Supplier{
		Name:            "网宿AI网关",
		Code:            "wangsu_aigw",
		BaseURL:         "https://aigateway.edgecloudapp.com",
		Description:     "网宿 AI 网关统一签名模式，支持 OpenAI 兼容图片生成接口。",
		IsActive:        true,
		SortOrder:       130,
		AccessType:      "api",
		Discount:        1.0,
		Status:          "active",
		InputPricePerM:  0,
		OutputPricePerM: 0,
	}
	if err := db.Create(&sup).Error; err != nil {
		return nil, err
	}
	return &sup, nil
}

func ensureWangsuImageCategory(db *gorm.DB, supplierID uint) (*model.ModelCategory, error) {
	var cat model.ModelCategory
	err := db.Where("code = ?", "wangsu_image").First(&cat).Error
	if err == nil {
		return &cat, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	cat = model.ModelCategory{
		SupplierID:  supplierID,
		Name:        "网宿-图片生成",
		Code:        "wangsu_image",
		Description: "网宿 AI 网关图片生成模型",
		SortOrder:   40,
	}
	if err := db.Create(&cat).Error; err != nil {
		return nil, err
	}
	return &cat, nil
}

func ensureWangsuImageChannel(db *gorm.DB, supplierID uint) (*model.Channel, error) {
	apiKey := strings.TrimSpace(os.Getenv("WANGSU_IMAGE_KEY"))
	if apiKey == "" {
		apiKey = wangsuImageDefaultKey
	}

	var ch model.Channel
	err := db.Where("name = ?", "网宿-图片生成").First(&ch).Error
	if err == nil {
		updates := map[string]any{
			"supplier_id":            supplierID,
			"type":                   "openai",
			"channel_type":           "MIXED",
			"supported_capabilities": "image",
			"endpoint":               wangsuImageGatewayEndpoint,
			"api_protocol":           "openai_images",
			"api_path":               "/images/generations",
			"auth_method":            "bearer",
			"status":                 "active",
			"verified":               true,
			"max_concurrency":        100,
			"qpm":                    60,
		}
		if strings.TrimSpace(ch.APIKey) == "" || ch.APIKey != apiKey {
			updates["api_key"] = apiKey
		}
		if err := db.Model(&model.Channel{}).Where("id = ?", ch.ID).Updates(updates).Error; err != nil {
			return nil, err
		}
		if err := db.First(&ch, ch.ID).Error; err != nil {
			return nil, err
		}
		return &ch, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	ch = model.Channel{
		Name:                  "网宿-图片生成",
		SupplierID:            supplierID,
		Type:                  "openai",
		ChannelType:           "MIXED",
		SupportedCapabilities: "image",
		Endpoint:              wangsuImageGatewayEndpoint,
		APIKey:                apiKey,
		Weight:                1,
		Priority:              100,
		Status:                "active",
		Verified:              true,
		MaxConcurrency:        100,
		QPM:                   60,
		ApiProtocol:           "openai_images",
		ApiPath:               "/images/generations",
		AuthMethod:            "bearer",
	}
	if err := db.Create(&ch).Error; err != nil {
		return nil, err
	}
	return &ch, nil
}

func upsertWangsuImageModel(db *gorm.DB, supplierID, categoryID uint, def wangsuImageModelDef) (*model.AIModel, bool, error) {
	officialRMB := round6(def.OfficialUSD * USDCNYSnapshot)
	costRMB := round6(officialRMB * wangsuImageCostDiscount)
	desc := fmt.Sprintf("%s via 网宿 AI 网关（网关ID %s，通道 %s）。官网价 $%.6f/张（%s），成本按 8 折=%.6f 元/张，售价与官网价一致=%.6f 元/张。来源：%s",
		def.DisplayName, wangsuImageGatewayID, wangsuImageGatewayName, def.OfficialUSD, def.Variant, costRMB, officialRMB, def.OfficialSource)
	if def.PricingNote != "" {
		desc += "；" + def.PricingNote
	}

	var ai model.AIModel
	err := db.Where("supplier_id = ? AND model_name = ?", supplierID, def.ModelName).First(&ai).Error
	if err == nil {
		updates := map[string]any{
			"category_id":            categoryID,
			"display_name":           def.DisplayName,
			"description":            desc,
			"is_active":              true,
			"status":                 "online",
			"input_cost_rmb":         costRMB,
			"output_cost_rmb":        0,
			"input_price_per_token":  credits.RMBToCredits(costRMB),
			"output_price_per_token": int64(0),
			"currency":               "CREDIT",
			"source":                 "manual",
			"model_type":             model.ModelTypeImageGeneration,
			"pricing_unit":           model.UnitPerImage,
			"variant":                def.Variant,
			"domain":                 "image",
			"max_tokens":             def.MaxOutputTokens,
			"max_output_tokens":      def.MaxOutputTokens,
			"context_window":         def.ContextWindow,
			"discount":               wangsuImageCostDiscount,
			"supplier_status":        "Active",
			"tags":                   "Wangsu,ImageGeneration",
		}
		if err := db.Model(&model.AIModel{}).Where("id = ?", ai.ID).Updates(updates).Error; err != nil {
			return nil, false, err
		}
		if err := db.First(&ai, ai.ID).Error; err != nil {
			return nil, false, err
		}
		return &ai, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}

	ai = model.AIModel{
		CategoryID:          categoryID,
		SupplierID:          supplierID,
		ModelName:           def.ModelName,
		DisplayName:         def.DisplayName,
		Description:         desc,
		IsActive:            true,
		Status:              "online",
		MaxTokens:           def.MaxOutputTokens,
		ContextWindow:       def.ContextWindow,
		MaxOutputTokens:     def.MaxOutputTokens,
		InputCostRMB:        costRMB,
		OutputCostRMB:       0,
		InputPricePerToken:  credits.RMBToCredits(costRMB),
		OutputPricePerToken: 0,
		Currency:            "CREDIT",
		Source:              "manual",
		ModelType:           model.ModelTypeImageGeneration,
		PricingUnit:         model.UnitPerImage,
		Variant:             def.Variant,
		Domain:              "image",
		Tags:                "Wangsu,ImageGeneration",
		Discount:            wangsuImageCostDiscount,
		SupplierStatus:      "Active",
	}
	if err := db.Create(&ai).Error; err != nil {
		return nil, false, err
	}
	return &ai, true, nil
}

func upsertWangsuImagePricing(db *gorm.DB, modelID uint, def wangsuImageModelDef) error {
	sellRMB := round6(def.OfficialUSD * USDCNYSnapshot)
	now := time.Now()
	updates := map[string]any{
		"input_price_per_token":  credits.RMBToCredits(sellRMB),
		"input_price_rmb":        sellRMB,
		"output_price_per_token": int64(0),
		"output_price_rmb":       float64(0),
		"currency":               "CREDIT",
		"effective_from":         &now,
	}

	var pricing model.ModelPricing
	err := db.Where("model_id = ?", modelID).First(&pricing).Error
	if err == nil {
		return db.Model(&model.ModelPricing{}).Where("id = ?", pricing.ID).Updates(updates).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	pricing = model.ModelPricing{
		ModelID:             modelID,
		InputPricePerToken:  credits.RMBToCredits(sellRMB),
		InputPriceRMB:       sellRMB,
		OutputPricePerToken: 0,
		OutputPriceRMB:      0,
		Currency:            "CREDIT",
		EffectiveFrom:       &now,
	}
	return db.Create(&pricing).Error
}

func upsertWangsuImageChannelModel(db *gorm.DB, channelID uint, modelName string) error {
	var cm model.ChannelModel
	err := db.Where("channel_id = ? AND vendor_model_id = ?", channelID, modelName).First(&cm).Error
	if err == nil {
		return db.Model(&model.ChannelModel{}).Where("id = ?", cm.ID).Updates(map[string]any{
			"standard_model_id": modelName,
			"is_active":         true,
			"source":            "manual",
		}).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	cm = model.ChannelModel{
		ChannelID:       channelID,
		StandardModelID: modelName,
		VendorModelID:   modelName,
		IsActive:        true,
		Source:          "manual",
	}
	return db.Create(&cm).Error
}
