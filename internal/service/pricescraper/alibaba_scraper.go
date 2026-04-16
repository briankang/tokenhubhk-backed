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
	// alibabaAPIURL 阿里云 DashScope 原生模型列表 API
	alibabaAPIURL = "https://dashscope.aliyuncs.com/api/v1/models"
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

	log.Info("阿里云 API 价格获取完成",
		zap.Int("api_models", len(allModels)),
		zap.Int("with_prices", len(scrapedModels)))

	return &ScrapedPriceData{
		SupplierName: alibabaSupplierName,
		FetchedAt:    time.Now(),
		Models:       scrapedModels,
		SourceURL:    alibabaAPIURL,
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
// 只处理有 token 定价的模型（跳过图片/视频/音频按量计费模型）
func (s *AlibabaScraper) convertModel(m alibabaModel) *ScrapedModel {
	if len(m.Prices) == 0 {
		return nil
	}

	sm := ScrapedModel{
		ModelName:   m.Model,
		DisplayName: m.Name,
		Currency:    "CNY",
	}

	// 解析价格层级
	for _, priceRange := range m.Prices {
		var inputPrice, outputPrice float64
		var hasTokenPrice bool

		for _, item := range priceRange.Prices {
			price, err := strconv.ParseFloat(item.Price, 64)
			if err != nil || price <= 0 {
				continue
			}

			// 只处理 token 计费的价格类型
			switch item.Type {
			case "input_token":
				inputPrice = price
				hasTokenPrice = true
			case "output_token":
				outputPrice = price
				hasTokenPrice = true
			}
		}

		if !hasTokenPrice {
			continue
		}

		// 第一个价格区间作为基础价格
		if sm.InputPrice == 0 && inputPrice > 0 {
			sm.InputPrice = inputPrice
		}
		if sm.OutputPrice == 0 && outputPrice > 0 {
			sm.OutputPrice = outputPrice
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
		// 注入阶梯缓存价格（隐式命中价 = 输入价 × 0.2）
		if inputPrice > 0 {
			tier.CacheInputPrice = inputPrice * 0.20
			tier.CacheWritePrice = inputPrice * 1.25
		}
		// 尝试从 price_range 解析 token 范围
		parseTierRange(tierName, &tier)
		sm.PriceTiers = append(sm.PriceTiers, tier)
	}

	// 跳过没有 token 价格的模型（如纯图片/视频模型）
	if sm.InputPrice == 0 && sm.OutputPrice == 0 {
		return nil
	}

	// 阿里云百炼支持 both 模式缓存（隐式 auto + 显式 explicit 互斥）
	// 隐式缓存命中价 = 输入价 × 0.20（节省 80%，自动触发，无写入费）
	// 显式缓存命中价 = 输入价 × 0.10（节省 90%，需 cache_control 参数）
	// 显式缓存写入价 = 输入价 × 1.25（写入时收取溢价）
	if sm.InputPrice > 0 {
		sm.SupportsCache = true
		sm.CacheMechanism = "both"
		sm.CacheMinTokens = 1024 // 显式缓存最小 Token 门槛
		sm.CacheInputPrice = sm.InputPrice * 0.20
		sm.CacheExplicitInputPrice = sm.InputPrice * 0.10
		sm.CacheWritePrice = sm.InputPrice * 1.25
	}

	return &sm
}

// parseTierRange 从价格区间描述解析 token 范围
// 支持中文和英文格式:
//   中文: "输入<=256k", "128k<输入<=256k", "256k<输入<=1m"
//   英文: "input<=128k", "128k<input<=256k"
func parseTierRange(rangeStr string, tier *model.PriceTier) {
	// 统一处理：将中文"输入"替换为"input"，方便后续统一解析
	normalized := strings.ToLower(rangeStr)
	normalized = strings.ReplaceAll(normalized, "输入", "input")
	normalized = strings.ReplaceAll(normalized, "输出", "output")

	parseTokenCount := func(s string) int64 {
		s = strings.TrimSpace(s)
		s = strings.ReplaceAll(s, ",", "")
		multiplier := int64(1)
		if strings.HasSuffix(s, "k") {
			multiplier = 1000
			s = strings.TrimSuffix(s, "k")
		} else if strings.HasSuffix(s, "m") {
			multiplier = 1000000
			s = strings.TrimSuffix(s, "m")
		}
		val, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0
		}
		return int64(val) * multiplier
	}

	// 匹配 "NNk<input<=NNk" 格式（有下限和上限）
	if idx := strings.Index(normalized, "<input"); idx > 0 {
		// 提取下限
		minStr := normalized[:idx]
		tier.MinTokens = parseTokenCount(minStr)
		// 提取上限
		if idx2 := strings.Index(normalized, "<="); idx2 > idx {
			maxStr := normalized[idx2+2:]
			maxVal := parseTokenCount(maxStr)
			if maxVal > 0 {
				tier.MaxTokens = &maxVal
			}
		}
		return
	}

	// 匹配 "input<=NNk" 格式（只有上限）
	if strings.Contains(normalized, "input") && strings.Contains(normalized, "<=") {
		parts := strings.SplitN(normalized, "<=", 2)
		if len(parts) == 2 {
			maxVal := parseTokenCount(parts[1])
			if maxVal > 0 {
				tier.MaxTokens = &maxVal
			}
		}
	}
}
