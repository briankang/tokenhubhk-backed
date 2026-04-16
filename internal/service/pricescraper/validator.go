package pricescraper

import (
	"fmt"
	"math"

	"tokenhub-server/internal/model"
)

// =====================================================
// 价格验证器
// 对爬取的价格数据执行多维度校验，包括空值、范围、阶梯顺序等
// =====================================================

// ValidationError 验证错误
type ValidationError struct {
	ModelName string `json:"model_name"` // 出错的模型名称
	Field     string `json:"field"`      // 出错字段
	Message   string `json:"message"`    // 错误描述
	Severity  string `json:"severity"`   // 严重程度: error / warning
}

// 价格合理范围常量（RMB/百万token）
const (
	minPriceRMB = 0.0001 // 最低价格下限
	maxPriceRMB = 500.0  // 最高价格上限（部分多模态模型较贵）
)

// 不同计费单位的价格合理范围
var pricingUnitRanges = map[string][2]float64{
	PricingUnitPerMillionTokens:     {0.0001, 500.0}, // 元/百万tokens：0.0001 ~ 500
	PricingUnitPerImage:             {0.01, 50.0},    // 元/张：0.01 ~ 50
	PricingUnitPerSecond:            {0.001, 10.0},   // 元/秒：0.001 ~ 10（视频）
	PricingUnitPerMinute:            {0.01, 50.0},    // 元/分钟：0.01 ~ 50（whisper 约 0.036）
	PricingUnitPer10kCharacters:     {0.1, 100.0},    // 元/万字符：0.1 ~ 100
	PricingUnitPerKChars:            {0.1, 100.0},    // 历史别名，等价于 per_10k_characters
	PricingUnitPerMillionCharacters: {1.0, 2000.0},   // 元/百万字符：1 ~ 2000
	PricingUnitPerCall:              {0.0001, 10.0},  // 元/次：0.0001 ~ 10
	PricingUnitPerHour:              {0.1, 100.0},    // 元/小时：0.1 ~ 100
}

// getPriceRange 根据计费单位获取价格合理范围
func getPriceRange(pricingUnit string) (float64, float64) {
	if r, ok := pricingUnitRanges[pricingUnit]; ok {
		return r[0], r[1]
	}
	return minPriceRMB, maxPriceRMB // 默认使用 token 范围
}

// pricingUnitLabel 获取计费单位的中文标签（用于日志）
func pricingUnitLabel(unit string) string {
	switch unit {
	case PricingUnitPerImage:
		return "元/张"
	case PricingUnitPerSecond:
		return "元/秒"
	case PricingUnitPerMinute:
		return "元/分钟"
	case PricingUnitPer10kCharacters, PricingUnitPerKChars:
		return "元/万字符"
	case PricingUnitPerMillionCharacters:
		return "元/百万字符"
	case PricingUnitPerCall:
		return "元/次"
	case PricingUnitPerHour:
		return "元/小时"
	default:
		return "元/百万token"
	}
}

// ValidateScrapedData 验证爬取的价格数据
// 对整个爬取结果执行以下验证：
// 1. 空值检查：模型名称非空，至少有输入或输出价格
// 2. 数值范围：价格在合理区间内
// 3. 阶梯顺序：token 区间递增，价格递减或相等
// 4. 零值过滤：输入输出价格不能都为 0
func ValidateScrapedData(data *ScrapedPriceData) []ValidationError {
	var errors []ValidationError

	if data == nil {
		errors = append(errors, ValidationError{
			ModelName: "",
			Field:     "data",
			Message:   "爬取数据为空",
			Severity:  "error",
		})
		return errors
	}

	if len(data.Models) == 0 {
		errors = append(errors, ValidationError{
			ModelName: "",
			Field:     "models",
			Message:   "未爬取到任何模型数据",
			Severity:  "error",
		})
		return errors
	}

	for i, m := range data.Models {
		modelErrors := validateSingleModel(m, i)
		errors = append(errors, modelErrors...)

		// 将验证警告也写入模型的 Warnings 字段
		for _, e := range modelErrors {
			data.Models[i].Warnings = append(data.Models[i].Warnings, fmt.Sprintf("[%s] %s: %s", e.Severity, e.Field, e.Message))
		}
	}

	return errors
}

// validateSingleModel 验证单个模型的价格数据
func validateSingleModel(m ScrapedModel, index int) []ValidationError {
	var errors []ValidationError
	nameForLog := m.ModelName
	if nameForLog == "" {
		nameForLog = fmt.Sprintf("模型#%d", index)
	}

	// 1. 模型名称非空检查
	if m.ModelName == "" {
		errors = append(errors, ValidationError{
			ModelName: nameForLog,
			Field:     "model_name",
			Message:   "模型名称为空",
			Severity:  "error",
		})
	}

	// 2. 输入输出价格不能都为 0
	if m.InputPrice == 0 && m.OutputPrice == 0 && len(m.PriceTiers) == 0 {
		errors = append(errors, ValidationError{
			ModelName: nameForLog,
			Field:     "price",
			Message:   "输入和输出价格均为 0，且无阶梯价格",
			Severity:  "warning",
		})
	}

	// 3. 价格范围检查（根据计费单位选择合理范围）
	minP, maxP := getPriceRange(m.PricingUnit)
	unitLabel := pricingUnitLabel(m.PricingUnit)

	if m.InputPrice != 0 {
		if m.InputPrice < minP || m.InputPrice > maxP {
			errors = append(errors, ValidationError{
				ModelName: nameForLog,
				Field:     "input_price",
				Message:   fmt.Sprintf("输入价格 %.4f 超出合理范围 [%.4f, %.1f] %s", m.InputPrice, minP, maxP, unitLabel),
				Severity:  "warning",
			})
		}
	}
	if m.OutputPrice != 0 {
		if m.OutputPrice < minP || m.OutputPrice > maxP {
			errors = append(errors, ValidationError{
				ModelName: nameForLog,
				Field:     "output_price",
				Message:   fmt.Sprintf("输出价格 %.4f 超出合理范围 [%.4f, %.1f] %s", m.OutputPrice, minP, maxP, unitLabel),
				Severity:  "warning",
			})
		}
	}

	// 4. 阶梯价格验证
	if len(m.PriceTiers) > 0 {
		tierErrors := validatePriceTiers(nameForLog, m.PriceTiers)
		errors = append(errors, tierErrors...)
	}

	return errors
}

// validatePriceTiers 验证阶梯价格的合理性
// 规则：token 区间必须递增，同阶梯内价格应递减或相等（量大优惠）
func validatePriceTiers(modelName string, tiers []model.PriceTier) []ValidationError {
	var errors []ValidationError

	for i, tier := range tiers {
		// 检查阶梯内价格范围
		if tier.InputPrice != 0 && (tier.InputPrice < minPriceRMB || tier.InputPrice > maxPriceRMB) {
			errors = append(errors, ValidationError{
				ModelName: modelName,
				Field:     fmt.Sprintf("tier[%d].input_price", i),
				Message:   fmt.Sprintf("阶梯 %s 输入价格 %.4f 超出合理范围", tier.Name, tier.InputPrice),
				Severity:  "warning",
			})
		}
		if tier.OutputPrice != 0 && (tier.OutputPrice < minPriceRMB || tier.OutputPrice > maxPriceRMB) {
			errors = append(errors, ValidationError{
				ModelName: modelName,
				Field:     fmt.Sprintf("tier[%d].output_price", i),
				Message:   fmt.Sprintf("阶梯 %s 输出价格 %.4f 超出合理范围", tier.Name, tier.OutputPrice),
				Severity:  "warning",
			})
		}

		// 检查 token 区间递增
		if i > 0 {
			prevTier := tiers[i-1]
			if tier.MinTokens <= prevTier.MinTokens {
				errors = append(errors, ValidationError{
					ModelName: modelName,
					Field:     fmt.Sprintf("tier[%d].min_tokens", i),
					Message:   fmt.Sprintf("阶梯 %s 的 MinTokens(%d) 应大于上一阶梯 %s 的 MinTokens(%d)", tier.Name, tier.MinTokens, prevTier.Name, prevTier.MinTokens),
					Severity:  "error",
				})
			}

			// 检查价格递减或相等（量大优惠原则）
			if tier.InputPrice > prevTier.InputPrice && prevTier.InputPrice > 0 {
				errors = append(errors, ValidationError{
					ModelName: modelName,
					Field:     fmt.Sprintf("tier[%d].input_price", i),
					Message:   fmt.Sprintf("阶梯 %s 输入价格 %.4f 高于上一阶梯 %.4f（预期量大优惠递减）", tier.Name, tier.InputPrice, prevTier.InputPrice),
					Severity:  "warning",
				})
			}
			if tier.OutputPrice > prevTier.OutputPrice && prevTier.OutputPrice > 0 {
				errors = append(errors, ValidationError{
					ModelName: modelName,
					Field:     fmt.Sprintf("tier[%d].output_price", i),
					Message:   fmt.Sprintf("阶梯 %s 输出价格 %.4f 高于上一阶梯 %.4f（预期量大优惠递减）", tier.Name, tier.OutputPrice, prevTier.OutputPrice),
					Severity:  "warning",
				})
			}
		}
	}

	return errors
}

// DetectAnomalies 检测价格异常变动
// current: 当前数据库中的价格
// newPrice: 新爬取的价格
// threshold: 变动阈值（0.5 = 50%），超过此比例视为异常
// 返回：是否异常、变动比率、警告信息
func DetectAnomalies(current, newPrice float64, threshold float64) (isAnomaly bool, ratio float64, warning string) {
	// 如果当前价格为 0，无法计算变动比率
	if current == 0 {
		if newPrice > 0 {
			return false, 0, "当前价格为 0，新价格为首次设置"
		}
		return false, 0, ""
	}

	// 如果新价格为 0，标记为异常（可能爬取失败）
	if newPrice == 0 {
		return true, -1.0, fmt.Sprintf("新价格为 0，当前价格为 %.4f，可能爬取失败", current)
	}

	// 计算变动比率: (new - current) / current
	ratio = (newPrice - current) / current

	// 判断是否超过阈值
	if math.Abs(ratio) > threshold {
		direction := "上涨"
		if ratio < 0 {
			direction = "下降"
		}
		warning = fmt.Sprintf("价格%s %.1f%%（%.4f → %.4f），超过 %.0f%% 阈值",
			direction, math.Abs(ratio)*100, current, newPrice, threshold*100)
		return true, ratio, warning
	}

	return false, ratio, ""
}
