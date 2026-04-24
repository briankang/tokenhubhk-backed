package database

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

const (
	wangsuVideoSupplierCode = "wangsu_aigw"
	wangsuVideoChannelName  = "Wangsu AI Gateway - Video"
	wangsuVideoEndpoint     = "https://aigateway.edgecloudapp.com/v1/be98584eecab40826dceef13355d4392/coze-gpt-video"
	wangsuVideoAPIKey       = "0637ea0ccdd84e4c98d95eaa17a2030f"
	wangsuVideoDiscount     = 0.80
)

type wangsuVideoModelDef struct {
	ModelName      string
	DisplayName    string
	OfficialUSD    float64
	PricingUnit    string
	DefaultSeconds int
	Variant        string
	Description    string
	SourceURL      string
}

var wangsuVideoModels = []wangsuVideoModelDef{
	{
		ModelName:      "sora-2",
		DisplayName:    "Sora 2",
		OfficialUSD:    0.10,
		PricingUnit:    model.UnitPerSecond,
		DefaultSeconds: 8,
		Variant:        "720p",
		Description:    "OpenAI Sora 2 video generation via Wangsu AI Gateway. Cost is official API price x 0.8; selling price equals official API price.",
		SourceURL:      "https://openai.com/api/pricing/",
	},
	{
		ModelName:      "MiniMax-Hailuo-02",
		DisplayName:    "MiniMax Hailuo 02",
		OfficialUSD:    0.28 / 6,
		PricingUnit:    model.UnitPerSecond,
		DefaultSeconds: 6,
		Variant:        "768p-6s",
		Description:    "MiniMax Hailuo 02 video generation via Wangsu AI Gateway. Default price uses official 768p/6s pay-as-you-go price converted to per-second billing.",
		SourceURL:      "https://platform.minimax.io/docs/guides/pricing-paygo",
	},
	{
		ModelName:      "veo-3.1-fast-generate-preview",
		DisplayName:    "Veo 3.1 Fast Generate Preview",
		OfficialUSD:    0.15,
		PricingUnit:    model.UnitPerSecond,
		DefaultSeconds: 8,
		Variant:        "fast-audio",
		Description:    "Google Veo 3.1 Fast video generation via Wangsu AI Gateway. Cost is official API price x 0.8; selling price equals official API price.",
		SourceURL:      "https://ai.google.dev/gemini-api/docs/pricing",
	},
	{
		ModelName:      "veo-3.1-generate-preview",
		DisplayName:    "Veo 3.1 Generate Preview",
		OfficialUSD:    0.40,
		PricingUnit:    model.UnitPerSecond,
		DefaultSeconds: 8,
		Variant:        "standard-audio",
		Description:    "Google Veo 3.1 standard video generation via Wangsu AI Gateway. Cost is official API price x 0.8; selling price equals official API price.",
		SourceURL:      "https://ai.google.dev/gemini-api/docs/pricing",
	},
	{
		ModelName:      "viduq3-pro",
		DisplayName:    "Vidu Q3 Pro",
		OfficialUSD:    0.125,
		PricingUnit:    model.UnitPerSecond,
		DefaultSeconds: 8,
		Variant:        "720p",
		Description:    "Vidu Q3 Pro video generation via Wangsu AI Gateway. Default price uses official 720p general video price; cost is official API price x 0.8 and selling price equals official API price.",
		SourceURL:      "https://platform.vidu.com/docs/pricing/",
	},
}

// RunSeedWangsuVideo upserts the Wangsu AI Gateway video channel and the video models shown in the gateway screenshot.
func RunSeedWangsuVideo(db *gorm.DB) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}
	if db == nil {
		log.Warn("seed_wangsu_video: db is nil, skip")
		return
	}

	sup, err := ensureWangsuVideoSupplier(db)
	if err != nil {
		log.Warn("seed_wangsu_video: supplier unavailable", zap.Error(err))
		return
	}
	cat, err := ensureWangsuVideoCategory(db, sup.ID)
	if err != nil {
		log.Warn("seed_wangsu_video: category unavailable", zap.Error(err))
		return
	}
	ch, err := ensureWangsuVideoChannel(db, sup.ID)
	if err != nil {
		log.Warn("seed_wangsu_video: channel unavailable", zap.Error(err))
		return
	}

	created, updated := 0, 0
	for _, def := range wangsuVideoModels {
		ai, wasCreated, err := upsertWangsuVideoModel(db, cat.ID, sup.ID, def)
		if err != nil {
			log.Warn("seed_wangsu_video: model upsert failed", zap.String("model", def.ModelName), zap.Error(err))
			continue
		}
		if wasCreated {
			created++
		} else {
			updated++
		}
		if err := upsertWangsuVideoPricing(db, ai.ID, def); err != nil {
			log.Warn("seed_wangsu_video: pricing upsert failed", zap.String("model", def.ModelName), zap.Error(err))
		}
		if err := upsertWangsuVideoChannelModel(db, ch.ID, def.ModelName); err != nil {
			log.Warn("seed_wangsu_video: channel model upsert failed", zap.String("model", def.ModelName), zap.Error(err))
		}
	}

	log.Info("seed_wangsu_video: complete", zap.Int("created", created), zap.Int("updated", updated), zap.Int("total", len(wangsuVideoModels)))
}

func ensureWangsuVideoSupplier(db *gorm.DB) (*model.Supplier, error) {
	var sup model.Supplier
	err := db.Where("code = ? AND access_type = ?", wangsuVideoSupplierCode, "api").First(&sup).Error
	if err == nil {
		updates := map[string]interface{}{
			"name":        "Wangsu AI Gateway",
			"base_url":    "https://aigateway.edgecloudapp.com",
			"description": "Wangsu AI Gateway API access for chat, image, and video models.",
			"is_active":   true,
			"status":      "active",
		}
		_ = db.Model(&model.Supplier{}).Where("id = ?", sup.ID).Updates(updates).Error
		return &sup, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	sup = model.Supplier{
		Name:            "Wangsu AI Gateway",
		Code:            wangsuVideoSupplierCode,
		BaseURL:         "https://aigateway.edgecloudapp.com",
		Description:     "Wangsu AI Gateway API access for chat, image, and video models.",
		IsActive:        true,
		SortOrder:       130,
		AccessType:      "api",
		InputPricePerM:  0,
		OutputPricePerM: 0,
		Discount:        1.0,
		Status:          "active",
	}
	if err := db.Create(&sup).Error; err != nil {
		return nil, err
	}
	return &sup, nil
}

func ensureWangsuVideoCategory(db *gorm.DB, supplierID uint) (*model.ModelCategory, error) {
	var cat model.ModelCategory
	err := db.Where("code = ?", "wangsu_video").First(&cat).Error
	if err == nil {
		_ = db.Model(&model.ModelCategory{}).Where("id = ?", cat.ID).Updates(map[string]interface{}{
			"supplier_id": supplierID,
			"name":        "Wangsu Video",
			"description": "Video generation models routed through Wangsu AI Gateway.",
			"sort_order":  40,
		}).Error
		return &cat, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	cat = model.ModelCategory{
		SupplierID:  supplierID,
		Name:        "Wangsu Video",
		Code:        "wangsu_video",
		Description: "Video generation models routed through Wangsu AI Gateway.",
		SortOrder:   40,
	}
	if err := db.Create(&cat).Error; err != nil {
		return nil, err
	}
	return &cat, nil
}

func ensureWangsuVideoChannel(db *gorm.DB, supplierID uint) (*model.Channel, error) {
	key := strings.TrimSpace(os.Getenv("WANGSU_VIDEO_KEY"))
	if key == "" {
		key = wangsuVideoAPIKey
	}
	var ch model.Channel
	err := db.Where("name = ?", wangsuVideoChannelName).First(&ch).Error
	status := "active"
	verified := true
	if key == "" {
		status = "inactive"
		verified = false
	}
	updates := map[string]interface{}{
		"supplier_id":            supplierID,
		"type":                   "openai",
		"channel_type":           "MIXED",
		"supported_capabilities": "video",
		"endpoint":               wangsuVideoEndpoint,
		"api_key":                key,
		"weight":                 1,
		"priority":               110,
		"status":                 status,
		"verified":               verified,
		"max_concurrency":        20,
		"qpm":                    30,
		"api_protocol":           "wangsu_video",
		"api_path":               "/videos",
		"auth_method":            "bearer",
	}
	if err == nil {
		if err := db.Model(&model.Channel{}).Where("id = ?", ch.ID).Updates(updates).Error; err != nil {
			return nil, err
		}
		return &ch, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	ch = model.Channel{
		Name:                  wangsuVideoChannelName,
		SupplierID:            supplierID,
		Type:                  "openai",
		ChannelType:           "MIXED",
		SupportedCapabilities: "video",
		Endpoint:              wangsuVideoEndpoint,
		APIKey:                key,
		Weight:                1,
		Priority:              110,
		Status:                status,
		Verified:              verified,
		MaxConcurrency:        20,
		QPM:                   30,
		ApiProtocol:           "wangsu_video",
		ApiPath:               "/videos",
		AuthMethod:            "bearer",
	}
	if err := db.Create(&ch).Error; err != nil {
		return nil, err
	}
	return &ch, nil
}

func upsertWangsuVideoModel(db *gorm.DB, categoryID, supplierID uint, def wangsuVideoModelDef) (*model.AIModel, bool, error) {
	officialRMB := round6(def.OfficialUSD * USDCNYSnapshot)
	costRMB := round6(officialRMB * wangsuVideoDiscount)
	extra := map[string]interface{}{
		"official_usd":     def.OfficialUSD,
		"official_rmb":     officialRMB,
		"discount":         wangsuVideoDiscount,
		"default_seconds":  def.DefaultSeconds,
		"source_url":       def.SourceURL,
		"gateway_id":       "x75mnyzs",
		"gateway_name":     "coze-gpt-video",
		"gateway_api_path": "/videos",
	}
	extraJSON, _ := json.Marshal(extra)

	var existing model.AIModel
	err := db.Where("supplier_id = ? AND model_name = ?", supplierID, def.ModelName).First(&existing).Error
	updates := map[string]interface{}{
		"category_id":            categoryID,
		"display_name":           def.DisplayName,
		"description":            def.Description,
		"is_active":              true,
		"status":                 "online",
		"max_tokens":             0,
		"context_window":         0,
		"max_output_tokens":      0,
		"input_cost_rmb":         costRMB,
		"output_cost_rmb":        0,
		"input_price_per_token":  int64(math.Round(costRMB * 10000)),
		"output_price_per_token": int64(0),
		"currency":               "CREDIT",
		"source":                 "seed",
		"model_type":             model.ModelTypeVideoGeneration,
		"pricing_unit":           def.PricingUnit,
		"variant":                def.Variant,
		"extra_params":           model.JSON(extraJSON),
		"supports_cache":         false,
		"cache_mechanism":        "none",
		"tags":                   "Wangsu,Video,AI Gateway",
		"discount":               wangsuVideoDiscount,
		"supplier_status":        "Active",
	}
	if err == nil {
		if err := db.Model(&model.AIModel{}).Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
			return nil, false, err
		}
		return &existing, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}
	ai := model.AIModel{
		CategoryID:          categoryID,
		SupplierID:          supplierID,
		ModelName:           def.ModelName,
		DisplayName:         def.DisplayName,
		Description:         def.Description,
		IsActive:            true,
		Status:              "online",
		InputCostRMB:        costRMB,
		InputPricePerToken:  int64(math.Round(costRMB * 10000)),
		OutputPricePerToken: 0,
		Currency:            "CREDIT",
		Source:              "seed",
		ModelType:           model.ModelTypeVideoGeneration,
		PricingUnit:         def.PricingUnit,
		Variant:             def.Variant,
		ExtraParams:         model.JSON(extraJSON),
		SupportsCache:       false,
		CacheMechanism:      "none",
		Tags:                "Wangsu,Video,AI Gateway",
		Discount:            wangsuVideoDiscount,
		SupplierStatus:      "Active",
	}
	if err := db.Create(&ai).Error; err != nil {
		return nil, false, err
	}
	return &ai, true, nil
}

func upsertWangsuVideoPricing(db *gorm.DB, modelID uint, def wangsuVideoModelDef) error {
	officialRMB := round6(def.OfficialUSD * USDCNYSnapshot)
	now := time.Now()
	var existing model.ModelPricing
	err := db.Where("model_id = ?", modelID).First(&existing).Error
	updates := map[string]interface{}{
		"input_price_per_token":  int64(math.Round(officialRMB * 10000)),
		"input_price_rmb":        officialRMB,
		"output_price_per_token": int64(0),
		"output_price_rmb":       0,
		"currency":               "CREDIT",
		"effective_from":         &now,
	}
	if err == nil {
		return db.Model(&model.ModelPricing{}).Where("id = ?", existing.ID).Updates(updates).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	mp := model.ModelPricing{
		ModelID:             modelID,
		InputPricePerToken:  int64(math.Round(officialRMB * 10000)),
		InputPriceRMB:       officialRMB,
		OutputPricePerToken: 0,
		OutputPriceRMB:      0,
		Currency:            "CREDIT",
		EffectiveFrom:       &now,
	}
	return db.Create(&mp).Error
}

func upsertWangsuVideoChannelModel(db *gorm.DB, channelID uint, modelName string) error {
	var existing model.ChannelModel
	err := db.Where("channel_id = ? AND vendor_model_id = ?", channelID, modelName).First(&existing).Error
	updates := map[string]interface{}{
		"standard_model_id": modelName,
		"is_active":         true,
		"source":            "manual",
	}
	if err == nil {
		return db.Model(&model.ChannelModel{}).Where("id = ?", existing.ID).Updates(updates).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return db.Create(&model.ChannelModel{
		ChannelID:       channelID,
		StandardModelID: modelName,
		VendorModelID:   modelName,
		IsActive:        true,
		Source:          "manual",
	}).Error
}
