package model

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// DefaultTier 生成默认兜底阶梯 (0, +∞] × (0, +∞]
// 用于没有显式分档的模型，保证计费链路统一走阶梯选择器
func DefaultTier(inputPrice, outputPrice float64) PriceTier {
	return PriceTier{
		Name:               "(0, +∞] × (0, +∞]",
		InputMin:           0,
		InputMinExclusive:  true,
		InputMax:           nil,
		OutputMin:          0,
		OutputMinExclusive: true,
		OutputMax:          nil,
		InputPrice:         inputPrice,
		OutputPrice:        outputPrice,
	}
}

// IsDefaultTier 判断一个阶梯是否是"全覆盖默认阶梯"
// 全覆盖即 Input/Output 都是 (0, +∞]
func (t PriceTier) IsDefaultTier() bool {
	return t.InputMin == 0 && t.InputMinExclusive && t.InputMax == nil &&
		t.OutputMin == 0 && t.OutputMinExclusive && t.OutputMax == nil
}

// Matches 判断 (inputTokens, outputTokens) 是否落在本阶梯区间内
// 二维 AND 条件：输入必须命中 AND 输出必须命中
func (t PriceTier) Matches(inputTokens, outputTokens int64) bool {
	return matchRange(inputTokens, t.InputMin, t.InputMinExclusive, t.InputMax, t.InputMaxExclusive) &&
		matchRange(outputTokens, t.OutputMin, t.OutputMinExclusive, t.OutputMax, t.OutputMaxExclusive)
}

// matchRange 单维度区间匹配
// min 开(Exclusive=true)=严格大于，闭=大于等于；max nil=+∞
func matchRange(v, min int64, minExcl bool, max *int64, maxExcl bool) bool {
	// 下界
	if minExcl {
		if v <= min {
			return false
		}
	} else {
		if v < min {
			return false
		}
	}
	// 上界
	if max != nil {
		if maxExcl {
			if v >= *max {
				return false
			}
		} else {
			if v > *max {
				return false
			}
		}
	}
	return true
}

// Normalize 同步新旧字段 + 修正零值
// 旧字段 MinTokens/MaxTokens 迁移到 InputMin/InputMax（仅当新字段为默认值时）
// Output 维度未设置时填默认 (0, +∞]
func (t *PriceTier) Normalize() {
	// 1. 从旧字段回填到新字段（仅在新字段未设置时）
	//    判定"未设置"：InputMin==0 && InputMinExclusive==false && InputMax==nil
	legacyActive := t.MinTokens > 0 || t.MaxTokens != nil
	newDefault := t.InputMin == 0 && !t.InputMinExclusive && t.InputMax == nil
	if legacyActive && newDefault {
		t.InputMin = t.MinTokens
		t.InputMax = t.MaxTokens
		// 旧语义：MinTokens 为闭区间下界，保持 Exclusive=false
		t.InputMinExclusive = false
		t.InputMaxExclusive = false
	}

	// 2. 同步新字段回旧字段（方便读旧代码）
	t.MinTokens = t.InputMin
	t.MaxTokens = t.InputMax

	// 3. Output 维度如果完全未设置，填默认 (0, +∞]
	//    判定"未设置"：OutputMin==0 && OutputMinExclusive==false && OutputMax==nil
	if t.OutputMin == 0 && !t.OutputMinExclusive && t.OutputMax == nil {
		t.OutputMinExclusive = true
	}

	// 4. 如果 InputMin==0 且没有任何其他标记，也视为 (0, +∞] 默认下界
	//    但用户显式设置 InputMinExclusive=false + InputMin=0 应保留（表示 [0, ...)）
	//    这里不主动改写，交由调用方。

	// 5. 自动生成 Name（UI 上用户不再手动输入阶梯名称）
	if strings.TrimSpace(t.Name) == "" {
		t.Name = t.AutoName()
	}
}

// AutoName 按区间自动生成阶梯名称，供 UI 展示与保存兜底。
// 规则：
//   - 默认全覆盖 (0, +∞] × (0, +∞] → "0-无限"
//   - 仅有输入上界 → "(0, <max>]" 或 "[0, <max>)"
//   - 有输入下界和上界 → "(min, max]" 等
//   - 无法表达 → "tier"
func (t PriceTier) AutoName() string {
	lowerBracket := "("
	if !t.InputMinExclusive {
		lowerBracket = "["
	}
	// 默认全覆盖
	if t.InputMin == 0 && t.InputMinExclusive && t.InputMax == nil {
		return "0-无限"
	}
	if t.InputMin == 0 && !t.InputMinExclusive && t.InputMax == nil {
		return "0-无限"
	}

	minStr := formatTokenLabel(t.InputMin)

	if t.InputMax == nil {
		return fmt.Sprintf("%s%s, +∞)", lowerBracket, minStr)
	}

	upperBracket := "]"
	if t.InputMaxExclusive {
		upperBracket = ")"
	}
	maxStr := formatTokenLabel(*t.InputMax)
	return fmt.Sprintf("%s%s, %s%s", lowerBracket, minStr, maxStr, upperBracket)
}

// formatTokenLabel 将 token 数转为简短标签
//   32000   → "32k"
//   128000  → "128k"
//   1000000 → "1M"
//   100     → "100"
func formatTokenLabel(n int64) string {
	if n <= 0 {
		return "0"
	}
	if n%1_000_000 == 0 {
		return fmt.Sprintf("%dM", n/1_000_000)
	}
	if n%1_000 == 0 {
		return fmt.Sprintf("%dk", n/1_000)
	}
	return strconv.FormatInt(n, 10)
}

// Validate 校验区间合法性
func (t PriceTier) Validate() error {
	if t.InputMin < 0 {
		return fmt.Errorf("input_min must be non-negative, got %d", t.InputMin)
	}
	if t.OutputMin < 0 {
		return fmt.Errorf("output_min must be non-negative, got %d", t.OutputMin)
	}
	if t.InputMax != nil && *t.InputMax < t.InputMin {
		return fmt.Errorf("input_max (%d) must be >= input_min (%d)", *t.InputMax, t.InputMin)
	}
	if t.OutputMax != nil && *t.OutputMax < t.OutputMin {
		return fmt.Errorf("output_max (%d) must be >= output_min (%d)", *t.OutputMax, t.OutputMin)
	}
	if t.InputPrice < 0 || t.OutputPrice < 0 {
		return fmt.Errorf("prices must be non-negative")
	}
	return nil
}

// SelectTier 在 tiers 列表中查找第一个能同时容纳 (inputTokens, outputTokens) 的阶梯
// tiers 应按 InputMin 升序排列（由调用方或 Sort 保证）
// 未命中返回 (-1, nil)
func SelectTier(tiers []PriceTier, inputTokens, outputTokens int64) (int, *PriceTier) {
	for i := range tiers {
		if tiers[i].Matches(inputTokens, outputTokens) {
			return i, &tiers[i]
		}
	}
	return -1, nil
}

// SortTiers 按 InputMin 升序排序（稳定排序）
func SortTiers(tiers []PriceTier) {
	sort.SliceStable(tiers, func(i, j int) bool {
		if tiers[i].InputMin != tiers[j].InputMin {
			return tiers[i].InputMin < tiers[j].InputMin
		}
		// InputMin 相同时按 OutputMin 排序
		return tiers[i].OutputMin < tiers[j].OutputMin
	})
}

// EnsureDefaultTier 若列表为空，注入默认阶梯（便于数据补齐与运行时兜底）
func EnsureDefaultTier(data *PriceTiersData, fallbackInput, fallbackOutput float64) {
	if data == nil {
		return
	}
	if len(data.Tiers) == 0 {
		data.Tiers = []PriceTier{DefaultTier(fallbackInput, fallbackOutput)}
	}
}

// ---- ParseRangeExpression ----
// 支持格式：
//   [0, 32]         闭区间
//   (32, 128]       左开右闭
//   [32, 128)       左闭右开
//   (32, 128)       全开
//   [0, +∞)          无上界
//   32k<input<=128k 不等式
//   input<=128k     只有上界
//   input>=32k      只有下界
//   32k-128k        连字符（一律按 [32k, 128k] 闭区间）
//   0-1M            带单位
//   输入长度 [0, 32]  含中文前缀（自动剥离）

var (
	// 区间式正则：[/( <数> , <数> ]/) 支持 +∞
	rangeRe = regexp.MustCompile(`(?i)([\[\(])\s*([+\-]?[\d.]+[kKmM]?|\+?∞|inf|infinity)\s*,\s*([+\-]?[\d.]+[kKmM]?|\+?∞|inf|infinity)\s*([\]\)])`)
	// 不等式正则：a<b<=c 或 b<=c 或 b>=a
	ineqBothRe = regexp.MustCompile(`(?i)([+\-]?[\d.]+[kKmM]?)\s*(<|<=|≤)\s*\w+\s*(<|<=|≤)\s*([+\-]?[\d.]+[kKmM]?)`)
	ineqLERe   = regexp.MustCompile(`(?i)\w+\s*(<|<=|≤)\s*([+\-]?[\d.]+[kKmM]?)`)
	ineqGERe   = regexp.MustCompile(`(?i)\w+\s*(>|>=|≥)\s*([+\-]?[\d.]+[kKmM]?)`)
	// 连字符式：a-b
	dashRe = regexp.MustCompile(`([+\-]?[\d.]+[kKmM]?)\s*[-–—]\s*([+\-]?[\d.]+[kKmM]?)`)
)

// ParseRangeExpression 解析区间表达式
// 返回 (min, minExclusive, max *int64, maxExclusive, err)
// 无上界时 max=nil；解析失败返回错误
func ParseRangeExpression(expr string) (int64, bool, *int64, bool, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return 0, false, nil, false, fmt.Errorf("empty expression")
	}

	// 1. 尝试区间式 [a, b] / (a, b] 等
	if m := rangeRe.FindStringSubmatch(expr); m != nil {
		leftBracket := m[1]
		minStr := m[2]
		maxStr := m[3]
		rightBracket := m[4]

		minVal, err := parseTokenCount(minStr)
		if err != nil {
			return 0, false, nil, false, fmt.Errorf("parse min: %w", err)
		}
		var maxPtr *int64
		maxExcl := rightBracket == ")"
		if !isInfinity(maxStr) {
			mv, err := parseTokenCount(maxStr)
			if err != nil {
				return 0, false, nil, false, fmt.Errorf("parse max: %w", err)
			}
			maxPtr = &mv
		} else {
			// +∞ 时 max=nil，exclusive 语义无意义，统一设为 false
			maxExcl = false
		}
		return minVal, leftBracket == "(", maxPtr, maxExcl, nil
	}

	// 2. 尝试双不等式 a < x <= b
	if m := ineqBothRe.FindStringSubmatch(expr); m != nil {
		minVal, err := parseTokenCount(m[1])
		if err != nil {
			return 0, false, nil, false, err
		}
		maxVal, err := parseTokenCount(m[4])
		if err != nil {
			return 0, false, nil, false, err
		}
		return minVal, m[2] == "<", &maxVal, m[3] == "<", nil
	}

	// 3. 尝试单边 x <= b
	if m := ineqLERe.FindStringSubmatch(expr); m != nil {
		maxVal, err := parseTokenCount(m[2])
		if err != nil {
			return 0, false, nil, false, err
		}
		return 0, true, &maxVal, m[1] == "<", nil
	}

	// 4. 尝试单边 x >= a
	if m := ineqGERe.FindStringSubmatch(expr); m != nil {
		minVal, err := parseTokenCount(m[2])
		if err != nil {
			return 0, false, nil, false, err
		}
		return minVal, m[1] == ">", nil, false, nil
	}

	// 5. 连字符式 a-b（按闭区间）
	if m := dashRe.FindStringSubmatch(expr); m != nil {
		minVal, err := parseTokenCount(m[1])
		if err != nil {
			return 0, false, nil, false, err
		}
		maxVal, err := parseTokenCount(m[2])
		if err != nil {
			return 0, false, nil, false, err
		}
		return minVal, false, &maxVal, false, nil
	}

	return 0, false, nil, false, fmt.Errorf("unrecognized range expression: %s", expr)
}

// parseTokenCount 解析带 k/M 后缀的数字为整数 token 数
//   "32" -> 32
//   "32k" -> 32000
//   "128K" -> 128000
//   "1M" -> 1000000
//   "+∞" / "inf" -> 无上界（由调用方处理）
func parseTokenCount(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	if isInfinity(s) {
		return 0, fmt.Errorf("infinity cannot be parsed as concrete value")
	}

	multiplier := int64(1)
	suffix := s[len(s)-1]
	switch suffix {
	case 'k', 'K':
		multiplier = 1000
		s = s[:len(s)-1]
	case 'm', 'M':
		multiplier = 1_000_000
		s = s[:len(s)-1]
	}

	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("parse float %q: %w", s, err)
	}
	return int64(f * float64(multiplier)), nil
}

func isInfinity(s string) bool {
	ls := strings.ToLower(strings.TrimSpace(s))
	return ls == "+∞" || ls == "∞" || ls == "inf" || ls == "infinity" || ls == "+inf"
}
