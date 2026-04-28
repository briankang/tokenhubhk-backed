package model

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func DefaultTier(inputPrice, outputPrice float64) PriceTier {
	return PriceTier{
		Name:               "(0, +inf)",
		InputMin:           0,
		InputMinExclusive:  true,
		OutputMin:          0,
		OutputMinExclusive: true,
		InputPrice:         inputPrice,
		OutputPrice:        outputPrice,
	}
}

func (t PriceTier) IsDefaultTier() bool {
	return t.InputMin == 0 && t.InputMinExclusive && t.InputMax == nil &&
		t.OutputMin == 0 && t.OutputMinExclusive && t.OutputMax == nil
}

func (t PriceTier) Matches(inputTokens, outputTokens int64) bool {
	return matchRange(inputTokens, t.InputMin, t.InputMinExclusive, t.InputMax, t.InputMaxExclusive) &&
		matchRange(outputTokens, t.OutputMin, t.OutputMinExclusive, t.OutputMax, t.OutputMaxExclusive)
}

// MatchesDims 检查 tier 的 DimValues 是否与请求 dims 完全匹配（S1 多维匹配）
//
// 语义：
//   - tier.DimValues 中**每个非空键**都必须在 dims 中找到相同值
//   - tier.DimValues 中的空字符串值视为"该维度不限定"，不参与匹配
//   - dims 中存在但 tier.DimValues 未声明的键 → 不影响匹配（tier 维度更宽松）
//   - tier.DimValues == nil 或全空 → 不参与维度匹配（旧行为，调用方应回退到 token 区间）
//
// 返回：
//   - tier 是否声明了维度（是否需要走维度匹配路径）
//   - 维度匹配是否成功
//
// 调用方应当：tier 未声明维度 → 走 token 区间；声明了 → 必须命中维度才算匹配
func (t PriceTier) MatchesDims(dims map[string]string) (declared bool, matched bool) {
	if len(t.DimValues) == 0 {
		return false, false
	}
	for key, expected := range t.DimValues {
		if expected == "" {
			continue // 空值视为通配
		}
		declared = true
		actual, ok := dims[key]
		if !ok || actual != expected {
			return true, false
		}
	}
	if !declared {
		// 全是空值通配 → 等同未声明
		return false, false
	}
	return true, true
}

func matchRange(v, min int64, minExcl bool, max *int64, maxExcl bool) bool {
	if minExcl {
		if v <= min {
			return false
		}
	} else if v < min {
		return false
	}
	if max != nil {
		if maxExcl {
			if v >= *max {
				return false
			}
		} else if v > *max {
			return false
		}
	}
	return true
}

func (t *PriceTier) Normalize() {
	if t.OutputMin == 0 && !t.OutputMinExclusive && t.OutputMax == nil {
		t.OutputMinExclusive = true
	}
	if strings.TrimSpace(t.Name) == "" {
		t.Name = t.AutoName()
	}
}

func (t PriceTier) AutoName() string {
	lowerBracket := "("
	if !t.InputMinExclusive {
		lowerBracket = "["
	}
	if t.InputMin == 0 && t.InputMax == nil {
		return "0-inf"
	}
	minStr := formatTokenLabel(t.InputMin)
	if t.InputMax == nil {
		return fmt.Sprintf("%s%s, +inf)", lowerBracket, minStr)
	}
	upperBracket := "]"
	if t.InputMaxExclusive {
		upperBracket = ")"
	}
	return fmt.Sprintf("%s%s, %s%s", lowerBracket, minStr, formatTokenLabel(*t.InputMax), upperBracket)
}

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

func SelectTier(tiers []PriceTier, inputTokens, outputTokens int64) (int, *PriceTier) {
	for i := range tiers {
		if tiers[i].Matches(inputTokens, outputTokens) {
			return i, &tiers[i]
		}
	}
	return -1, nil
}

// SelectTierByDims 仅按 DimValues 匹配（S1，2026-04-28）
//
// 用途：当请求侧已知业务维度（resolution/has_input_video/thinking_mode 等）时，
// 优先按维度精确命中 tier，避免 magic-InputMin 编码维度的脆弱性。
//
// 行为：
//   - 遍历 tiers，找第一个 MatchesDims 命中的 tier
//   - 如果有声明 dims 但都不命中 → 返回 -1（调用方应回退到 token 区间或最大档兜底）
//   - 如果 tiers 中**没有任何 tier 声明 DimValues** → 返回 -1（调用方走旧路径）
//
// 注：dims 全空时直接返回 -1，避免误命中"全维度通配"tier
func SelectTierByDims(tiers []PriceTier, dims map[string]string) (int, *PriceTier) {
	if len(dims) == 0 {
		return -1, nil
	}
	hasAnyDeclared := false
	for i := range tiers {
		declared, matched := tiers[i].MatchesDims(dims)
		if declared {
			hasAnyDeclared = true
			if matched {
				return i, &tiers[i]
			}
		}
	}
	_ = hasAnyDeclared
	return -1, nil
}

// SelectTierOrLargest 先严格按输入/输出区间匹配；若配置了阶梯但未命中，返回最大阶梯兜底。
//
// 注：此签名保持向后兼容（不接受 dims）。多维匹配请显式调用 SelectTierByDims，
// 由调用方决定优先级（推荐：先 SelectTierByDims，再 SelectTierOrLargest，参见
// pricing/tier_calculator.go 的 selectPriceForTokens 三步匹配实现）。
func SelectTierOrLargest(tiers []PriceTier, inputTokens, outputTokens int64) (int, *PriceTier, bool) {
	idx, tier := SelectTier(tiers, inputTokens, outputTokens)
	if tier != nil {
		return idx, tier, true
	}
	if len(tiers) == 0 {
		return -1, nil, false
	}
	maxIdx := 0
	for i := 1; i < len(tiers); i++ {
		if tierGreater(tiers[i], tiers[maxIdx]) {
			maxIdx = i
		}
	}
	return maxIdx, &tiers[maxIdx], false
}

func tierGreater(a, b PriceTier) bool {
	if a.InputMin != b.InputMin {
		return a.InputMin > b.InputMin
	}
	if a.OutputMin != b.OutputMin {
		return a.OutputMin > b.OutputMin
	}
	if compareMaxBound(a.InputMax, b.InputMax) != 0 {
		return compareMaxBound(a.InputMax, b.InputMax) > 0
	}
	return compareMaxBound(a.OutputMax, b.OutputMax) > 0
}

func compareMaxBound(a, b *int64) int {
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return 1
	}
	if b == nil {
		return -1
	}
	if *a > *b {
		return 1
	}
	if *a < *b {
		return -1
	}
	return 0
}

func SortTiers(tiers []PriceTier) {
	sort.SliceStable(tiers, func(i, j int) bool {
		if tiers[i].InputMin != tiers[j].InputMin {
			return tiers[i].InputMin < tiers[j].InputMin
		}
		return tiers[i].OutputMin < tiers[j].OutputMin
	})
}

func EnsureDefaultTier(data *PriceTiersData, fallbackInput, fallbackOutput float64) {
	if data == nil {
		return
	}
	if len(data.Tiers) == 0 {
		data.Tiers = []PriceTier{DefaultTier(fallbackInput, fallbackOutput)}
	}
}

var (
	rangeRe    = regexp.MustCompile(`(?i)([\[\(])\s*([+\-]?[\d.]+[kKmM]?|\+?inf|infinity)\s*,\s*([+\-]?[\d.]+[kKmM]?|\+?inf|infinity)\s*([\]\)])`)
	ineqBothRe = regexp.MustCompile(`(?i)([+\-]?[\d.]+[kKmM]?)\s*(<|<=)\s*\w+\s*(<|<=)\s*([+\-]?[\d.]+[kKmM]?)`)
	ineqLERe   = regexp.MustCompile(`(?i)\w+\s*(<|<=)\s*([+\-]?[\d.]+[kKmM]?)`)
	ineqGERe   = regexp.MustCompile(`(?i)\w+\s*(>|>=)\s*([+\-]?[\d.]+[kKmM]?)`)
	dashRe     = regexp.MustCompile(`([+\-]?[\d.]+[kKmM]?)\s*-\s*([+\-]?[\d.]+[kKmM]?)`)
)

func ParseRangeExpression(expr string) (int64, bool, *int64, bool, error) {
	expr = strings.TrimSpace(strings.ToLower(expr))
	if expr == "" {
		return 0, false, nil, false, fmt.Errorf("empty expression")
	}
	expr = strings.ReplaceAll(expr, "token", "")
	expr = strings.ReplaceAll(expr, "tokens", "")
	expr = strings.ReplaceAll(expr, " ", "")

	if m := rangeRe.FindStringSubmatch(expr); m != nil {
		minVal, err := parseTokenCount(m[2])
		if err != nil {
			return 0, false, nil, false, fmt.Errorf("parse min: %w", err)
		}
		maxExcl := m[4] == ")"
		var maxPtr *int64
		if !isInfinity(m[3]) {
			maxVal, err := parseTokenCount(m[3])
			if err != nil {
				return 0, false, nil, false, fmt.Errorf("parse max: %w", err)
			}
			maxPtr = &maxVal
		} else {
			maxExcl = false
		}
		return minVal, m[1] == "(", maxPtr, maxExcl, nil
	}

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

	if m := ineqLERe.FindStringSubmatch(expr); m != nil {
		maxVal, err := parseTokenCount(m[2])
		if err != nil {
			return 0, false, nil, false, err
		}
		return 0, true, &maxVal, m[1] == "<", nil
	}

	if m := ineqGERe.FindStringSubmatch(expr); m != nil {
		minVal, err := parseTokenCount(m[2])
		if err != nil {
			return 0, false, nil, false, err
		}
		return minVal, m[1] == ">", nil, false, nil
	}

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

func parseTokenCount(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	if isInfinity(s) {
		return 0, fmt.Errorf("infinity cannot be parsed as a concrete value")
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
	return ls == "+inf" || ls == "inf" || ls == "infinity"
}
