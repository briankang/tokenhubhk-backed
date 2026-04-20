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

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// =====================================================
// 腾讯混元（Tencent Hunyuan）价格爬虫
//
// 策略:
//  1. GET https://api.hunyuan.cloud.tencent.com/v1/models → 获取最新模型列表
//  2. 补充硬编码价格数据（混元无公开价格 API，价格来源：官网 2026-04 定价）
//  3. 匹配合并，生成 ScrapedModel 列表
// =====================================================

const (
	hunyuanModelsAPIURL = "https://api.hunyuan.cloud.tencent.com/v1/models"
	hunyuanSupplierName = "腾讯混元"
	// 文本模型定价页
	hunyuanPricePageURL = "https://cloud.tencent.com/document/product/1729/97731"
	// v3.5：非文本模型（生图等）定价页
	hunyuanNonTextPriceURL = "https://cloud.tencent.com/document/product/1729/105925"
)

// HunyuanScraper 腾讯混元价格爬虫
type HunyuanScraper struct {
	apiKey     string
	httpClient *http.Client
}

// SetAPIKey 动态更新 API Key（优先使用渠道配置的 Key）
func (s *HunyuanScraper) SetAPIKey(key string) {
	if key != "" {
		s.apiKey = key
	}
}

// NewHunyuanScraper 创建混元爬虫实例
func NewHunyuanScraper(apiKey string) *HunyuanScraper {
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
	return &HunyuanScraper{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout:   60 * time.Second,
			Transport: transport,
		},
	}
}

// ---- API 响应结构（OpenAI 兼容格式）----

type hunyuanModelListResponse struct {
	Object string             `json:"object"` // "list"
	Data   []hunyuanAPIModel  `json:"data"`
}

type hunyuanAPIModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// ScrapePrices 执行混元价格获取
func (s *HunyuanScraper) ScrapePrices(ctx context.Context) (*ScrapedPriceData, error) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	// Step 1: 通过 API 获取模型列表
	apiModels, err := s.fetchModelList(ctx)
	if err != nil {
		log.Warn("混元 API 获取模型列表失败，仅使用内置价格数据", zap.Error(err))
		apiModels = nil
	} else {
		log.Info("混元 API 获取模型列表成功", zap.Int("count", len(apiModels)))
	}

	// Step 2: 获取硬编码补充价格
	supplementaryPrices := getHunyuanSupplementaryPrices()

	// Step 3: 合并 API 模型列表 + 补充价格
	result := s.mergeModels(apiModels, supplementaryPrices)

	log.Info("混元价格获取完成",
		zap.Int("api_models", len(apiModels)),
		zap.Int("supplementary", len(supplementaryPrices)),
		zap.Int("merged", len(result)))

	return &ScrapedPriceData{
		SupplierName: hunyuanSupplierName,
		FetchedAt:    time.Now(),
		Models:       result,
		SourceURL:    hunyuanPricePageURL,
	}, nil
}

// fetchModelList 调用 /v1/models 获取模型列表（含重试）
func (s *HunyuanScraper) fetchModelList(ctx context.Context) ([]hunyuanAPIModel, error) {
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
			if t, ok := s.httpClient.Transport.(*http.Transport); ok {
				t.CloseIdleConnections()
			}
		}
	}
	return nil, fmt.Errorf("重试 3 次后仍失败: %w", lastErr)
}

// doFetchModelList 执行单次模型列表请求
func (s *HunyuanScraper) doFetchModelList(ctx context.Context) ([]hunyuanAPIModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, hunyuanModelsAPIURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
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

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API 返回 %d: %s", resp.StatusCode, truncateStr(string(body), 500))
	}

	var apiResp hunyuanModelListResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("解析 JSON 失败: %w", err)
	}

	return apiResp.Data, nil
}

// mergeModels 合并 API 模型列表与补充价格数据
func (s *HunyuanScraper) mergeModels(apiModels []hunyuanAPIModel, supplementary []ScrapedModel) []ScrapedModel {
	suppMap := make(map[string]ScrapedModel, len(supplementary))
	for _, sm := range supplementary {
		suppMap[normalizeHunyuanID(sm.ModelName)] = sm
	}

	processed := make(map[string]bool)
	var result []ScrapedModel

	// Step 1: 处理 API 返回的模型，优先使用补充价格
	for _, m := range apiModels {
		if m.ID == "" {
			continue
		}
		key := normalizeHunyuanID(m.ID)
		if processed[key] {
			continue
		}
		processed[key] = true

		sm := ScrapedModel{
			ModelName:   m.ID,
			DisplayName: inferHunyuanDisplayName(m.ID),
			Currency:    "CNY",
			ModelType:   inferHunyuanModelType(m.ID),
			PricingUnit: inferHunyuanPricingUnit(m.ID),
		}

		if supp, ok := suppMap[key]; ok {
			sm.InputPrice = supp.InputPrice
			sm.OutputPrice = supp.OutputPrice
			sm.PriceTiers = supp.PriceTiers
			sm.DisplayName = supp.DisplayName
			sm.ModelType = supp.ModelType
			sm.PricingUnit = supp.PricingUnit
		}

		// 腾讯云混元官方定价页（https://cloud.tencent.com/document/product/1729/97731）
		// 截至 2026-04 未开放公开的 cached_tokens 计费 SKU，显式标记为不支持缓存
		sm.SupportsCache = false
		sm.CacheMechanism = "none"
		result = append(result, sm)
	}

	// Step 2: 补充价格中 API 未返回的模型
	for _, supp := range supplementary {
		key := normalizeHunyuanID(supp.ModelName)
		if processed[key] {
			continue
		}
		processed[key] = true
		// 同上：显式标记不支持缓存
		supp.SupportsCache = false
		supp.CacheMechanism = "none"
		result = append(result, supp)
	}

	return result
}

// =====================================================
// 硬编码补充价格数据（来源: 腾讯云混元官网 2026-04 定价）
// https://cloud.tencent.com/document/product/1729/97731
// =====================================================

func getHunyuanSupplementaryPrices() []ScrapedModel {
	return []ScrapedModel{
		// ---- 通用对话系列 ----
		{
			ModelName:   "hunyuan-lite",
			DisplayName: "Hunyuan Lite",
			InputPrice:  0,
			OutputPrice: 0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},
		{
			ModelName:   "hunyuan-standard",
			DisplayName: "Hunyuan Standard",
			InputPrice:  4.5,
			OutputPrice: 5,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},
		{
			ModelName:   "hunyuan-standard-256k",
			DisplayName: "Hunyuan Standard 256K",
			InputPrice:  15,
			OutputPrice: 60,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},
		{
			ModelName:   "hunyuan-pro",
			DisplayName: "Hunyuan Pro",
			InputPrice:  30,
			OutputPrice: 100,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},
		{
			ModelName:   "hunyuan-turbo",
			DisplayName: "Hunyuan Turbo",
			InputPrice:  15,
			OutputPrice: 50,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},
		{
			ModelName:   "hunyuan-turbo-latest",
			DisplayName: "Hunyuan Turbo Latest",
			InputPrice:  15,
			OutputPrice: 50,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},
		{
			ModelName:   "hunyuan-large",
			DisplayName: "Hunyuan Large",
			InputPrice:  4,
			OutputPrice: 8,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},

		// ---- 代码生成 ----
		{
			ModelName:   "hunyuan-code",
			DisplayName: "Hunyuan Code",
			InputPrice:  4.5,
			OutputPrice: 5,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},

		// ---- 工具与角色 ----
		{
			ModelName:   "hunyuan-role",
			DisplayName: "Hunyuan Role",
			InputPrice:  4.5,
			OutputPrice: 5,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},
		{
			ModelName:   "hunyuan-functioncall",
			DisplayName: "Hunyuan FunctionCall",
			InputPrice:  4.5,
			OutputPrice: 5,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},

		// ---- 多模态视觉 ----
		{
			ModelName:   "hunyuan-vision",
			DisplayName: "Hunyuan Vision",
			InputPrice:  18,
			OutputPrice: 22,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "VLM",
		},
		{
			ModelName:   "hunyuan-turbo-vision",
			DisplayName: "Hunyuan Turbo Vision",
			InputPrice:  40,
			OutputPrice: 80,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "VLM",
		},

		// ---- Embedding ----
		{
			ModelName:   "hunyuan-embedding",
			DisplayName: "Hunyuan Embedding",
			InputPrice:  0.7,
			OutputPrice: 0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "Embedding",
		},

		// ---- 新一代 TurboS 系列（2025 性价比旗舰）----
		{
			ModelName:   "hunyuan-turbos",
			DisplayName: "Hunyuan TurboS",
			InputPrice:  0.8,
			OutputPrice: 2,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},
		{
			ModelName:   "hunyuan-turbos-latest",
			DisplayName: "Hunyuan TurboS Latest",
			InputPrice:  0.8,
			OutputPrice: 2,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},
		{
			ModelName:   "hunyuan-turbos-20250226",
			DisplayName: "Hunyuan TurboS 20250226",
			InputPrice:  0.8,
			OutputPrice: 2,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},
		{
			ModelName:   "hunyuan-turbos-vision",
			DisplayName: "Hunyuan TurboS Vision",
			InputPrice:  3,
			OutputPrice: 9,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "VLM",
		},

		// ---- T1 深度推理系列（对标 DeepSeek-R1）----
		{
			ModelName:   "hunyuan-t1-latest",
			DisplayName: "Hunyuan T1 Latest",
			InputPrice:  1,
			OutputPrice: 4,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},
		{
			ModelName:   "hunyuan-t1-20250321",
			DisplayName: "Hunyuan T1 20250321",
			InputPrice:  1,
			OutputPrice: 4,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},

		// ---- 翻译专用 ----
		{
			ModelName:   "hunyuan-translation",
			DisplayName: "Hunyuan Translation",
			InputPrice:  15,
			OutputPrice: 15,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},
		{
			ModelName:   "hunyuan-translation-lite",
			DisplayName: "Hunyuan Translation Lite",
			InputPrice:  5,
			OutputPrice: 5,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},

		// ---- 长上下文扩展 ----
		{
			ModelName:   "hunyuan-large-longcontext",
			DisplayName: "Hunyuan Large LongContext",
			InputPrice:  6,
			OutputPrice: 18,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},

		// ---- 视觉扩展 ----
		{
			ModelName:   "hunyuan-standard-vision",
			DisplayName: "Hunyuan Standard Vision",
			InputPrice:  8,
			OutputPrice: 12,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "VLM",
		},
		{
			ModelName:   "hunyuan-lite-vision",
			DisplayName: "Hunyuan Lite Vision",
			InputPrice:  0,
			OutputPrice: 0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "VLM",
		},

		// ---- 开源系列（腾讯开源 Hunyuan 模型，官方 API 免费）----
		{
			ModelName:   "hunyuan-7b",
			DisplayName: "Hunyuan 7B (Open Source)",
			InputPrice:  0,
			OutputPrice: 0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},
		{
			ModelName:   "hunyuan-a13b",
			DisplayName: "Hunyuan A13B (Open Source)",
			InputPrice:  0,
			OutputPrice: 0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerMillionTokens,
			ModelType:   "LLM",
		},

		// ---- 图像生成（按月用量分档，数据源 https://cloud.tencent.com/document/product/1729/105925） ----
		// 混元生图：月用量 <1万: ¥0.5/张；≥1万: 阶梯化降价
		// 注意：阶梯字段表达"单次请求的输入 token 数"语义，此处 InputMin/Max 借用为"月累计调用张数"，
		// 计费引擎调用 SelectTier 时会传入调用方统计的月累计调用数作为 inputTokens 参数来命中阶梯。
		{
			ModelName:   "hunyuan-image",
			DisplayName: "Hunyuan Image",
			InputPrice:  0.5, // 首阶梯单价作为展示
			OutputPrice: 0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerImage,
			ModelType:   "ImageGeneration",
			PriceTiers: []model.PriceTier{
				{Name: "月用量<1万张", InputMax: i64Hunyuan(10000), InputPrice: 0.5},
				{Name: "月用量≥1万张", InputMin: 10000, InputPrice: 0.099},
			},
		},
		// 混元文生图轻量版：¥0.099/张起（大用量 ¥0.066）
		{
			ModelName:   "hunyuan-dit",
			DisplayName: "Hunyuan DiT (轻量版)",
			InputPrice:  0.099,
			OutputPrice: 0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerImage,
			ModelType:   "ImageGeneration",
			PriceTiers: []model.PriceTier{
				{Name: "月用量<100万张", InputMax: i64Hunyuan(1000000), InputPrice: 0.099},
				{Name: "月用量≥100万张", InputMin: 1000000, InputPrice: 0.066},
			},
		},
		// 混元生图快速版
		{
			ModelName:   "hunyuan-image-lite",
			DisplayName: "Hunyuan Image Lite",
			InputPrice:  0.099,
			OutputPrice: 0,
			Currency:    "CNY",
			PricingUnit: PricingUnitPerImage,
			ModelType:   "ImageGeneration",
		},
	}
}

// i64Hunyuan 指针化 int64（混元专用）
func i64Hunyuan(v int64) *int64 { return &v }

// =====================================================
// 辅助函数
// =====================================================

// normalizeHunyuanID 规范化模型 ID 用于比较
func normalizeHunyuanID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

// inferHunyuanDisplayName 根据模型 ID 推断展示名称
func inferHunyuanDisplayName(modelID string) string {
	// hunyuan-turbo-latest → Hunyuan Turbo Latest
	parts := strings.Split(modelID, "-")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

// inferHunyuanModelType 根据模型 ID 推断模型类型
func inferHunyuanModelType(id string) string {
	lower := strings.ToLower(id)
	switch {
	case strings.Contains(lower, "vision"):
		return "VLM"
	case strings.Contains(lower, "embedding"):
		return "Embedding"
	case strings.Contains(lower, "image"):
		return "ImageGeneration"
	case strings.Contains(lower, "video"):
		return "VideoGeneration"
	default:
		return "LLM"
	}
}

// inferHunyuanPricingUnit 根据模型类型推断计费单位
func inferHunyuanPricingUnit(id string) string {
	modelType := inferHunyuanModelType(id)
	switch modelType {
	case "ImageGeneration":
		return PricingUnitPerImage
	default:
		return PricingUnitPerMillionTokens
	}
}

// truncateStr 截断字符串（避免与 qianfan_scraper.go 中的 truncate 冲突）
func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
