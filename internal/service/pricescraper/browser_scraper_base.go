package pricescraper

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	redislib "github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
	pkgredis "tokenhub-server/internal/pkg/redis"
)

// =====================================================
// 浏览器抓取基础组件（v3.5）
//
// 职责：
//   1. 统一的多页面浏览器抓取流程（Docker 内 Chromium 模拟真实用户）
//   2. URL 链式回退解析（supplier.pricing_url → 多页配置 → Redis 缓存 → 爬虫默认）
//   3. 表格级智能分类（按表头关键字推断 ModelType + PricingUnit）
//   4. 跨页面去重合并（同模型多页出现时选择价格最完整的条目）
//
// 使用方式：
//   每个供应商的 scraper 嵌入 *BrowserScraperBase，在 ScrapePrices 中调用
//   ScrapeMultiPages 即可获取全类型定价数据。
// =====================================================

// BrowserScraperBase 为各供应商爬虫提供通用浏览器抓取能力
type BrowserScraperBase struct {
	browserMgr   *BrowserManager
	supplierCode string
	redis        *redislib.Client // 可选：用于缓存 last_successful_url
}

// NewBrowserScraperBase 创建浏览器抓取基础组件
// redis 可为 nil（降级为无 URL 缓存模式）
func NewBrowserScraperBase(supplierCode string, browserMgr *BrowserManager) *BrowserScraperBase {
	return &BrowserScraperBase{
		browserMgr:   browserMgr,
		supplierCode: supplierCode,
		redis:        pkgredis.Client, // 复用全局 Redis 客户端（可能为 nil）
	}
}

// PageSpec 单个定价页的抓取配置
type PageSpec struct {
	URL      string // 定价页 URL
	TypeHint string // 模型类型提示（如 "VideoGeneration"）— 用于爬取后的分类兜底
}

// ResolvePricingURLs 按优先级返回最终抓取的 URL 列表
//
// 优先级链（第一项匹配即返回）：
//  1. supplier.pricing_urls（JSON 数组，管理员配置的多页）
//  2. supplier.pricing_url（单 URL，管理员手动维护）
//  3. Redis 缓存的上次成功 URL（pricescraper:url:{supplier_code}）
//  4. 爬虫内置的 fallback URL 列表
//
// 返回 []PageSpec：至少一项；完全失败时返回 fallback 列表
func (b *BrowserScraperBase) ResolvePricingURLs(
	ctx context.Context,
	supplier model.Supplier,
	fallback []PageSpec,
) []PageSpec {
	// 1. 优先使用多页配置（PricingURLs JSON）
	if len(supplier.PricingURLs) > 0 {
		var entries []model.PricingURLEntry
		if err := json.Unmarshal(supplier.PricingURLs, &entries); err == nil && len(entries) > 0 {
			specs := make([]PageSpec, 0, len(entries))
			for _, e := range entries {
				if strings.TrimSpace(e.URL) == "" {
					continue
				}
				specs = append(specs, PageSpec{URL: e.URL, TypeHint: e.TypeHint})
			}
			if len(specs) > 0 {
				return specs
			}
		}
	}

	// 2. 单 URL 兜底
	if strings.TrimSpace(supplier.PricingURL) != "" {
		return []PageSpec{{URL: supplier.PricingURL}}
	}

	// 3. Redis 缓存（可选）
	if b.redis != nil {
		key := fmt.Sprintf("pricescraper:url:%s", b.supplierCode)
		if cachedURL, err := b.redis.Get(ctx, key).Result(); err == nil && cachedURL != "" {
			log := logger.L
			if log != nil {
				log.Info("使用 Redis 缓存的定价 URL",
					zap.String("supplier_code", b.supplierCode),
					zap.String("url", cachedURL))
			}
			return []PageSpec{{URL: cachedURL}}
		}
	}

	// 4. 爬虫内置的 fallback
	return fallback
}

// ScrapeMultiPages 按顺序抓取多个定价页并合并去重
//
// 流程：
//  1. 对每个 PageSpec，调用 BrowserManager.FetchRenderedHTML 获取 JS 渲染后的 HTML
//  2. 使用 goquery 解析 HTML 中的所有 <table>
//  3. 按表头关键字自动推断每个表格的 ModelType + PricingUnit
//  4. 合并跨页面的模型（同名 + 同计费单位 + 同 variant 为一组）
//  5. 单页失败不影响其他页面（fail-open）
//
// 返回合并后的 ScrapedModel 列表
func (b *BrowserScraperBase) ScrapeMultiPages(
	ctx context.Context,
	specs []PageSpec,
) ([]ScrapedModel, error) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	if len(specs) == 0 {
		return nil, fmt.Errorf("no pricing URLs provided")
	}

	var allModels []ScrapedModel
	var lastSuccessURL string

	for i, spec := range specs {
		if strings.TrimSpace(spec.URL) == "" {
			continue
		}

		log.Info("抓取定价页",
			zap.String("supplier_code", b.supplierCode),
			zap.Int("page_index", i+1),
			zap.Int("total_pages", len(specs)),
			zap.String("url", spec.URL),
			zap.String("type_hint", spec.TypeHint))

		// 单页超时 90 秒（JS 渲染 + 等待 <table>）
		pageCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		html, err := b.browserMgr.FetchRenderedHTML(pageCtx, spec.URL)
		cancel()

		if err != nil {
			log.Warn("定价页抓取失败，跳过",
				zap.String("url", spec.URL),
				zap.Error(err))
			continue
		}

		doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
		if err != nil {
			log.Warn("HTML 解析失败，跳过",
				zap.String("url", spec.URL),
				zap.Error(err))
			continue
		}

		// 使用通用的表格提取器（helpers.go::extractPriceTables）
		pageModels := extractPriceTables(doc)

		// 应用表头智能分类（依据每个 table 的表头推断 type/unit）
		pageModels = b.annotateTableModels(doc, pageModels, spec.TypeHint)

		log.Info("定价页抓取完成",
			zap.String("url", spec.URL),
			zap.Int("models_extracted", len(pageModels)))

		allModels = append(allModels, pageModels...)
		if len(pageModels) > 0 {
			lastSuccessURL = spec.URL
		}
	}

	// 跨页去重：按 (ModelName, PricingUnit, Variant) 三元组
	merged := dedupeAcrossPages(allModels)

	log.Info("多页抓取合并完成",
		zap.String("supplier_code", b.supplierCode),
		zap.Int("pages", len(specs)),
		zap.Int("raw_models", len(allModels)),
		zap.Int("merged", len(merged)))

	// 记忆最近成功 URL 到 Redis（TTL 7 天）
	if b.redis != nil && lastSuccessURL != "" {
		key := fmt.Sprintf("pricescraper:url:%s", b.supplierCode)
		_ = b.redis.Set(ctx, key, lastSuccessURL, 7*24*time.Hour).Err()
	}

	return merged, nil
}

// annotateTableModels 根据页面所有 <table> 的表头文字，为每个模型回填 ModelType + PricingUnit
//
// 匹配规则（表头关键字 → 类型/单位）：
//   - "元/张" / "张数" / "image" → ImageGeneration + per_image
//   - "元/秒" / "秒" / "视频时长" → VideoGeneration + per_second
//   - "元/万字符" / "万字" → TTS + per_10k_characters
//   - "元/百万字符" / "百万字符" → TTS + per_million_characters
//   - "元/小时" / "小时" / "hour" → ASR + per_hour
//   - "元/分钟" / "分钟" → ASR + per_minute
//   - "元/次" / "调用" → Rerank + per_call
//   - 默认 → LLM + per_million_tokens
//
// typeHintFromURL: 若表格内无法判断，使用 URL 配置的 TypeHint 作为兜底
func (b *BrowserScraperBase) annotateTableModels(
	doc *goquery.Document,
	models []ScrapedModel,
	typeHintFromURL string,
) []ScrapedModel {
	// 建立 ModelName → TableHeader 映射
	modelToHeader := make(map[string]string, len(models))
	doc.Find("table").Each(func(_ int, table *goquery.Selection) {
		headers := extractTableHeaders(table)
		if !isPriceTable(headers) {
			return
		}
		headerStr := strings.ToLower(strings.Join(headers, " "))
		// 该表下的所有模型继承同一表头
		tableModels := parseTableRows(table, headers)
		for _, tm := range tableModels {
			if _, exists := modelToHeader[tm.ModelName]; !exists {
				modelToHeader[tm.ModelName] = headerStr
			}
		}
	})

	for i := range models {
		header := modelToHeader[models[i].ModelName]
		mt, pu := classifyByHeader(header)
		// 仅在原值为默认 LLM/per_million_tokens 时才覆盖（保留显式配置）
		if mt != "" && (models[i].ModelType == "" || models[i].ModelType == "LLM") {
			// TypeHint 从 URL 来的优先级高于表头启发
			if typeHintFromURL != "" {
				models[i].ModelType = typeHintFromURL
			} else {
				models[i].ModelType = mt
			}
		}
		if pu != "" && (models[i].PricingUnit == "" || models[i].PricingUnit == PricingUnitPerMillionTokens) {
			models[i].PricingUnit = pu
		}
	}

	return models
}

// classifyByHeader 根据表头文本推断 ModelType + PricingUnit
func classifyByHeader(headerLower string) (modelType, pricingUnit string) {
	if headerLower == "" {
		return "", ""
	}
	switch {
	case containsAnyStr(headerLower, "元/张", "张数", "image", "/ 张", "按张"):
		return "ImageGeneration", PricingUnitPerImage
	case containsAnyStr(headerLower, "元/秒", "视频时长", "/秒", "/ 秒", "second"):
		// 需和 ASR 的"元/秒"区分，视频生成为优先
		if containsAnyStr(headerLower, "视频", "video") {
			return "VideoGeneration", PricingUnitPerSecond
		}
		// 默认按 ASR 处理（部分 ASR 按秒计费）
		return "ASR", PricingUnitPerSecond
	case containsAnyStr(headerLower, "元/万字符", "万字符", "/万字"):
		return "TTS", PricingUnitPer10kCharacters
	case containsAnyStr(headerLower, "元/百万字符", "百万字符"):
		return "TTS", PricingUnitPerMillionCharacters
	case containsAnyStr(headerLower, "元/小时", "/小时", "/ 小时", "hour", "/hr"):
		return "ASR", PricingUnitPerHour
	case containsAnyStr(headerLower, "元/分钟", "/分钟", "minute"):
		return "ASR", PricingUnitPerMinute
	case containsAnyStr(headerLower, "元/次", "次调用", "/次", "per call", "call"):
		return "Rerank", PricingUnitPerCall
	case containsAnyStr(headerLower, "embedding", "嵌入"):
		return "Embedding", PricingUnitPerMillionTokens
	default:
		return "", "" // 不改写
	}
}

// dedupeAcrossPages 跨页面按 (ModelName, PricingUnit, Variant) 去重
// 同键组中保留 priceScore 最高的条目
func dedupeAcrossPages(models []ScrapedModel) []ScrapedModel {
	type key struct {
		Name    string
		Unit    string
		Variant string
	}

	best := make(map[key]ScrapedModel)
	order := make([]key, 0, len(models))

	for _, m := range models {
		k := key{
			Name:    strings.ToLower(m.ModelName),
			Unit:    m.PricingUnit,
			Variant: m.Variant,
		}
		existing, ok := best[k]
		if !ok {
			best[k] = m
			order = append(order, k)
			continue
		}
		if priceScore(m) > priceScore(existing) {
			best[k] = m
		}
	}

	result := make([]ScrapedModel, 0, len(best))
	for _, k := range order {
		result = append(result, best[k])
	}
	return result
}
