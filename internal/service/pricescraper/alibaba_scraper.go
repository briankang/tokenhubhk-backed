package pricescraper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"go.uber.org/zap"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// =====================================================
// 阿里云（百炼平台）价格爬虫
// 通过阿里云帮助文档 JSON API 获取定价数据
// 目标接口: https://help.aliyun.com/help/json/ssr/dynamic.json?alias=/model-studio/model-pricing
// =====================================================

const (
	// alibabaURL 阿里云定价 JSON API 地址
	alibabaURL = "https://help.aliyun.com/help/json/ssr/dynamic.json?alias=/model-studio/model-pricing"
	// alibabaSupplierName 供应商名称标识
	alibabaSupplierName = "阿里云百炼"
)

// AlibabaScraper 阿里云价格爬虫
type AlibabaScraper struct {
	client *http.Client
}

// NewAlibabaScraper 创建阿里云爬虫实例
func NewAlibabaScraper() *AlibabaScraper {
	return &AlibabaScraper{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// ScrapePrices 执行阿里云价格爬取
// 流程: HTTP GET JSON API → 解析响应 → 从 HTML 内容中提取定价表格 → 返回结构化数据
func (s *AlibabaScraper) ScrapePrices(ctx context.Context) (*ScrapedPriceData, error) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	// 带重试的 HTTP 请求
	body, err := s.fetchWithRetry(ctx, alibabaURL, 3)
	if err != nil {
		return nil, fmt.Errorf("获取阿里云定价数据失败: %w", err)
	}
	defer body.Close()

	// 读取响应体
	bodyBytes, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("读取阿里云响应体失败: %w", err)
	}

	// 解析 JSON 响应，提取 HTML 内容
	htmlContent, err := s.extractHTMLFromJSON(bodyBytes)
	if err != nil {
		return nil, fmt.Errorf("解析阿里云 JSON 响应失败: %w", err)
	}

	if htmlContent == "" {
		return nil, fmt.Errorf("阿里云 JSON 响应中未找到 HTML 内容")
	}

	// 从 HTML 中解析定价表格
	models, err := s.parseHTMLContent(htmlContent)
	if err != nil {
		return nil, fmt.Errorf("解析阿里云定价 HTML 内容失败: %w", err)
	}

	if len(models) == 0 {
		log.Warn("阿里云定价页面未提取到任何模型数据", zap.String("url", alibabaURL))
	} else {
		log.Info("阿里云价格爬取完成",
			zap.Int("models_count", len(models)),
			zap.String("url", alibabaURL))
	}

	return &ScrapedPriceData{
		SupplierName: alibabaSupplierName,
		FetchedAt:    time.Now(),
		Models:       models,
		SourceURL:    alibabaURL,
	}, nil
}

// fetchWithRetry 带指数退避重试的 HTTP GET 请求
func (s *AlibabaScraper) fetchWithRetry(ctx context.Context, url string, maxRetries int) (io.ReadCloser, error) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// 指数退避: 1s, 2s, 4s...
			backoff := time.Duration(math.Pow(2, float64(attempt-1))) * time.Second
			log.Info("重试获取阿里云数据",
				zap.Int("attempt", attempt+1),
				zap.Duration("backoff", backoff))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
		req.Header.Set("Accept", "application/json, text/html, */*;q=0.8")
		req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
		req.Header.Set("Referer", "https://help.aliyun.com/")

		resp, err := s.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}

		return resp.Body, nil
	}

	return nil, fmt.Errorf("重试 %d 次后仍然失败: %w", maxRetries, lastErr)
}

// extractHTMLFromJSON 从阿里云 JSON 响应中提取 HTML 内容
// 阿里云帮助文档 API 的 JSON 结构可能有多种嵌套方式，此处做自适应解析
func (s *AlibabaScraper) extractHTMLFromJSON(data []byte) (string, error) {
	// 首先尝试解析为通用 map
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		// 如果不是 JSON 对象，可能直接就是 HTML
		content := string(data)
		if strings.Contains(content, "<table") || strings.Contains(content, "<tr") {
			return content, nil
		}
		return "", fmt.Errorf("无法解析 JSON: %w", err)
	}

	// 策略1: 尝试常见的 data.content / data.body 路径
	commonPaths := []string{"content", "body", "html", "data", "article", "pageContent"}
	for _, key := range commonPaths {
		if val, ok := raw[key]; ok {
			content := s.tryExtractString(val)
			if content != "" && (strings.Contains(content, "<table") || strings.Contains(content, "<tr")) {
				return content, nil
			}
		}
	}

	// 策略2: 尝试嵌套的 data 对象
	if dataRaw, ok := raw["data"]; ok {
		var dataObj map[string]json.RawMessage
		if err := json.Unmarshal(dataRaw, &dataObj); err == nil {
			for _, key := range commonPaths {
				if val, ok := dataObj[key]; ok {
					content := s.tryExtractString(val)
					if content != "" && (strings.Contains(content, "<table") || strings.Contains(content, "<tr")) {
						return content, nil
					}
				}
			}

			// 策略2.1: 检查 data.data 嵌套
			if innerData, ok := dataObj["data"]; ok {
				var innerObj map[string]json.RawMessage
				if err := json.Unmarshal(innerData, &innerObj); err == nil {
					for _, key := range commonPaths {
						if val, ok := innerObj[key]; ok {
							content := s.tryExtractString(val)
							if content != "" && (strings.Contains(content, "<table") || strings.Contains(content, "<tr")) {
								return content, nil
							}
						}
					}
				}
				// 也可能 data.data 直接就是字符串
				content := s.tryExtractString(innerData)
				if content != "" && (strings.Contains(content, "<table") || strings.Contains(content, "<tr")) {
					return content, nil
				}
			}
		}

		// 策略2.2: data 可能直接就是 HTML 字符串
		content := s.tryExtractString(dataRaw)
		if content != "" && (strings.Contains(content, "<table") || strings.Contains(content, "<tr")) {
			return content, nil
		}
	}

	// 策略3: 遍历所有顶层字段，找包含 HTML 表格的字符串
	for _, val := range raw {
		content := s.tryExtractString(val)
		if content != "" && strings.Contains(content, "<table") {
			return content, nil
		}
	}

	// 策略4: 将整个 JSON 当作字符串搜索 HTML 片段
	fullStr := string(data)
	if idx := strings.Index(fullStr, "<table"); idx >= 0 {
		// 找到 table 标签的开始位置，向前找到包含的 JSON 字符串边界
		return fullStr, nil
	}

	return "", fmt.Errorf("JSON 响应中未找到包含 HTML 表格的内容")
}

// tryExtractString 尝试将 JSON 值解析为字符串
func (s *AlibabaScraper) tryExtractString(raw json.RawMessage) string {
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		return str
	}
	return ""
}

// parseHTMLContent 从 HTML 内容中解析定价表格
func (s *AlibabaScraper) parseHTMLContent(html string) ([]ScrapedModel, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, fmt.Errorf("解析 HTML 失败: %w", err)
	}

	var allModels []ScrapedModel
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	// 遍历所有表格
	doc.Find("table").Each(func(tableIdx int, table *goquery.Selection) {
		headers := s.extractTableHeaders(table)
		if !s.isPriceTable(headers) {
			return
		}

		log.Debug("阿里云: 发现定价表格",
			zap.Int("table_index", tableIdx),
			zap.Strings("headers", headers))

		models := s.parseTableRows(table, headers)
		allModels = append(allModels, models...)
	})

	return allModels, nil
}

// extractTableHeaders 提取表格表头
func (s *AlibabaScraper) extractTableHeaders(table *goquery.Selection) []string {
	var headers []string
	table.Find("thead tr th, thead tr td").Each(func(_ int, th *goquery.Selection) {
		text := strings.TrimSpace(th.Text())
		headers = append(headers, text)
	})
	if len(headers) == 0 {
		table.Find("tr").First().Find("th, td").Each(func(_ int, cell *goquery.Selection) {
			text := strings.TrimSpace(cell.Text())
			headers = append(headers, text)
		})
	}
	return headers
}

// isPriceTable 判断是否为定价表格
func (s *AlibabaScraper) isPriceTable(headers []string) bool {
	hasModel := false
	hasPrice := false
	for _, h := range headers {
		h = strings.ToLower(h)
		if strings.Contains(h, "模型") || strings.Contains(h, "model") || strings.Contains(h, "名称") {
			hasModel = true
		}
		if strings.Contains(h, "价格") || strings.Contains(h, "price") ||
			strings.Contains(h, "token") || strings.Contains(h, "计费") ||
			strings.Contains(h, "输入") || strings.Contains(h, "输出") ||
			strings.Contains(h, "单价") || strings.Contains(h, "费用") {
			hasPrice = true
		}
	}
	return hasModel && hasPrice
}

// parseTableRows 解析表格数据行
func (s *AlibabaScraper) parseTableRows(table *goquery.Selection, headers []string) []ScrapedModel {
	var models []ScrapedModel
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	colMap := s.identifyColumns(headers)

	rows := table.Find("tbody tr")
	if rows.Length() == 0 {
		rows = table.Find("tr").Slice(1, goquery.ToEnd)
	}

	rows.Each(func(rowIdx int, row *goquery.Selection) {
		cells := row.Find("td")
		if cells.Length() == 0 {
			return
		}

		// 提取模型名称
		modelName := ""
		if colMap.modelCol >= 0 && colMap.modelCol < cells.Length() {
			modelName = cleanText(cells.Eq(colMap.modelCol).Text())
		}
		if modelName == "" {
			return
		}

		sm := ScrapedModel{
			ModelName:   modelName,
			DisplayName: modelName,
			Currency:    "CNY",
		}

		// 提取输入价格
		if colMap.inputCol >= 0 && colMap.inputCol < cells.Length() {
			priceText := cleanText(cells.Eq(colMap.inputCol).Text())
			price, unit := parsePrice(priceText)
			sm.InputPrice = convertToPerMillion(price, unit)
		}

		// 提取输出价格
		if colMap.outputCol >= 0 && colMap.outputCol < cells.Length() {
			priceText := cleanText(cells.Eq(colMap.outputCol).Text())
			price, unit := parsePrice(priceText)
			sm.OutputPrice = convertToPerMillion(price, unit)
		}

		// 尝试从多个价格列提取阶梯价格
		if len(colMap.tierCols) > 0 {
			tiers := s.extractTierPrices(cells, colMap)
			if len(tiers) > 0 {
				sm.PriceTiers = tiers
			}
		}

		// 兜底：阶梯价格填充基础价格
		if sm.InputPrice == 0 && len(sm.PriceTiers) > 0 {
			sm.InputPrice = sm.PriceTiers[0].InputPrice
		}
		if sm.OutputPrice == 0 && len(sm.PriceTiers) > 0 {
			sm.OutputPrice = sm.PriceTiers[0].OutputPrice
		}

		if sm.InputPrice > 0 || sm.OutputPrice > 0 || len(sm.PriceTiers) > 0 {
			models = append(models, sm)
		} else {
			log.Debug("阿里云: 跳过无价格的行",
				zap.String("model", modelName),
				zap.Int("row", rowIdx))
		}
	})

	return models
}

// identifyColumns 根据表头识别各列语义
func (s *AlibabaScraper) identifyColumns(headers []string) columnMap {
	cm := columnMap{modelCol: -1, inputCol: -1, outputCol: -1}

	for i, h := range headers {
		h = strings.ToLower(h)
		switch {
		case strings.Contains(h, "模型") || strings.Contains(h, "model") || strings.Contains(h, "名称"):
			if cm.modelCol < 0 {
				cm.modelCol = i
			}
		case strings.Contains(h, "输入") || strings.Contains(h, "input"):
			if cm.inputCol < 0 {
				cm.inputCol = i
			}
		case strings.Contains(h, "输出") || strings.Contains(h, "output"):
			if cm.outputCol < 0 {
				cm.outputCol = i
			}
		case strings.Contains(h, "价格") || strings.Contains(h, "price") || strings.Contains(h, "单价") || strings.Contains(h, "费用"):
			cm.tierCols = append(cm.tierCols, i)
		}
	}

	if cm.modelCol < 0 && len(headers) > 0 {
		cm.modelCol = 0
	}

	return cm
}

// extractTierPrices 从多价格列中提取阶梯价格
func (s *AlibabaScraper) extractTierPrices(cells *goquery.Selection, colMap columnMap) []model.PriceTier {
	var tiers []model.PriceTier

	for idx, col := range colMap.tierCols {
		if col >= cells.Length() {
			continue
		}

		priceText := cleanText(cells.Eq(col).Text())
		price, unit := parsePrice(priceText)
		if price <= 0 {
			continue
		}

		pricePerM := convertToPerMillion(price, unit)
		tierName := fmt.Sprintf("tier_%d", idx+1)
		tier := model.PriceTier{
			Name:       tierName,
			MinTokens:  int64(idx) * 1000000,
			InputPrice: pricePerM,
		}
		if idx < len(colMap.tierCols)-1 {
			maxT := int64(idx+1) * 1000000
			tier.MaxTokens = &maxT
		}

		tiers = append(tiers, tier)
	}

	return tiers
}
