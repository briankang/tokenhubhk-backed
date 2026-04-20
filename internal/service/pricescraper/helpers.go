package pricescraper

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"tokenhub-server/internal/model"
)

// =====================================================
// 共享工具函数
// 为火山引擎和阿里云爬虫提供通用的 HTML 表格解析和价格处理能力
// =====================================================

// priceRegex 匹配数字（含小数点）
var priceRegex = regexp.MustCompile(`[\d]+\.?[\d]*`)

// conditionRegex 匹配阶梯条件文本（如 "输入长度 [0, 32]"、"32K<Token≤128K"、"上下文长度"、"请求上下文"）
var conditionRegex = regexp.MustCompile(`(?i)(?:输入长度|输出长度|上下文长度|请求上下文|上下文|tokens区间|token|Token|<|≤|<=|>|≥|>=|\[.*\])`)

// annotationPatterns 模型名称中需要去除的注解（阿里云常见）
var annotationPatterns = []string{
	"Batch调用半价",
	"上下文缓存享有折扣",
	"Batch调用",
	"限时免费",
	"限时优惠",
}

// columnMap 表格列的语义映射
type columnMap struct {
	modelCol     int   // 模型名称列
	conditionCol int   // 条件列（阶梯条件，如 "输入长度 [0, 32]"）
	inputCol     int   // 输入价格列
	outputCol    int   // 输出价格列
	tierCols     []int // 阶梯价格列（可能有多个）
}

// cleanText 清洗文本：去除空白字符和特殊字符
func cleanText(s string) string {
	s = strings.TrimSpace(s)
	// 去除零宽字符（火山引擎/阿里云页面常见）
	s = strings.ReplaceAll(s, "\u200B", "") // zero-width space
	s = strings.ReplaceAll(s, "\u200C", "") // zero-width non-joiner
	s = strings.ReplaceAll(s, "\u200D", "") // zero-width joiner
	s = strings.ReplaceAll(s, "\uFEFF", "") // BOM
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.ReplaceAll(s, "\u00a0", " ") // non-breaking space
	// 压缩连续空格
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return strings.TrimSpace(s)
}

// parsePrice 从文本中提取价格数值
// 火山引擎和阿里云的表头已标注单位为"元/百万token"，所以返回值直接为 per_m
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
		// 火山引擎/阿里云表头标注为"元/百万token"，数据单元格只有数字
		// 默认按每百万 token 处理
		unit = "per_m"
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

// extractTableHeaders 提取表格的表头列名
func extractTableHeaders(table *goquery.Selection) []string {
	var headers []string
	table.Find("thead tr th, thead tr td").Each(func(_ int, th *goquery.Selection) {
		text := cleanText(th.Text())
		headers = append(headers, text)
	})
	// 如果 thead 为空，尝试取第一行 tr
	if len(headers) == 0 {
		table.Find("tr").First().Find("th, td").Each(func(_ int, cell *goquery.Selection) {
			text := cleanText(cell.Text())
			headers = append(headers, text)
		})
	}
	return headers
}

// isPriceTable 判断表头是否包含价格相关关键字
// 定价表格通常包含"模型"、"价格"或"token"等关键词
func isPriceTable(headers []string) bool {
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

// identifyColumns 根据表头识别各列的语义
func identifyColumns(headers []string) columnMap {
	cm := columnMap{modelCol: -1, conditionCol: -1, inputCol: -1, outputCol: -1}

	for i, h := range headers {
		hLower := strings.ToLower(h)
		switch {
		case strings.Contains(hLower, "模型") || strings.Contains(hLower, "model") || strings.Contains(hLower, "名称"):
			if cm.modelCol < 0 {
				cm.modelCol = i
			}
		case strings.Contains(hLower, "条件") || strings.Contains(hLower, "context"):
			if cm.conditionCol < 0 {
				cm.conditionCol = i
			}
		case strings.Contains(hLower, "输入") || strings.Contains(hLower, "input"):
			if cm.inputCol < 0 {
				cm.inputCol = i
			}
		case strings.Contains(hLower, "输出") || strings.Contains(hLower, "output"):
			if cm.outputCol < 0 {
				cm.outputCol = i
			}
		case strings.Contains(hLower, "价格") || strings.Contains(hLower, "price") || strings.Contains(hLower, "单价") || strings.Contains(hLower, "费用"):
			cm.tierCols = append(cm.tierCols, i)
		}
	}

	// 默认取第一列为模型列
	if cm.modelCol < 0 && len(headers) > 0 {
		cm.modelCol = 0
	}

	return cm
}

// isConditionText 判断文本是否是阶梯条件（非模型名称）
func isConditionText(text string) bool {
	return conditionRegex.MatchString(text)
}

// cleanModelName 去除模型名称中的注解文字，只保留模型标识
// 例如 "qwen3-max Batch调用半价 上下文缓存享有折扣" → "qwen3-max"
func cleanModelName(name string) string {
	for _, pattern := range annotationPatterns {
		name = strings.ReplaceAll(name, pattern, "")
	}
	name = strings.TrimSpace(name)
	// 压缩连续空格
	for strings.Contains(name, "  ") {
		name = strings.ReplaceAll(name, "  ", " ")
	}
	return name
}

// parseTableRows 解析表格数据行，提取模型名称和价格
// 支持 rowspan 合并行：当模型名称单元格为空时，继承上一行的模型名称
// 阶梯条件行（如不同输入长度对应不同价格）会合并到同一模型的 PriceTiers 中
func parseTableRows(table *goquery.Selection, headers []string) []ScrapedModel {
	colMap := identifyColumns(headers)

	// 使用 map 将同一模型的多行数据合并
	type modelEntry struct {
		sm    ScrapedModel
		order int // 保持原始顺序
	}
	modelMap := make(map[string]*modelEntry)
	var modelOrder []string // 保持插入顺序
	orderCounter := 0

	// 追踪 rowspan 导致的模型名称继承
	lastModelName := ""

	// 遍历数据行（跳过表头行）
	rows := table.Find("tbody tr")
	if rows.Length() == 0 {
		rows = table.Find("tr").Slice(1, goquery.ToEnd)
	}

	rows.Each(func(rowIdx int, row *goquery.Selection) {
		cells := row.Find("td")
		if cells.Length() == 0 {
			return
		}

		// 提取模型名称（支持 rowspan 继承）
		modelName := ""
		if colMap.modelCol >= 0 && colMap.modelCol < cells.Length() {
			modelName = cleanText(cells.Eq(colMap.modelCol).Text())
		}

		// 清洗模型名称（去除注解文字）
		modelName = cleanModelName(modelName)

		// 如果模型名称为空（rowspan 合并行），使用上一个模型名称
		if modelName == "" {
			modelName = lastModelName
		} else {
			// 检查是否实际是条件文本被误读为模型名称
			if isConditionText(modelName) && lastModelName != "" {
				// 这是条件行，不是新模型
				modelName = lastModelName
			} else {
				lastModelName = modelName
			}
		}

		if modelName == "" {
			return
		}

		// 提取输入价格
		var inputPrice float64
		if colMap.inputCol >= 0 && colMap.inputCol < cells.Length() {
			priceText := cleanText(cells.Eq(colMap.inputCol).Text())
			price, unit := parsePrice(priceText)
			inputPrice = convertToPerMillion(price, unit)
		}

		// 提取输出价格
		var outputPrice float64
		if colMap.outputCol >= 0 && colMap.outputCol < cells.Length() {
			priceText := cleanText(cells.Eq(colMap.outputCol).Text())
			price, unit := parsePrice(priceText)
			outputPrice = convertToPerMillion(price, unit)
		}

		// 提取阶梯价格
		var tierPrices []model.PriceTier
		if len(colMap.tierCols) > 0 {
			tierPrices = extractTierPrices(cells, colMap)
		}

		// 提取条件文本（用于阶梯命名）
		conditionText := ""
		if colMap.conditionCol >= 0 && colMap.conditionCol < cells.Length() {
			conditionText = cleanText(cells.Eq(colMap.conditionCol).Text())
		}

		// 跳过没有价格的行
		if inputPrice == 0 && outputPrice == 0 && len(tierPrices) == 0 {
			return
		}

		// 查找或创建模型条目
		entry, exists := modelMap[modelName]
		if !exists {
			entry = &modelEntry{
				sm: ScrapedModel{
					ModelName:   modelName,
					DisplayName: modelName,
					Currency:    "CNY",
					PricingUnit: PricingUnitPerMillionTokens,
					ModelType:   "LLM",
				},
				order: orderCounter,
			}
			orderCounter++
			modelMap[modelName] = entry
			modelOrder = append(modelOrder, modelName)
		}

		// 第一行（基础价格行）
		if entry.sm.InputPrice == 0 && inputPrice > 0 {
			entry.sm.InputPrice = inputPrice
		}
		if entry.sm.OutputPrice == 0 && outputPrice > 0 {
			entry.sm.OutputPrice = outputPrice
		}

		// 有条件文本时，作为阶梯价格记录
		if conditionText != "" && (inputPrice > 0 || outputPrice > 0) {
			tier := model.PriceTier{
				Name:        conditionText,
				InputPrice:  inputPrice,
				OutputPrice: outputPrice,
			}
			// 解析条件文本，填充 InputMin/InputMax/OutputMin/OutputMax
			parseConditionInto2D(conditionText, &tier)
			tier.Normalize()
			entry.sm.PriceTiers = append(entry.sm.PriceTiers, tier)
		}

		// 合并阶梯价格
		if len(tierPrices) > 0 {
			entry.sm.PriceTiers = append(entry.sm.PriceTiers, tierPrices...)
		}
	})

	// 按原始顺序输出结果
	var models []ScrapedModel
	for _, name := range modelOrder {
		entry := modelMap[name]
		sm := entry.sm

		// 兜底：阶梯价格填充基础价格
		if sm.InputPrice == 0 && len(sm.PriceTiers) > 0 {
			sm.InputPrice = sm.PriceTiers[0].InputPrice
		}
		if sm.OutputPrice == 0 && len(sm.PriceTiers) > 0 {
			sm.OutputPrice = sm.PriceTiers[0].OutputPrice
		}

		if sm.InputPrice > 0 || sm.OutputPrice > 0 || len(sm.PriceTiers) > 0 {
			models = append(models, sm)
		}
	}

	return models
}

// extractTierPrices 从表格行中提取阶梯价格
func extractTierPrices(cells *goquery.Selection, colMap columnMap) []model.PriceTier {
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

// extractPriceTables 从 HTML 文档中提取所有定价表格（通用版）
// 跨表格去重：同名模型保留价格更完整的条目（优先保留 input+output 都有值的）
func extractPriceTables(doc *goquery.Document) []ScrapedModel {
	// 使用 map 去重，保留最优条目
	bestModels := make(map[string]ScrapedModel)
	var insertOrder []string

	doc.Find("table").Each(func(tableIdx int, table *goquery.Selection) {
		headers := extractTableHeaders(table)
		if !isPriceTable(headers) {
			return
		}
		models := parseTableRows(table, headers)
		for _, m := range models {
			existing, exists := bestModels[m.ModelName]
			if !exists {
				bestModels[m.ModelName] = m
				insertOrder = append(insertOrder, m.ModelName)
				continue
			}
			// 选择更完整的条目：优先两个价格都有值的
			existingScore := priceScore(existing)
			newScore := priceScore(m)
			if newScore > existingScore {
				bestModels[m.ModelName] = m
			}
		}
	})

	// 按插入顺序输出
	var result []ScrapedModel
	for _, name := range insertOrder {
		result = append(result, bestModels[name])
	}
	return result
}

// priceScore 评估价格条目的完整度（用于去重时选择最优）
func priceScore(m ScrapedModel) int {
	score := 0
	if m.InputPrice > 0 {
		score += 2
	}
	if m.OutputPrice > 0 {
		score += 2
	}
	if m.InputPrice > 0 && m.OutputPrice > 0 {
		score += 3 // 两者都有额外加分
	}
	score += len(m.PriceTiers)
	return score
}

// parseConditionInto2D 解析条件文本到 PriceTier 的二维区间字段
// 支持格式：
//   "输入长度 [0, 32]"  → InputMin=0, InputMax=32（闭区间）
//   "(32, 128]"        → InputMin=32 (开), InputMax=128 (闭) — 默认视为输入维度
//   "输入(0,32k] 输出<=4k" → 双维度同时设置
//   "32K<Token≤128K"   → InputMin=32000, InputMax=128000
//   "输入<=128k"        → InputMax=128000
//
// 无法识别的文本保持 tier.Name 原样，Input/Output 保持零值（由 Normalize 后变默认全覆盖）
func parseConditionInto2D(text string, tier *model.PriceTier) {
	if text == "" || tier == nil {
		return
	}

	// 按分隔符切分子表达式（支持：斜杠、逗号、×、x、中文"与/且"、英文 "and"）
	sep := regexp.MustCompile(`(?i)\s*(?:/|,|，|×|x|\bx\b|\band\b|与|且)\s*`)
	parts := sep.Split(text, -1)

	parsed := false
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		lower := strings.ToLower(part)
		isOutput := strings.Contains(part, "输出") ||
			strings.Contains(lower, "output") ||
			strings.Contains(lower, "completion")
		isInput := strings.Contains(part, "输入") ||
			strings.Contains(lower, "input") ||
			strings.Contains(lower, "prompt") ||
			strings.Contains(part, "上下文") ||
			strings.Contains(part, "请求上下文") ||
			strings.Contains(lower, "context")

		// 剥离中文前缀便于 ParseRangeExpression 识别
		stripped := stripDimensionKeyword(part)

		min, minExcl, maxPtr, maxExcl, err := model.ParseRangeExpression(stripped)
		if err != nil {
			continue
		}

		if isOutput {
			tier.OutputMin = min
			tier.OutputMinExclusive = minExcl
			tier.OutputMax = maxPtr
			tier.OutputMaxExclusive = maxExcl
			parsed = true
		} else {
			// 未标记或显式输入 → 视为输入维度
			tier.InputMin = min
			tier.InputMinExclusive = minExcl
			tier.InputMax = maxPtr
			tier.InputMaxExclusive = maxExcl
			_ = isInput
			parsed = true
		}
	}

	_ = parsed
}

// =====================================================
// 批量抓取辅助工具
// 为 batch_scrape.go 提供模型名归一化、模糊匹配、缓存行识别能力
// =====================================================

// NormalizeModelName 归一化模型名，用于跨源匹配
// 处理：小写 / 去除空白 / 统一版本分隔符（1-5 → 1.5 或反之）/ 去除日期后缀 (-YYYYMMDD / -MMDD)
func NormalizeModelName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	// 替换全角/半角分隔符
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, ".", "-")
	s = strings.ReplaceAll(s, " ", "-")
	// 压缩连续 -
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-")
	// 去除形如 -20241201 / -1201 的日期后缀
	s = trimDatedSuffix(s)
	return s
}

// trimDatedSuffix 去除 YYYYMMDD / MMDD 结尾
func trimDatedSuffix(s string) string {
	// 8 位数字结尾
	if len(s) > 9 && s[len(s)-9] == '-' && isAllDigits(s[len(s)-8:]) {
		return s[:len(s)-9]
	}
	// 4 位数字结尾
	if len(s) > 5 && s[len(s)-5] == '-' && isAllDigits(s[len(s)-4:]) {
		return s[:len(s)-5]
	}
	return s
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// FuzzyMatchModel 在已抓取的模型池中寻找与 target 最匹配的一条
// 策略（按优先级降序）：
//  1. 精确匹配（原始小写）
//  2. 归一化后精确匹配
//  3. 归一化后互为前缀（阈值 5 字符以上）
//  4. 归一化后一方包含另一方（阈值 5 字符以上）
// 返回 nil 表示未找到。
func FuzzyMatchModel(target string, pool []ScrapedModel) *ScrapedModel {
	if target == "" || len(pool) == 0 {
		return nil
	}
	tLower := strings.ToLower(strings.TrimSpace(target))
	tNorm := NormalizeModelName(target)

	// 1. 精确匹配
	for i := range pool {
		if strings.ToLower(pool[i].ModelName) == tLower {
			return &pool[i]
		}
	}

	// 2. 归一化精确匹配
	var bestPrefix *ScrapedModel
	var bestPrefixLen int
	var bestContain *ScrapedModel
	var bestContainLen int
	for i := range pool {
		p := &pool[i]
		pNorm := NormalizeModelName(p.ModelName)
		if pNorm == tNorm {
			return p
		}
		// 3. 前缀匹配
		if len(tNorm) >= 5 && len(pNorm) >= 5 {
			if strings.HasPrefix(pNorm, tNorm) || strings.HasPrefix(tNorm, pNorm) {
				l := len(pNorm)
				if len(tNorm) < l {
					l = len(tNorm)
				}
				if l > bestPrefixLen {
					bestPrefix = p
					bestPrefixLen = l
				}
				continue
			}
			// 4. 包含匹配
			if strings.Contains(pNorm, tNorm) || strings.Contains(tNorm, pNorm) {
				l := len(pNorm)
				if len(tNorm) < l {
					l = len(tNorm)
				}
				if l > bestContainLen {
					bestContain = p
					bestContainLen = l
				}
			}
		}
	}
	if bestPrefix != nil {
		return bestPrefix
	}
	return bestContain
}

// CacheRow 从定价页识别到的缓存相关价格行
type CacheRow struct {
	ModelName      string
	HitPrice       float64 // 缓存命中价（隐式 或 explicit 的 read）
	WritePrice     float64 // 缓存写入价
	StoragePrice   float64 // 缓存存储价
	Mechanism      string  // auto / explicit / both（猜测）
	MinTokens      int     // 门槛 tokens，可选
	RawDescription string
}

// cacheKeywordsHit 定价页中标识"缓存命中"的关键字
var cacheKeywordsHit = []string{"缓存命中", "命中缓存", "cache read", "cache hit", "cache-hit", "input (cached)", "缓存读取", "读取缓存"}

// cacheKeywordsWrite 缓存写入关键字
var cacheKeywordsWrite = []string{"缓存写入", "写入缓存", "cache write", "cache-write", "cache creation"}

// cacheKeywordsStorage 缓存存储关键字
var cacheKeywordsStorage = []string{"缓存存储", "存储缓存", "cache storage", "cache-store"}

// extractCacheRows 扫描 HTML 文档中带"缓存"字样的表格行，返回 modelNameLower -> CacheRow
// 识别逻辑：某一行若包含上面三类关键字之一，即视为缓存行；从同行/上一行/rowspan 推断模型名
func extractCacheRows(doc *goquery.Document) map[string]CacheRow {
	result := make(map[string]CacheRow)
	if doc == nil {
		return result
	}

	doc.Find("table").Each(func(_ int, table *goquery.Selection) {
		headers := extractTableHeaders(table)
		if !isPriceTable(headers) {
			return
		}
		colMap := identifyColumns(headers)

		lastModelName := ""
		rows := table.Find("tbody tr")
		if rows.Length() == 0 {
			rows = table.Find("tr").Slice(1, goquery.ToEnd)
		}

		rows.Each(func(_ int, row *goquery.Selection) {
			cells := row.Find("td")
			if cells.Length() == 0 {
				return
			}

			modelName := ""
			if colMap.modelCol >= 0 && colMap.modelCol < cells.Length() {
				modelName = cleanModelName(cleanText(cells.Eq(colMap.modelCol).Text()))
			}
			if modelName == "" || isConditionText(modelName) {
				modelName = lastModelName
			} else {
				lastModelName = modelName
			}
			if modelName == "" {
				return
			}

			// 拼接本行全部文本用于关键词识别
			rowText := cleanText(row.Text())
			rowLower := strings.ToLower(rowText)

			isHit := containsAnyStr(rowLower, cacheKeywordsHit...)
			isWrite := containsAnyStr(rowLower, cacheKeywordsWrite...)
			isStorage := containsAnyStr(rowLower, cacheKeywordsStorage...)
			if !isHit && !isWrite && !isStorage {
				return
			}

			// 提取该行的输入价作为缓存价
			var price float64
			if colMap.inputCol >= 0 && colMap.inputCol < cells.Length() {
				p, unit := parsePrice(cleanText(cells.Eq(colMap.inputCol).Text()))
				price = convertToPerMillion(p, unit)
			}
			if price <= 0 {
				// 退化扫描全部数字单元格，取第一个非零
				cells.Each(func(_ int, td *goquery.Selection) {
					if price > 0 {
						return
					}
					p, unit := parsePrice(cleanText(td.Text()))
					if p > 0 {
						price = convertToPerMillion(p, unit)
					}
				})
			}
			if price <= 0 {
				return
			}

			entry := result[strings.ToLower(modelName)]
			entry.ModelName = modelName
			entry.RawDescription = rowText
			if isHit && entry.HitPrice == 0 {
				entry.HitPrice = price
			}
			if isWrite && entry.WritePrice == 0 {
				entry.WritePrice = price
				if entry.Mechanism == "" {
					entry.Mechanism = "explicit"
				}
			}
			if isStorage && entry.StoragePrice == 0 {
				entry.StoragePrice = price
			}
			result[strings.ToLower(modelName)] = entry
		})
	})

	return result
}

// stripDimensionKeyword 去除"输入/输出/上下文"等维度前缀，保留纯区间表达式
func stripDimensionKeyword(s string) string {
	keywords := []string{
		"输入长度", "输出长度", "请求上下文", "上下文长度", "上下文",
		"输入", "输出",
		"prompt tokens", "completion tokens", "context tokens",
		"prompt", "completion", "context", "input", "output",
	}
	lower := strings.ToLower(s)
	for _, kw := range keywords {
		lkw := strings.ToLower(kw)
		if idx := strings.Index(lower, lkw); idx >= 0 {
			// 去除该关键词
			s = s[:idx] + s[idx+len(kw):]
			lower = strings.ToLower(s)
		}
	}
	return strings.TrimSpace(s)
}
