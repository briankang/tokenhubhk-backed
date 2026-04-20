package pricescraper

import (
	"context"
	"fmt"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"go.uber.org/zap"

	"tokenhub-server/internal/pkg/logger"
)

// =====================================================
// URL 自动发现器（v3.5）
//
// 当硬编码的定价 URL 失效（404 / 页面无表格 / 解析结果 0 模型），
// 触发"搜索发现"机制：
//   1. 访问各供应商预置的搜索入口 / 目录页
//   2. 使用 goquery 提取候选 <a> 链接
//   3. 通过 Filter 函数筛选符合"定价/价格/计费"关键字的链接
//   4. 返回候选列表（唯一命中则自动回写 supplier.pricing_url）
//
// 设计原则：
//   - fail-open：发现失败不影响主流程，仅作为回退路径
//   - 保守候选：返回原始 URL 不直接替换管理员配置
//   - 日志齐全：所有发现尝试都记录到 price_sync_log
// =====================================================

// SearchConfig 供应商搜索配置
type SearchConfig struct {
	// SearchURL 搜索入口 URL，可含 {kw} 占位符（会被 url.QueryEscape(Keyword) 替换）
	// 若 Keyword 为空，则 URL 本身就是目录页（如火山/腾讯的文档中心）
	SearchURL string

	// Keyword 搜索关键词（如 "模型定价"）。为空时跳过 {kw} 替换，直接用 SearchURL
	Keyword string

	// ResultSelector goquery CSS 选择器，用于定位结果列表的 <a> 标签
	// 示例："div.search-list a.title" / "aside a" / "div.J-markdown-box a"
	ResultSelector string

	// Filter 过滤函数（URL + 链接文字 → 是否保留）
	// 返回 true 表示保留该候选
	Filter func(url, text string) bool

	// BaseURL 用于补全相对 URL（如 "/docs/xxx" → "https://example.com/docs/xxx"）
	BaseURL string
}

// URLDiscoverer URL 发现器
type URLDiscoverer struct {
	browserMgr *BrowserManager
}

// NewURLDiscoverer 创建 URL 发现器实例
func NewURLDiscoverer(browserMgr *BrowserManager) *URLDiscoverer {
	return &URLDiscoverer{browserMgr: browserMgr}
}

// DiscoverByKeyword 在搜索页或目录页查找候选定价 URL
//
// 返回候选 URL 列表（按顺序，最多 10 条）
// 无候选或失败时返回空列表（调用方应继续走 fallback）
func (d *URLDiscoverer) DiscoverByKeyword(
	ctx context.Context,
	cfg SearchConfig,
) ([]string, error) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	if cfg.SearchURL == "" {
		return nil, fmt.Errorf("empty search URL")
	}

	// 构造最终搜索 URL
	url := cfg.SearchURL
	if cfg.Keyword != "" && strings.Contains(url, "{kw}") {
		// 关键词 URL 编码（浏览器会自动处理中文）
		escaped := strings.ReplaceAll(cfg.Keyword, " ", "+")
		url = strings.ReplaceAll(url, "{kw}", escaped)
	}

	log.Info("URL 发现：访问搜索/目录页",
		zap.String("url", url),
		zap.String("keyword", cfg.Keyword))

	html, err := d.browserMgr.FetchRenderedHTML(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("fetch search page failed: %w", err)
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, fmt.Errorf("parse search HTML failed: %w", err)
	}

	// 提取候选链接
	selector := cfg.ResultSelector
	if selector == "" {
		selector = "a[href]"
	}

	seen := make(map[string]bool)
	var candidates []string

	doc.Find(selector).Each(func(_ int, a *goquery.Selection) {
		href, ok := a.Attr("href")
		if !ok || href == "" || strings.HasPrefix(href, "#") {
			return
		}
		text := strings.TrimSpace(a.Text())

		// 补全相对 URL
		if cfg.BaseURL != "" && !strings.HasPrefix(href, "http") {
			if strings.HasPrefix(href, "/") {
				href = strings.TrimRight(cfg.BaseURL, "/") + href
			} else {
				href = strings.TrimRight(cfg.BaseURL, "/") + "/" + href
			}
		}

		// 应用 Filter
		if cfg.Filter != nil && !cfg.Filter(href, text) {
			return
		}

		if seen[href] {
			return
		}
		seen[href] = true
		candidates = append(candidates, href)

		if len(candidates) >= 10 {
			return
		}
	})

	log.Info("URL 发现完成",
		zap.String("url", url),
		zap.Int("candidates", len(candidates)))

	return candidates, nil
}

// SearchConfigs 4 家主流供应商的预置搜索配置
// 当硬编码的定价页失效时，使用这些配置做自动发现
var SearchConfigs = map[string]SearchConfig{
	"alibaba": {
		SearchURL:      "https://help.aliyun.com/search.html?k={kw}&product=model-studio",
		Keyword:        "模型定价",
		ResultSelector: "div.search-result a, .search-list a, a[href*='model-studio']",
		BaseURL:        "https://help.aliyun.com",
		Filter: func(url, text string) bool {
			low := strings.ToLower(text)
			return strings.Contains(text, "定价") || strings.Contains(text, "价格") ||
				strings.Contains(low, "pricing") || strings.Contains(low, "price")
		},
	},

	"volcengine": {
		// 火山引擎文档中心目录页，左侧有"模型定价"子章节
		SearchURL:      "https://www.volcengine.com/docs/82379",
		Keyword:        "",
		ResultSelector: "aside a, nav a, .sidebar a, a[href*='82379']",
		BaseURL:        "https://www.volcengine.com",
		Filter: func(url, text string) bool {
			return strings.Contains(text, "定价") || strings.Contains(text, "计费") ||
				strings.Contains(text, "价格")
		},
	},

	"qianfan": {
		SearchURL:      "https://cloud.baidu.com/search?q={kw}&domain=doc",
		Keyword:        "千帆 模型价格",
		ResultSelector: "div.search-list a.title, a[href*='qianfan']",
		BaseURL:        "https://cloud.baidu.com",
		Filter: func(url, text string) bool {
			return strings.Contains(url, "qianfan") &&
				(strings.Contains(text, "价格") || strings.Contains(text, "定价") || strings.Contains(text, "计费"))
		},
	},

	"hunyuan": {
		// 腾讯混元文档中心
		SearchURL:      "https://cloud.tencent.com/document/product/1729",
		Keyword:        "",
		ResultSelector: "div.J-markdown-box a, .docs-sidebar a, a[href*='1729']",
		BaseURL:        "https://cloud.tencent.com",
		Filter: func(url, text string) bool {
			return strings.Contains(text, "定价") || strings.Contains(text, "计费") ||
				strings.Contains(text, "价格")
		},
	},
}

// DiscoverForSupplier 根据供应商 code 执行 URL 发现（便捷封装）
// 未找到配置时返回 nil
func (d *URLDiscoverer) DiscoverForSupplier(
	ctx context.Context,
	supplierCode string,
) ([]string, error) {
	cfg, ok := SearchConfigs[strings.ToLower(supplierCode)]
	if !ok {
		return nil, fmt.Errorf("no search config for supplier %q", supplierCode)
	}
	return d.DiscoverByKeyword(ctx, cfg)
}
