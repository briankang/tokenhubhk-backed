package admin

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/database"
	"tokenhub-server/internal/middleware"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	aimodelsvc "tokenhub-server/internal/service/aimodel"
	"tokenhub-server/internal/service/modeldiscovery"
	"tokenhub-server/internal/service/pricescraper"
	"tokenhub-server/internal/taskqueue"
)

// invalidatePublicModelsCache 清除公开模型列表缓存，使管理员操作立即对用户可见
func invalidatePublicModelsCache() {
	middleware.CacheInvalidate("cache:/api/v1/public/models*")
}

// AIModelHandler AI模型管理接口处理器
type AIModelHandler struct {
	svc    *aimodelsvc.AIModelService
	bridge *taskqueue.SSEBridge // nil=单体模式，非nil=委派模式
}

// NewAIModelHandler 创建AI模型管理Handler实例
func NewAIModelHandler(svc *aimodelsvc.AIModelService, bridge ...*taskqueue.SSEBridge) *AIModelHandler {
	if svc == nil {
		panic("admin ai model handler: service is nil")
	}
	h := &AIModelHandler{svc: svc}
	if len(bridge) > 0 {
		h.bridge = bridge[0]
	}
	return h
}

// LabelDTO 模型标签 k:v 数据传输对象（公开 API 使用）
// LabelDTO 标签 DTO（v3.5 扩展：包含字典表返回的 name/color/icon 元数据）
// 前端直接用服务端返回的 name 渲染（已按当前 locale 选字段），不再硬编码任何语言
type LabelDTO struct {
	Key      string `json:"key"`
	Value    string `json:"value,omitempty"`
	Name     string `json:"name,omitempty"`     // 按 Accept-Language 返回的展示名（如 zh="热卖"/en="Hot"）
	Color    string `json:"color,omitempty"`    // Tailwind 色板值（red/amber/emerald/blue/purple/gray）
	Icon     string `json:"icon,omitempty"`     // Lucide 图标名（flame/sparkles/gift/alert-circle 等）
	Category string `json:"category,omitempty"` // user / pricing / brand / system
	Priority int    `json:"priority,omitempty"` // 排序权重（热卖 100 > 优惠 80 > ...）
}

// parseLocaleForLabels 从请求头提取细粒度 locale（保留 zh-CN/zh-TW/en/ja/...）
// 与 middleware/i18n.go 的 normalizeLang 不同，该函数保留子语言标签用于字典表列选择。
func parseLocaleForLabels(acceptLang string) string {
	if acceptLang == "" {
		return "en"
	}
	// 取第一个 tag，剥离 q 值
	parts := strings.Split(acceptLang, ",")
	if len(parts) == 0 {
		return "en"
	}
	tag := strings.TrimSpace(parts[0])
	if idx := strings.Index(tag, ";"); idx > 0 {
		tag = tag[:idx]
	}
	// 规范化大小写：zh-CN / zh-TW 保留连字符后部，其他转小写
	lower := strings.ToLower(tag)
	switch {
	case strings.HasPrefix(lower, "zh-tw"), strings.HasPrefix(lower, "zh-hant"):
		return "zh-TW"
	case strings.HasPrefix(lower, "zh-cn"), strings.HasPrefix(lower, "zh-hans"), lower == "zh":
		return "zh-CN"
	case strings.HasPrefix(lower, "en"):
		return "en"
	default:
		// 其他语言取前 2 字母（ja/ko/es/fr/de/ru/ar/pt/vi/th/id/hi/it/nl/pl/tr/ms/fil/he/fa）
		if len(lower) >= 2 {
			return lower[:2]
		}
		return "en"
	}
}

// PublicModelResponse 公开模型列表响应格式（供前端 /models 页面使用）
// 字段命名遵循前端 AdminModel 类型定义
type PublicModelResponse struct {
	ID                 uint       `json:"id"`                    // 模型 ID
	ModelID            string     `json:"model_id"`              // 模型标识符（如 gpt-4o）
	Name               string     `json:"name"`                  // 展示名称
	Provider           string     `json:"provider"`              // 供应商名称
	ProviderIcon       string     `json:"provider_icon"`         // 供应商图标（emoji）
	Description        string     `json:"description"`           // 模型描述
	ContextWindow      int        `json:"context_window"`        // 上下文窗口大小
	InputPrice         int64      `json:"input_price"`           // 输入价格（积分/百万token）
	OutputPrice        int64      `json:"output_price"`          // 输出价格（积分/百万token）
	InputPriceRMB      float64    `json:"input_price_rmb"`       // 输入价格（人民币/百万token）
	OutputPriceRMB     float64    `json:"output_price_rmb"`      // 输出价格（人民币/百万token）
	Capabilities       []string   `json:"capabilities"`          // 能力标签
	Status             string     `json:"status"`                // 状态：online/offline
	IsNew              bool       `json:"is_new"`                // 是否新品
	IsFeatured         bool       `json:"is_featured"`           // 是否推荐
	MaxTokens          int        `json:"max_tokens"`            // 最大输出 Token 数
	ModelType          string     `json:"model_type"`            // 模型类型: LLM/VLM/ImageGeneration/VideoGeneration/Audio 等
	Tags               string     `json:"tags"`                  // 搜索标签（逗号分隔）
	Labels             []LabelDTO `json:"labels,omitempty"`      // k:v 标签列表（热卖/开源/优惠等）
	Discount           int        `json:"discount,omitempty"`    // 折扣百分比（如85表示85折），0表示无折扣信息
	AvgLatencyMs       int64      `json:"avg_latency_ms,omitempty"`  // 平均延迟（毫秒），最近24小时
	SuccessRate        float64    `json:"success_rate,omitempty"`    // 成功率（0-100），最近24小时
	RequestCount       int64      `json:"request_count,omitempty"`   // 请求量，最近24小时
	// 多计费单位支持（v3.2）
	PricingUnit        string     `json:"pricing_unit,omitempty"`    // 计费单位: per_million_tokens / per_image / per_second / per_minute / per_10k_characters / per_million_characters / per_call / per_hour
	Variant            string     `json:"variant,omitempty"`         // 变体/质量档（如 1024x1024/hd/720p）

	// 缓存定价展示字段（v3.3）
	SupportsCache                 bool    `json:"supports_cache"`                              // 是否支持缓存
	CacheMechanism                string  `json:"cache_mechanism,omitempty"`                   // auto / explicit / both / none
	CacheMinTokens                int     `json:"cache_min_tokens,omitempty"`                  // 触发缓存的最小 token 数
	CacheInputPriceRMB            float64 `json:"cache_input_price_rmb,omitempty"`             // 缓存命中价（隐式/auto）
	CacheExplicitInputPriceRMB    float64 `json:"cache_explicit_input_price_rmb,omitempty"`    // 显式缓存命中价（both 模式）
	CacheWritePriceRMB            float64 `json:"cache_write_price_rmb,omitempty"`             // 显式缓存写入价

	// 阶梯定价展示字段（v3.4）
	PriceTiers          []PublicPriceTier          `json:"price_tiers,omitempty"`          // 阶梯价格列表
	HasTieredPricing    bool                       `json:"has_tiered_pricing"`             // 是否多阶梯（>1 条或非默认兜底）
	VideoPricingConfig  *model.VideoPricingConfig  `json:"video_pricing_config,omitempty"` // 视频模型特殊计价配置

	// 能力特性字段（v3.5）— 由 features JSON 字段解析而来
	SupportsThinking  bool `json:"supports_thinking"`   // 是否支持深度思考
	SupportsWebSearch bool `json:"supports_web_search"` // 是否支持联网搜索
	SupportsJsonMode  bool `json:"supports_json_mode"`  // 是否支持 JSON 模式
	SupportsVision    bool `json:"supports_vision"`     // 是否支持图片输入
	RequiresStream    bool `json:"requires_stream"`     // 是否强制要求流式输出
}

// PublicPriceTier 公开展示用的阶梯价格简化视图
type PublicPriceTier struct {
	Name               string   `json:"name"`
	InputRange         string   `json:"input_range,omitempty"`         // 人类可读的输入区间，如 "(0, 32k]"
	OutputRange        string   `json:"output_range,omitempty"`        // 人类可读的输出区间
	InputMin           int64    `json:"input_min"`
	InputMinExclusive  bool     `json:"input_min_exclusive"`
	InputMax           *int64   `json:"input_max,omitempty"`
	InputMaxExclusive  bool     `json:"input_max_exclusive"`
	OutputMin          int64    `json:"output_min"`
	OutputMinExclusive bool     `json:"output_min_exclusive"`
	OutputMax          *int64   `json:"output_max,omitempty"`
	OutputMaxExclusive bool     `json:"output_max_exclusive"`
	InputPrice         float64  `json:"input_price"`
	OutputPrice        float64  `json:"output_price"`
	SellingInputPrice  *float64 `json:"selling_input_price,omitempty"`
	SellingOutputPrice *float64 `json:"selling_output_price,omitempty"`
}

// buildTierRangeString 构造人类可读的区间字符串 "(0, 32k]" / "[0, +∞)"
func buildTierRangeString(min int64, minExcl bool, max *int64, maxExcl bool) string {
	lb := "["
	if minExcl {
		lb = "("
	}
	rb := "]"
	if maxExcl {
		rb = ")"
	}
	maxStr := "+∞"
	if max != nil {
		maxStr = formatTokenCount(*max)
	}
	return lb + formatTokenCount(min) + ", " + maxStr + rb
}

// formatTokenCount 把 token 数转成人类友好表示（32000 → "32k", 1500000 → "1.5M"）
func formatTokenCount(n int64) string {
	if n >= 1_000_000 {
		v := float64(n) / 1_000_000
		if v == float64(int64(v)) {
			return fmt.Sprintf("%dM", int64(v))
		}
		return fmt.Sprintf("%.1fM", v)
	}
	if n >= 1000 {
		v := float64(n) / 1000
		if v == float64(int64(v)) {
			return fmt.Sprintf("%dk", int64(v))
		}
		return fmt.Sprintf("%.1fk", v)
	}
	return fmt.Sprintf("%d", n)
}

// convertTiersToPublic 将 PriceTier 列表转为公开视图
func convertTiersToPublic(tiers []model.PriceTier) []PublicPriceTier {
	out := make([]PublicPriceTier, 0, len(tiers))
	for _, t := range tiers {
		out = append(out, PublicPriceTier{
			Name:               t.Name,
			InputRange:         buildTierRangeString(t.InputMin, t.InputMinExclusive, t.InputMax, t.InputMaxExclusive),
			OutputRange:        buildTierRangeString(t.OutputMin, t.OutputMinExclusive, t.OutputMax, t.OutputMaxExclusive),
			InputMin:           t.InputMin,
			InputMinExclusive:  t.InputMinExclusive,
			InputMax:           t.InputMax,
			InputMaxExclusive:  t.InputMaxExclusive,
			OutputMin:          t.OutputMin,
			OutputMinExclusive: t.OutputMinExclusive,
			OutputMax:          t.OutputMax,
			OutputMaxExclusive: t.OutputMaxExclusive,
			InputPrice:         t.InputPrice,
			OutputPrice:        t.OutputPrice,
			SellingInputPrice:  t.SellingInputPrice,
			SellingOutputPrice: t.SellingOutputPrice,
		})
	}
	return out
}

// decodePriceTiersJSON 从 JSON 解析 PriceTiersData
func decodePriceTiersJSON(raw model.JSON) *model.PriceTiersData {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var data model.PriceTiersData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil
	}
	return &data
}

// decodeVideoPricingJSON 从 JSON 解析 VideoPricingConfig
func decodeVideoPricingJSON(raw model.JSON) *model.VideoPricingConfig {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var cfg model.VideoPricingConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil
	}
	return &cfg
}

// providerIconMap 供应商图标映射（emoji）
var providerIconMap = map[string]string{
	"openai":           "🟢",
	"anthropic":        "🟣",
	"google_gemini":    "🔵",
	"azure_openai":     "🔷",
	"deepseek":         "🟠",
	"aliyun_dashscope": "🔶",
	"volcengine":       "🌋",
	"moonshot":         "🌙",
	"zhipu":            "🔮",
	"baidu_wenxin":     "🔵",
}

// capabilityKeywords 能力关键词映射（基于模型名称推断能力）
func capabilityKeywords(modelName string) []string {
	caps := []string{}
	modelNameLower := strings.ToLower(modelName)

	// 基础能力
	caps = append(caps, "文本生成")

	// 根据模型名称推断能力
	if strings.Contains(modelNameLower, "code") || strings.Contains(modelNameLower, "coder") {
		caps = append(caps, "代码生成")
	}
	if strings.Contains(modelNameLower, "vision") || strings.Contains(modelNameLower, "gemini") || strings.Contains(modelNameLower, "gpt-4o") {
		caps = append(caps, "视觉理解")
	}
	if strings.Contains(modelNameLower, "reason") || strings.Contains(modelNameLower, "think") {
		caps = append(caps, "推理")
	}
	if strings.Contains(modelNameLower, "flash") || strings.Contains(modelNameLower, "mini") || strings.Contains(modelNameLower, "haiku") {
		caps = append(caps, "快速响应")
	}
	if strings.Contains(modelNameLower, "pro") || strings.Contains(modelNameLower, "max") || strings.Contains(modelNameLower, "plus") {
		caps = append(caps, "高性能")
	}

	// 默认能力
	caps = append(caps, "对话")
	caps = append(caps, "函数调用")

	return caps
}

// toPublicResponse 将 AIModel 转换为公开 API 响应格式
// 只有 status=online 且 is_active=true 的模型才会显示为 online
func toPublicResponse(m model.AIModel) PublicModelResponse {
	// 确定展示名称
	name := m.DisplayName
	if name == "" {
		name = m.ModelName
	}

	// 获取供应商名称
	provider := ""
	if m.Supplier.Name != "" {
		provider = m.Supplier.Name
	}

	// 获取供应商图标
	icon := "🔵"
	if m.Supplier.Code != "" {
		if emoji, ok := providerIconMap[m.Supplier.Code]; ok {
			icon = emoji
		}
	}

	// 确定状态：只有同时满足 is_active=true 且 status=online 才显示为 online
	status := "offline"
	if m.IsActive && m.Status == "online" {
		status = "online"
	} else if m.Status == "error" {
		status = "error"
	}

	// 动态生成完整标签：合并数据库存储的 tags + 供应商品牌
	// 确保即使数据库中 tags 未回填，API 也能返回正确的品牌标签
	tags := modeldiscovery.InferModelTags(m.ModelName, m.Supplier.Code)

	// 显示售价：优先使用 ModelPricing 中的售价，未配置时默认官方价9折
	var inputPriceRMB, outputPriceRMB float64
	if m.Pricing != nil && (m.Pricing.InputPriceRMB > 0 || m.Pricing.OutputPriceRMB > 0) {
		inputPriceRMB = m.Pricing.InputPriceRMB
		outputPriceRMB = m.Pricing.OutputPriceRMB
	} else {
		// 售价未配置，默认官方成本价9折
		inputPriceRMB = math.Round(m.InputCostRMB*0.9*10000) / 10000
		outputPriceRMB = math.Round(m.OutputCostRMB*0.9*10000) / 10000
	}
	inputPrice := int64(inputPriceRMB * 10000)
	outputPrice := int64(outputPriceRMB * 10000)

	// 计算折扣百分比（基于输入价格）
	// 只有折扣力度超过10%（即售价低于原价9折以下）才在前端展示折扣标签
	discount := 0
	if m.InputCostRMB > 0 && inputPriceRMB < m.InputCostRMB {
		d := int(math.Round(inputPriceRMB / m.InputCostRMB * 100))
		if d < 90 { // < 9折才显示（折扣 > 10%）
			discount = d
		}
	}

	// 提取能力标志（用于描述生成和字段输出，避免重复解析 features JSON）
	supportsThinking := extractBoolFeature(m.Features, "supports_thinking")
	supportsWebSearch := extractBoolFeature(m.Features, "supports_web_search")
	supportsJsonMode := extractBoolFeature(m.Features, "supports_json_mode")
	supportsVision := extractBoolFeature(m.Features, "supports_vision")
	requiresStream := extractBoolFeature(m.Features, "requires_stream")
	supportsFunctionCall := extractBoolFeature(m.Features, "supports_function_call")

	// 描述回退：DB 中 description 为空时，基于模型名 + 供应商 + 能力自动生成一段富文本简介
	description := m.Description
	if strings.TrimSpace(description) == "" {
		description = buildAutoDescription(m.ModelName, name, provider, m.ModelType, m.ContextWindow,
			supportsThinking, supportsVision, supportsWebSearch, supportsJsonMode, supportsFunctionCall)
	}

	return PublicModelResponse{
		ID:             m.ID,
		ModelID:        m.ModelName,
		Name:           name,
		Provider:       provider,
		ProviderIcon:   icon,
		Description:    description,
		ContextWindow:  m.ContextWindow,
		InputPrice:     inputPrice,
		OutputPrice:    outputPrice,
		InputPriceRMB:  inputPriceRMB,
		OutputPriceRMB: outputPriceRMB,
		Capabilities:   capabilityKeywords(m.ModelName),
		Status:         status,
		IsNew:          false, // TODO: 可根据创建时间计算
		IsFeatured:     false, // TODO: 可根据配置决定
		MaxTokens:      m.MaxTokens,
		ModelType:       m.ModelType,
		Tags:            tags,
		Discount:        discount,
		PricingUnit:     m.PricingUnit,
		Variant:         m.Variant,

		// 缓存定价字段（展示用）
		SupportsCache:              m.SupportsCache,
		CacheMechanism:             m.CacheMechanism,
		CacheMinTokens:             m.CacheMinTokens,
		CacheInputPriceRMB:         m.CacheInputPriceRMB,
		CacheExplicitInputPriceRMB: m.CacheExplicitInputPriceRMB,
		CacheWritePriceRMB:         m.CacheWritePriceRMB,

		// 阶梯定价 + 视频配置
		PriceTiers:         buildPublicTiers(m.PriceTiers),
		HasTieredPricing:   hasTieredPricing(m.PriceTiers),
		VideoPricingConfig: decodeVideoPricingJSON(m.VideoPricingConfig),

		// 能力特性（解析 features JSON）
		SupportsThinking:  supportsThinking,
		SupportsWebSearch: supportsWebSearch,
		SupportsJsonMode:  supportsJsonMode,
		SupportsVision:    supportsVision,
		RequiresStream:    requiresStream,
	}
}

// buildAutoDescription 根据模型元数据生成富文本中文简介
// 当 DB 中 description 为空时调用，拼接供应商/模型家族/上下文窗口/能力标志等信息
func buildAutoDescription(modelID, displayName, provider, modelType string, ctxWindow int,
	thinking, vision, webSearch, jsonMode, functionCall bool) string {
	family := detectModelFamily(modelID)
	mt := strings.ToLower(modelType)

	// 1) 主句：模型家族归属 + 类型定位
	var head string
	switch {
	case strings.Contains(mt, "embedding"):
		head = fmt.Sprintf("%s 提供的文本嵌入（Embedding）模型", provider)
	case strings.Contains(mt, "rerank"):
		head = fmt.Sprintf("%s 提供的重排序（Rerank）模型", provider)
	case strings.Contains(mt, "imagegeneration"):
		head = fmt.Sprintf("%s 提供的图像生成模型", provider)
	case strings.Contains(mt, "videogeneration"):
		head = fmt.Sprintf("%s 提供的视频生成模型", provider)
	case strings.Contains(mt, "tts"):
		head = fmt.Sprintf("%s 提供的语音合成（TTS）模型", provider)
	case strings.Contains(mt, "asr"):
		head = fmt.Sprintf("%s 提供的语音识别（ASR）模型", provider)
	case strings.Contains(mt, "vision"):
		head = fmt.Sprintf("%s 提供的多模态视觉大模型", provider)
	default:
		head = fmt.Sprintf("%s 提供的大语言模型", provider)
	}
	if family != "" {
		head = fmt.Sprintf("%s，隶属 %s 家族", head, family)
	}
	head += "。"

	// 2) 规格：上下文窗口
	var specs []string
	if ctxWindow >= 1_000_000 {
		specs = append(specs, fmt.Sprintf("上下文窗口 %.1fM tokens", float64(ctxWindow)/1_000_000))
	} else if ctxWindow >= 1000 {
		specs = append(specs, fmt.Sprintf("上下文窗口 %dK tokens", ctxWindow/1000))
	}

	// 3) 能力标签
	var caps []string
	if thinking {
		caps = append(caps, "深度思考")
	}
	if webSearch {
		caps = append(caps, "联网搜索")
	}
	if vision {
		caps = append(caps, "图片理解")
	}
	if jsonMode {
		caps = append(caps, "JSON 结构化输出")
	}
	if functionCall {
		caps = append(caps, "函数调用")
	}

	// 4) 名称启发式场景描述
	lowerName := strings.ToLower(displayName + " " + modelID)
	switch {
	case strings.Contains(lowerName, "code") || strings.Contains(lowerName, "coder"):
		caps = append(caps, "代码生成与分析")
	case strings.Contains(lowerName, "math"):
		caps = append(caps, "数学推导")
	}
	switch {
	case strings.Contains(lowerName, "flash"), strings.Contains(lowerName, "mini"), strings.Contains(lowerName, "haiku"), strings.Contains(lowerName, "lite"), strings.Contains(lowerName, "turbo"):
		caps = append(caps, "低延迟响应")
	case strings.Contains(lowerName, "pro"), strings.Contains(lowerName, "max"), strings.Contains(lowerName, "plus"), strings.Contains(lowerName, "ultra"), strings.Contains(lowerName, "opus"):
		caps = append(caps, "高推理性能")
	}

	// 5) 组装
	out := head
	if len(specs) > 0 {
		out += strings.Join(specs, "、") + "，"
	}
	if len(caps) > 0 {
		out += "支持" + strings.Join(dedupStrings(caps), "、") + "。"
	}
	out += "通过 OpenAI 兼容协议 / v1/chat/completions 统一接入，无需修改现有代码。"
	return out
}

// detectModelFamily 从模型 ID 中识别常见家族名
func detectModelFamily(modelID string) string {
	s := strings.ToLower(modelID)
	switch {
	case strings.HasPrefix(s, "gpt-"), strings.HasPrefix(s, "o1"), strings.HasPrefix(s, "o3"), strings.HasPrefix(s, "o4"):
		return "GPT"
	case strings.HasPrefix(s, "claude"):
		return "Claude"
	case strings.HasPrefix(s, "gemini"):
		return "Gemini"
	case strings.HasPrefix(s, "deepseek"):
		return "DeepSeek"
	case strings.HasPrefix(s, "qwen") || strings.Contains(s, "qwen"):
		return "通义千问 Qwen"
	case strings.HasPrefix(s, "doubao") || strings.HasPrefix(s, "seed"):
		return "豆包 / Seed"
	case strings.HasPrefix(s, "moonshot") || strings.HasPrefix(s, "kimi"):
		return "Kimi"
	case strings.HasPrefix(s, "glm"):
		return "智谱 GLM"
	case strings.HasPrefix(s, "ernie"):
		return "文心 ERNIE"
	case strings.HasPrefix(s, "hunyuan"):
		return "腾讯混元"
	case strings.HasPrefix(s, "llama"):
		return "Llama"
	case strings.HasPrefix(s, "mistral"), strings.HasPrefix(s, "mixtral"):
		return "Mistral"
	case strings.HasPrefix(s, "yi-"):
		return "Yi"
	}
	return ""
}

// dedupStrings 保序去重
func dedupStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// extractBoolFeature 从 features JSON 中提取 bool 特性值，解析失败或不存在时返回 false
func extractBoolFeature(features model.JSON, key string) bool {
	if len(features) == 0 {
		return false
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(features), &m); err != nil {
		return false
	}
	v, ok := m[key]
	if !ok {
		return false
	}
	b, isBool := v.(bool)
	return isBool && b
}

// buildPublicTiers 从 JSON 解析 tier 列表并转公开视图
func buildPublicTiers(raw model.JSON) []PublicPriceTier {
	data := decodePriceTiersJSON(raw)
	if data == nil || len(data.Tiers) == 0 {
		return nil
	}
	return convertTiersToPublic(data.Tiers)
}

// hasTieredPricing 判断是否有效的多阶梯（>1 条或非默认兜底）
func hasTieredPricing(raw model.JSON) bool {
	data := decodePriceTiersJSON(raw)
	if data == nil || len(data.Tiers) == 0 {
		return false
	}
	if len(data.Tiers) > 1 {
		return true
	}
	return !data.Tiers[0].IsDefaultTier()
}

// Stats 返回模型统计数量（总数/已启用/在线），直接聚合全量数据，不受分页限制
// GET /api/v1/admin/ai-models/stats
func (h *AIModelHandler) Stats(c *gin.Context) {
	stats, err := h.svc.GetStats(c.Request.Context())
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, stats)
}

// List 分页获取AI模型列表 GET /api/v1/admin/ai-models
// 管理员接口：返回完整 AIModel 数据（包含 model_type, display_name, context_window,
// max_input_tokens, max_output_tokens, supplier_status, features, input_modalities,
// output_modalities, task_types, domain, version 等扩展字段），供前端 DashModelsPage 使用
// AdminModelItem 管理员模型列表条目：完整 AIModel + k:v 标签 + 解析后阶梯
type AdminModelItem struct {
	model.AIModel
	Labels           []LabelDTO        `json:"labels,omitempty"`
	PriceTiersParsed []PublicPriceTier `json:"price_tiers_parsed,omitempty"` // 便于前端直接编辑（AIModel.PriceTiers 是 []byte，默认 base64 序列化）
	HasTieredPricing bool              `json:"has_tiered_pricing"`
	// v3.5 零价诊断：若 input_cost_rmb = output_cost_rmb = 0，给出原因分类
	//   FREE / NEEDS_PRICING / NON_TOKEN_UNIT_NOT_CONFIGURED / UNKNOWN_MODEL_TYPE / UNKNOWN
	//   空字符串表示有价格，无需诊断
	ZeroPriceReason string `json:"zero_price_reason,omitempty"`
}

// diagnoseZeroPrice 返回零价模型的原因分类，便于 Admin UI 展示 badge
func diagnoseZeroPrice(m model.AIModel) string {
	if m.InputCostRMB > 0 || m.OutputCostRMB > 0 {
		return ""
	}
	tagsLower := strings.ToLower(m.Tags)
	// 1. 官方免费模型
	if strings.Contains(tagsLower, "free") {
		return "FREE"
	}
	// 2. 已停用待管理员定价
	if strings.Contains(tagsLower, "needssellprice") ||
		strings.Contains(tagsLower, "needspricing") ||
		strings.Contains(tagsLower, "needs_sell_price") ||
		strings.Contains(tagsLower, "needs_pricing") ||
		strings.Contains(tagsLower, "待定价") ||
		strings.Contains(tagsLower, "待设售价") {
		return "NEEDS_PRICING"
	}
	// 3. 非 Token 计费单位（图片/视频/TTS/ASR）未配置补充价
	if m.PricingUnit != "" && m.PricingUnit != "per_million_tokens" {
		return "NON_TOKEN_UNIT_NOT_CONFIGURED"
	}
	// 4. 未识别的模型类型（如火山 Router / 3DGeneration）
	if m.ModelType == "Router" || m.ModelType == "3DGeneration" || m.ModelType == "" {
		return "UNKNOWN_MODEL_TYPE"
	}
	return "UNKNOWN"
}

func (h *AIModelHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	supplierID, _ := strconv.Atoi(c.DefaultQuery("supplier_id", "0"))
	search := c.DefaultQuery("search", "")

	models, total, err := h.svc.ListWithFilter(c.Request.Context(), page, pageSize, uint(supplierID), search)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 包装为含标签的条目 + 解析阶梯 + 零价诊断
	items := make([]AdminModelItem, len(models))
	for i, m := range models {
		items[i] = AdminModelItem{
			AIModel:          m,
			PriceTiersParsed: buildPublicTiers(m.PriceTiers),
			HasTieredPricing: hasTieredPricing(m.PriceTiers),
			ZeroPriceReason:  diagnoseZeroPrice(m),
		}
	}

	// v3.5 批量加载 k:v 标签 + JOIN 字典表（按 Accept-Language 选字段）
	// 一次 SQL 同时拿到 key/value/name/color/icon/category/priority
	if len(models) > 0 {
		modelIDs := make([]uint, len(models))
		for i, m := range models {
			modelIDs[i] = m.ID
		}

		locale := parseLocaleForLabels(c.GetHeader("Accept-Language"))
		nameCol := model.PickNameColumn(locale)

		// JOIN SQL：缺失翻译时 fallback 到 name_en
		type labelWithMeta struct {
			ModelID  uint   `gorm:"column:model_id"`
			Key      string `gorm:"column:label_key"`
			Value    string `gorm:"column:label_value"`
			Name     string `gorm:"column:name"`
			Color    string `gorm:"column:color"`
			Icon     string `gorm:"column:icon"`
			Category string `gorm:"column:category"`
			Priority int    `gorm:"column:priority"`
		}
		var allLabels []labelWithMeta
		// 注意：MySQL 保留字 key 需反引号；列名动态拼接前已由 PickNameColumn 白名单化（避免 SQL 注入）
		selectSQL := "ml.model_id, ml.label_key, ml.label_value, " +
			"COALESCE(NULLIF(ld." + nameCol + ", ''), ld.name_en) AS name, " +
			"ld.color, ld.icon, ld.category, ld.priority"
		database.DB.
			Table("model_labels AS ml").
			Select(selectSQL).
			Joins("LEFT JOIN label_dictionary ld ON ld.`key` = ml.label_key AND ld.is_active = 1").
			Where("ml.model_id IN ? AND ml.deleted_at IS NULL", modelIDs).
			Scan(&allLabels)

		labelMap := make(map[uint][]LabelDTO, len(models))
		for _, lbl := range allLabels {
			// 若字典中没有该 key（LEFT JOIN 返回 NULL），降级展示原 key
			name := lbl.Name
			if name == "" {
				name = lbl.Key
			}
			labelMap[lbl.ModelID] = append(labelMap[lbl.ModelID], LabelDTO{
				Key:      lbl.Key,
				Value:    lbl.Value,
				Name:     name,
				Color:    lbl.Color,
				Icon:     lbl.Icon,
				Category: lbl.Category,
				Priority: lbl.Priority,
			})
		}
		for i, m := range models {
			if lbls, ok := labelMap[m.ID]; ok {
				items[i].Labels = lbls
			}
		}
	}

	response.PageResult(c, items, total, page, pageSize)
}

// PublicList 公开模型列表 GET /api/v1/public/models
// 只返回 status=online 且 is_active=true 的模型
// 支持 ?type=ImageGeneration / VideoGeneration 等过滤（不传则默认返回聊天类 LLM/VLM）
func (h *AIModelHandler) PublicList(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	modelType := c.DefaultQuery("type", "")
	sort := c.DefaultQuery("sort", "")

	var models []model.AIModel
	var total int64
	var err error
	if sort == "popular" {
		models, total, err = h.svc.ListOnlineByPopularity(c.Request.Context(), page, pageSize)
	} else if modelType != "" {
		models, total, err = h.svc.ListOnline(c.Request.Context(), page, pageSize, modelType)
	} else {
		models, total, err = h.svc.ListOnline(c.Request.Context(), page, pageSize)
	}
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 转换为公开 API 响应格式
	list := make([]PublicModelResponse, len(models))
	for i, m := range models {
		list[i] = toPublicResponse(m)
	}

	// 批量查询最近24小时的模型统计数据（延迟、成功率、请求量）
	type modelStat struct {
		ModelName    string  `gorm:"column:model_name"`
		AvgLatency   float64 `gorm:"column:avg_latency"`
		SuccessRate  float64 `gorm:"column:success_rate"`
		RequestCount int64   `gorm:"column:request_count"`
	}
	var stats []modelStat
	since := time.Now().Add(-24 * time.Hour)
	modelNames := make([]string, len(models))
	for i, m := range models {
		modelNames[i] = m.ModelName
	}
	database.DB.Raw(`
		SELECT model_name,
			ROUND(AVG(latency_ms)) AS avg_latency,
			ROUND(SUM(CASE WHEN status_code >= 200 AND status_code < 400 THEN 1 ELSE 0 END) * 100.0 / COUNT(*), 1) AS success_rate,
			COUNT(*) AS request_count
		FROM channel_logs
		WHERE model_name IN ? AND created_at >= ? AND deleted_at IS NULL
		GROUP BY model_name
	`, modelNames, since).Scan(&stats)

	statMap := make(map[string]*modelStat, len(stats))
	for i := range stats {
		statMap[stats[i].ModelName] = &stats[i]
	}
	for i, m := range models {
		if s, ok := statMap[m.ModelName]; ok {
			list[i].AvgLatencyMs = int64(s.AvgLatency)
			list[i].SuccessRate = s.SuccessRate
			list[i].RequestCount = s.RequestCount
		}
	}

	// v3.5 批量加载 k:v 标签 + JOIN label_dictionary（按 Accept-Language 选 name 字段）
	modelIDs := make([]uint, len(models))
	for i, m := range models {
		modelIDs[i] = m.ID
	}
	locale := parseLocaleForLabels(c.GetHeader("Accept-Language"))
	nameCol := model.PickNameColumn(locale)
	type publicLabelWithMeta struct {
		ModelID  uint   `gorm:"column:model_id"`
		Key      string `gorm:"column:label_key"`
		Value    string `gorm:"column:label_value"`
		Name     string `gorm:"column:name"`
		Color    string `gorm:"column:color"`
		Icon     string `gorm:"column:icon"`
		Category string `gorm:"column:category"`
		Priority int    `gorm:"column:priority"`
	}
	var allLabels []publicLabelWithMeta
	selectSQL := "ml.model_id, ml.label_key, ml.label_value, " +
		"COALESCE(NULLIF(ld." + nameCol + ", ''), ld.name_en) AS name, " +
		"ld.color, ld.icon, ld.category, ld.priority"
	database.DB.
		Table("model_labels AS ml").
		Select(selectSQL).
		Joins("LEFT JOIN label_dictionary ld ON ld.`key` = ml.label_key AND ld.is_active = 1").
		Where("ml.model_id IN ? AND ml.deleted_at IS NULL", modelIDs).
		Scan(&allLabels)
	labelMap := make(map[uint][]LabelDTO, len(models))
	for _, lbl := range allLabels {
		name := lbl.Name
		if name == "" {
			name = lbl.Key
		}
		labelMap[lbl.ModelID] = append(labelMap[lbl.ModelID], LabelDTO{
			Key:      lbl.Key,
			Value:    lbl.Value,
			Name:     name,
			Color:    lbl.Color,
			Icon:     lbl.Icon,
			Category: lbl.Category,
			Priority: lbl.Priority,
		})
	}
	for i, m := range models {
		if lbls, ok := labelMap[m.ID]; ok {
			list[i].Labels = lbls
		}
	}

	response.PageResult(c, list, total, page, pageSize)
}

// Create 新建AI模型 POST /api/v1/admin/ai-models
func (h *AIModelHandler) Create(c *gin.Context) {
	var m model.AIModel
	if err := c.ShouldBindJSON(&m); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	if err := h.svc.Create(c.Request.Context(), &m); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, m)
}

// GetByID 根据ID获取AI模型详情 GET /api/v1/admin/ai-models/:id
func (h *AIModelHandler) GetByID(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	m, err := h.svc.GetByID(c.Request.Context(), uint(id))
	if err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrModelNotFound.Code, err.Error())
		return
	}

	response.Success(c, m)
}

// Update 更新AI模型信息 PUT /api/v1/admin/ai-models/:id
func (h *AIModelHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	var updates map[string]interface{}
	if err := c.ShouldBindJSON(&updates); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	// 前端发 input_price_rmb，后端字段为 input_cost_rmb，需要映射
	if v, ok := updates["input_price_rmb"]; ok {
		updates["input_cost_rmb"] = v
		delete(updates, "input_price_rmb")
	}
	if v, ok := updates["output_price_rmb"]; ok {
		updates["output_cost_rmb"] = v
		delete(updates, "output_price_rmb")
	}

	// JSON 字段需要序列化为 model.JSON 字节（extra_params / task_types / input_modalities / output_modalities / features / price_tiers / video_pricing_config）
	jsonFields := []string{"extra_params", "task_types", "input_modalities", "output_modalities", "features", "price_tiers", "video_pricing_config"}
	for _, field := range jsonFields {
		if val, ok := updates[field]; ok {
			if val == nil {
				updates[field] = model.JSON(nil)
			} else {
				// 对 price_tiers：如果是数组，包装为 PriceTiersData，并对每个阶梯调用 Normalize()
				// 以自动生成 Name（当前端留空时）和同步新旧字段
				if field == "price_tiers" {
					if arr, ok := val.([]interface{}); ok {
						// 先将 arr 序列化 → 反序列化为 []PriceTier，以调用 Normalize
						arrBytes, _ := json.Marshal(arr)
						var tiers []model.PriceTier
						_ = json.Unmarshal(arrBytes, &tiers)
						for i := range tiers {
							tiers[i].Normalize()
						}
						model.SortTiers(tiers)
						wrapped := model.PriceTiersData{
							Tiers:     tiers,
							Currency:  "CNY",
							UpdatedAt: time.Now(),
						}
						bytes, _ := json.Marshal(wrapped)
						updates[field] = model.JSON(bytes)
						continue
					}
				}
				bytes, _ := json.Marshal(val)
				updates[field] = model.JSON(bytes)
			}
		}
	}

	// 提取售价字段，保存到 ModelPricing 表
	sellingInputRmb, hasSellingIn := updates["selling_input_rmb"]
	sellingOutputRmb, hasSellingOut := updates["selling_output_rmb"]
	delete(updates, "selling_input_rmb")
	delete(updates, "selling_output_rmb")

	if err := h.svc.Update(c.Request.Context(), uint(id), updates); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	// 更新或创建平台售价
	if hasSellingIn || hasSellingOut {
		modelID := uint(id)
		var pricing model.ModelPricing
		db := database.DB
		err := db.Where("model_id = ?", modelID).First(&pricing).Error
		if err != nil {
			// 不存在则创建
			pricing = model.ModelPricing{ModelID: modelID}
		}
		if hasSellingIn {
			if v, ok := sellingInputRmb.(float64); ok {
				pricing.InputPriceRMB = v
				pricing.InputPricePerToken = int64(v * 10000)
			}
		}
		if hasSellingOut {
			if v, ok := sellingOutputRmb.(float64); ok {
				pricing.OutputPriceRMB = v
				pricing.OutputPricePerToken = int64(v * 10000)
			}
		}
		if pricing.ID == 0 {
			db.Create(&pricing)
		} else {
			db.Save(&pricing)
		}
	}

	invalidatePublicModelsCache()
	response.Success(c, gin.H{"message": "updated"})
}

// Delete 删除AI模型 DELETE /api/v1/admin/ai-models/:id
func (h *AIModelHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	if err := h.svc.Delete(c.Request.Context(), uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	invalidatePublicModelsCache()
	response.Success(c, gin.H{"message": "deleted"})
}

// Verify 验证模型并上线 POST /api/v1/admin/ai-models/:id/verify
// 将模型状态设置为 online，使其对用户可见
func (h *AIModelHandler) Verify(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	// 设置模型状态为 online
	if err := h.svc.SetStatus(c.Request.Context(), uint(id), "online"); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	invalidatePublicModelsCache()
	response.Success(c, gin.H{"message": "model verified and online", "status": "online"})
}

// SetOffline 将模型下线 POST /api/v1/admin/ai-models/:id/offline
// 将模型状态设置为 offline
func (h *AIModelHandler) SetOffline(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	// 设置模型状态为 offline
	if err := h.svc.SetStatus(c.Request.Context(), uint(id), "offline"); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	invalidatePublicModelsCache()
	response.Success(c, gin.H{"message": "model set offline", "status": "offline"})
}

// Reactivate 手动重新上线模型 POST /api/v1/admin/ai-models/:id/reactivate
//
// 用途：管理员确认某个被批量检测自动下线的模型实际可用，需要立即恢复并阻止下次检测再次自动下线。
//
// 流程：
//  1. 模型 status=online
//  2. 写入一条 model_check_log{available=true, error="manual_reactivate", upstream_status="manual_override"}
//     - 该成功记录会让 IsModelMarkedUnavailableSoft / discovery.isModelCheckFailed 立即返回 false
//     - 下一次批量检测的连续失败计数从 0 重新开始
//  3. 清除公开模型缓存
func (h *AIModelHandler) Reactivate(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	// 加载模型确认存在并取 ModelName（写日志冗余字段需要）
	var aiModel model.AIModel
	if err := database.DB.First(&aiModel, uint(id)).Error; err != nil {
		response.ErrorMsg(c, http.StatusNotFound, 40401, "模型不存在")
		return
	}

	// 1. 设置模型状态为 online
	if err := h.svc.SetStatus(c.Request.Context(), uint(id), "online"); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	// 2. 写入一条 manual_override 检测日志
	log := &model.ModelCheckLog{
		ModelID:        aiModel.ID,
		ModelName:      aiModel.ModelName,
		Available:      true,
		Error:          "manual_reactivate",
		CheckedAt:      time.Now(),
		AutoDisabled:   false,
		UpstreamStatus: "manual_override",
	}
	if err := database.DB.Create(log).Error; err != nil {
		// 日志写入失败不阻塞主流程，仅记录
		response.Success(c, gin.H{"message": "model reactivated (log write failed)", "status": "online"})
		return
	}

	// 3. 清除公开模型缓存
	invalidatePublicModelsCache()
	response.Success(c, gin.H{"message": "model reactivated", "status": "online", "log_id": log.ID})
}

// ========== 模型下线扫描与批量下线 ==========

// DeprecationScanResult 下线扫描结果（含检测报告）
type DeprecationScanResult struct {
	// 我们数据库中有、但供应商 API 已不返回的模型（可能被下线）
	PossiblyDeprecated []DeprecationCandidate `json:"possibly_deprecated"`
	// 供应商返回的、我们数据库中没有的新模型（仅供参考）
	NewModelsFromProvider int `json:"new_models_from_provider"`
	// 供应商返回但仅在我们 offline 库中存在的模型（供应商仍在但我们标记了下线）
	NewModelsFromProviderList []string `json:"new_models_from_provider_list,omitempty"`
	// 扫描的供应商名称
	SupplierName string `json:"supplier_name"`
	// 供应商 API 返回的模型总数
	ProviderTotal int `json:"provider_total"`
	// 我们数据库中该供应商的 online 模型总数
	OurOnlineTotal int `json:"our_online_total"`
	// 我们数据库中该供应商的 offline 模型总数
	OurOfflineTotal int `json:"our_offline_total"`
	// 已下线模型列表（供应商 API 已不返回、且在我们数据库中已标记 offline 的模型）
	AlreadyOfflineModels []DeprecationCandidate `json:"already_offline_models"`
	// 官方公告已下线模型列表（命中本地维护的官方下线清单，含下线日期/原因）
	OfficiallyDeprecated []OfficialDeprecationItem `json:"officially_deprecated,omitempty"`
	// 是否支持本供应商的官方下线名单查询（前端据此显示「一键标记」按钮）
	HasOfficialList bool `json:"has_official_list"`
	// 扫描耗时（毫秒）
	ScanDurationMs int64 `json:"scan_duration_ms"`
}

// DeprecationCandidate 可能下线的模型候选
type DeprecationCandidate struct {
	ID          uint   `json:"id"`
	ModelName   string `json:"model_name"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
	SupplierID  uint   `json:"supplier_id"`
	ModelType   string `json:"model_type,omitempty"`
	PricingUnit string `json:"pricing_unit,omitempty"`
}

// OfficialDeprecationItem 官方公告下线模型条目（在本地数据库中匹配的）
type OfficialDeprecationItem struct {
	ID          uint   `json:"id"`
	ModelName   string `json:"model_name"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`     // 当前状态: online/offline
	IsActive    bool   `json:"is_active"`  // 当前 is_active
	ModelType   string `json:"model_type,omitempty"`
	RetireDate  string `json:"retire_date"`            // 官方下线日期
	Reason      string `json:"reason"`                 // 下线原因/分类
	Replacement string `json:"replacement,omitempty"`  // 官方推荐替代模型
}

// scanSupplierDeprecation 扫描单个供应商的模型下线情况（核心逻辑）
func (h *AIModelHandler) scanSupplierDeprecation(supplier model.Supplier) (*DeprecationScanResult, error) {
	// 获取数据库中该供应商的所有模型（online + offline 均需要）
	var onlineModels []model.AIModel
	database.DB.Where("supplier_id = ? AND status = ?", supplier.ID, "online").Find(&onlineModels)

	var offlineModels []model.AIModel
	database.DB.Where("supplier_id = ? AND status = ?", supplier.ID, "offline").Find(&offlineModels)

	// 通过 DiscoveryService 拉取供应商当前可用模型列表
	discoverySvc := modeldiscovery.NewDiscoveryService(database.DB)
	providerModelNames, err := discoverySvc.FetchProviderModelNames(supplier.ID)
	if err != nil {
		return nil, err
	}

	// 构建供应商模型名称集合
	providerSet := make(map[string]bool, len(providerModelNames))
	for _, name := range providerModelNames {
		providerSet[strings.ToLower(name)] = true
	}

	// 构建我们 online 模型名称集合
	onlineSet := make(map[string]bool, len(onlineModels))
	for _, m := range onlineModels {
		onlineSet[strings.ToLower(m.ModelName)] = true
	}
	// 构建我们全部模型名称集合（online+offline）
	allDBSet := make(map[string]bool, len(onlineModels)+len(offlineModels))
	for _, m := range onlineModels {
		allDBSet[strings.ToLower(m.ModelName)] = true
	}
	for _, m := range offlineModels {
		allDBSet[strings.ToLower(m.ModelName)] = true
	}

	// ① 找出我们 online 但供应商已不返回的模型 → 需要下线的候选
	var candidates []DeprecationCandidate
	for _, m := range onlineModels {
		if !providerSet[strings.ToLower(m.ModelName)] {
			candidates = append(candidates, DeprecationCandidate{
				ID:          m.ID,
				ModelName:   m.ModelName,
				DisplayName: m.DisplayName,
				Status:      m.Status,
				SupplierID:  m.SupplierID,
				ModelType:   m.ModelType,
				PricingUnit: m.PricingUnit,
			})
		}
	}

	// ② 找出我们 offline 且供应商也不返回的模型 → 确认已下线
	var alreadyOffline []DeprecationCandidate
	for _, m := range offlineModels {
		if !providerSet[strings.ToLower(m.ModelName)] {
			alreadyOffline = append(alreadyOffline, DeprecationCandidate{
				ID:          m.ID,
				ModelName:   m.ModelName,
				DisplayName: m.DisplayName,
				Status:      m.Status,
				SupplierID:  m.SupplierID,
				ModelType:   m.ModelType,
				PricingUnit: m.PricingUnit,
			})
		}
	}

	// ③ 统计供应商有但我们完全没有的新模型（排除 online+offline 都有的）
	var newModelNames []string
	for _, name := range providerModelNames {
		if !allDBSet[strings.ToLower(name)] {
			newModelNames = append(newModelNames, name)
		}
	}

	// ④ 加载本地维护的"官方下线名单"，匹配 DB 中存在的模型
	officiallyDeprecated, hasOfficialList := h.collectOfficialDeprecated(supplier, append(onlineModels, offlineModels...))

	return &DeprecationScanResult{
		PossiblyDeprecated:        candidates,
		NewModelsFromProvider:     len(newModelNames),
		NewModelsFromProviderList: newModelNames,
		SupplierName:              supplier.Name,
		ProviderTotal:             len(providerModelNames),
		OurOnlineTotal:            len(onlineModels),
		OurOfflineTotal:           len(offlineModels),
		AlreadyOfflineModels:      alreadyOffline,
		OfficiallyDeprecated:      officiallyDeprecated,
		HasOfficialList:           hasOfficialList,
	}, nil
}

// collectOfficialDeprecated 加载供应商的官方下线名单，与本地数据库中的模型做匹配
// 返回匹配的模型列表 + 是否有该供应商的官方名单可用
//
// 当前支持的供应商：
//   - baidu_qianfan   (https://cloud.baidu.com/doc/qianfan/s/zmh4stou3)
//   - aliyun_dashscope / alibaba (https://help.aliyun.com/zh/model-studio/model-depreciation)
//
// 未来扩展：其他供应商（tencent_hunyuan 等）可在此添加 case 分支
func (h *AIModelHandler) collectOfficialDeprecated(supplier model.Supplier, allModels []model.AIModel) ([]OfficialDeprecationItem, bool) {
	// 按 supplier code 分发到对应的官方下线名单
	var deprecatedMap map[string]pricescraper.ModelDeprecation
	switch supplier.Code {
	case "baidu_qianfan":
		deprecatedMap = pricescraper.GetQianfanDeprecatedModels()
	case "aliyun_dashscope", "alibaba":
		deprecatedMap = pricescraper.GetAliyunDeprecatedModels()
	default:
		return nil, false // 该供应商暂未维护官方下线名单
	}
	if len(deprecatedMap) == 0 {
		return nil, true
	}

	// 匹配本地 DB 模型
	items := make([]OfficialDeprecationItem, 0)
	for _, m := range allModels {
		dep, ok := deprecatedMap[strings.ToLower(m.ModelName)]
		if !ok {
			continue
		}
		items = append(items, OfficialDeprecationItem{
			ID:          m.ID,
			ModelName:   m.ModelName,
			DisplayName: m.DisplayName,
			Status:      m.Status,
			IsActive:    m.IsActive,
			ModelType:   m.ModelType,
			RetireDate:  dep.RetireDate,
			Reason:      dep.Reason,
			Replacement: dep.Replacement,
		})
	}
	return items, true
}

// DeprecationScan POST /admin/models/deprecation-scan
// 通过对比数据库模型与供应商 API 返回的模型列表，找出可能已下线的模型
// 同时返回已下线模型列表（供应商已不返回 + 我们库中 offline 的模型）
func (h *AIModelHandler) DeprecationScan(c *gin.Context) {
	startTime := time.Now()
	supplierCode := c.DefaultQuery("supplier", "alibaba")

	// 查询指定供应商
	var supplier model.Supplier
	if err := database.DB.Where("code = ?", supplierCode).First(&supplier).Error; err != nil {
		// 尝试按名称查找
		if err2 := database.DB.Where("name LIKE ?", "%"+supplierCode+"%").First(&supplier).Error; err2 != nil {
			response.ErrorMsg(c, http.StatusBadRequest, 40001, "未找到指定供应商："+supplierCode)
			return
		}
	}

	result, err := h.scanSupplierDeprecation(supplier)
	if err != nil {
		response.ErrorMsg(c, http.StatusServiceUnavailable, 50301, "无法连接供应商API："+err.Error())
		return
	}

	scanDuration := time.Since(startTime).Milliseconds()
	result.ScanDurationMs = scanDuration

	response.Success(c, result)
}

// ScannedOfflineAllResult 所有供应商扫描下线模型汇总结果
type ScannedOfflineAllResult struct {
	Groups           []ScannedOfflineGroup `json:"groups"`
	TotalModels      int                   `json:"total_models"`
	SuppliersScanned int                   `json:"suppliers_scanned"`
	SuppliersFailed  int                   `json:"suppliers_failed"`
	ScanDurationMs   int64                 `json:"scan_duration_ms"`
}

// ScannedOfflineGroup 单个供应商的扫描下线模型分组
type ScannedOfflineGroup struct {
	SupplierID           uint                      `json:"supplier_id"`
	SupplierCode         string                    `json:"supplier_code"`
	SupplierName         string                    `json:"supplier_name"`
	Models               []DeprecationCandidate    `json:"models"`                          // 上游 API 已不返回 + 本地已 offline 的模型（确认下线）
	OfficiallyDeprecated []OfficialDeprecationItem `json:"officially_deprecated,omitempty"` // 官方下线公告命中的模型（含 online 状态，需要管理员处理）
	HasOfficialList      bool                      `json:"has_official_list"`               // 该供应商是否维护了官方下线名单
}

// ScanOfflineAll GET /admin/models/scanned-offline
// 聚合所有API型供应商的扫描下线模型列表
func (h *AIModelHandler) ScanOfflineAll(c *gin.Context) {
	// 三服务模式：委派给 Worker
	if h.bridge != nil {
		result, err := h.bridge.PublishAndWait(c.Request.Context(), taskqueue.TaskScanOffline, nil)
		if err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
			return
		}
		// 直接返回 Worker 的 JSON 结果
		c.Data(http.StatusOK, "application/json", []byte(`{"code":0,"message":"ok","data":`+result.Data+`}`))
		return
	}

	// 单体模式：本地执行
	startTime := time.Now()

	// 查询所有活跃的 API 型供应商
	var suppliers []model.Supplier
	if err := database.DB.Where("status = ? AND is_active = ? AND (access_type = ? OR access_type = ?)",
		"active", true, "api", "").Find(&suppliers).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, "查询供应商失败："+err.Error())
		return
	}

	if len(suppliers) == 0 {
		response.Success(c, ScannedOfflineAllResult{
			Groups:           []ScannedOfflineGroup{},
			TotalModels:      0,
			SuppliersScanned: 0,
			SuppliersFailed:  0,
			ScanDurationMs:   time.Since(startTime).Milliseconds(),
		})
		return
	}

	// 并发扫描所有供应商，使用信号量限制并发数为 5
	var (
		mu              sync.Mutex
		wg              sync.WaitGroup
		groups          []ScannedOfflineGroup
		suppliersScanned int
		suppliersFailed  int
		totalModels     int
		semaphore       = make(chan struct{}, 5)
	)

	for _, supplier := range suppliers {
		wg.Add(1)
		go func(sup model.Supplier) {
			defer wg.Done()

			// 获取信号量
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			result, err := h.scanSupplierDeprecation(sup)
			if err != nil {
				log.Printf("[WARN] 扫描供应商 %s (ID=%d) 失败: %v", sup.Name, sup.ID, err)
				mu.Lock()
				suppliersFailed++
				mu.Unlock()
				return
			}

			// 收集 already_offline_models（确认已下线）+ officially_deprecated（官方公告命中）
			// 官方公告项按 model_name 去重（与 already_offline 可能重合）
			alreadyOfflineNames := make(map[string]bool, len(result.AlreadyOfflineModels))
			for _, m := range result.AlreadyOfflineModels {
				alreadyOfflineNames[strings.ToLower(m.ModelName)] = true
			}
			var officialOnly []OfficialDeprecationItem
			for _, d := range result.OfficiallyDeprecated {
				if !alreadyOfflineNames[strings.ToLower(d.ModelName)] {
					officialOnly = append(officialOnly, d)
				}
			}

			if len(result.AlreadyOfflineModels) > 0 || len(officialOnly) > 0 {
				mu.Lock()
				groups = append(groups, ScannedOfflineGroup{
					SupplierID:           sup.ID,
					SupplierCode:         sup.Code,
					SupplierName:         sup.Name,
					Models:               result.AlreadyOfflineModels,
					OfficiallyDeprecated: officialOnly,
					HasOfficialList:      result.HasOfficialList,
				})
				totalModels += len(result.AlreadyOfflineModels) + len(officialOnly)
				suppliersScanned++
				mu.Unlock()
			} else {
				mu.Lock()
				suppliersScanned++
				mu.Unlock()
			}
		}(supplier)
	}

	wg.Wait()

	scanDuration := time.Since(startTime).Milliseconds()

	response.Success(c, ScannedOfflineAllResult{
		Groups:           groups,
		TotalModels:      totalModels,
		SuppliersScanned: suppliersScanned,
		SuppliersFailed:  suppliersFailed,
		ScanDurationMs:   scanDuration,
	})
}


// BulkDeprecateRequest 批量下线请求
type BulkDeprecateRequest struct {
	ModelIDs             []uint `json:"model_ids" binding:"required,min=1"`
	OfflineDays          int    `json:"offline_days"`           // 多少天后正式下线，默认 7
	AnnouncementTitle    string `json:"announcement_title"`
	AnnouncementContent  string `json:"announcement_content"`
}

// BulkDeprecate POST /admin/models/bulk-deprecate
// 批量标记模型为 pending_offline，创建 model_deprecation 类型公告
func (h *AIModelHandler) BulkDeprecate(c *gin.Context) {
	var req BulkDeprecateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, err.Error())
		return
	}
	if req.OfflineDays <= 0 {
		req.OfflineDays = 7
	}

	// 将选中模型标记为 offline（立刻对用户不可见），并记录下线时间
	now := time.Now()
	offlineAt := now.AddDate(0, 0, req.OfflineDays)
	_ = offlineAt // 可用于未来的定时任务记录

	var affectedModels []model.AIModel
	database.DB.Where("id IN ?", req.ModelIDs).Find(&affectedModels)
	if len(affectedModels) == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 40002, "未找到指定模型")
		return
	}

	// 将这些模型设为 offline
	database.DB.Model(&model.AIModel{}).Where("id IN ?", req.ModelIDs).Update("status", "offline")
	invalidatePublicModelsCache()

	// 构建公告标题和内容
	title := req.AnnouncementTitle
	if title == "" {
		modelNames := make([]string, 0, len(affectedModels))
		for _, m := range affectedModels {
			name := m.DisplayName
			if name == "" {
				name = m.ModelName
			}
			modelNames = append(modelNames, name)
		}
		title = "模型下线通知：" + strings.Join(modelNames, "、")
	}
	content := req.AnnouncementContent
	if content == "" {
		modelList := ""
		for _, m := range affectedModels {
			name := m.DisplayName
			if name == "" {
				name = m.ModelName
			}
			modelList += "- `" + name + "`\n"
		}
		content = "以下模型将于 **" + offlineAt.Format("2006-01-02") + "** 正式下线，请提前完成迁移：\n\n" + modelList + "\n如有疑问请联系客服。"
	}

	// 创建模型下线公告
	expiresAt := offlineAt.AddDate(0, 0, 7) // 公告在下线后7天过期
	// 持久化关联的模型 ID 列表（一键检测时据此跳过已确认下线的模型）
	modelIDsJSON, _ := json.Marshal(req.ModelIDs)
	ann := &model.Announcement{
		Title:      title,
		Content:    content,
		Type:       "model_deprecation",
		Priority:   "high",
		Status:     "active",
		ShowBanner: true,
		ExpiresAt:  &expiresAt,
		ModelIDs:   modelIDsJSON,
	}
	if uid, ok := c.Get("userId"); ok {
		ann.CreatedBy, _ = uid.(uint)
	}
	database.DB.Create(ann)

	response.Success(c, gin.H{
		"affected_count":  len(affectedModels),
		"announcement_id": ann.ID,
		"offline_at":      offlineAt.Format("2006-01-02"),
	})
}

// MarkOfficialDeprecated 根据供应商官方下线公告批量标记本地数据库中的模型为 offline
// POST /api/v1/admin/models/mark-official-deprecated/:supplierCode
//
// 支持的供应商：
//   - baidu_qianfan               (https://cloud.baidu.com/doc/qianfan/s/zmh4stou3)
//   - aliyun_dashscope / alibaba  (https://help.aliyun.com/zh/model-studio/model-depreciation)
//
// 自动创建 model_deprecation 类型公告（含下线日期、推荐替代），与 BulkDeprecate 保持一致
func (h *AIModelHandler) MarkOfficialDeprecated(c *gin.Context) {
	supplierCode := c.Param("supplierCode")

	// 按 supplier code 分发到对应的官方下线名单
	var deprecatedMap map[string]pricescraper.ModelDeprecation
	switch supplierCode {
	case "baidu_qianfan":
		deprecatedMap = pricescraper.GetQianfanDeprecatedModels()
	case "aliyun_dashscope", "alibaba":
		deprecatedMap = pricescraper.GetAliyunDeprecatedModels()
	default:
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code,
			"unsupported supplier: "+supplierCode+" (supported: baidu_qianfan, aliyun_dashscope, alibaba)")
		return
	}

	// 加载供应商
	var supplier model.Supplier
	if err := database.DB.Where("code = ?", supplierCode).First(&supplier).Error; err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrNotFound.Code,
			"supplier not found: "+supplierCode)
		return
	}

	if len(deprecatedMap) == 0 {
		response.Success(c, gin.H{"message": "no deprecated models defined", "marked": 0})
		return
	}

	// 提取所有下线模型名
	deprecatedNames := make([]string, 0, len(deprecatedMap))
	for name := range deprecatedMap {
		deprecatedNames = append(deprecatedNames, name)
	}

	// 查询本地数据库中匹配的模型
	var matchedModels []model.AIModel
	if err := database.DB.
		Where("supplier_id = ? AND LOWER(model_name) IN ?", supplier.ID, deprecatedNames).
		Find(&matchedModels).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrDatabase.Code, err.Error())
		return
	}

	// 区分：之前非 offline 的（本次状态变更）vs 已是 offline 的（仅校正 is_active）
	var newlyOfflineIDs []uint
	var allIDs []uint
	type detail struct {
		ID          uint   `json:"id"`
		ModelName   string `json:"model_name"`
		WasStatus   string `json:"was_status"`
		WasActive   bool   `json:"was_active"`
		RetireDate  string `json:"retire_date"`
		Reason      string `json:"reason"`
		Replacement string `json:"replacement"`
	}
	details := make([]detail, 0, len(matchedModels))
	for _, m := range matchedModels {
		dep := deprecatedMap[strings.ToLower(m.ModelName)]
		details = append(details, detail{
			ID: m.ID, ModelName: m.ModelName, WasStatus: m.Status, WasActive: m.IsActive,
			RetireDate: dep.RetireDate, Reason: dep.Reason, Replacement: dep.Replacement,
		})
		allIDs = append(allIDs, m.ID)
		if m.Status != "offline" {
			newlyOfflineIDs = append(newlyOfflineIDs, m.ID)
		}
	}

	// 批量更新所有匹配模型为 status=offline 且 is_active=false
	updated := int64(0)
	if len(allIDs) > 0 {
		result := database.DB.Model(&model.AIModel{}).
			Where("id IN ?", allIDs).
			Updates(map[string]interface{}{
				"status":    "offline",
				"is_active": false,
			})
		if result.Error != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrDatabase.Code, result.Error.Error())
			return
		}
		updated = int64(len(newlyOfflineIDs))
	}

	// 清除前端公开模型列表缓存
	if updated > 0 {
		invalidatePublicModelsCache()
	}

	// 创建 model_deprecation 公告（仅在有新下线模型时）
	var announcementID uint
	if len(newlyOfflineIDs) > 0 {
		now := time.Now()
		// 按 RetireDate 分组，生成结构化内容
		modelLines := ""
		for _, m := range matchedModels {
			if m.Status == "offline" {
				continue // 仅包含本次新下线的
			}
			isNewly := false
			for _, nid := range newlyOfflineIDs {
				if nid == m.ID {
					isNewly = true
					break
				}
			}
			if !isNewly {
				continue
			}
			dep := deprecatedMap[strings.ToLower(m.ModelName)]
			name := m.DisplayName
			if name == "" {
				name = m.ModelName
			}
			line := "- `" + name + "`"
			if dep.RetireDate != "" {
				line += "（下线日期：" + dep.RetireDate + "）"
			}
			if dep.Replacement != "" {
				line += " → 推荐替代：`" + dep.Replacement + "`"
			}
			if dep.Reason != "" {
				line += "\n  " + dep.Reason
			}
			modelLines += line + "\n"
		}

		title := "【" + supplier.Name + "】官方下线模型通知（" + strconv.Itoa(len(newlyOfflineIDs)) + " 个）"
		content := "根据 **" + supplier.Name + "** 官方下线公告，以下模型已标记为下线，请尽快迁移到推荐替代模型：\n\n" + modelLines + "\n> 数据来源：供应商官方下线公告"

		expiresAt := now.AddDate(0, 1, 0) // 公告保留 1 个月
		modelIDsJSON, _ := json.Marshal(newlyOfflineIDs)
		ann := &model.Announcement{
			Title:      title,
			Content:    content,
			Type:       "model_deprecation",
			Priority:   "high",
			Status:     "active",
			ShowBanner: true,
			ExpiresAt:  &expiresAt,
			ModelIDs:   modelIDsJSON,
		}
		if uid, ok := c.Get("userId"); ok {
			ann.CreatedBy, _ = uid.(uint)
		}
		if err := database.DB.Create(ann).Error; err == nil {
			announcementID = ann.ID
		}
	}

	response.Success(c, gin.H{
		"supplier_code":    supplierCode,
		"deprecated_total": len(deprecatedMap),
		"matched_in_db":    len(matchedModels),
		"newly_marked":     updated,
		"already_offline":  len(matchedModels) - int(updated),
		"announcement_id":  announcementID,
		"details":          details,
	})
}
