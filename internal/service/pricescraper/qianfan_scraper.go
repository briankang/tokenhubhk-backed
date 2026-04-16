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

	"go.uber.org/zap"

	"tokenhub-server/internal/pkg/logger"
)

// =====================================================
// 百度千帆（Qianfan V2）价格爬虫
//
// 策略:
//  1. GET https://qianfan.baidubce.com/v2/models  → 获取最新模型列表
//  2. 补充硬编码价格数据（千帆无公开价格 API，价格来源：官网 2026-04 定价）
//  3. 匹配合并，生成 ScrapedModel 列表
// =====================================================

const (
	qianfanModelsAPIURL  = "https://qianfan.baidubce.com/v2/models"
	qianfanSupplierName  = "百度千帆"
	qianfanPricePageURL  = "https://cloud.baidu.com/doc/qianfan-api/s/3m9b5lqft"
)

// QianfanScraper 百度千帆价格爬虫
type QianfanScraper struct {
	apiKey     string
	httpClient *http.Client
}

// NewQianfanScraper 创建千帆爬虫实例
func NewQianfanScraper(apiKey string) *QianfanScraper {
	// 强制 HTTP/1.1，避免 Docker 环境 HTTP/2 EOF 问题
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
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     false,
	}
	return &QianfanScraper{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout:   60 * time.Second,
			Transport: transport,
		},
	}
}

// ---- API 响应结构（OpenAI 兼容格式）----

type qianfanModelListResponse struct {
	Object string           `json:"object"` // "list"
	Data   []qianfanAPIModel `json:"data"`
}

type qianfanAPIModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`  // "model"
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// ScrapePrices 执行千帆价格获取
func (s *QianfanScraper) ScrapePrices(ctx context.Context) (*ScrapedPriceData, error) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	// Step 1: 通过 API 获取模型列表
	apiModels, err := s.fetchModelList(ctx)
	if err != nil {
		log.Warn("千帆 API 获取模型列表失败，仅使用内置价格数据", zap.Error(err))
		apiModels = nil // 降级：仅使用内置价格
	} else {
		log.Info("千帆 API 获取模型列表成功", zap.Int("count", len(apiModels)))
	}

	// Step 2: 获取硬编码补充价格
	supplementaryPrices := getQianfanSupplementaryPrices()

	// Step 3: 合并 API 模型列表 + 补充价格
	result := s.mergeModels(apiModels, supplementaryPrices)

	log.Info("千帆价格获取完成",
		zap.Int("api_models", len(apiModels)),
		zap.Int("supplementary", len(supplementaryPrices)),
		zap.Int("merged", len(result)))

	return &ScrapedPriceData{
		SupplierName: qianfanSupplierName,
		FetchedAt:    time.Now(),
		Models:       result,
		SourceURL:    qianfanPricePageURL,
	}, nil
}

// fetchModelList 调用 /v2/models 获取模型列表（含重试）
func (s *QianfanScraper) fetchModelList(ctx context.Context) ([]qianfanAPIModel, error) {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		models, err := s.doFetchModelList(ctx)
		if err == nil {
			return models, nil
		}
		lastErr = err
		if attempt < 3 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
			// 关闭空闲连接，强制重建
			if t, ok := s.httpClient.Transport.(*http.Transport); ok {
				t.CloseIdleConnections()
			}
		}
	}
	return nil, fmt.Errorf("重试 3 次后仍失败: %w", lastErr)
}

// doFetchModelList 执行单次模型列表请求
func (s *QianfanScraper) doFetchModelList(ctx context.Context) ([]qianfanAPIModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, qianfanModelsAPIURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "close") // 避免连接复用导致 EOF

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP 请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API 返回 %d: %s", resp.StatusCode, truncate(string(body), 500))
	}

	var apiResp qianfanModelListResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("解析 JSON 失败: %w", err)
	}

	return apiResp.Data, nil
}

// mergeModels 合并 API 模型列表与补充价格数据
// API 列表提供权威模型 ID；补充价格提供价格、类型、单位信息
func (s *QianfanScraper) mergeModels(apiModels []qianfanAPIModel, supplementary []ScrapedModel) []ScrapedModel {
	// 构建补充价格索引（key: 规范化模型ID）
	suppMap := make(map[string]ScrapedModel, len(supplementary))
	for _, sm := range supplementary {
		suppMap[normalizeModelID(sm.ModelName)] = sm
	}

	// 用于去重的已处理集合
	processed := make(map[string]bool)
	var result []ScrapedModel

	// Step 1: 处理 API 返回的模型，优先使用补充价格
	for _, m := range apiModels {
		if m.ID == "" {
			continue
		}
		key := normalizeModelID(m.ID)
		if processed[key] {
			continue
		}
		processed[key] = true

		sm := ScrapedModel{
			ModelName:   m.ID,
			DisplayName: inferQianfanDisplayName(m.ID),
			Currency:    "CNY",
			ModelType:   inferQianfanModelType(m.ID),
			PricingUnit: inferQianfanPricingUnit(m.ID),
		}

		// 使用补充价格覆盖
		if supp, ok := suppMap[key]; ok {
			sm.InputPrice = supp.InputPrice
			sm.OutputPrice = supp.OutputPrice
			sm.PriceTiers = supp.PriceTiers
			sm.DisplayName = supp.DisplayName
			sm.ModelType = supp.ModelType
			sm.PricingUnit = supp.PricingUnit
		}

		result = append(result, sm)
	}

	// Step 2: 补充价格中 API 未返回的模型（已知重要模型）
	for _, supp := range supplementary {
		key := normalizeModelID(supp.ModelName)
		if processed[key] {
			continue
		}
		processed[key] = true
		result = append(result, supp)
	}

	return result
}

// =====================================================
// 硬编码补充价格数据（来源: 百度千帆官网 2026-04 定价）
// https://cloud.baidu.com/doc/qianfan-api/s/3m9b5lqft
// =====================================================

func getQianfanSupplementaryPrices() []ScrapedModel {
	return []ScrapedModel{
		// ---- ERNIE 4.5 系列（旗舰）----
		{
			ModelName:   "ernie-4.5-8k",
			DisplayName: "ERNIE 4.5 8K",
			InputPrice:  4.0,   // ¥4/百万tokens
			OutputPrice: 16.0,  // ¥16/百万tokens
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},
		{
			ModelName:   "ernie-4.5-8k-preview",
			DisplayName: "ERNIE 4.5 8K Preview",
			InputPrice:  4.0,
			OutputPrice: 16.0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},
		{
			ModelName:   "ernie-4.5-turbo-8k",
			DisplayName: "ERNIE 4.5 Turbo 8K",
			InputPrice:  2.0,
			OutputPrice: 8.0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},
		{
			ModelName:   "ernie-4.5-turbo-128k",
			DisplayName: "ERNIE 4.5 Turbo 128K",
			InputPrice:  2.0,
			OutputPrice: 8.0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},

		// ---- ERNIE X1 系列（推理模型）----
		{
			ModelName:   "ernie-x1",
			DisplayName: "ERNIE X1",
			InputPrice:  4.0,
			OutputPrice: 16.0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},
		{
			ModelName:   "ernie-x1-turbo",
			DisplayName: "ERNIE X1 Turbo",
			InputPrice:  2.0,
			OutputPrice: 8.0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},

		// ---- ERNIE 4.0 系列----
		{
			ModelName:   "ernie-4.0-8k-latest",
			DisplayName: "ERNIE 4.0 8K Latest",
			InputPrice:  30.0,
			OutputPrice: 60.0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},
		{
			ModelName:   "ernie-4.0-8k",
			DisplayName: "ERNIE 4.0 8K",
			InputPrice:  30.0,
			OutputPrice: 60.0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},
		{
			ModelName:   "ernie-4.0-turbo-8k",
			DisplayName: "ERNIE 4.0 Turbo 8K",
			InputPrice:  20.0,
			OutputPrice: 60.0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},
		{
			ModelName:   "ernie-4.0-8k-0613",
			DisplayName: "ERNIE 4.0 8K 0613",
			InputPrice:  30.0,
			OutputPrice: 60.0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},

		// ---- ERNIE 3.5 系列----
		{
			ModelName:   "ernie-3.5-8k",
			DisplayName: "ERNIE 3.5 8K",
			InputPrice:  0.8,
			OutputPrice: 2.0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},
		{
			ModelName:   "ernie-3.5-128k",
			DisplayName: "ERNIE 3.5 128K",
			InputPrice:  0.8,
			OutputPrice: 2.0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},
		{
			ModelName:   "ernie-3.5-8k-0701",
			DisplayName: "ERNIE 3.5 8K 0701",
			InputPrice:  0.8,
			OutputPrice: 2.0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},

		// ---- ERNIE Speed Pro 系列（付费）----
		{
			ModelName:   "ernie-speed-pro-8k",
			DisplayName: "ERNIE Speed Pro 8K",
			InputPrice:  3.0,
			OutputPrice: 9.0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},
		{
			ModelName:   "ernie-speed-pro-128k",
			DisplayName: "ERNIE Speed Pro 128K",
			InputPrice:  3.0,
			OutputPrice: 9.0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},

		// ---- ERNIE Speed 系列（免费）----
		{
			ModelName:   "ernie-speed-8k",
			DisplayName: "ERNIE Speed 8K",
			InputPrice:  0.0,
			OutputPrice: 0.0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},
		{
			ModelName:   "ernie-speed-128k",
			DisplayName: "ERNIE Speed 128K",
			InputPrice:  0.0,
			OutputPrice: 0.0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},

		// ---- ERNIE Lite 系列（免费）----
		{
			ModelName:   "ernie-lite-8k",
			DisplayName: "ERNIE Lite 8K",
			InputPrice:  0.0,
			OutputPrice: 0.0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},
		{
			ModelName:   "ernie-lite-pro-8k",
			DisplayName: "ERNIE Lite Pro 8K",
			InputPrice:  0.4,
			OutputPrice: 1.0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},

		// ---- ERNIE Tiny（免费）----
		{
			ModelName:   "ernie-tiny-8k",
			DisplayName: "ERNIE Tiny 8K",
			InputPrice:  0.0,
			OutputPrice: 0.0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},

		// ---- Embedding 系列----
		{
			ModelName:   "embedding-v1",
			DisplayName: "Embedding V1",
			InputPrice:  0.5,
			OutputPrice: 0.0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "Embedding",
		},
		{
			ModelName:   "bge-large-zh",
			DisplayName: "BGE Large ZH",
			InputPrice:  0.5,
			OutputPrice: 0.0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "Embedding",
		},
		{
			ModelName:   "bge-large-en",
			DisplayName: "BGE Large EN",
			InputPrice:  0.5,
			OutputPrice: 0.0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "Embedding",
		},
		{
			ModelName:   "tao-8k",
			DisplayName: "TAO 8K",
			InputPrice:  0.5,
			OutputPrice: 0.0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "Embedding",
		},

		// ---- 图像生成----
		{
			ModelName:   "stable-diffusion-xl",
			DisplayName: "Stable Diffusion XL",
			InputPrice:  0.06,  // ¥0.06/张
			OutputPrice: 0.0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerImage,
			ModelType:   "ImageGeneration",
		},
		{
			ModelName:   "stable-diffusion-v1-5",
			DisplayName: "Stable Diffusion v1.5",
			InputPrice:  0.04,
			OutputPrice: 0.0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerImage,
			ModelType:   "ImageGeneration",
		},
	}
}

// =====================================================
// 辅助函数
// =====================================================

// normalizeModelID 规范化模型 ID 用于比较（小写 + 去空格）
func normalizeModelID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

// inferQianfanDisplayName 根据模型 ID 推断展示名称
func inferQianfanDisplayName(modelID string) string {
	// 将 ernie-4.5-8k → ERNIE 4.5 8K
	upper := strings.ToUpper(modelID)
	upper = strings.ReplaceAll(upper, "-", " ")
	return upper
}

// inferQianfanModelType 根据模型 ID 推断模型类型
func inferQianfanModelType(id string) string {
	lower := strings.ToLower(id)
	switch {
	case strings.Contains(lower, "embedding"), strings.Contains(lower, "bge"), strings.Contains(lower, "tao-"):
		return "Embedding"
	case strings.Contains(lower, "stable-diffusion"), strings.Contains(lower, "image"):
		return "ImageGeneration"
	case strings.Contains(lower, "x1"):
		return "LLM" // reasoning model, still LLM category
	default:
		return "LLM"
	}
}

// inferQianfanPricingUnit 根据模型类型推断计费单位
func inferQianfanPricingUnit(id string) string {
	modelType := inferQianfanModelType(id)
	switch modelType {
	case "ImageGeneration":
		return PricingUnitPerImage
	default:
		return PricingUnitPerMillionTokens
	}
}

// truncate 截断字符串
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
