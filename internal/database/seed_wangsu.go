package database

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// WangsuSellMarkup 网宿模型默认售价加价系数
// 售价 = 成本价 × WangsuSellMarkup；管理员可在「定价管理」批量调整
const WangsuSellMarkup = 1.30

// RunSeedWangsu 幂等种子：网宿 AI 网关供应商 + 3 个协议通道（OpenAI/Anthropic/Gemini）+ 模型清单
//
// 设计原则：
//   - 按 code+access_type 检查供应商是否已存在，已存在则跳过供应商/分类/渠道创建
//   - 按 model_name+supplier_id 检查模型，已存在则跳过
//   - 通道 API Key 从环境变量读取：WANGSU_GPT_KEY / WANGSU_CLAUDE_KEY / WANGSU_GEMINI_KEY
//     （env 未设置时：通道仍创建但 APIKey 留空 + status=inactive，管理员可在 UI 手填）
//   - 价格：上游官网 USD 价 × USDCNYSnapshot × 家族折扣 = 成本价 RMB
//
// 调用场景：
//   - RunAllSeeds() 中按序执行
//   - 管理员升级端点 POST /api/v1/admin/system/migrate
//
// 参考文档：
//   - 网宿 AI 网关: https://www.wangsu.com/document/eca/aigateway003
//   - 令牌获取:     https://www.wangsu.com/document/eca/aigateway006
//   - Anthropic 直连: https://www.wangsu.com/document/eca/api-anthropic-direct-mode
func RunSeedWangsu(db *gorm.DB) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}
	if db == nil {
		log.Warn("seed_wangsu: db is nil, skip")
		return
	}

	log.Info("seed_wangsu: 开始导入网宿 AI 网关数据...")

	// ---- 1. 供应商 ----
	var sup model.Supplier
	err := db.Where("code = ? AND access_type = ?", "wangsu_aigw", "api").First(&sup).Error
	if err != nil {
		sup = model.Supplier{
			Name:            "网宿AI网关",
			Code:            "wangsu_aigw",
			BaseURL:         "https://aigateway.edgecloudapp.com",
			Description:     "AuthType: bearer_token / x-api-key / x-goog-api-key | 网宿 AI 网关代理，支持 OpenAI/Anthropic/Gemini 三协议直连",
			IsActive:        true,
			SortOrder:       130,
			AccessType:      "api",
			InputPricePerM:  0, // 价格在模型级别
			OutputPricePerM: 0,
			Discount:        1.0, // 折扣在模型级别（GPT 0.795 / Claude 0.848 / Gemini 0.795）
			Status:          "active",
		}
		if createErr := db.Create(&sup).Error; createErr != nil {
			log.Error("seed_wangsu: 创建供应商失败", zap.Error(createErr))
			return
		}
		log.Info("seed_wangsu: 创建供应商成功", zap.Uint("id", sup.ID))
	} else {
		log.Info("seed_wangsu: 供应商已存在，跳过", zap.Uint("id", sup.ID))
	}

	// ---- 2. 分类（按家族分 3 个）----
	type catDef struct {
		Code, Name, Description string
		SortOrder               int
	}
	catDefs := []catDef{
		{"wangsu_gpt", "网宿-GPT", "OpenAI GPT 系列（经网宿网关代理）", 10},
		{"wangsu_claude", "网宿-Claude", "Anthropic Claude 系列（经网宿网关代理）", 20},
		{"wangsu_gemini", "网宿-Gemini", "Google Gemini 系列（经网宿网关代理）", 30},
	}
	catIDMap := make(map[string]uint, len(catDefs))
	for _, cd := range catDefs {
		var cat model.ModelCategory
		if catErr := db.Where("code = ?", cd.Code).First(&cat).Error; catErr != nil {
			cat = model.ModelCategory{
				SupplierID:  sup.ID,
				Name:        cd.Name,
				Code:        cd.Code,
				Description: cd.Description,
				SortOrder:   cd.SortOrder,
			}
			if createErr := db.Create(&cat).Error; createErr != nil {
				log.Error("seed_wangsu: 创建分类失败",
					zap.String("code", cd.Code), zap.Error(createErr))
				continue
			}
			log.Info("seed_wangsu: 创建分类成功", zap.String("code", cd.Code))
		}
		catIDMap[cd.Code] = cat.ID
	}

	// ---- 3. 三个协议通道 ----
	// API Key 优先从环境变量读取（生产推荐），env 缺失时留空由管理员手填
	gptKey := strings.TrimSpace(os.Getenv("WANGSU_GPT_KEY"))
	claudeKey := strings.TrimSpace(os.Getenv("WANGSU_CLAUDE_KEY"))
	geminiKey := strings.TrimSpace(os.Getenv("WANGSU_GEMINI_KEY"))

	chGPT := seedWangsuChannel(db, log, seedChannelSpec{
		Name:                  "网宿-GPT",
		SupplierID:            sup.ID,
		Type:                  "openai",
		SupportedCapabilities: "chat,image",
		Endpoint:              "https://aigateway.edgecloudapp.com/v1/be98584eecab40826dceef13355d4392/tokenhubhk_gpt",
		APIKey:                gptKey,
		APIProtocol:           "openai_chat",
		APIPath:               "/chat/completions",
		AuthMethod:            "bearer",
		CustomParams:          nil,
		Priority:              100,
		Weight:                1,
	})

	// ⚠️ 2026-04-22 实测结论：网宿三个 /tokenhubhk_{gpt,claude,gemini} 的主入口均为"统一签名模式"
	// 接收 OpenAI 格式请求 + `Authorization: Bearer <token>` 鉴权 + 返回 OpenAI 格式响应。
	// Anthropic 直连（X-Api-Key）和 Gemini 直连（x-goog-api-key + {MODEL} path）是独立的附加路径。
	// 默认 seed 走 OpenAI-compat 主路径以最大化简化与可用性。
	chClaude := seedWangsuChannel(db, log, seedChannelSpec{
		Name:                  "网宿-Claude",
		SupplierID:            sup.ID,
		Type:                  "openai",
		SupportedCapabilities: "chat",
		Endpoint:              "https://aigateway.edgecloudapp.com/v1/be98584eecab40826dceef13355d4392/tokenhubhk_claude",
		APIKey:                claudeKey,
		APIProtocol:           "openai_chat",
		APIPath:               "",
		AuthMethod:            "bearer",
		Priority:              100,
		Weight:                1,
	})

	chGemini := seedWangsuChannel(db, log, seedChannelSpec{
		Name:                  "网宿-Gemini",
		SupplierID:            sup.ID,
		Type:                  "openai",
		SupportedCapabilities: "chat,image",
		Endpoint:              "https://aigateway.edgecloudapp.com/v1/be98584eecab40826dceef13355d4392/tokenhubhk_gemini",
		APIKey:                geminiKey,
		APIProtocol:           "openai_chat",
		APIPath:               "",
		AuthMethod:            "bearer",
		Priority:              100,
		Weight:                1,
	})

	// ---- 4. 模型清单 ----
	familyToCatCode := map[string]string{
		"gpt":    "wangsu_gpt",
		"claude": "wangsu_claude",
		"gemini": "wangsu_gemini",
	}
	familyToChannel := map[string]*model.Channel{
		"gpt":    chGPT,
		"claude": chClaude,
		"gemini": chGemini,
	}

	inserted := 0
	skipped := 0
	for _, m := range wangsuModels {
		catCode, ok := familyToCatCode[m.Family]
		if !ok {
			log.Warn("seed_wangsu: 未知家族，跳过", zap.String("model", m.ModelName), zap.String("family", m.Family))
			continue
		}
		catID, ok := catIDMap[catCode]
		if !ok {
			log.Warn("seed_wangsu: 分类未找到，跳过", zap.String("cat", catCode))
			continue
		}

		// 计算成本价（RMB/百万 tokens）= USD × 汇率 × 折扣
		inputCost := round6(m.InputUSDPerM * USDCNYSnapshot * m.Discount)
		outputCost := round6(m.OutputUSDPerM * USDCNYSnapshot * m.Discount)
		cacheReadCost := round6(m.CacheReadUSDPerM * USDCNYSnapshot * m.Discount)
		cacheWriteCost := round6(m.CacheWriteUSDPerM * USDCNYSnapshot * m.Discount)

		// 幂等检查：模型已存在 → 尝试补齐 model_pricings（若缺失）后跳过
		var existing model.AIModel
		if err := db.Where("supplier_id = ? AND model_name = ?", sup.ID, m.ModelName).First(&existing).Error; err == nil {
			ensureWangsuPricing(db, log, existing.ID, inputCost, outputCost, m.ModelName)
			skipped++
			continue
		}

		features := buildWangsuFeaturesJSON(m)

		ai := model.AIModel{
			CategoryID:                 catID,
			SupplierID:                 sup.ID,
			ModelName:                  m.ModelName,
			DisplayName:                m.DisplayName,
			Description:                fmt.Sprintf("%s - 经网宿网关代理（官方定价 × %.3f 折扣）", m.DisplayName, m.Discount),
			IsActive:                   true,
			Status:                     "online",
			MaxTokens:                  m.MaxOutputTokens,
			ContextWindow:              m.ContextWindow,
			MaxOutputTokens:            m.MaxOutputTokens,
			InputCostRMB:               inputCost,
			OutputCostRMB:              outputCost,
			InputPricePerToken:         int64(math.Round(inputCost * 10000)), // 积分 = RMB × 10000
			OutputPricePerToken:        int64(math.Round(outputCost * 10000)),
			Currency:                   "CREDIT",
			ModelType:                  m.ModelType,
			Source:                     "manual",
			PricingUnit:                "per_million_tokens",
			SupportsCache:              m.SupportsCache,
			CacheMechanism:             pickStr(m.CacheMechanism, "none"),
			CacheMinTokens:             m.CacheMinTokens,
			CacheInputPriceRMB:         cacheReadCost,
			CacheExplicitInputPriceRMB: 0, // 非 both 模式
			CacheWritePriceRMB:         cacheWriteCost,
			Features:                   features,
			Tags:                       m.Tags,
			Discount:                   m.Discount,
			SupplierStatus:             "Active",
		}

		if createErr := db.Create(&ai).Error; createErr != nil {
			log.Error("seed_wangsu: 创建模型失败",
				zap.String("model", m.ModelName), zap.Error(createErr))
			continue
		}
		inserted++

		// 创建对应 ModelPricing（售价 = 成本价 × WangsuSellMarkup）
		now := time.Now()
		sellIn := round6(inputCost * WangsuSellMarkup)
		sellOut := round6(outputCost * WangsuSellMarkup)
		mp := model.ModelPricing{
			ModelID:             ai.ID,
			InputPricePerToken:  int64(math.Round(sellIn * 10000)),
			InputPriceRMB:       sellIn,
			OutputPricePerToken: int64(math.Round(sellOut * 10000)),
			OutputPriceRMB:      sellOut,
			Currency:            "CREDIT",
			EffectiveFrom:       &now,
		}
		if pErr := db.Create(&mp).Error; pErr != nil {
			log.Warn("seed_wangsu: 创建售价失败（非致命）",
				zap.String("model", m.ModelName), zap.Error(pErr))
		}

		// 绑定 channel_models
		ch := familyToChannel[m.Family]
		if ch == nil || ch.ID == 0 {
			continue
		}
		vendorID := m.ModelName
		// Gemini URL 里的 {MODEL} 去掉 "gemini." 前缀（Wangsu Gemini 直连时 path 里的模型名不带前缀）
		if m.Family == "gemini" {
			vendorID = strings.TrimPrefix(m.ModelName, "gemini.")
		}
		cm := model.ChannelModel{
			ChannelID:       ch.ID,
			StandardModelID: m.ModelName,
			VendorModelID:   vendorID,
			IsActive:        true,
			Source:          "manual",
		}
		if chErr := db.Create(&cm).Error; chErr != nil {
			// 可能已存在（幂等），忽略
			log.Debug("seed_wangsu: channel_models 绑定已存在或失败",
				zap.String("model", m.ModelName), zap.Error(chErr))
		}
	}

	log.Info("seed_wangsu: 完成",
		zap.Int("inserted", inserted),
		zap.Int("skipped", skipped),
		zap.Int("total", len(wangsuModels)))

	// 提示 API Key 状态
	missingKeys := []string{}
	if gptKey == "" {
		missingKeys = append(missingKeys, "WANGSU_GPT_KEY")
	}
	if claudeKey == "" {
		missingKeys = append(missingKeys, "WANGSU_CLAUDE_KEY")
	}
	if geminiKey == "" {
		missingKeys = append(missingKeys, "WANGSU_GEMINI_KEY")
	}
	if len(missingKeys) > 0 {
		log.Warn("seed_wangsu: 部分 API Key 环境变量未设置，对应渠道需在管理后台手动配置 Key",
			zap.Strings("missing", missingKeys))
	}
}

// seedChannelSpec 封装渠道 seed 参数，避免长参数列表
type seedChannelSpec struct {
	Name                  string
	SupplierID            uint
	Type                  string
	SupportedCapabilities string
	Endpoint              string
	APIKey                string
	APIProtocol           string
	APIPath               string
	AuthMethod            string
	AuthHeader            string
	CustomParams          []byte // 可为 nil
	Priority              int
	Weight                int
}

// seedWangsuChannel 幂等创建渠道，返回已创建或已存在的渠道引用
func seedWangsuChannel(db *gorm.DB, log *zap.Logger, spec seedChannelSpec) *model.Channel {
	var ch model.Channel
	if err := db.Where("name = ?", spec.Name).First(&ch).Error; err == nil {
		log.Info("seed_wangsu: 渠道已存在，跳过", zap.String("name", spec.Name), zap.Uint("id", ch.ID))
		return &ch
	}

	status := "unverified"
	if spec.APIKey == "" {
		status = "inactive" // 无 Key 时保持未激活
	}

	ch = model.Channel{
		Name:                  spec.Name,
		SupplierID:            spec.SupplierID,
		Type:                  spec.Type,
		ChannelType:           "CHAT",
		SupportedCapabilities: spec.SupportedCapabilities,
		Endpoint:              spec.Endpoint,
		APIKey:                spec.APIKey,
		Weight:                spec.Weight,
		Priority:              spec.Priority,
		Status:                status,
		Verified:              false,
		MaxConcurrency:        100,
		QPM:                   60,
		ApiProtocol:           spec.APIProtocol,
		ApiPath:               spec.APIPath,
		AuthMethod:            spec.AuthMethod,
		AuthHeader:            spec.AuthHeader,
	}
	if len(spec.CustomParams) > 0 {
		ch.CustomParams = spec.CustomParams
	}

	if createErr := db.Create(&ch).Error; createErr != nil {
		log.Error("seed_wangsu: 创建渠道失败", zap.String("name", spec.Name), zap.Error(createErr))
		return nil
	}
	log.Info("seed_wangsu: 创建渠道成功",
		zap.String("name", spec.Name),
		zap.Uint("id", ch.ID),
		zap.String("status", status))
	return &ch
}

// buildWangsuFeaturesJSON 按 WangsuModelCapability 构造 features JSON
func buildWangsuFeaturesJSON(m WangsuModelCapability) model.JSON {
	features := map[string]any{}
	if m.SupportsVision {
		features["supports_vision"] = true
	}
	if m.SupportsFunctionCall {
		features["supports_function_call"] = true
	}
	if m.SupportsJSONMode {
		features["supports_json_mode"] = true
	}
	if m.SupportsThinking {
		features["supports_thinking"] = true
	}
	if m.SupportsWebSearch {
		features["supports_web_search"] = true
	}
	if m.SupportsCache {
		features["supports_cache"] = true
	}
	if m.RequiresStream {
		features["requires_stream"] = true
	}
	if len(features) == 0 {
		return nil
	}
	b, err := json.Marshal(features)
	if err != nil {
		return nil
	}
	return model.JSON(b)
}

// ensureWangsuPricing 为已存在的 Wangsu 模型补齐 model_pricings（若尚未创建）
// 幂等：有任何活跃 pricing 行即跳过
func ensureWangsuPricing(db *gorm.DB, log *zap.Logger, modelID uint, costIn, costOut float64, modelName string) {
	var count int64
	db.Model(&model.ModelPricing{}).Where("model_id = ?", modelID).Count(&count)
	if count > 0 {
		return
	}
	now := time.Now()
	sellIn := round6(costIn * WangsuSellMarkup)
	sellOut := round6(costOut * WangsuSellMarkup)
	mp := model.ModelPricing{
		ModelID:             modelID,
		InputPricePerToken:  int64(math.Round(sellIn * 10000)),
		InputPriceRMB:       sellIn,
		OutputPricePerToken: int64(math.Round(sellOut * 10000)),
		OutputPriceRMB:      sellOut,
		Currency:            "CREDIT",
		EffectiveFrom:       &now,
	}
	if err := db.Create(&mp).Error; err != nil {
		log.Warn("seed_wangsu: 补齐售价失败（非致命）",
			zap.String("model", modelName), zap.Error(err))
		return
	}
	log.Info("seed_wangsu: 补齐售价", zap.String("model", modelName),
		zap.Float64("sell_in", sellIn), zap.Float64("sell_out", sellOut))
}

func round6(v float64) float64 {
	return math.Round(v*1e6) / 1e6
}

func pickStr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
