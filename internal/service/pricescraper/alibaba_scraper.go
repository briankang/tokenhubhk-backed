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

// Alibaba DashScope price scraper.
// It uses the native model list API and supplements known official pricing gaps.

const (
	alibabaAPIURL       = "https://dashscope.aliyuncs.com/api/v1/models"
	alibabaPriceDocURL  = "https://help.aliyun.com/zh/model-studio/model-pricing"
	alibabaSupplierName = "阿里云百炼"
	alibabaPageSize     = 200
)

type AlibabaScraper struct {
	apiKey     string
	httpClient *http.Client
}

// SetAPIKey updates the scraper API key, usually from channel config.
func (s *AlibabaScraper) SetAPIKey(key string) {
	if key != "" {
		s.apiKey = key
	}
}

func NewAlibabaScraper(apiKey string) *AlibabaScraper {
	// 鑷畾涔?Transport锛氬己鍒?HTTP/1.1锛堥伩鍏?Docker 鐜涓?HTTP/2 瀵艰嚧 unexpected EOF锛夛紝
	// TLSNextProto 璁句负绌?map 鍙交搴曠鐢?HTTP/2
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
		TLSNextProto: make(map[string]func(authority string, c *tls.Conn) http.RoundTripper), // 寮哄埗 HTTP/1.1
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     false,
	}

	return &AlibabaScraper{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout:   60 * time.Second,
			Transport: transport,
		},
	}
}

// ---- API 鍝嶅簲缁撴瀯 ----

type alibabaAPIResponse struct {
	Code    interface{} `json:"code"`
	Message string      `json:"message"`
	Success bool        `json:"success"`
	Output  struct {
		Total    int            `json:"total"`
		PageNo   int            `json:"page_no"`
		PageSize int            `json:"page_size"`
		Models   []alibabaModel `json:"models"`
	} `json:"output"`
}

type alibabaModel struct {
	Model        string              `json:"model"`        // 妯″瀷 ID锛屽 "qwen3-max"
	Name         string              `json:"name"`         // 灞曠ず鍚嶇О锛屽 "Qwen3-Max"
	Description  string              `json:"description"`  // 涓枃鎻忚堪
	Provider     string              `json:"provider"`     // 鎻愪緵鍟嗭紝濡?"qwen"
	Capabilities []string            `json:"capabilities"` // 鑳藉姏鏍囩 ["TG", "Reasoning"]
	ModelInfo    *alibabaModelInfo   `json:"model_info"`
	Prices       []alibabaPriceRange `json:"prices"` // 鍒嗗眰瀹氫环
}

type alibabaModelInfo struct {
	ContextWindow   int `json:"context_window"`
	MaxInputTokens  int `json:"max_input_tokens"`
	MaxOutputTokens int `json:"max_output_tokens"`
}

type alibabaPriceRange struct {
	RangeName  string             `json:"range_name"`
	PriceRange string             `json:"price_range"`
	Prices     []alibabaPriceItem `json:"prices"`
}

type alibabaPriceItem struct {
	Type      string `json:"type"`
	Price     string `json:"price"`
	PriceUnit string `json:"price_unit"`
	PriceName string `json:"price_name"`
}

func (s *AlibabaScraper) ScrapePrices(ctx context.Context) (*ScrapedPriceData, error) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	log.Info("start fetching Alibaba DashScope prices", zap.String("url", alibabaAPIURL))

	var allModels []alibabaModel
	pageNo := 1

	for {
		models, total, err := s.fetchPage(ctx, pageNo)
		if err != nil {
			return nil, fmt.Errorf("鑾峰彇闃块噷浜戞ā鍨嬪垪琛ㄧ %d 椤靛け璐? %w", pageNo, err)
		}

		allModels = append(allModels, models...)
		log.Info("fetched Alibaba model page",
			zap.Int("page", pageNo),
			zap.Int("fetched", len(models)),
			zap.Int("total", total))

		if len(allModels) >= total || len(models) == 0 {
			break
		}
		pageNo++
	}

	// 杞崲涓?ScrapedModel
	var scrapedModels []ScrapedModel
	seen := make(map[string]bool) // 鍘婚噸

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

	// Merge supplementary prices for non-token models when the API omits them.
	scrapedModels = mergeAlibabaWithSupplementary(scrapedModels, getAlibabaSupplementaryPrices())

	// Merge explicit thinking-output prices documented on the official pricing page.
	scrapedModels = applyAlibabaThinkingOverrides(scrapedModels, getAlibabaThinkingPrices())

	log.Info("闃块噷浜?API 浠锋牸鑾峰彇瀹屾垚",
		zap.Int("api_models", len(allModels)),
		zap.Int("with_prices", len(scrapedModels)))

	return &ScrapedPriceData{
		SupplierName: alibabaSupplierName,
		FetchedAt:    time.Now(),
		Models:       scrapedModels,
		// SourceURL 浣跨敤瀹樻柟瀹氫环椤碉紙涓嶆槸 API 绔偣锛夛紝渚夸簬鍓嶇"鏌ョ湅瀹樼綉瀹氫环"璺宠浆
		SourceURL: alibabaPriceDocURL,
	}, nil
}

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
			log.Warn("Alibaba API request failed, retrying",
				zap.Int("attempt", attempt),
				zap.Int("max_retries", maxRetries),
				zap.Error(err))
		}

		// Close idle connections after transient EOF/network errors.
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

	return nil, 0, fmt.Errorf("閲嶈瘯 %d 娆″悗浠嶅け璐? %w", maxRetries, lastErr)
}

// doFetchPage 鎵ц鍗曟 HTTP 璇锋眰鑾峰彇妯″瀷鏁版嵁
func (s *AlibabaScraper) doFetchPage(ctx context.Context, url string) ([]alibabaModel, int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept", "application/json")
	// 浣跨敤 Connection: close 閬垮厤杩炴帴澶嶇敤瀵艰嚧鐨?EOF
	req.Header.Set("Connection", "close")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("HTTP 璇锋眰澶辫触: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("璇诲彇鍝嶅簲澶辫触: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, 0, fmt.Errorf("API 杩斿洖 %d: %s", resp.StatusCode, string(body[:min(len(body), 500)]))
	}

	var apiResp alibabaAPIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, 0, fmt.Errorf("瑙ｆ瀽 JSON 澶辫触: %w", err)
	}

	return apiResp.Output.Models, apiResp.Output.Total, nil
}

// convertModel 灏?API 妯″瀷鏁版嵁杞崲涓?ScrapedModel
// v3.5锛氭敮鎸佸叏绫诲瀷妯″瀷锛圠LM/VLM/Image/Video/TTS/ASR/Embedding/Rerank锛?// 鎸?Prices[].Type 鎺ㄦ柇璁¤垂鍗曚綅锛屾寜妯″瀷鍚?+ capabilities 鎺ㄦ柇妯″瀷绫诲瀷
func (s *AlibabaScraper) convertModel(m alibabaModel) *ScrapedModel {
	sm := ScrapedModel{
		ModelName:   m.Model,
		DisplayName: m.Name,
		Currency:    "CNY",
	}

	// Infer model type from capabilities first, then model name.
	sm.ModelType = inferAlibabaModelType(m.Model, m.Capabilities)

	// 2. 閬嶅巻鎵€鏈変环鏍煎眰绾э紝鎸?type 鍒嗙被
	// 娉ㄦ剰 Aliyun API 鐨?Type 瀛楁鍙兘鍖呭惈锛?	//   input_token / output_token                 鈥?Token 璁¤垂锛圠LM/VLM/Embedding锛?	//   input_image / output_image / image / per_image 鈥?鍥剧墖璁¤垂锛堟寜寮狅級
	//   input_video_sec / per_second / per_sec     鈥?瑙嗛/ASR 鎸夌
	//   input_audio_10k_chars / per_10k_chars      鈥?TTS 鎸変竾瀛楃
	//   per_million_chars / per_million_characters  鈥?TTS 鎸夌櫨涓囧瓧绗?	//   per_call / per_request                     鈥?Rerank 鎸夋
	//   per_hour / per_minute                     duration-based ASR units
	var hasAnyPrice bool
	for _, priceRange := range m.Prices {
		var inputPrice, outputPrice float64
		var tierUnit string
		var outputThinkingPrice float64
		for _, item := range priceRange.Prices {
			price, err := strconv.ParseFloat(item.Price, 64)
			if err != nil || price <= 0 {
				continue
			}

			typeLower := strings.ToLower(item.Type)
			priceNameLower := strings.ToLower(item.PriceName)
			// Identify thinking/reasoning output prices when the API exposes a split.
			isThinking := strings.Contains(typeLower, "thinking") ||
				strings.Contains(typeLower, "reasoning") ||
				strings.Contains(priceNameLower, "thinking") ||
				strings.Contains(item.PriceName, "思考模式") ||
				strings.Contains(item.PriceName, "思维链")

			switch {
			// ---- Token 璁¤垂 ----
			case typeLower == "input_token" || typeLower == "input_tokens":
				inputPrice = price
				tierUnit = PricingUnitPerMillionTokens
			case typeLower == "output_token" || typeLower == "output_tokens":
				if isThinking {
					outputThinkingPrice = price
				} else {
					outputPrice = price
				}
				tierUnit = PricingUnitPerMillionTokens
				// 鎬濊€冩ā寮忎笓鐢?Type锛堣嫢闃块噷浜?API 鏈潵鏀圭敤鐙珛 type 瀛楁锛?			case typeLower == "output_token_thinking" || typeLower == "output_tokens_thinking":
				outputThinkingPrice = price
				tierUnit = PricingUnitPerMillionTokens
			// ---- 鍥剧墖璁¤垂锛堟寜寮狅級----
			case strings.Contains(typeLower, "image") || strings.Contains(typeLower, "per_image"):
				inputPrice = price
				tierUnit = PricingUnitPerImage
			// ---- 瑙嗛 / ASR 鎸夌 ----
			case typeLower == "per_second" || typeLower == "per_sec" ||
				strings.Contains(typeLower, "video_sec") || strings.Contains(typeLower, "_sec"):
				inputPrice = price
				tierUnit = PricingUnitPerSecond
			// ---- ASR 鎸夊垎閽?----
			case strings.Contains(typeLower, "per_minute") || strings.Contains(typeLower, "minute"):
				inputPrice = price
				tierUnit = PricingUnitPerMinute
			// ---- ASR 鎸夊皬鏃?----
			case strings.Contains(typeLower, "per_hour") || strings.Contains(typeLower, "hour"):
				inputPrice = price
				tierUnit = PricingUnitPerHour
			// ---- TTS 鎸変竾瀛楃 ----
			case strings.Contains(typeLower, "10k_chars") || strings.Contains(typeLower, "10k_character") ||
				strings.Contains(typeLower, "万字符"):
				inputPrice = price
				tierUnit = PricingUnitPer10kCharacters
			// ---- TTS 鎸夌櫨涓囧瓧绗?----
			case strings.Contains(typeLower, "million_char") || strings.Contains(typeLower, "鐧句竾瀛楃") ||
				strings.Contains(typeLower, "per_m_chars"):
				inputPrice = price
				tierUnit = PricingUnitPerMillionCharacters
			// ---- Rerank 鎸夋 ----
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

		// 棣栦釜鍖洪棿浣滀负鍩虹浠锋牸 + 璁¤垂鍗曚綅
		if sm.InputPrice == 0 && inputPrice > 0 {
			sm.InputPrice = inputPrice
		}
		if sm.OutputPrice == 0 && outputPrice > 0 {
			sm.OutputPrice = outputPrice
		}
		if sm.OutputPriceThinking == 0 && outputThinkingPrice > 0 {
			sm.OutputPriceThinking = outputThinkingPrice
		}
		if sm.PricingUnit == "" && tierUnit != "" {
			sm.PricingUnit = tierUnit
		}

		// 鎵€鏈夊尯闂磋褰曚负 PriceTiers
		tierName := priceRange.RangeName
		if tierName == "" {
			tierName = priceRange.PriceRange
		}
		if tierName == "" {
			tierName = "default"
		}
		tier := model.PriceTier{
			Name:                tierName,
			InputPrice:          inputPrice,
			OutputPrice:         outputPrice,
			OutputPriceThinking: outputThinkingPrice,
		}
		if tierUnit == PricingUnitPerMillionTokens && inputPrice > 0 {
			tier.CacheInputPrice = inputPrice * 0.20
			tier.CacheWritePrice = inputPrice * 1.25
		}
		if tierUnit == PricingUnitPerMillionTokens {
			parseTierRange(tierName, &tier)
		}
		sm.PriceTiers = append(sm.PriceTiers, tier)
	}

	// 3. 鏃犱换浣曚环鏍?鈫?璺宠繃
	if !hasAnyPrice {
		return nil
	}

	// 4. 璁¤垂鍗曚綅鍏滃簳锛堝熀浜庢ā鍨嬬被鍨嬶級
	if sm.PricingUnit == "" {
		sm.PricingUnit = inferPricingUnitFromName(sm.ModelName, sm.ModelType)
	}

	// 5. 缂撳瓨瀹氫环锛氫粎 LLM/VLM 鍚敤 both 妯″紡锛圗mbedding/Image/Video/TTS/ASR/Rerank 涓嶆敮鎸侊級
	// 闃块噷浜戠櫨鐐肩紦瀛樿鍒欙紙2026-04锛夛細
	//   - 闅愬紡 auto锛氳緭鍏ヤ环 脳 0.20锛堣妭鐪?80%锛岃嚜鍔ㄨЕ鍙戯紝鏃犲啓鍏ヨ垂锛屾渶灏?1024 Token锛?	//   - 鏄惧紡 explicit锛氳緭鍏ヤ环 脳 0.10锛堣妭鐪?90%锛岄渶 cache_control 鍙傛暟锛?	//   - 鏄惧紡鍐欏叆锛氳緭鍏ヤ环 脳 1.25锛堥娆″啓鍏ユ孩浠凤級
	if (sm.ModelType == "LLM" || sm.ModelType == "VLM" || sm.ModelType == "Vision") &&
		sm.PricingUnit == PricingUnitPerMillionTokens && sm.InputPrice > 0 {
		sm.SupportsCache = true
		sm.CacheMechanism = "both"
		sm.CacheInputPrice = sm.InputPrice * 0.20
		sm.CacheExplicitInputPrice = sm.InputPrice * 0.10
		sm.CacheWritePrice = sm.InputPrice * 1.25
	}

	return &sm
}

// inferAlibabaModelType 鎺ㄦ柇闃块噷浜戞ā鍨嬬殑绫诲瀷
// 浼樺厛绾э細Capabilities 鈫?妯″瀷鍚嶅叧閿瓧 鈫?榛樿 LLM
func inferAlibabaModelType(modelName string, capabilities []string) string {
	name := strings.ToLower(modelName)

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

	// 2. 鎸夋ā鍨嬪悕鎺ㄦ柇锛堜笌 modeldiscovery / scraper_service 淇濇寔涓€鑷达級
	// 娉ㄦ剰妫€鏌ラ『搴忥細video 蹇呴』鍏堜簬 image锛?-t2v" 浼樺厛浜?"wan2"锛夛紝
	// 鍚﹀垯 "wan2.7-t2v" 浼氳 "wan2" 鍚炴垚 ImageGeneration
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

// parseTierRange 浠庝环鏍煎尯闂存弿杩拌В鏋?token 鑼冨洿锛屽啓鍏?tier 鐨?InputMin/InputMax 瀛楁銆?// 鏀寔鏍煎紡锛堜腑鑻辨枃娣峰悎锛夛細
//   "杈撳叆<=256k"           鈫?(0, 256k]
//   "128k<杈撳叆<=256k"      鈫?(128k, 256k]
//   "256k<杈撳叆<=1m"        鈫?(256k, 1m]
//   "杈撳叆>128k"            鈫?(128k, +鈭?
//   "input<=128k"          鈫?(0, 128k]
//   "128k<input<=256k"     鈫?(128k, 256k]
//   "涓婁笅鏂?=32k"          鈫?鍚?"杈撳叆<=32k"
//   "32k tokens"           鈫?浠呭惈鏁板瓧锛堝鏉捐В鏋愶紝浣滀笂闄愶級
func parseTierRange(rangeStr string, tier *model.PriceTier) {
	normalized := strings.ToLower(rangeStr)
	normalized = strings.ReplaceAll(normalized, "输入token数", "input")
	normalized = strings.ReplaceAll(normalized, "杈撳叆tokens", "input")
	normalized = strings.ReplaceAll(normalized, "杈撳叆token", "input")
	normalized = strings.ReplaceAll(normalized, "上下文长度", "input")
	normalized = strings.ReplaceAll(normalized, "上下文", "input")
	normalized = strings.ReplaceAll(normalized, "杈撳叆", "input")
	normalized = strings.ReplaceAll(normalized, "杈撳嚭", "output")
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

	// 鎯呭喌1: "NNk<input<=NNk" 鎴?"NNk<input<NNk"锛堟湁涓嬮檺鍜屼笂闄愶級
	if idx := strings.Index(normalized, "<input"); idx > 0 {
		minStr := strings.TrimSpace(normalized[:idx])
		minVal := parseTokenCount(minStr)
		tier.InputMin = minVal
		tier.InputMinExclusive = true
		// 鎻愬彇涓婇檺锛?= 鎴?<
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
		return
	}

	// 鎯呭喌2: "input<=NNk" 鎴?"input<NNk"锛堝彧鏈変笂闄愶級
	if strings.HasPrefix(normalized, "input") {
		rest := strings.TrimPrefix(normalized, "input")
		rest = strings.TrimSpace(rest)
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
		} else if strings.HasPrefix(rest, ">=") {
			minVal := parseTokenCount(strings.TrimPrefix(rest, ">="))
			if minVal > 0 {
				tier.InputMin = minVal
			}
		} else if strings.HasPrefix(rest, ">") {
			minVal := parseTokenCount(strings.TrimPrefix(rest, ">"))
			if minVal > 0 {
				tier.InputMin = minVal
				tier.InputMinExclusive = true
			}
		}
		return
	}

	// 鎯呭喌3: "NNk<=" 鎴?"NNk<" + "input" 浣嶄簬鍚庢柟锛堝凡鍦ㄦ儏鍐?澶勭悊锛屾澶勫鐞嗙畝鍖栧舰寮忥級
	// 瀹芥澗鍥為€€锛氬彧鏈夋暟瀛楋紝瑙嗕负涓婇檺
	if v := parseTokenCount(normalized); v > 0 {
		tier.InputMax = ptr(v)
	}
}
