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
// 百度千帆（Qianfan V2）价格爬虫
//
// 策略:
//  1. GET https://qianfan.baidubce.com/v2/models  → 获取最新模型列表
//  2. 补充硬编码价格数据（千帆无公开价格 API，价格来源：官网 2026-04 定价）
//  3. 匹配合并，生成 ScrapedModel 列表
// =====================================================

const (
	qianfanModelsAPIURL = "https://qianfan.baidubce.com/v2/models"
	qianfanSupplierName = "百度千帆"
	// v3.5：修正为千帆文档最新的模型价格页 URL（原 qianfan-api/s/3m9b5lqft 已失效）
	qianfanPricePageURL = "https://cloud.baidu.com/doc/qianfan-docs/s/Jm8r1826a"
)

// QianfanScraper 百度千帆价格爬虫
type QianfanScraper struct {
	apiKey     string
	httpClient *http.Client
}

// SetAPIKey 动态更新 API Key（优先使用渠道配置的 Key）
func (s *QianfanScraper) SetAPIKey(key string) {
	if key != "" {
		s.apiKey = key
	}
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
//
// 匹配策略（双重）：
//  1. 精确匹配：API 模型名 与 补充表条目名 完全相同
//  2. 反向前缀匹配：补充表某条目名 是 API 模型名的前缀（按 - 分隔）
//     选择最长（最具体）的匹配。例：API 返回 "ernie-4.0-8k-preview" 命中补充表 "ernie-4.0-8k"
func (s *QianfanScraper) mergeModels(apiModels []qianfanAPIModel, supplementary []ScrapedModel) []ScrapedModel {
	// 构建补充价格索引（key: 规范化模型ID）
	suppMap := make(map[string]ScrapedModel, len(supplementary))
	suppKeys := make([]string, 0, len(supplementary))
	for _, sm := range supplementary {
		k := normalizeModelID(sm.ModelName)
		suppMap[k] = sm
		suppKeys = append(suppKeys, k)
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

		// 双重匹配策略获取价格
		if supp, ok := lookupSupplement(key, suppMap, suppKeys); ok {
			sm.InputPrice = supp.InputPrice
			sm.OutputPrice = supp.OutputPrice
			sm.PriceTiers = supp.PriceTiers
			// DisplayName 仅当 API 模型名与补充表条目完全相同时才覆盖（避免 "ERNIE 4.0 8K" 应用到 "ERNIE 4.0 8K Preview"）
			if normalizeModelID(supp.ModelName) == key {
				sm.DisplayName = supp.DisplayName
			}
			// ModelType 和 PricingUnit 优先使用补充表（更准确）
			if supp.ModelType != "" {
				sm.ModelType = supp.ModelType
			}
			if supp.PricingUnit != "" {
				sm.PricingUnit = supp.PricingUnit
			}
		}

		annotateQianfanCacheSupport(&sm)
		result = append(result, sm)
	}

	// Step 2: 补充价格中 API 未返回的模型（已知重要模型）
	for _, supp := range supplementary {
		key := normalizeModelID(supp.ModelName)
		if processed[key] {
			continue
		}
		processed[key] = true
		annotateQianfanCacheSupport(&supp)
		result = append(result, supp)
	}

	return result
}

// annotateQianfanCacheSupport 为百度千帆 ERNIE 系列 LLM/Vision 模型注入缓存定价信息
//
// 千帆缓存定价机制（both 模式，与阿里云百炼一致，2026-04）：
//   - 隐式缓存命中：基础输入价 × 20%（自动生效，无最小 token 限制）
//   - 显式缓存命中：基础输入价 × 10%（需 cache_control 参数，min 1024 tokens）
//   - 显式缓存写入：基础输入价 × 125%（首次写入溢价）
//
// 适用范围：LLM / Vision 类型的 ERNIE 和 DeepSeek 托管模型；Embedding / 图像 / 视频 / OCR 等跳过。
func annotateQianfanCacheSupport(sm *ScrapedModel) {
	if sm == nil {
		return
	}
	if sm.ModelType != "LLM" && sm.ModelType != "Vision" {
		return
	}
	if sm.InputPrice <= 0 {
		return
	}
	sm.SupportsCache = true
	sm.CacheMechanism = "both"
	sm.CacheMinTokens = 1024
	sm.CacheInputPrice = sm.InputPrice * 0.20
	sm.CacheExplicitInputPrice = sm.InputPrice * 0.10
	sm.CacheWritePrice = sm.InputPrice * 1.25
}

// lookupSupplement 在补充表中查找匹配的价格
// 优先精确匹配，失败后尝试反向前缀匹配（选最长前缀）
func lookupSupplement(apiKey string, suppMap map[string]ScrapedModel, suppKeys []string) (ScrapedModel, bool) {
	// 1) 精确匹配
	if v, ok := suppMap[apiKey]; ok {
		return v, true
	}
	// 2) 反向前缀匹配：补充表条目名作为 API 模型名的前缀（必须按 "-" 边界）
	bestKey := ""
	for _, k := range suppKeys {
		if strings.HasPrefix(apiKey, k+"-") {
			if len(k) > len(bestKey) {
				bestKey = k
			}
		}
	}
	if bestKey != "" {
		return suppMap[bestKey], true
	}
	return ScrapedModel{}, false
}

// =====================================================
// 硬编码补充价格数据（来源: 百度千帆官网 2026-04 定价）
// https://cloud.baidu.com/doc/qianfan/s/wmh4sv6ya
//
// 命名约定：所有 ModelName 必须严格匹配千帆 API /v2/models 返回的 ID。
// 通过 mergeModels() 中的反向前缀匹配，短前缀条目可同时命中
// 如 "ernie-4.0-8k-latest"、"ernie-4.0-8k-preview"、"ernie-4.0-8k-0613" 等变体。
// =====================================================

// i64 辅助函数：指针化 int64
func i64Qianfan(v int64) *int64 { return &v }

func getQianfanSupplementaryPrices() []ScrapedModel {
	return []ScrapedModel{
		// ---- ERNIE 5.0 系列（最新旗舰，带 ≤32k/>32k 阶梯）----
		// 官方定价（2026-04）：
		//   输入 ≤32k: ¥6/M, 输出 ≤32k: ¥24/M
		//   输入 >32k: ¥10/M, 输出 >32k: ¥40/M
		{
			ModelName: "ernie-5.0", DisplayName: "ERNIE 5.0",
			InputPrice: 6.0, OutputPrice: 24.0, Currency: "CNY",
			PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM",
			PriceTiers: []model.PriceTier{
				{Name: "≤32k上下文", InputMax: i64Qianfan(32000), InputPrice: 6.0, OutputPrice: 24.0},
				{Name: ">32k上下文", InputMin: 32000, InputMinExclusive: true, InputPrice: 10.0, OutputPrice: 40.0},
			},
		},

		// ---- ERNIE 4.5 系列（VLM/小参数 区分定价）----
		// 注意：ernie-4.5-vl 和 ernie-4.5-turbo-vl 必须放在 ernie-4.5 / ernie-4.5-turbo 之前，
		// 因为反向前缀匹配选最长，需保证 vl 变体优先匹配 vl 价格
		{ModelName: "ernie-4.5-turbo-vl", DisplayName: "ERNIE 4.5 Turbo VL", InputPrice: 3.0, OutputPrice: 9.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "Vision"},
		{ModelName: "ernie-4.5-vl-28b-a3b", DisplayName: "ERNIE 4.5 VL 28B-A3B", InputPrice: 1.0, OutputPrice: 4.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "Vision"},
		// ERNIE 4.5 Turbo 128K：官方带 ≤128k 的阶梯结构（早期版本按单价计费，后续可能细分）
		{ModelName: "ernie-4.5-turbo-128k", DisplayName: "ERNIE 4.5 Turbo 128K", InputPrice: 0.8, OutputPrice: 3.2, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		// ERNIE 4.5 Turbo 32K 和通用 Turbo：同单价
		{ModelName: "ernie-4.5-turbo-32k", DisplayName: "ERNIE 4.5 Turbo 32K", InputPrice: 0.8, OutputPrice: 3.2, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "ernie-4.5-turbo", DisplayName: "ERNIE 4.5 Turbo", InputPrice: 0.8, OutputPrice: 3.2, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		// ERNIE 4.5 8K 标准版：带 ≤32k / >32k 阶梯（类似 4.0 8K 的收费结构）
		{
			ModelName: "ernie-4.5-8k", DisplayName: "ERNIE 4.5 8K",
			InputPrice: 4.0, OutputPrice: 16.0, Currency: "CNY",
			PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM",
			PriceTiers: []model.PriceTier{
				{Name: "≤32k上下文", InputMax: i64Qianfan(32000), InputPrice: 4.0, OutputPrice: 16.0},
			},
		},
		{ModelName: "ernie-4.5-8k-preview", DisplayName: "ERNIE 4.5 8K Preview", InputPrice: 4.0, OutputPrice: 16.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "ernie-4.5-21b-a3b-thinking", DisplayName: "ERNIE 4.5 21B-A3B Thinking", InputPrice: 0.5, OutputPrice: 2.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "ernie-4.5-21b-a3b", DisplayName: "ERNIE 4.5 21B-A3B", InputPrice: 0.5, OutputPrice: 2.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "ernie-4.5-0.3b", DisplayName: "ERNIE 4.5 0.3B", InputPrice: 0.1, OutputPrice: 0.4, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},

		// ---- ERNIE X1 / X1.1 系列（推理模型，价格 = DeepSeek-R1 的 1/2）----
		{ModelName: "ernie-x1.1-preview", DisplayName: "ERNIE X1.1 Preview", InputPrice: 2.0, OutputPrice: 8.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "ernie-x1-turbo-32k", DisplayName: "ERNIE X1 Turbo 32K", InputPrice: 1.0, OutputPrice: 4.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "ernie-x1-32k", DisplayName: "ERNIE X1 32K", InputPrice: 2.0, OutputPrice: 8.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		// 通用前缀，命中 ernie-x1-turbo-latest / ernie-x1-turbo-32k-preview 等变体
		{ModelName: "ernie-x1-turbo", DisplayName: "ERNIE X1 Turbo", InputPrice: 1.0, OutputPrice: 4.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},

		// ---- ERNIE 4.0 系列（命中 -latest/-preview/-0613/-0628 等所有变体）----
		{ModelName: "ernie-4.0-turbo-128k", DisplayName: "ERNIE 4.0 Turbo 128K", InputPrice: 20.0, OutputPrice: 60.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "ernie-4.0-turbo-8k", DisplayName: "ERNIE 4.0 Turbo 8K", InputPrice: 20.0, OutputPrice: 60.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "ernie-4.0-8k", DisplayName: "ERNIE 4.0 8K", InputPrice: 30.0, OutputPrice: 60.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},

		// ---- ERNIE 3.5 系列（带 ≤32k 阶梯）----
		{
			ModelName: "ernie-3.5-128k", DisplayName: "ERNIE 3.5 128K",
			InputPrice: 0.8, OutputPrice: 2.0, Currency: "CNY",
			PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM",
			PriceTiers: []model.PriceTier{
				{Name: "≤32k上下文", InputMax: i64Qianfan(32000), InputPrice: 0.8, OutputPrice: 2.0},
				{Name: ">32k上下文", InputMin: 32000, InputMinExclusive: true, InputPrice: 1.2, OutputPrice: 3.0},
			},
		},
		{ModelName: "ernie-3.5-8k", DisplayName: "ERNIE 3.5 8K", InputPrice: 0.8, OutputPrice: 2.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},

		// ---- ERNIE Speed/Lite/Tiny 系列 ----
		{ModelName: "ernie-speed-pro", DisplayName: "ERNIE Speed Pro", InputPrice: 0.3, OutputPrice: 0.6, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "ernie-speed", DisplayName: "ERNIE Speed", InputPrice: 0.0, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "ernie-lite-pro", DisplayName: "ERNIE Lite Pro", InputPrice: 0.2, OutputPrice: 0.4, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "ernie-lite", DisplayName: "ERNIE Lite", InputPrice: 0.0, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "ernie-tiny", DisplayName: "ERNIE Tiny", InputPrice: 0.0, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},

		// ---- ERNIE Character/Novel/Functions/Video 系列（垂直场景）----
		// 注意：ernie-char-8k 和 ernie-functions-8k 已于 2026-04-23 下线，但保留价格作为历史参考
		// 价格来源：https://cloud.baidu.com/doc/qianfan/s/wmh4sv6ya（千帆刊例价 2026-04）
		{ModelName: "ernie-char-fiction", DisplayName: "ERNIE Char Fiction", InputPrice: 0.3, OutputPrice: 0.6, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "ernie-char", DisplayName: "ERNIE Character", InputPrice: 0.3, OutputPrice: 0.6, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "ernie-novel", DisplayName: "ERNIE Novel", InputPrice: 40.0, OutputPrice: 120.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		// ERNIE Video 按张/秒计费，无公开统一定价，标记为 0 待人工补充
		{ModelName: "ernie-video-1.0-i2v", DisplayName: "ERNIE Video 1.0 I2V", InputPrice: 0.0, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerSecond, ModelType: "VideoGeneration"},
		{ModelName: "ernie-video-1.0-t2v", DisplayName: "ERNIE Video 1.0 T2V", InputPrice: 0.0, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerSecond, ModelType: "VideoGeneration"},
		// ERNIE iRAG 图像编辑
		{ModelName: "ernie-irag-edit", DisplayName: "ERNIE iRAG Edit", InputPrice: 0.14, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerImage, ModelType: "ImageGeneration"},
		{ModelName: "irag-1.0", DisplayName: "iRAG 1.0", InputPrice: 0.14, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerImage, ModelType: "ImageGeneration"},

		// ---- 千帆自研 VLM/Agent/OCR ----
		{ModelName: "qianfan-vl-70b", DisplayName: "Qianfan VL 70B", InputPrice: 8.0, OutputPrice: 24.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "Vision"},
		{ModelName: "qianfan-vl-8b", DisplayName: "Qianfan VL 8B", InputPrice: 2.0, OutputPrice: 6.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "Vision"},
		// qianfan-vl 通用前缀，命中其他 vl 变体
		{ModelName: "qianfan-vl", DisplayName: "Qianfan VL", InputPrice: 2.0, OutputPrice: 6.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "Vision"},
		// qianfan-agent / qianfan-ocr / qianfan-* 通用条目（按张/调用计费，标记 0 待人工补充具体价格）
		{ModelName: "qianfan-agent", DisplayName: "Qianfan Agent", InputPrice: 0.0, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "qianfan-ocr", DisplayName: "Qianfan OCR", InputPrice: 0.18, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerImage, ModelType: "Vision"},

		// ---- DeepSeek 千帆托管 ----
		{ModelName: "deepseek-v3.2", DisplayName: "DeepSeek V3.2", InputPrice: 2.0, OutputPrice: 3.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "deepseek-v3.1", DisplayName: "DeepSeek V3.1", InputPrice: 4.0, OutputPrice: 12.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		// deepseek-chat-* 别名（千帆 OpenAI 兼容路径）
		{ModelName: "deepseek-chat-v3.1", DisplayName: "DeepSeek Chat V3.1", InputPrice: 4.0, OutputPrice: 12.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "deepseek-chat", DisplayName: "DeepSeek Chat", InputPrice: 2.0, OutputPrice: 8.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "deepseek-v3", DisplayName: "DeepSeek V3", InputPrice: 2.0, OutputPrice: 8.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "deepseek-r1", DisplayName: "DeepSeek R1", InputPrice: 4.0, OutputPrice: 16.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		// DeepSeek 蒸馏模型走更低价
		{ModelName: "deepseek-r1-distill-qwen", DisplayName: "DeepSeek R1 Distill Qwen", InputPrice: 0.5, OutputPrice: 1.5, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},

		// ---- Qwen3 千帆托管 ----
		// 注意：reranker/embedding/vl 必须在 qwen3 通用前缀之前，确保特化匹配
		{ModelName: "qwen3-reranker", DisplayName: "Qwen3 Reranker", InputPrice: 0.8, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "Rerank"},
		{ModelName: "qwen3-embedding", DisplayName: "Qwen3 Embedding", InputPrice: 0.5, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "Embedding"},
		{ModelName: "qwen3-vl", DisplayName: "Qwen3 VL", InputPrice: 2.0, OutputPrice: 8.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "Vision"},
		{ModelName: "qwen3-235b", DisplayName: "Qwen3 235B", InputPrice: 2.0, OutputPrice: 8.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "qwen3-32b", DisplayName: "Qwen3 32B", InputPrice: 2.0, OutputPrice: 8.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "qwen3-14b", DisplayName: "Qwen3 14B", InputPrice: 1.0, OutputPrice: 4.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "qwen3-8b", DisplayName: "Qwen3 8B", InputPrice: 0.5, OutputPrice: 2.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		// Qwen2.5
		{ModelName: "qwen2.5-7b-instruct", DisplayName: "Qwen2.5 7B Instruct", InputPrice: 0.5, OutputPrice: 1.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},

		// ---- Kimi K2 ----
		{ModelName: "kimi-k2-instruct", DisplayName: "Kimi K2 Instruct", InputPrice: 4.0, OutputPrice: 16.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},

		// ---- Kling 视频生成（按秒计费，参考官方 2026-04 价格）----
		// Kling V1 Standard: ¥3.5/秒；Kling Pro: ¥6/秒
		{ModelName: "kling-v1-pro", DisplayName: "Kling V1 Pro", InputPrice: 6.0, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerSecond, ModelType: "VideoGeneration"},
		{ModelName: "kling-v1", DisplayName: "Kling V1", InputPrice: 3.5, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerSecond, ModelType: "VideoGeneration"},

		// ---- Vidu / ViduQ 视频生成 ----
		// Vidu 2.0 Standard: ¥2.8/秒 (text2video); ViduQ3 Turbo: ¥2.0/秒
		{ModelName: "vidu-2.0", DisplayName: "Vidu 2.0", InputPrice: 2.8, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerSecond, ModelType: "VideoGeneration"},
		{ModelName: "viduq3-turbo", DisplayName: "ViduQ3 Turbo", InputPrice: 2.0, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerSecond, ModelType: "VideoGeneration"},
		// viduq3 系列（命中 viduq3-turbo_text2video / viduq300-* 等所有变体）
		{ModelName: "viduq3", DisplayName: "ViduQ3", InputPrice: 2.8, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerSecond, ModelType: "VideoGeneration"},
		{ModelName: "viduq300", DisplayName: "ViduQ300", InputPrice: 2.8, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerSecond, ModelType: "VideoGeneration"},

		// ---- Wan 系列（万相 视频生成，按秒计费；千帆托管版参考价）----
		{ModelName: "wan-2.1", DisplayName: "Wan 2.1", InputPrice: 0.24, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerSecond, ModelType: "VideoGeneration"},
		{ModelName: "wan", DisplayName: "Wan", InputPrice: 0.24, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerSecond, ModelType: "VideoGeneration"},

		// ---- MuseSteamer 系列（百度自研图像/视频）----
		// MuseSteamer-Air-image: 图像生成
		{ModelName: "musesteamer-air-image", DisplayName: "MuseSteamer Air Image", InputPrice: 0.05, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerImage, ModelType: "ImageGeneration"},
		// MuseSteamer 2.0/2.1/Air 视频系列（按 5 秒计费，¥1/5sec ≈ ¥0.2/sec）
		{ModelName: "musesteamer-2.0-pro-i2v", DisplayName: "MuseSteamer 2.0 Pro I2V", InputPrice: 0.2, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerSecond, ModelType: "VideoGeneration"},
		{ModelName: "musesteamer-2.0-turbo-i2v", DisplayName: "MuseSteamer 2.0 Turbo I2V", InputPrice: 0.2, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerSecond, ModelType: "VideoGeneration"},
		{ModelName: "musesteamer-2.0-lite-i2v", DisplayName: "MuseSteamer 2.0 Lite I2V", InputPrice: 0.2, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerSecond, ModelType: "VideoGeneration"},
		{ModelName: "musesteamer-2.1-turbo-i2v", DisplayName: "MuseSteamer 2.1 Turbo I2V", InputPrice: 0.2, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerSecond, ModelType: "VideoGeneration"},
		{ModelName: "musesteamer-2.1-lite-i2v", DisplayName: "MuseSteamer 2.1 Lite I2V", InputPrice: 0.2, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerSecond, ModelType: "VideoGeneration"},
		{ModelName: "musesteamer-air-i2v", DisplayName: "MuseSteamer Air I2V", InputPrice: 0.2, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerSecond, ModelType: "VideoGeneration"},
		// MuseSteamer 通用前缀，命中其他 -wallpaper / -storybook / -product 等变体
		{ModelName: "musesteamer", DisplayName: "MuseSteamer", InputPrice: 0.2, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerSecond, ModelType: "VideoGeneration"},

		// ---- MiniMax 千帆托管 ----
		{ModelName: "minimax", DisplayName: "MiniMax", InputPrice: 0.0, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},

		// ---- Embedding 系列 ----
		{ModelName: "bge-large-zh", DisplayName: "BGE Large ZH", InputPrice: 0.5, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "Embedding"},
		{ModelName: "bge-large-en", DisplayName: "BGE Large EN", InputPrice: 0.5, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "Embedding"},
		{ModelName: "tao-8k", DisplayName: "TAO 8K", InputPrice: 0.5, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "Embedding"},
		{ModelName: "embedding-v1", DisplayName: "Embedding V1", InputPrice: 0.5, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "Embedding"},

		// ---- Vision 视觉理解（千帆托管开源 InternVL）----
		{ModelName: "internvl3", DisplayName: "InternVL3", InputPrice: 2.0, OutputPrice: 6.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "Vision"},
		{ModelName: "internvl2.5", DisplayName: "InternVL 2.5", InputPrice: 2.0, OutputPrice: 6.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "Vision"},

		// ---- PaddleOCR / PP-StructureV3（百度自研 OCR） ----
		{ModelName: "paddleocr-vl", DisplayName: "PaddleOCR VL", InputPrice: 0.18, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerImage, ModelType: "Vision"},
		{ModelName: "pp-structurev3", DisplayName: "PP-StructureV3", InputPrice: 0.18, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerImage, ModelType: "Vision"},

		// ---- 图像生成 ----
		{ModelName: "stable-diffusion-xl", DisplayName: "Stable Diffusion XL", InputPrice: 0.06, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerImage, ModelType: "ImageGeneration"},
		{ModelName: "flux.1-schnell", DisplayName: "Flux.1 Schnell", InputPrice: 0.05, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerImage, ModelType: "ImageGeneration"},

		// ---- 第三方开源 LLM（已下线/低优先级，全部按免费/极低价处理） ----
		// 这些模型在千帆已大多下线或转为免费体验，价格设为 0
		{ModelName: "yi-34b-chat", DisplayName: "Yi 34B Chat", InputPrice: 0.0, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "llama-2-7b-chat", DisplayName: "Llama 2 7B Chat", InputPrice: 0.0, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "llama-2-13b-chat", DisplayName: "Llama 2 13B Chat", InputPrice: 0.0, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "llama-2-70b-chat", DisplayName: "Llama 2 70B Chat", InputPrice: 0.0, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "meta-llama-3-8b", DisplayName: "Meta Llama 3 8B", InputPrice: 0.0, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "mixtral-8x7b-instruct", DisplayName: "Mixtral 8x7B Instruct", InputPrice: 0.0, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "gemma-7b-it", DisplayName: "Gemma 7B IT", InputPrice: 0.0, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "chatglm2-6b-32k", DisplayName: "ChatGLM2 6B 32K", InputPrice: 0.0, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "aquilachat-7b", DisplayName: "AquilaChat 7B", InputPrice: 0.0, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "codellama-7b-instruct", DisplayName: "CodeLlama 7B Instruct", InputPrice: 0.0, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "bloomz-7b", DisplayName: "BLOOMZ 7B", InputPrice: 0.0, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "xuanyuan-70b-chat-4bit", DisplayName: "XuanYuan 70B Chat 4bit", InputPrice: 0.0, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
		{ModelName: "sqlcoder-7b", DisplayName: "SQLCoder 7B", InputPrice: 0.0, OutputPrice: 0.0, Currency: "CNY", PricingUnit: PricingUnitPerMillionTokens, ModelType: "LLM"},
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

// =====================================================
// 官方下线模型清单
// 来源: https://cloud.baidu.com/doc/qianfan/s/zmh4stou3
// 用于：将本地数据库中已被千帆官方下线的模型批量标记为 offline
// 命名约定：所有 key 必须为小写规范化的 API 模型 ID
// =====================================================

// ModelDeprecation 供应商官方下线模型信息（通用结构，供 qianfan/aliyun 等共享）
type ModelDeprecation struct {
	RetireDate  string // 下线日期 (YYYY-MM-DD)
	Reason      string // 下线原因/分类
	Replacement string // 官方推荐替代模型（可选，多个用 "、" 分隔）
}

// QianfanDeprecation 千帆模型下线信息（保留类型别名，向后兼容）
type QianfanDeprecation = ModelDeprecation

// GetQianfanDeprecatedModels 返回所有官方下线/将下线的千帆模型
//
// 数据来源: 百度千帆模型退役公告（截止 2026-04 抓取的官方文档）
// https://cloud.baidu.com/doc/qianfan/s/zmh4stou3
func GetQianfanDeprecatedModels() map[string]QianfanDeprecation {
	return map[string]QianfanDeprecation{
		// ---- 预置文本模型 (2026-04 ~ 2026-04 集中下线) ----
		"deepseek-r1-250120":               {RetireDate: "2026-04-28", Reason: "preset_model_retired"},
		"minimax-m2.1":                     {RetireDate: "2026-04-23", Reason: "preset_model_retired"},
		"qwen2.5-7b-instruct":              {RetireDate: "2026-04-23", Reason: "preset_model_retired"},
		"qwq-32b":                          {RetireDate: "2026-04-23", Reason: "preset_model_retired"},
		"ernie-4.5-21b-a3b":                {RetireDate: "2026-04-23", Reason: "preset_model_retired"},
		"ernie-4.5-21b-a3b-thinking":       {RetireDate: "2026-04-23", Reason: "preset_model_retired"},
		"ernie-character-8k":               {RetireDate: "2026-04-23", Reason: "preset_model_retired"},
		"ernie-char-8k":                    {RetireDate: "2026-04-23", Reason: "preset_model_retired"},
		"ernie-char-fiction-8k":            {RetireDate: "2026-04-23", Reason: "preset_model_retired"},
		"ernie-char-fiction-8k-preview":    {RetireDate: "2026-04-23", Reason: "preset_model_retired"},
		"ernie-functions-8k":               {RetireDate: "2026-04-23", Reason: "preset_model_retired"},
		"kimi-k2-instruct":                 {RetireDate: "2026-03-26", Reason: "preset_model_retired"},
		"qwen3-coder-480b-a35b-instruct":   {RetireDate: "2026-04-23", Reason: "preset_model_retired"},
		"qwen3-235b-a22b-instruct-2507":    {RetireDate: "2026-04-23", Reason: "preset_model_retired"},
		"qwen3-vl-32b-instruct":            {RetireDate: "2026-04-23", Reason: "preset_model_retired"},
		"qwen2.5-vl-7b-instruct":           {RetireDate: "2026-04-23", Reason: "preset_model_retired"},
		"glm-4.7":                          {RetireDate: "2026-04-23", Reason: "preset_model_retired"},
		"qianfan-lightning-128b-a19b":      {RetireDate: "2026-04-23", Reason: "preset_model_retired"},

		// ---- ERNIE 历史版本 (2024-2026 陆续下线) ----
		"ernie-3.5-8k-1222": {RetireDate: "2024-05-30", Reason: "ernie_version_retired"},
		"ernie-3.5-4k-0205": {RetireDate: "2024-05-30", Reason: "ernie_version_retired"},
		"ernie-4.0-8k":      {RetireDate: "2025-12-31", Reason: "ernie_version_retired"},
		"ernie-lite-8k":     {RetireDate: "2026-01-27", Reason: "ernie_version_retired"},
		"ernie-speed-8k":    {RetireDate: "2026-01-27", Reason: "ernie_version_retired"},
		"ernie-lite-8k-0922": {RetireDate: "2026-01-27", Reason: "ernie_version_retired"},

		// ---- MuseSteamer 老版本 (2026-02-06 下线) ----
		"musesteamer-2.0-pro-i2v":              {RetireDate: "2026-02-06", Reason: "musesteamer_v2_retired"},
		"musesteamer-2.0-turbo-i2v":            {RetireDate: "2026-02-06", Reason: "musesteamer_v2_retired"},
		"musesteamer-2.0-lite-i2v":             {RetireDate: "2026-02-06", Reason: "musesteamer_v2_retired"},
		"musesteamer-2.0-turbo-i2v-audio":      {RetireDate: "2026-02-06", Reason: "musesteamer_v2_retired"},
		"musesteamer-2.0-turbo-i2v-effect":     {RetireDate: "2026-02-06", Reason: "musesteamer_v2_retired"},
		"musesteamer-2.0-turbo-i2v-product":    {RetireDate: "2026-02-06", Reason: "musesteamer_v2_retired"},
		"musesteamer-2.0-turbo-i2v-storybook":  {RetireDate: "2026-02-06", Reason: "musesteamer_v2_retired"},
		"musesteamer-2.0-turbo-i2v-wallpaper":  {RetireDate: "2026-02-06", Reason: "musesteamer_v2_retired"},

		// ---- GPT-OSS 千帆托管 (2026-01-27 下线) ----
		"gpt-oss-20b":  {RetireDate: "2026-01-27", Reason: "preset_model_retired"},
		"gpt-oss-120b": {RetireDate: "2026-01-27", Reason: "preset_model_retired"},

		// ---- 千帆自研 8B/70B (2026-01-20 下线) ----
		"qianfan-8b":  {RetireDate: "2026-01-20", Reason: "preset_model_retired"},
		"qianfan-70b": {RetireDate: "2026-01-20", Reason: "preset_model_retired"},

		// ---- 一键部署开源模型 (2025-10-21 集中下线) ----
		"meta-llama-3-8b":         {RetireDate: "2025-10-21", Reason: "open_source_oneclick_retired"},
		"meta-llama-3-70b":        {RetireDate: "2025-10-21", Reason: "open_source_oneclick_retired"},
		"llama-2-7b-chat":         {RetireDate: "2025-10-21", Reason: "open_source_oneclick_retired"},
		"llama-2-13b-chat":        {RetireDate: "2025-10-21", Reason: "open_source_oneclick_retired"},
		"llama-2-70b-chat":        {RetireDate: "2025-10-21", Reason: "open_source_oneclick_retired"},
		"codellama-7b-instruct":   {RetireDate: "2025-10-21", Reason: "open_source_oneclick_retired"},
		"chatglm2-6b":             {RetireDate: "2025-10-21", Reason: "open_source_oneclick_retired"},
		"chatglm2-6b-32k":         {RetireDate: "2025-10-21", Reason: "open_source_oneclick_retired"},
		"chatglm2-6b-int4":        {RetireDate: "2025-10-21", Reason: "open_source_oneclick_retired"},
		"chatglm3-6b":             {RetireDate: "2025-10-21", Reason: "open_source_oneclick_retired"},
		"chatglm3-6b-32k":         {RetireDate: "2025-10-21", Reason: "open_source_oneclick_retired"},
		"baichuan2-7b-chat":       {RetireDate: "2025-10-21", Reason: "open_source_oneclick_retired"},
		"yi-34b-chat":             {RetireDate: "2025-10-21", Reason: "open_source_oneclick_retired"},
		"mixtral-8x7b-instruct":   {RetireDate: "2025-10-21", Reason: "open_source_oneclick_retired"},
		"bloomz-7b":               {RetireDate: "2025-10-21", Reason: "open_source_oneclick_retired"},
		"qianfan-bloomz-7b-compressed": {RetireDate: "2025-10-21", Reason: "open_source_oneclick_retired"},
		"xverse-13b-chat":         {RetireDate: "2025-10-21", Reason: "open_source_oneclick_retired"},
		"falcon-7b-v5-fp16":       {RetireDate: "2025-10-21", Reason: "open_source_oneclick_retired"},
		"falcon-40b-instruct":     {RetireDate: "2025-10-21", Reason: "open_source_oneclick_retired"},
		"rwkv-4-world":            {RetireDate: "2025-10-21", Reason: "open_source_oneclick_retired"},
		"rwkv-raven-14b":          {RetireDate: "2025-10-21", Reason: "open_source_oneclick_retired"},
		"rwkv-4-pile-14b":         {RetireDate: "2025-10-21", Reason: "open_source_oneclick_retired"},
		"aquilachat-7b":           {RetireDate: "2025-10-21", Reason: "open_source_oneclick_retired"},
		"xuanyuan-70b-chat-4bit":  {RetireDate: "2025-10-21", Reason: "open_source_oneclick_retired"},
		"sqlcoder-7b":             {RetireDate: "2025-10-21", Reason: "open_source_oneclick_retired"},
		"gemma-7b-it":             {RetireDate: "2025-10-21", Reason: "open_source_oneclick_retired"},
	}
}

// IsQianfanDeprecated 判断模型是否已被千帆官方下线
func IsQianfanDeprecated(modelName string) (QianfanDeprecation, bool) {
	dep, ok := GetQianfanDeprecatedModels()[normalizeModelID(modelName)]
	return dep, ok
}
