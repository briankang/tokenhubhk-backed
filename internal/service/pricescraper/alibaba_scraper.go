package pricescraper

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// =====================================================
// 阿里云（百炼平台）价格爬虫
// 使用 DashScope 原生 API 获取模型列表及定价
// API 端点: GET https://dashscope.aliyuncs.com/api/v1/models
// 该 API 返回完整的模型元数据和分层价格，无需浏览器渲染
// =====================================================

const (
	// alibabaAPIURL 阿里云 DashScope 原生模型列表 API（用于获取模型元数据）
	alibabaAPIURL = "https://dashscope.aliyuncs.com/api/v1/models"
	// alibabaPriceDocURL 阿里云百炼官方定价页（用于 SourceURL 展示和浏览器抓取）
	alibabaPriceDocURL = "https://help.aliyun.com/zh/model-studio/model-pricing"
	// alibabaSupplierName 供应商名称标识
	alibabaSupplierName = "阿里云百炼"
	// alibabaPageSize 每页模型数
	alibabaPageSize = 200
)

// AlibabaScraper 阿里云价格爬虫（基于 API）
type AlibabaScraper struct {
	apiKey     string
	httpClient *http.Client
}

// SetAPIKey 动态更新 API Key（优先使用渠道配置的 Key）
func (s *AlibabaScraper) SetAPIKey(key string) {
	if key != "" {
		s.apiKey = key
	}
}

// NewAlibabaScraper 创建阿里云爬虫实例
func NewAlibabaScraper(apiKey string) *AlibabaScraper {
	// 自定义 Transport：强制 HTTP/1.1（避免 Docker 环境下 HTTP/2 导致 unexpected EOF），
	// TLSNextProto 设为空 map 可彻底禁用 HTTP/2
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
		TLSNextProto: make(map[string]func(authority string, c *tls.Conn) http.RoundTripper), // 强制 HTTP/1.1
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout:  30 * time.Second,
		MaxIdleConns:           10,
		IdleConnTimeout:        90 * time.Second,
		ForceAttemptHTTP2:      false,
	}

	return &AlibabaScraper{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout:   60 * time.Second,
			Transport: transport,
		},
	}
}

// ---- API 响应结构 ----

type alibabaAPIResponse struct {
	Code    interface{} `json:"code"`
	Message string      `json:"message"`
	Success bool        `json:"success"`
	Output  struct {
		Total    int             `json:"total"`
		PageNo   int             `json:"page_no"`
		PageSize int             `json:"page_size"`
		Models   []alibabaModel  `json:"models"`
	} `json:"output"`
}

type alibabaModel struct {
	Model       string              `json:"model"`       // 模型 ID，如 "qwen3-max"
	Name        string              `json:"name"`        // 展示名称，如 "Qwen3-Max"
	Description string              `json:"description"` // 中文描述
	Provider    string              `json:"provider"`    // 提供商，如 "qwen"
	Capabilities []string           `json:"capabilities"` // 能力标签 ["TG", "Reasoning"]
	ModelInfo   *alibabaModelInfo   `json:"model_info"`
	Prices      []alibabaPriceRange `json:"prices"`      // 分层定价
}

type alibabaModelInfo struct {
	ContextWindow   int `json:"context_window"`
	MaxInputTokens  int `json:"max_input_tokens"`
	MaxOutputTokens int `json:"max_output_tokens"`
}

type alibabaPriceRange struct {
	RangeName  string             `json:"range_name"`  // 如 "input<=128k"
	PriceRange string             `json:"price_range"`  // 兼容旧格式
	Prices     []alibabaPriceItem `json:"prices"`       // 实际字段名为 prices
}

type alibabaPriceItem struct {
	Type      string `json:"type"`       // "input_token", "output_token" 等
	Price     string `json:"price"`      // 价格字符串，如 "2.5"
	PriceUnit string `json:"price_unit"` // "每百万tokens"
	PriceName string `json:"price_name"` // 中文名
}

// ScrapePrices 通过 API 获取阿里云模型价格
func (s *AlibabaScraper) ScrapePrices(ctx context.Context) (*ScrapedPriceData, error) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	log.Info("开始通过 API 获取阿里云百炼价格", zap.String("url", alibabaAPIURL))

	// 分页获取所有模型
	var allModels []alibabaModel
	pageNo := 1

	for {
		models, total, err := s.fetchPage(ctx, pageNo)
		if err != nil {
			return nil, fmt.Errorf("获取阿里云模型列表第 %d 页失败: %w", pageNo, err)
		}

		allModels = append(allModels, models...)
		log.Info("获取阿里云模型列表",
			zap.Int("page", pageNo),
			zap.Int("fetched", len(models)),
			zap.Int("total", total))

		if len(allModels) >= total || len(models) == 0 {
			break
		}
		pageNo++
	}

	// 转换为 ScrapedModel
	var scrapedModels []ScrapedModel
	seen := make(map[string]bool) // 去重

	for _, m := range allModels {
		if seen[m.Model] {
			continue
		}
		seen[m.Model] = true

		sm := s.convertModel(m)
		if sm != nil {
			scrapedModels = append(scrapedModels, *sm)
		}
	}

	// v3.5：合并补充价格表（图片/视频/TTS/ASR/Embedding/Rerank 等非 Token 模型）
	// API 返回值优先，仅在 API 未覆盖时用硬编码数据兜底
	scrapedModels = mergeAlibabaWithSupplementary(scrapedModels, getAlibabaSupplementaryPrices())

	log.Info("阿里云 API 价格获取完成",
		zap.Int("api_models", len(allModels)),
		zap.Int("with_prices", len(scrapedModels)))

	return &ScrapedPriceData{
		SupplierName: alibabaSupplierName,
		FetchedAt:    time.Now(),
		Models:       scrapedModels,
		// SourceURL 使用官方定价页（不是 API 端点），便于前端"查看官网定价"跳转
		SourceURL: alibabaPriceDocURL,
	}, nil
}

// fetchPage 获取单页模型数据（含重试）
func (s *AlibabaScraper) fetchPage(ctx context.Context, pageNo int) ([]alibabaModel, int, error) {
	url := fmt.Sprintf("%s?page_no=%d&page_size=%d", alibabaAPIURL, pageNo, alibabaPageSize)

	var lastErr error
	maxRetries := 3

	for attempt := 1; attempt <= maxRetries; attempt++ {
		models, total, err := s.doFetchPage(ctx, url)
		if err == nil {
			return models, total, nil
		}
		lastErr = err

		log := logger.L
		if log != nil {
			log.Warn("阿里云 API 请求失败，准备重试",
				zap.Int("attempt", attempt),
				zap.Int("max_retries", maxRetries),
				zap.Error(err))
		}

		// EOF 错误时关闭空闲连接，强制建立新连接
		if s.httpClient.Transport != nil {
			if t, ok := s.httpClient.Transport.(*http.Transport); ok {
				t.CloseIdleConnections()
			}
		}

		if attempt < maxRetries {
			select {
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
	}

	return nil, 0, fmt.Errorf("重试 %d 次后仍失败: %w", maxRetries, lastErr)
}

// doFetchPage 执行单次 HTTP 请求获取模型数据
func (s *AlibabaScraper) doFetchPage(ctx context.Context, url string) ([]alibabaModel, int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept", "application/json")
	// 使用 Connection: close 避免连接复用导致的 EOF
	req.Header.Set("Connection", "close")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("HTTP 请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, 0, fmt.Errorf("API 返回 %d: %s", resp.StatusCode, string(body[:min(len(body), 500)]))
	}

	var apiResp alibabaAPIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, 0, fmt.Errorf("解析 JSON 失败: %w", err)
	}

	return apiResp.Output.Models, apiResp.Output.Total, nil
}

// convertModel 将 API 模型数据转换为 ScrapedModel
// v3.5：支持全类型模型（LLM/VLM/Image/Video/TTS/ASR/Embedding/Rerank）
// 按 Prices[].Type 推断计费单位，按模型名 + capabilities 推断模型类型
func (s *AlibabaScraper) convertModel(m alibabaModel) *ScrapedModel {
	sm := ScrapedModel{
		ModelName:   m.Model,
		DisplayName: m.Name,
		Currency:    "CNY",
	}

	// 1. 推断模型类型（优先 capabilities，其次模型名）
	sm.ModelType = inferAlibabaModelType(m.Model, m.Capabilities)

	// 2. 遍历所有价格层级，按 type 分类
	// 注意 Aliyun API 的 Type 字段可能包含：
	//   input_token / output_token                 — Token 计费（LLM/VLM/Embedding）
	//   input_image / output_image / image / per_image — 图片计费（按张）
	//   input_video_sec / per_second / per_sec     — 视频/ASR 按秒
	//   input_audio_10k_chars / per_10k_chars      — TTS 按万字符
	//   per_million_chars / per_million_characters  — TTS 按百万字符
	//   per_call / per_request                     — Rerank 按次
	//   per_hour                                   — ASR 按小时
	//   per_minute                                 — ASR 按分钟
	var hasAnyPrice bool
	for _, priceRange := range m.Prices {
		var inputPrice, outputPrice float64
		var tierUnit string
		for _, item := range priceRange.Prices {
			price, err := strconv.ParseFloat(item.Price, 64)
			if err != nil || price <= 0 {
				continue
			}

			typeLower := strings.ToLower(item.Type)
			switch {
			// ---- Token 计费 ----
			case typeLower == "input_token" || typeLower == "input_tokens":
				inputPrice = price
				tierUnit = PricingUnitPerMillionTokens
			case typeLower == "output_token" || typeLower == "output_tokens":
				outputPrice = price
				tierUnit = PricingUnitPerMillionTokens
			// ---- 图片计费（按张）----
			case strings.Contains(typeLower, "image") || strings.Contains(typeLower, "per_image"):
				inputPrice = price
				tierUnit = PricingUnitPerImage
			// ---- 视频 / ASR 按秒 ----
			case typeLower == "per_second" || typeLower == "per_sec" ||
				strings.Contains(typeLower, "video_sec") || strings.Contains(typeLower, "_sec"):
				inputPrice = price
				tierUnit = PricingUnitPerSecond
			// ---- ASR 按分钟 ----
			case strings.Contains(typeLower, "per_minute") || strings.Contains(typeLower, "minute"):
				inputPrice = price
				tierUnit = PricingUnitPerMinute
			// ---- ASR 按小时 ----
			case strings.Contains(typeLower, "per_hour") || strings.Contains(typeLower, "hour"):
				inputPrice = price
				tierUnit = PricingUnitPerHour
			// ---- TTS 按万字符 ----
			case strings.Contains(typeLower, "10k_chars") || strings.Contains(typeLower, "10k_character") ||
				strings.Contains(typeLower, "万字符"):
				inputPrice = price
				tierUnit = PricingUnitPer10kCharacters
			// ---- TTS 按百万字符 ----
			case strings.Contains(typeLower, "million_char") || strings.Contains(typeLower, "百万字符") ||
				strings.Contains(typeLower, "per_m_chars"):
				inputPrice = price
				tierUnit = PricingUnitPerMillionCharacters
			// ---- Rerank 按次 ----
			case strings.Contains(typeLower, "per_call") || strings.Contains(typeLower, "per_request") ||
				typeLower == "call":
				inputPrice = price
				tierUnit = PricingUnitPerCall
			}
			if inputPrice > 0 || outputPrice > 0 {
				hasAnyPrice = true
			}
		}

		if inputPrice == 0 && outputPrice == 0 {
			continue
		}

		// 首个区间作为基础价格 + 计费单位
		if sm.InputPrice == 0 && inputPrice > 0 {
			sm.InputPrice = inputPrice
		}
		if sm.OutputPrice == 0 && outputPrice > 0 {
			sm.OutputPrice = outputPrice
		}
		if sm.PricingUnit == "" && tierUnit != "" {
			sm.PricingUnit = tierUnit
		}

		// 所有区间记录为 PriceTiers
		tierName := priceRange.RangeName
		if tierName == "" {
			tierName = priceRange.PriceRange
		}
		if tierName == "" {
			tierName = "default"
		}
		tier := model.PriceTier{
			Name:        tierName,
			InputPrice:  inputPrice,
			OutputPrice: outputPrice,
		}
		// 仅 Token 计费单位注入阶梯缓存价（按张/按秒等不支持缓存）
		if tierUnit == PricingUnitPerMillionTokens && inputPrice > 0 {
			tier.CacheInputPrice = inputPrice * 0.20
			tier.CacheWritePrice = inputPrice * 1.25
		}
		// 尝试从 price_range 解析 token 范围（Token 单位才有意义）
		if tierUnit == PricingUnitPerMillionTokens {
			parseTierRange(tierName, &tier)
		}
		sm.PriceTiers = append(sm.PriceTiers, tier)
	}

	// 3. 无任何价格 → 跳过
	if !hasAnyPrice {
		return nil
	}

	// 4. 计费单位兜底（基于模型类型）
	if sm.PricingUnit == "" {
		sm.PricingUnit = inferPricingUnitFromName(sm.ModelName, sm.ModelType)
	}

	// 5. 缓存定价：仅 LLM/VLM 启用 both 模式（Embedding/Image/Video/TTS/ASR/Rerank 不支持）
	// 阿里云百炼缓存规则（2026-04）：
	//   - 隐式 auto：输入价 × 0.20（节省 80%，自动触发，无写入费，最小 1024 Token）
	//   - 显式 explicit：输入价 × 0.10（节省 90%，需 cache_control 参数）
	//   - 显式写入：输入价 × 1.25（首次写入溢价）
	if (sm.ModelType == "LLM" || sm.ModelType == "VLM" || sm.ModelType == "Vision") &&
		sm.PricingUnit == PricingUnitPerMillionTokens && sm.InputPrice > 0 {
		sm.SupportsCache = true
		sm.CacheMechanism = "both"
		sm.CacheMinTokens = 1024
		sm.CacheInputPrice = sm.InputPrice * 0.20
		sm.CacheExplicitInputPrice = sm.InputPrice * 0.10
		sm.CacheWritePrice = sm.InputPrice * 1.25
	}

	return &sm
}

// inferAlibabaModelType 推断阿里云模型的类型
// 优先级：Capabilities → 模型名关键字 → 默认 LLM
func inferAlibabaModelType(modelName string, capabilities []string) string {
	name := strings.ToLower(modelName)

	// 1. 通过 capabilities 标签判断（阿里云 API 返回如 ["TG","Reasoning"]）
	for _, c := range capabilities {
		switch strings.ToUpper(c) {
		case "IMAGE_GENERATION", "T2I", "I2I":
			return "ImageGeneration"
		case "VIDEO_GENERATION", "T2V", "I2V":
			return "VideoGeneration"
		case "EMBEDDING", "TEXT_EMBEDDING":
			return "Embedding"
		case "TTS", "SPEECH_SYNTHESIS":
			return "TTS"
		case "ASR", "SPEECH_RECOGNITION":
			return "ASR"
		case "RERANK", "RERANKING":
			return "Rerank"
		}
	}

	// 2. 按模型名推断（与 modeldiscovery / scraper_service 保持一致）
	// 注意检查顺序：video 必须先于 image（"-t2v" 优先于 "wan2"），
	// 否则 "wan2.7-t2v" 会被 "wan2" 吞成 ImageGeneration
	switch {
	case containsAnyStr(name, "-t2v", "video", "wanx-video", "wanx-t2v"):
		return "VideoGeneration"
	case containsAnyStr(name, "-t2i", "image", "wanx-t2i", "wanx2", "wan2", "qwen-image"):
		return "ImageGeneration"
	case containsAnyStr(name, "embedding", "text-embedding", "gte-"):
		if strings.Contains(name, "gte-rerank") {
			return "Rerank"
		}
		return "Embedding"
	case containsAnyStr(name, "rerank"):
		return "Rerank"
	case containsAnyStr(name, "tts", "cosyvoice", "qwen-tts", "qwen3-tts", "speech-synthesis"):
		return "TTS"
	case containsAnyStr(name, "asr", "paraformer", "qwen-asr", "qwen3-asr", "sensevoice", "fun-asr"):
		return "ASR"
	case containsAnyStr(name, "vl", "qvq", "omni", "-mm", "multimodal"):
		return "VLM"
	default:
		return "LLM"
	}
}

// parseTierRange 从价格区间描述解析 token 范围，写入 tier 的 InputMin/InputMax 字段。
// 支持格式（中英文混合）：
//   "输入<=256k"           → (0, 256k]
//   "128k<输入<=256k"      → (128k, 256k]
//   "256k<输入<=1m"        → (256k, 1m]
//   "输入>128k"            → (128k, +∞)
//   "input<=128k"          → (0, 128k]
//   "128k<input<=256k"     → (128k, 256k]
//   "上下文<=32k"          → 同 "输入<=32k"
//   "32k tokens"           → 仅含数字（宽松解析，作上限）
func parseTierRange(rangeStr string, tier *model.PriceTier) {
	// 统一替换中文关键字为英文占位符
	normalized := strings.ToLower(rangeStr)
	normalized = strings.ReplaceAll(normalized, "输入token数", "input")
	normalized = strings.ReplaceAll(normalized, "输入tokens", "input")
	normalized = strings.ReplaceAll(normalized, "输入token", "input")
	normalized = strings.ReplaceAll(normalized, "上下文长度", "input")
	normalized = strings.ReplaceAll(normalized, "上下文", "input")
	normalized = strings.ReplaceAll(normalized, "输入", "input")
	normalized = strings.ReplaceAll(normalized, "输出", "output")
	normalized = strings.ReplaceAll(normalized, "tokens", "")
	normalized = strings.ReplaceAll(normalized, "token", "")
	normalized = strings.TrimSpace(normalized)

	parseTokenCount := func(s string) int64 {
		s = strings.TrimSpace(s)
		s = strings.ReplaceAll(s, ",", "")
		s = strings.ReplaceAll(s, " ", "")
		multiplier := int64(1)
		switch {
		case strings.HasSuffix(s, "b"): // billion
			multiplier = 1_000_000_000
			s = strings.TrimSuffix(s, "b")
		case strings.HasSuffix(s, "m"):
			multiplier = 1_000_000
			s = strings.TrimSuffix(s, "m")
		case strings.HasSuffix(s, "k"):
			multiplier = 1_000
			s = strings.TrimSuffix(s, "k")
		}
		val, err := strconv.ParseFloat(s, 64)
		if err != nil || val < 0 {
			return 0
		}
		return int64(val * float64(multiplier))
	}

	ptr := func(v int64) *int64 { return &v }

	// 情况1: "NNk<input<=NNk" 或 "NNk<input<NNk"（有下限和上限）
	if idx := strings.Index(normalized, "<input"); idx > 0 {
		minStr := strings.TrimSpace(normalized[:idx])
		minVal := parseTokenCount(minStr)
		tier.InputMin = minVal
		tier.InputMinExclusive = true
		// 提取上限：<= 或 <
		rest := normalized[idx+len("<input"):]
		if strings.HasPrefix(rest, "<=") {
			maxVal := parseTokenCount(rest[2:])
			if maxVal > 0 {
				tier.InputMax = ptr(maxVal)
			}
		} else if strings.HasPrefix(rest, "<") {
			maxVal := parseTokenCount(rest[1:])
			if maxVal > 0 {
				tier.InputMax = ptr(maxVal)
				tier.InputMaxExclusive = true
			}
		}
		tier.MinTokens = tier.InputMin // 同步旧字段
		tier.MaxTokens = tier.InputMax
		return
	}

	// 情况2: "input<=NNk" 或 "input<NNk"（只有上限）
	if strings.HasPrefix(normalized, "input") {
		rest := strings.TrimPrefix(normalized, "input")
		rest = strings.TrimSpace(rest)
		if strings.HasPrefix(rest, "<=") {
			maxVal := parseTokenCount(rest[2:])
			if maxVal > 0 {
				tier.InputMax = ptr(maxVal)
				tier.MaxTokens = tier.InputMax
			}
		} else if strings.HasPrefix(rest, "<") {
			maxVal := parseTokenCount(rest[1:])
			if maxVal > 0 {
				tier.InputMax = ptr(maxVal)
				tier.InputMaxExclusive = true
				tier.MaxTokens = tier.InputMax
			}
		} else if strings.HasPrefix(rest, ">") {
			// "input>NNk" → 下界（开区间）
			minVal := parseTokenCount(strings.TrimPrefix(rest, ">"))
			if minVal > 0 {
				tier.InputMin = minVal
				tier.InputMinExclusive = true
				tier.MinTokens = minVal
			}
		} else if strings.HasPrefix(rest, ">=") {
			minVal := parseTokenCount(strings.TrimPrefix(rest, ">="))
			if minVal > 0 {
				tier.InputMin = minVal
				tier.MinTokens = minVal
			}
		}
		return
	}

	// 情况3: "NNk<=" 或 "NNk<" + "input" 位于后方（已在情况1处理，此处处理简化形式）
	// 宽松回退：只有数字，视为上限
	if v := parseTokenCount(normalized); v > 0 {
		tier.InputMax = ptr(v)
		tier.MaxTokens = tier.InputMax
	}
}
