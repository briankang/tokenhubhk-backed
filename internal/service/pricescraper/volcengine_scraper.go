package pricescraper

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"go.uber.org/zap"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// =====================================================
// 火山引擎价格爬虫
// 从火山引擎文档页面 HTML 解析模型定价表格
// 目标页面: https://www.volcengine.com/docs/82379/1544106
// =====================================================

const (
	// volcengineURL 火山引擎定价页面地址
	volcengineURL = "https://www.volcengine.com/docs/82379/1544106"
	// volcengineSupplierName 供应商名称标识
	volcengineSupplierName = "火山引擎"
)

// VolcengineScraper 火山引擎价格爬虫
type VolcengineScraper struct {
	client *http.Client
}

// NewVolcengineScraper 创建火山引擎爬虫实例
func NewVolcengineScraper() *VolcengineScraper {
	return &VolcengineScraper{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// ScrapePrices 执行火山引擎价格爬取
// 流程: HTTP GET 页面 → goquery 解析 HTML → 提取定价表格 → 返回结构化数据
func (s *VolcengineScraper) ScrapePrices(ctx context.Context) (*ScrapedPriceData, error) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	// 带重试的 HTTP 请求
	body, err := s.fetchWithRetry(ctx, volcengineURL, 3)
	if err != nil {
		return nil, fmt.Errorf("获取火山引擎定价页面失败: %w", err)
	}
	defer body.Close()

	// 使用 goquery 解析 HTML
	doc, err := goquery.NewDocumentFromReader(body)
	if err != nil {
		return nil, fmt.Errorf("解析火山引擎 HTML 失败: %w", err)
	}

	// 提取所有定价表格
	models, err := s.extractPriceTables(doc)
	if err != nil {
		return nil, fmt.Errorf("提取火山引擎定价表格失败: %w", err)
	}

	if len(models) == 0 {
		log.Warn("火山引擎定价页面未提取到任何模型数据", zap.String("url", volcengineURL))
	} else {
		log.Info("火山引擎价格爬取完成",
			zap.Int("models_count", len(models)),
			zap.String("url", volcengineURL))
	}

	return &ScrapedPriceData{
		SupplierName: volcengineSupplierName,
		FetchedAt:    time.Now(),
		Models:       models,
		SourceURL:    volcengineURL,
	}, nil
}

// fetchWithRetry 带指数退避重试的 HTTP GET 请求
// maxRetries: 最大重试次数
func (s *VolcengineScraper) fetchWithRetry(ctx context.Context, url string, maxRetries int) (io.ReadCloser, error) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// 指数退避: 1s, 2s, 4s...
			backoff := time.Duration(math.Pow(2, float64(attempt-1))) * time.Second
			log.Info("重试获取页面",
				zap.Int("attempt", attempt+1),
				zap.Duration("backoff", backoff),
				zap.String("url", url))
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
		// 设置浏览器 UA，避免被反爬拦截
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

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

// extractPriceTables 从 HTML 文档中提取所有定价表格
// 火山引擎文档页面通常包含多个表格，每个表格对应一类模型
func (s *VolcengineScraper) extractPriceTables(doc *goquery.Document) ([]ScrapedModel, error) {
	var allModels []ScrapedModel
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	// 遍历页面中所有 <table> 元素
	doc.Find("table").Each(func(tableIdx int, table *goquery.Selection) {
		// 解析表头，判断是否为定价表格
		headers := s.extractTableHeaders(table)
		if !s.isPriceTable(headers) {
			return // 非定价表格，跳过
		}

		log.Debug("发现定价表格",
			zap.Int("table_index", tableIdx),
			zap.Strings("headers", headers))

		// 解析表格数据行
		models := s.parseTableRows(table, headers)
		allModels = append(allModels, models...)
	})

	return allModels, nil
}

// extractTableHeaders 提取表格的表头列名
func (s *VolcengineScraper) extractTableHeaders(table *goquery.Selection) []string {
	var headers []string
	table.Find("thead tr th, thead tr td").Each(func(_ int, th *goquery.Selection) {
		text := strings.TrimSpace(th.Text())
		headers = append(headers, text)
	})
	// 如果 thead 为空，尝试取第一行 tr
	if len(headers) == 0 {
		table.Find("tr").First().Find("th, td").Each(func(_ int, cell *goquery.Selection) {
			text := strings.TrimSpace(cell.Text())
			headers = append(headers, text)
		})
	}
	return headers
}

// isPriceTable 判断表头是否包含价格相关关键字
// 火山引擎定价表格通常包含"模型"、"价格"或"token"等关键词
func (s *VolcengineScraper) isPriceTable(headers []string) bool {
	hasModel := false
	hasPrice := false

	for _, h := range headers {
		h = strings.ToLower(h)
		if strings.Contains(h, "模型") || strings.Contains(h, "model") {
			hasModel = true
		}
		if strings.Contains(h, "价格") || strings.Contains(h, "price") ||
			strings.Contains(h, "token") || strings.Contains(h, "计费") ||
			strings.Contains(h, "输入") || strings.Contains(h, "输出") {
			hasPrice = true
		}
	}

	return hasModel && hasPrice
}

// parseTableRows 解析表格数据行，提取模型名称和价格
func (s *VolcengineScraper) parseTableRows(table *goquery.Selection, headers []string) []ScrapedModel {
	var models []ScrapedModel
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	// 识别各列的语义索引
	colMap := s.identifyColumns(headers)

	// 遍历数据行（跳过表头行）
	rows := table.Find("tbody tr")
	if rows.Length() == 0 {
		// 没有 tbody，跳过第一行（表头）
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
			return // 无模型名称，跳过
		}

		model := ScrapedModel{
			ModelName:   modelName,
			DisplayName: modelName,
			Currency:    "CNY",
		}

		// 提取输入价格
		if colMap.inputCol >= 0 && colMap.inputCol < cells.Length() {
			priceText := cleanText(cells.Eq(colMap.inputCol).Text())
			price, unit := parsePrice(priceText)
			model.InputPrice = convertToPerMillion(price, unit)
		}

		// 提取输出价格
		if colMap.outputCol >= 0 && colMap.outputCol < cells.Length() {
			priceText := cleanText(cells.Eq(colMap.outputCol).Text())
			price, unit := parsePrice(priceText)
			model.OutputPrice = convertToPerMillion(price, unit)
		}

		// 尝试提取阶梯价格（如果表格有多个价格列）
		if len(colMap.tierCols) > 0 {
			tiers := s.extractTierPrices(cells, colMap)
			if len(tiers) > 0 {
				model.PriceTiers = tiers
			}
		}

		// 价格兜底：如果基础价格为 0 但有阶梯价格，取第一个阶梯的价格
		if model.InputPrice == 0 && len(model.PriceTiers) > 0 {
			model.InputPrice = model.PriceTiers[0].InputPrice
		}
		if model.OutputPrice == 0 && len(model.PriceTiers) > 0 {
			model.OutputPrice = model.PriceTiers[0].OutputPrice
		}

		if model.InputPrice > 0 || model.OutputPrice > 0 || len(model.PriceTiers) > 0 {
			models = append(models, model)
		} else {
			log.Debug("跳过无价格数据的行",
				zap.String("model", modelName),
				zap.Int("row", rowIdx))
		}
	})

	return models
}

// columnMap 表格列的语义映射
type columnMap struct {
	modelCol  int   // 模型名称列
	inputCol  int   // 输入价格列
	outputCol int   // 输出价格列
	tierCols  []int // 阶梯价格列（可能有多个）
}

// identifyColumns 根据表头识别各列的语义
func (s *VolcengineScraper) identifyColumns(headers []string) columnMap {
	cm := columnMap{modelCol: -1, inputCol: -1, outputCol: -1}

	for i, h := range headers {
		h = strings.ToLower(h)
		switch {
		case strings.Contains(h, "模型") || strings.Contains(h, "model"):
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
		case strings.Contains(h, "价格") || strings.Contains(h, "price") || strings.Contains(h, "token"):
			// 通用价格列，可能是阶梯
			cm.tierCols = append(cm.tierCols, i)
		}
	}

	// 默认取第一列为模型列
	if cm.modelCol < 0 && len(headers) > 0 {
		cm.modelCol = 0
	}

	return cm
}

// extractTierPrices 从表格行中提取阶梯价格
func (s *VolcengineScraper) extractTierPrices(cells *goquery.Selection, colMap columnMap) []model.PriceTier {
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

		// 构建阶梯名称（根据列索引）
		tierName := fmt.Sprintf("tier_%d", idx+1)
		tier := model.PriceTier{
			Name:       tierName,
			MinTokens:  int64(idx) * 1000000, // 假设每阶梯 100 万 token
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

// =====================================================
// 辅助函数：文本清洗和价格解析
// =====================================================

// priceRegex 匹配数字（含小数点）
var priceRegex = regexp.MustCompile(`[\d]+\.?[\d]*`)

// cleanText 清洗文本：去除空白字符和特殊字符
func cleanText(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.ReplaceAll(s, "\u00a0", " ") // non-breaking space
	// 压缩连续空格
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return s
}

// parsePrice 从文本中提取价格数值和单位
// 返回：价格数值，单位标识（"per_k"=每千token, "per_m"=每百万token, "unknown"）
func parsePrice(text string) (float64, string) {
	if text == "" {
		return 0, "unknown"
	}

	// 提取数字部分
	matches := priceRegex.FindString(text)
	if matches == "" {
		return 0, "unknown"
	}

	price, err := strconv.ParseFloat(matches, 64)
	if err != nil {
		return 0, "unknown"
	}

	// 判断单位
	textLower := strings.ToLower(text)
	unit := "unknown"
	switch {
	case strings.Contains(textLower, "百万") || strings.Contains(textLower, "million") || strings.Contains(textLower, "/m "):
		unit = "per_m" // 每百万 token
	case strings.Contains(textLower, "千") || strings.Contains(textLower, "1k") || strings.Contains(textLower, "/k"):
		unit = "per_k" // 每千 token
	default:
		// 火山引擎常用"元/千tokens"格式
		if strings.Contains(textLower, "tokens") || strings.Contains(textLower, "token") {
			if price < 1 {
				// 价格较小，可能是每千 token
				unit = "per_k"
			} else {
				unit = "per_m"
			}
		} else {
			// 无法判断单位，默认按每百万 token 处理
			unit = "per_m"
		}
	}

	return price, unit
}

// convertToPerMillion 将价格统一转换为 RMB/百万token
func convertToPerMillion(price float64, unit string) float64 {
	switch unit {
	case "per_k":
		// 每千 token → 每百万 token: ×1000
		return price * 1000
	case "per_m":
		return price
	default:
		return price // 默认当作每百万 token
	}
}
