package pricescraper

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"go.uber.org/zap"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// =====================================================
// 火山引擎价格爬虫
// 策略: API 获取模型列表 + 浏览器爬取价格 → 关联匹配
//
// 1. GET https://ark.cn-beijing.volces.com/api/v3/models → 模型元数据
// 2. 浏览器渲染定价页面 → 提取价格表格
// 3. 按模型名称匹配，将价格关联到 API 模型
// =====================================================

const (
	// volcengineAPIURL 火山方舟模型列表 API（OpenAI 兼容）
	volcengineAPIURL = "https://ark.cn-beijing.volces.com/api/v3/models"
	// volcenginePriceURL 火山引擎定价页面（需浏览器渲染）
	volcenginePriceURL = "https://www.volcengine.com/docs/82379/1544106"
	// volcengineSupplierName 供应商名称标识
	volcengineSupplierName = "火山引擎"
)

// VolcengineScraper 火山引擎价格爬虫（API + 浏览器混合）
type VolcengineScraper struct {
	apiKey     string
	browserMgr *BrowserManager
	httpClient *http.Client
}

// SetAPIKey 动态更新 API Key（优先使用渠道配置的 Key）
func (s *VolcengineScraper) SetAPIKey(key string) {
	if key != "" {
		s.apiKey = key
	}
}

// NewVolcengineScraper 创建火山引擎爬虫实例
func NewVolcengineScraper(apiKey string, browserMgr *BrowserManager) *VolcengineScraper {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
		TLSNextProto: make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ForceAttemptHTTP2:     false,
	}
	return &VolcengineScraper{
		apiKey:     apiKey,
		browserMgr: browserMgr,
		httpClient: &http.Client{Timeout: 60 * time.Second, Transport: transport},
	}
}

// ---- API 响应结构 ----

type volcAPIResponse struct {
	Data []volcAPIModel `json:"data"`
}

type volcAPIModel struct {
	ID          string           `json:"id"`        // "doubao-seed-2-0-pro-260215"
	Name        string           `json:"name"`      // "doubao-seed-2.0-pro" (不含版本后缀)
	Domain      string           `json:"domain"`    // "LLM", "VLM", "Embedding"
	Status      string           `json:"status"`    // "Shutdown", "Retiring" 或空（活跃）
	Version     string           `json:"version"`   // "260215"
	TaskType    []string         `json:"task_type"` // ["TextGeneration"]
	Modalities  *volcModalities  `json:"modalities"`
	TokenLimits *volcTokenLimits `json:"token_limits"`
}

type volcModalities struct {
	InputModalities  []string `json:"input_modalities"`
	OutputModalities []string `json:"output_modalities"`
}

type volcTokenLimits struct {
	ContextWindow  int `json:"context_window"`
	MaxInputToken  int `json:"max_input_token_length"`
	MaxOutputToken int `json:"max_output_token_length"`
}

// ScrapePrices 执行火山引擎价格获取
// 1. API 获取模型列表（含元数据）
// 2. 浏览器爬取定价页面
// 3. 匹配关联
func (s *VolcengineScraper) ScrapePrices(ctx context.Context) (*ScrapedPriceData, error) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	// Step 1: API 获取模型列表
	apiModels, err := s.fetchModelList(ctx)
	if err != nil {
		log.Warn("API 获取火山引擎模型列表失败，仅使用浏览器爬取", zap.Error(err))
		// 降级：仅使用浏览器爬取
		return s.scrapeByBrowserOnly(ctx)
	}
	log.Info("API 获取火山引擎模型列表成功", zap.Int("models_count", len(apiModels)))

	// Step 2: 浏览器爬取价格（限时120秒，首次加载需要更多时间）
	browserCtx, browserCancel := context.WithTimeout(ctx, 120*time.Second)
	defer browserCancel()
	priceMap, err := s.scrapePrices(browserCtx)
	if err != nil {
		log.Warn("浏览器爬取价格失败，使用补充价格数据作为降级方案", zap.Error(err))
		// 降级：将 API 模型与补充价格数据（视频/图片/Embedding/LLM 基准价）匹配
		// 比直接返回无价格列表更有价值，确保 doubao-pro/lite/1.5 等主力模型能拿到基准价
		fallbackModels := s.matchModelsWithPrices(apiModels, make(map[string]ScrapedModel))
		if len(fallbackModels) == 0 {
			log.Warn("补充价格也未能匹配，返回空模型列表（不写 DB）")
			return &ScrapedPriceData{
				SupplierName: volcengineSupplierName,
				FetchedAt:    time.Now(),
				Models:       nil,
				SourceURL:    volcenginePriceURL,
			}, nil
		}
		return &ScrapedPriceData{
			SupplierName: volcengineSupplierName,
			FetchedAt:    time.Now(),
			Models:       fallbackModels,
			SourceURL:    volcenginePriceURL,
		}, nil
	}
	log.Info("浏览器爬取火山引擎价格成功", zap.Int("price_entries", len(priceMap)))

	// Step 3: 关联匹配
	scrapedModels := s.matchModelsWithPrices(apiModels, priceMap)

	log.Info("火山引擎价格获取完成",
		zap.Int("api_models", len(apiModels)),
		zap.Int("price_entries", len(priceMap)),
		zap.Int("matched", len(scrapedModels)))

	return &ScrapedPriceData{
		SupplierName: volcengineSupplierName,
		FetchedAt:    time.Now(),
		Models:       scrapedModels,
		SourceURL:    volcenginePriceURL,
	}, nil
}

// fetchModelList 通过 API 获取模型列表（含重试）
func (s *VolcengineScraper) fetchModelList(ctx context.Context) ([]volcAPIModel, error) {
	var lastErr error
	maxRetries := 3

	for attempt := 1; attempt <= maxRetries; attempt++ {
		models, err := s.doFetchModelList(ctx)
		if err == nil {
			return models, nil
		}
		lastErr = err

		log := logger.L
		if log != nil {
			log.Warn("火山引擎 API 请求失败，准备重试",
				zap.Int("attempt", attempt),
				zap.Int("max_retries", maxRetries),
				zap.Error(err))
		}

		// 关闭空闲连接，强制建立新连接
		if s.httpClient.Transport != nil {
			if t, ok := s.httpClient.Transport.(*http.Transport); ok {
				t.CloseIdleConnections()
			}
		}

		if attempt < maxRetries {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
	}

	return nil, fmt.Errorf("重试 %d 次后仍失败: %w", maxRetries, lastErr)
}

// doFetchModelList 执行单次 API 请求获取模型列表
func (s *VolcengineScraper) doFetchModelList(ctx context.Context) ([]volcAPIModel, error) {
	if s.apiKey == "" {
		return nil, fmt.Errorf("VOLCENGINE_API_KEY 未配置")
	}

	req, err := http.NewRequestWithContext(ctx, "GET", volcengineAPIURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "close")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP 请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode != 200 {
		// 检查是否返回了 HTML（常见于 403/WAF 拦截）
		bodyStr := string(body[:min(len(body), 200)])
		if strings.HasPrefix(strings.TrimSpace(bodyStr), "<") {
			return nil, fmt.Errorf("API 返回 HTML（可能被 WAF 拦截），状态码 %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("API 返回 %d: %s", resp.StatusCode, bodyStr)
	}

	// 验证响应是 JSON 而非 HTML
	contentType := resp.Header.Get("Content-Type")
	bodyStr := strings.TrimSpace(string(body))
	if strings.HasPrefix(bodyStr, "<") {
		return nil, fmt.Errorf("响应内容为 HTML 而非 JSON (Content-Type: %s)", contentType)
	}

	var apiResp volcAPIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		// 提供更友好的错误信息
		preview := bodyStr
		if len(preview) > 100 {
			preview = preview[:100]
		}
		return nil, fmt.Errorf("解析 JSON 失败: %w (响应前100字符: %s)", err, preview)
	}

	// 过滤：只保留活跃的模型（排除 Shutdown/Retiring）
	var active []volcAPIModel
	for _, m := range apiResp.Data {
		if m.Status == "" || m.Status == "Active" {
			active = append(active, m)
		}
	}

	return active, nil
}

// scrapePrices 浏览器爬取定价页面，返回模型名 → ScrapedModel 的映射
func (s *VolcengineScraper) scrapePrices(ctx context.Context) (map[string]ScrapedModel, error) {
	html, err := s.browserMgr.FetchRenderedHTML(ctx, volcenginePriceURL)
	if err != nil {
		return nil, fmt.Errorf("获取定价页面失败: %w", err)
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, fmt.Errorf("解析 HTML 失败: %w", err)
	}

	models := extractPriceTables(doc)

	// 构建名称映射（小写）
	priceMap := make(map[string]ScrapedModel, len(models))
	for _, m := range models {
		key := strings.ToLower(m.ModelName)
		priceMap[key] = m
	}

	return priceMap, nil
}

// normalizeModelName 标准化模型名称用于匹配
// API 返回 "doubao-1-5-pro-32k" 或 "doubao-seed-2-0-pro"
// 价格页面使用 "doubao-1.5-pro" 或 "doubao-seed-2.0-pro"
// 核心差异：版本号中 "1-5" vs "1.5", "2-0" vs "2.0"
func normalizeModelName(name string) string {
	name = strings.ToLower(name)
	// 将数字-数字模式替换为数字.数字（如 1-5 → 1.5, 2-0 → 2.0）
	result := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		if i > 0 && i < len(name)-1 && name[i] == '-' &&
			name[i-1] >= '0' && name[i-1] <= '9' &&
			name[i+1] >= '0' && name[i+1] <= '9' {
			result = append(result, '.')
		} else {
			result = append(result, name[i])
		}
	}
	return string(result)
}

// matchModelsWithPrices 将 API 模型列表与爬取的价格关联
func (s *VolcengineScraper) matchModelsWithPrices(apiModels []volcAPIModel, priceMap map[string]ScrapedModel) []ScrapedModel {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	// 合并补充价格数据（视频/图片/Embedding/TTS/ASR）
	supplementary := getVolcengineSupplementaryPrices()
	for k, v := range supplementary {
		if _, exists := priceMap[k]; !exists {
			priceMap[k] = v
		}
	}

	var result []ScrapedModel
	matched := make(map[string]bool) // 记录已匹配的价格条目

	for _, api := range apiModels {
		sm := ScrapedModel{
			ModelName:   api.ID,   // 使用完整 ID 作为模型名（含版本）
			DisplayName: api.Name, // 友好展示名
			Currency:    "CNY",
		}

		// 根据 API 模型元数据推断模型类型和计费单位
		sm.ModelType, sm.PricingUnit = inferModelTypeAndUnit(api)

		// 尝试多种名称匹配价格
		price, found := s.findPrice(api, priceMap)
		if found {
			sm.InputPrice = price.InputPrice
			sm.OutputPrice = price.OutputPrice
			sm.PriceTiers = price.PriceTiers
			sm.PricingUnit = price.PricingUnit
			sm.ModelType = price.ModelType
			matched[strings.ToLower(price.ModelName)] = true
		}

		// 注入缓存定价信息（LLM/VLM 支持 auto 模式缓存）
		annotateVolcengineCacheSupport(&sm)

		// 只添加有价格的模型
		if sm.InputPrice > 0 || sm.OutputPrice > 0 {
			result = append(result, sm)
		}
	}

	// 添加价格表中有但 API 中没有的模型（可能是更高层的名称）
	for key, price := range priceMap {
		if matched[key] {
			continue
		}
		if price.InputPrice > 0 || price.OutputPrice > 0 {
			annotateVolcengineCacheSupport(&price)
			result = append(result, price)
		}
	}

	return result
}

// annotateVolcengineCacheSupport 为火山引擎 LLM/VLM 模型注入缓存定价信息
// 缓存命中价格 = 基础输入价格 × 40%（节省 60%）
// 计费单位：元/百万 Token（命中价）+ 元/百万 Token/小时（存储价，独立计费）
func annotateVolcengineCacheSupport(sm *ScrapedModel) {
	if sm.ModelType != "LLM" && sm.ModelType != "VLM" {
		return // 仅 LLM/VLM 支持 Token 缓存
	}
	if sm.InputPrice <= 0 {
		return // 无基础价格时不注入
	}
	sm.SupportsCache = true
	sm.CacheMechanism = "auto"
	sm.CacheInputPrice = sm.InputPrice * 0.4 // 缓存命中价 = 40% 基础输入价
	// 注：缓存存储价（元/百万Token/小时）因模型不同而有差异，需在管理后台手动配置
}

// inferModelTypeAndUnit 根据 API 模型元数据推断模型类型和计费单位
func inferModelTypeAndUnit(api volcAPIModel) (modelType, pricingUnit string) {
	nameLower := strings.ToLower(api.Name)
	idLower := strings.ToLower(api.ID)
	domainLower := strings.ToLower(api.Domain)

	// 视频生成
	if strings.Contains(nameLower, "seedance") || strings.Contains(idLower, "seedance") {
		return "VideoGeneration", PricingUnitPerMillionTokens
	}
	// 图片生成
	if strings.Contains(nameLower, "seedream") || strings.Contains(idLower, "seedream") {
		return "ImageGeneration", PricingUnitPerImage
	}
	// 语音合成
	if strings.Contains(nameLower, "tts") || strings.Contains(idLower, "tts") ||
		strings.Contains(nameLower, "语音合成") {
		return "TTS", PricingUnitPerKChars
	}
	// 语音识别
	if strings.Contains(nameLower, "asr") || strings.Contains(idLower, "asr") ||
		strings.Contains(nameLower, "语音识别") {
		return "ASR", PricingUnitPerHour
	}
	// Embedding
	if domainLower == "embedding" || strings.Contains(nameLower, "embedding") || strings.Contains(idLower, "embedding") {
		return "Embedding", PricingUnitPerMillionTokens
	}
	// VLM 视觉语言模型
	if domainLower == "vlm" || strings.Contains(nameLower, "vision") {
		return "VLM", PricingUnitPerMillionTokens
	}
	// 默认 LLM
	return "LLM", PricingUnitPerMillionTokens
}

// getVolcengineSupplementaryPrices 返回火山引擎文档页面未覆盖的模型价格
// 数据来源: 火山引擎官网定价页面（2026-04 更新）
// 这些价格因计费单位或文档位置不同，无法被浏览器爬虫自动获取
func getVolcengineSupplementaryPrices() map[string]ScrapedModel {
	prices := map[string]ScrapedModel{
		// ============ 视频生成模型 (Seedance) — 元/百万tokens ============
		// Seedance 2.0 / 2.0 Fast 带"有输入视频 vs 无输入视频"双档定价 + 输出时长最低 Token 下限
		// 详见 https://www.volcengine.com/docs/82379/1544106
		"doubao-seedance-2.0": {
			ModelName: "doubao-seedance-2.0", DisplayName: "Seedance 2.0",
			InputPrice: 0, OutputPrice: 46.0, Currency: "CNY",
			ModelType: "VideoGeneration", PricingUnit: PricingUnitPerMillionTokens,
			PriceTiers: []model.PriceTier{
				{Name: "480p/720p 在线推理 · 输入不含视频", InputMin: 0, InputMinExclusive: true, OutputMin: 0, OutputMinExclusive: true, InputPrice: 0, OutputPrice: 46.0},
				{Name: "480p/720p 在线推理 · 输入包含视频", InputMin: 1, OutputMin: 0, OutputMinExclusive: true, InputPrice: 0, OutputPrice: 28.0},
				{Name: "1080p 在线推理 · 输入不含视频", InputMin: 1080, OutputMin: 0, OutputMinExclusive: true, InputPrice: 0, OutputPrice: 51.0},
				{Name: "1080p 在线推理 · 输入包含视频", InputMin: 1081, OutputMin: 0, OutputMinExclusive: true, InputPrice: 0, OutputPrice: 31.0},
			},
			VideoPricingConfig: &model.VideoPricingConfig{
				RequireInputVideo:  true,
				HasInputVideoPrice: 28.0,
				NoInputVideoPrice:  46.0,
				MinTokensRules: []model.VideoMinTokenRule{
					// 参考 Seedance 2.0 官方最低Token表
					{OutputDurationSec: 5.0, MinTokens: 116508},
					{OutputDurationSec: 10.0, MinTokens: 233016},
				},
			},
		},
		"doubao-seedance-2.0-fast": {
			ModelName: "doubao-seedance-2.0-fast", DisplayName: "Seedance 2.0 Fast",
			InputPrice: 0, OutputPrice: 37.0, Currency: "CNY",
			ModelType: "VideoGeneration", PricingUnit: PricingUnitPerMillionTokens,
			PriceTiers: []model.PriceTier{
				{Name: "在线推理 · 输入不含视频", InputMin: 0, InputMinExclusive: true, OutputMin: 0, OutputMinExclusive: true, InputPrice: 0, OutputPrice: 37.0},
				{Name: "在线推理 · 输入包含视频", InputMin: 1, OutputMin: 0, OutputMinExclusive: true, InputPrice: 0, OutputPrice: 22.0},
			},
			VideoPricingConfig: &model.VideoPricingConfig{
				RequireInputVideo:  true,
				HasInputVideoPrice: 22.0,
				NoInputVideoPrice:  37.0,
				MinTokensRules: []model.VideoMinTokenRule{
					{OutputDurationSec: 5.0, MinTokens: 116508},
					{OutputDurationSec: 10.0, MinTokens: 233016},
				},
			},
		},
		"doubao-seedance-2.0-video-input": {
			ModelName: "doubao-seedance-2.0-video-input", DisplayName: "Seedance 2.0（含视频输入）",
			InputPrice: 0, OutputPrice: 28.0, Currency: "CNY",
			ModelType: "VideoGeneration", PricingUnit: PricingUnitPerMillionTokens,
			VideoPricingConfig: &model.VideoPricingConfig{
				RequireInputVideo:  true,
				HasInputVideoPrice: 28.0,
				MinTokensRules: []model.VideoMinTokenRule{
					{OutputDurationSec: 5.0, MinTokens: 116508},
					{OutputDurationSec: 10.0, MinTokens: 233016},
				},
			},
		},
		"doubao-seedance-1.5-pro": {
			ModelName: "doubao-seedance-1.5-pro", DisplayName: "Seedance 1.5 Pro",
			InputPrice: 0, OutputPrice: 16.0, Currency: "CNY",
			ModelType: "VideoGeneration", PricingUnit: PricingUnitPerMillionTokens,
			PriceTiers: seedance15PriceTiers(),
			VideoPricingConfig: &model.VideoPricingConfig{
				SupportDraft:    true,
				DraftCoefSilent: 0.7,
				DraftCoefAudio:  0.6,
			},
		},
		"doubao-seedance-1.0-pro": {
			ModelName: "doubao-seedance-1.0-pro", DisplayName: "Seedance 1.0 Pro",
			InputPrice: 0, OutputPrice: 15.0, Currency: "CNY",
			ModelType: "VideoGeneration", PricingUnit: PricingUnitPerMillionTokens,
			PriceTiers: seedanceOnlineOfflineTiers(15.0),
		},
		"doubao-seedance-1.0-pro-fast": {
			ModelName: "doubao-seedance-1.0-pro-fast", DisplayName: "Seedance 1.0 Pro Fast",
			InputPrice: 0, OutputPrice: 4.2, Currency: "CNY",
			ModelType: "VideoGeneration", PricingUnit: PricingUnitPerMillionTokens,
			PriceTiers: seedanceOnlineOfflineTiers(4.2),
		},
		"doubao-seedance-1.0-lite": {
			ModelName: "doubao-seedance-1.0-lite", DisplayName: "Seedance 1.0 Lite",
			InputPrice: 0, OutputPrice: 10.0, Currency: "CNY",
			ModelType: "VideoGeneration", PricingUnit: PricingUnitPerMillionTokens,
			PriceTiers: seedanceOnlineOfflineTiers(10.0),
		},

		// ============ 图片生成模型 (Seedream) — 元/张 ============
		"doubao-seedream-5.0-lite": {
			ModelName: "doubao-seedream-5.0-lite", DisplayName: "Seedream 5.0 Lite",
			InputPrice: 0.22, OutputPrice: 0, Currency: "CNY",
			ModelType: "ImageGeneration", PricingUnit: PricingUnitPerImage,
		},
		"doubao-seedream-4.5": {
			ModelName: "doubao-seedream-4.5", DisplayName: "Seedream 4.5",
			InputPrice: 0.25, OutputPrice: 0, Currency: "CNY",
			ModelType: "ImageGeneration", PricingUnit: PricingUnitPerImage,
		},
		"doubao-seedream-4.0": {
			ModelName: "doubao-seedream-4.0", DisplayName: "Seedream 4.0",
			InputPrice: 0.20, OutputPrice: 0, Currency: "CNY",
			ModelType: "ImageGeneration", PricingUnit: PricingUnitPerImage,
		},

		// ============ Embedding 模型 — 元/百万tokens ============
		"doubao-embedding-large": {
			ModelName: "doubao-embedding-large", DisplayName: "Doubao Embedding Large",
			InputPrice: 0.7, OutputPrice: 0, Currency: "CNY",
			ModelType: "Embedding", PricingUnit: PricingUnitPerMillionTokens,
		},
		"doubao-embedding-large-text": {
			ModelName: "doubao-embedding-large-text", DisplayName: "Doubao Embedding Large Text",
			InputPrice: 0.7, OutputPrice: 0, Currency: "CNY",
			ModelType: "Embedding", PricingUnit: PricingUnitPerMillionTokens,
		},
		"doubao-embedding": {
			ModelName: "doubao-embedding", DisplayName: "Doubao Embedding",
			InputPrice: 0.5, OutputPrice: 0, Currency: "CNY",
			ModelType: "Embedding", PricingUnit: PricingUnitPerMillionTokens,
		},
		"doubao-embedding-text": {
			ModelName: "doubao-embedding-text", DisplayName: "Doubao Embedding Text",
			InputPrice: 0.5, OutputPrice: 0, Currency: "CNY",
			ModelType: "Embedding", PricingUnit: PricingUnitPerMillionTokens,
		},
		"doubao-embedding-vision-text": {
			ModelName: "doubao-embedding-vision-text", DisplayName: "Doubao Embedding Vision-Text",
			InputPrice: 0.7, OutputPrice: 0, Currency: "CNY",
			ModelType: "Embedding", PricingUnit: PricingUnitPerMillionTokens,
		},
		"doubao-embedding-vision": {
			ModelName: "doubao-embedding-vision", DisplayName: "Doubao Embedding Vision",
			InputPrice: 0.7, OutputPrice: 0, Currency: "CNY",
			ModelType: "Embedding", PricingUnit: PricingUnitPerMillionTokens,
		},

		// ============ 语音合成 (TTS) — 元/万字符 ============
		"doubao-tts-2.0": {
			ModelName: "doubao-tts-2.0", DisplayName: "豆包语音合成 2.0",
			InputPrice: 2.8, OutputPrice: 0, Currency: "CNY",
			ModelType: "TTS", PricingUnit: PricingUnitPerKChars,
		},
		"doubao-tts-hd": {
			ModelName: "doubao-tts-hd", DisplayName: "大模型语音合成",
			InputPrice: 4.5, OutputPrice: 0, Currency: "CNY",
			ModelType: "TTS", PricingUnit: PricingUnitPerKChars,
		},

		// ============ 语音识别 (ASR) — 元/小时 ============
		"doubao-asr-streaming-2.0": {
			ModelName: "doubao-asr-streaming-2.0", DisplayName: "豆包流式语音识别 2.0",
			InputPrice: 0.9, OutputPrice: 0, Currency: "CNY",
			ModelType: "ASR", PricingUnit: PricingUnitPerHour,
		},
		"doubao-asr-hd": {
			ModelName: "doubao-asr-hd", DisplayName: "大模型流式语音识别",
			InputPrice: 4.0, OutputPrice: 0, Currency: "CNY",
			ModelType: "ASR", PricingUnit: PricingUnitPerHour,
		},
		"doubao-asr-file": {
			ModelName: "doubao-asr-file", DisplayName: "录音文件识别（标准版）",
			InputPrice: 2.0, OutputPrice: 0, Currency: "CNY",
			ModelType: "ASR", PricingUnit: PricingUnitPerHour,
		},
	}

	// ============ LLM / VLM 主力模型阶梯备用价格（2026-04 官网价）============
	// 用途：浏览器爬取失败时的降级保底数据，确保 doubao 主力模型不丢价格。
	// 阶梯边界基于官方计费规则：按单次请求的输入上下文 token 数分段计费。
	// 来源：https://www.volcengine.com/docs/82379/1544106
	//
	// 注意：这些价格会被浏览器成功爬取的结果覆盖，仅作 fallback 使用。
	i64 := func(v int64) *int64 { return &v } // 辅助函数：取 int64 指针

	llmPrices := map[string]ScrapedModel{
		// ---- doubao-pro 系列（上下文 ≤32k / >32k 阶梯）----
		// prefix "doubao-pro-" 可匹配 doubao-pro-4k/32k/128k/256k 等所有变体
		"doubao-pro": {
			ModelName: "doubao-pro", DisplayName: "Doubao Pro",
			InputPrice: 0.8, OutputPrice: 2.0, Currency: "CNY",
			ModelType: "LLM", PricingUnit: PricingUnitPerMillionTokens,
			PriceTiers: []model.PriceTier{
				{Name: "≤32k上下文", InputMax: i64(32000), InputPrice: 0.8, OutputPrice: 2.0},
				{Name: ">32k上下文", InputMin: 32000, InputMinExclusive: true, InputPrice: 5.0, OutputPrice: 9.0},
			},
		},
		// ---- doubao-lite 系列 ----
		"doubao-lite": {
			ModelName: "doubao-lite", DisplayName: "Doubao Lite",
			InputPrice: 0.3, OutputPrice: 0.6, Currency: "CNY",
			ModelType: "LLM", PricingUnit: PricingUnitPerMillionTokens,
			PriceTiers: []model.PriceTier{
				{Name: "≤32k上下文", InputMax: i64(32000), InputPrice: 0.3, OutputPrice: 0.6},
				{Name: ">32k上下文", InputMin: 32000, InputMinExclusive: true, InputPrice: 0.8, OutputPrice: 0.6},
			},
		},
		// ---- doubao-1.5-pro 系列 ----
		// API 返回 ID 格式：doubao-1-5-pro-32k-xxxxx → 标准化后 doubao-1.5-pro-32k
		// prefix "doubao-1.5-pro-" 可匹配所有变体
		"doubao-1.5-pro": {
			ModelName: "doubao-1.5-pro", DisplayName: "Doubao 1.5 Pro",
			InputPrice: 0.45, OutputPrice: 1.0, Currency: "CNY",
			ModelType: "LLM", PricingUnit: PricingUnitPerMillionTokens,
			PriceTiers: []model.PriceTier{
				{Name: "≤32k上下文", InputMax: i64(32000), InputPrice: 0.45, OutputPrice: 1.0},
				{Name: ">32k上下文", InputMin: 32000, InputMinExclusive: true, InputPrice: 3.0, OutputPrice: 9.0},
			},
		},
		// ---- doubao-1.5-lite 系列（无阶梯，超低价）----
		"doubao-1.5-lite": {
			ModelName: "doubao-1.5-lite", DisplayName: "Doubao 1.5 Lite",
			InputPrice: 0.2, OutputPrice: 0.3, Currency: "CNY",
			ModelType: "LLM", PricingUnit: PricingUnitPerMillionTokens,
		},
		// ---- doubao-1.5-vision 系列（VLM）----
		"doubao-1.5-vision": {
			ModelName: "doubao-1.5-vision", DisplayName: "Doubao 1.5 Vision",
			InputPrice: 0.3, OutputPrice: 0.3, Currency: "CNY",
			ModelType: "VLM", PricingUnit: PricingUnitPerMillionTokens,
			PriceTiers: []model.PriceTier{
				{Name: "≤32k上下文", InputMax: i64(32000), InputPrice: 0.3, OutputPrice: 0.3},
				{Name: ">32k上下文", InputMin: 32000, InputMinExclusive: true, InputPrice: 1.0, OutputPrice: 1.0},
			},
		},
		// ---- doubao-character 系列（角色扮演，同 pro 价格）----
		"doubao-character": {
			ModelName: "doubao-character", DisplayName: "Doubao Character",
			InputPrice: 0.8, OutputPrice: 2.0, Currency: "CNY",
			ModelType: "LLM", PricingUnit: PricingUnitPerMillionTokens,
		},
		// ---- doubao-thinking-pro（深度思考，高价）----
		"doubao-thinking-pro": {
			ModelName: "doubao-thinking-pro", DisplayName: "Doubao Thinking Pro",
			InputPrice: 4.0, OutputPrice: 16.0, Currency: "CNY",
			ModelType: "LLM", PricingUnit: PricingUnitPerMillionTokens,
		},
		// ---- doubao-seed-2.0 系列（VLM，旗舰多模态）----
		"doubao-seed-2.0": {
			ModelName: "doubao-seed-2.0", DisplayName: "Doubao Seed 2.0",
			InputPrice: 3.0, OutputPrice: 9.0, Currency: "CNY",
			ModelType: "VLM", PricingUnit: PricingUnitPerMillionTokens,
		},
		"doubao-seed-2.0-pro": {
			ModelName: "doubao-seed-2.0-pro", DisplayName: "Doubao Seed 2.0 Pro",
			InputPrice: 3.0, OutputPrice: 9.0, Currency: "CNY",
			ModelType: "VLM", PricingUnit: PricingUnitPerMillionTokens,
		},
		"doubao-seed-2.0-lite": {
			ModelName: "doubao-seed-2.0-lite", DisplayName: "Doubao Seed 2.0 Lite",
			InputPrice: 0.3, OutputPrice: 0.9, Currency: "CNY",
			ModelType: "VLM", PricingUnit: PricingUnitPerMillionTokens,
		},
		// ---- doubao-pro-m（MoE 架构）----
		"doubao-pro-m": {
			ModelName: "doubao-pro-m", DisplayName: "Doubao Pro M",
			InputPrice: 0.45, OutputPrice: 1.0, Currency: "CNY",
			ModelType: "LLM", PricingUnit: PricingUnitPerMillionTokens,
		},
	}

	for k, v := range llmPrices {
		if _, exists := prices[k]; !exists {
			prices[k] = v
		}
	}

	return prices
}

func seedance15PriceTiers() []model.PriceTier {
	return []model.PriceTier{
		{Name: "在线推理 · 有声视频", InputMin: 0, InputMinExclusive: true, OutputMin: 0, OutputMinExclusive: true, InputPrice: 0, OutputPrice: 16.0},
		{Name: "在线推理 · 无声视频", InputMin: 1, OutputMin: 0, OutputMinExclusive: true, InputPrice: 0, OutputPrice: 8.0},
		{Name: "离线推理 · 有声视频", InputMin: 2, OutputMin: 0, OutputMinExclusive: true, InputPrice: 0, OutputPrice: 8.0},
		{Name: "离线推理 · 无声视频", InputMin: 3, OutputMin: 0, OutputMinExclusive: true, InputPrice: 0, OutputPrice: 4.0},
	}
}

func seedanceOnlineOfflineTiers(online float64) []model.PriceTier {
	return []model.PriceTier{
		{Name: "在线推理", InputMin: 0, InputMinExclusive: true, OutputMin: 0, OutputMinExclusive: true, InputPrice: 0, OutputPrice: online},
		{Name: "离线推理", InputMin: 1, OutputMin: 0, OutputMinExclusive: true, InputPrice: 0, OutputPrice: online * 0.5},
	}
}

// findPrice 为 API 模型查找匹配的价格
func (s *VolcengineScraper) findPrice(api volcAPIModel, priceMap map[string]ScrapedModel) (ScrapedModel, bool) {
	// 尝试匹配策略（按优先级）：
	candidates := []string{
		strings.ToLower(api.Name),    // 原始 name
		normalizeModelName(api.Name), // 标准化 name（1-5 → 1.5）
		strings.ToLower(api.ID),      // 完整 ID
		normalizeModelName(api.ID),   // 标准化 ID
	}

	// 去除版本号后缀的 ID（如 doubao-seed-2-0-pro-260215 → doubao-seed-2-0-pro）
	if api.Version != "" && strings.HasSuffix(api.ID, "-"+api.Version) {
		baseID := strings.TrimSuffix(api.ID, "-"+api.Version)
		candidates = append(candidates, strings.ToLower(baseID), normalizeModelName(baseID))
	}

	// 对 Seedance/Seedream 类模型尝试额外的前缀匹配
	// API 可能返回 "seedance-2.0" 但补充数据用 "doubao-seedance-2.0"
	nameLower := strings.ToLower(api.Name)
	if strings.Contains(nameLower, "seedance") || strings.Contains(nameLower, "seedream") {
		candidates = append(candidates, "doubao-"+nameLower, "doubao-"+normalizeModelName(api.Name))
	}

	// Embedding 模型：API 返回名称如 "doubao-embedding-large-text" 但补充数据用 "doubao-embedding-large"
	// 尝试去除 "-text" 后缀匹配
	if strings.Contains(nameLower, "embedding") {
		trimmed := strings.TrimSuffix(nameLower, "-text")
		candidates = append(candidates, trimmed)
		// "doubao-embedding-vision" → "doubao-embedding-vision-text"
		if strings.Contains(nameLower, "vision") && !strings.Contains(nameLower, "vision-text") {
			candidates = append(candidates, nameLower+"-text")
		}
	}

	// 精确匹配
	for _, candidate := range candidates {
		if price, ok := priceMap[candidate]; ok {
			return price, true
		}
	}

	// 前缀模糊匹配（用于带版本日期后缀的模型名）
	// 例如 DB 中 "doubao-seedream-4-0-250828" → 标准化为 "doubao-seedream-4.0-250828"
	// 补充数据中 "doubao-seedream-4.0" → 前缀匹配成功
	for _, candidate := range candidates {
		for key, price := range priceMap {
			if strings.HasPrefix(candidate, key) && len(candidate) > len(key) {
				// 确保前缀后面跟的是分隔符（-）或版本日期
				rest := candidate[len(key):]
				if rest[0] == '-' {
					return price, true
				}
			}
		}
	}

	return ScrapedModel{}, false
}

// scrapeByBrowserOnly 降级方案：仅使用浏览器爬取（API 不可用时）
func (s *VolcengineScraper) scrapeByBrowserOnly(ctx context.Context) (*ScrapedPriceData, error) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	log.Info("使用降级方案：仅浏览器爬取火山引擎价格", zap.String("url", volcenginePriceURL))

	html, err := s.browserMgr.FetchRenderedHTML(ctx, volcenginePriceURL)
	if err != nil {
		return nil, fmt.Errorf("获取火山引擎渲染页面失败: %w", err)
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, fmt.Errorf("解析火山引擎 HTML 失败: %w", err)
	}

	models := extractPriceTables(doc)

	// 合并补充价格数据（视频/图片/Embedding/TTS/ASR）
	existingNames := make(map[string]bool, len(models))
	for _, m := range models {
		existingNames[strings.ToLower(m.ModelName)] = true
	}
	for _, sp := range getVolcengineSupplementaryPrices() {
		if !existingNames[strings.ToLower(sp.ModelName)] {
			models = append(models, sp)
		}
	}

	log.Info("火山引擎浏览器爬取完成（含补充数据）", zap.Int("models_count", len(models)))

	return &ScrapedPriceData{
		SupplierName: volcengineSupplierName,
		FetchedAt:    time.Now(),
		Models:       models,
		SourceURL:    volcenginePriceURL,
	}, nil
}

// apiModelsToScrapedData 将 API 模型列表转换为 ScrapedPriceData（无价格）
func (s *VolcengineScraper) apiModelsToScrapedData(apiModels []volcAPIModel) *ScrapedPriceData {
	var models []ScrapedModel
	for _, api := range apiModels {
		models = append(models, ScrapedModel{
			ModelName:   api.ID,
			DisplayName: api.Name,
			Currency:    "CNY",
			Warnings:    []string{"API 无价格数据，请检查定价页面"},
		})
	}
	return &ScrapedPriceData{
		SupplierName: volcengineSupplierName,
		FetchedAt:    time.Now(),
		Models:       models,
		SourceURL:    volcengineAPIURL,
	}
}
