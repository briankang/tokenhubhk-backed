package pricescraper

import (
	"fmt"
	"math"

	"tokenhub-server/internal/model"
)

// =====================================================
// 浠锋牸楠岃瘉鍣?
// 瀵圭埇鍙栫殑浠锋牸鏁版嵁鎵ц澶氱淮搴︽牎楠岋紝鍖呮嫭绌哄€笺€佽寖鍥淬€侀樁姊『搴忕瓑
// =====================================================

// ValidationError 楠岃瘉閿欒
type ValidationError struct {
	ModelName string `json:"model_name"` // 鍑洪敊鐨勬ā鍨嬪悕绉?
	Field     string `json:"field"`      // 鍑洪敊瀛楁
	Message   string `json:"message"`    // 閿欒鎻忚堪
	Severity  string `json:"severity"`   // 涓ラ噸绋嬪害: error / warning
}

// 浠锋牸鍚堢悊鑼冨洿甯搁噺锛圧MB/鐧句竾token锛?
const (
	minPriceRMB = 0.0001 // 鏈€浣庝环鏍间笅闄?
	maxPriceRMB = 500.0  // 鏈€楂樹环鏍间笂闄愶紙閮ㄥ垎澶氭ā鎬佹ā鍨嬭緝璐碉級
)

// 涓嶅悓璁¤垂鍗曚綅鐨勪环鏍煎悎鐞嗚寖鍥?
var pricingUnitRanges = map[string][2]float64{
	PricingUnitPerMillionTokens:     {0.0001, 500.0}, // 鍏?鐧句竾tokens锛?.0001 ~ 500
	PricingUnitPerImage:             {0.01, 50.0},    // 鍏?寮狅細0.01 ~ 50
	PricingUnitPerSecond:            {0.001, 10.0},   // 鍏?绉掞細0.001 ~ 10锛堣棰戯級
	PricingUnitPerMinute:            {0.01, 50.0},    // 鍏?鍒嗛挓锛?.01 ~ 50锛坵hisper 绾?0.036锛?
	PricingUnitPer10kCharacters:     {0.1, 100.0},    // 鍏?涓囧瓧绗︼細0.1 ~ 100
	PricingUnitPerKChars:            {0.1, 100.0},    // 鍘嗗彶鍒悕锛岀瓑浠蜂簬 per_10k_characters
	PricingUnitPerMillionCharacters: {1.0, 2000.0},   // 鍏?鐧句竾瀛楃锛? ~ 2000
	PricingUnitPerCall:              {0.0001, 10.0},  // 鍏?娆★細0.0001 ~ 10
	PricingUnitPerHour:              {0.1, 100.0},    // 鍏?灏忔椂锛?.1 ~ 100
}

// getPriceRange 鏍规嵁璁¤垂鍗曚綅鑾峰彇浠锋牸鍚堢悊鑼冨洿
func getPriceRange(pricingUnit string) (float64, float64) {
	if r, ok := pricingUnitRanges[pricingUnit]; ok {
		return r[0], r[1]
	}
	return minPriceRMB, maxPriceRMB // 榛樿浣跨敤 token 鑼冨洿
}

// pricingUnitLabel 鑾峰彇璁¤垂鍗曚綅鐨勪腑鏂囨爣绛撅紙鐢ㄤ簬鏃ュ織锛?
func pricingUnitLabel(unit string) string {
	switch unit {
	case PricingUnitPerImage:
		return "RMB/image"
	case PricingUnitPerSecond:
		return "RMB/second"
	case PricingUnitPerMinute:
		return "RMB/minute"
	case PricingUnitPer10kCharacters, PricingUnitPerKChars:
		return "RMB/10k characters"
	case PricingUnitPerMillionCharacters:
		return "RMB/million characters"
	case PricingUnitPerCall:
		return "RMB/call"
	case PricingUnitPerHour:
		return "RMB/hour"
	default:
		return "RMB/million tokens"
	}
}

// ValidateScrapedData 楠岃瘉鐖彇鐨勪环鏍兼暟鎹?
// 瀵规暣涓埇鍙栫粨鏋滄墽琛屼互涓嬮獙璇侊細
// 1. 绌哄€兼鏌ワ細妯″瀷鍚嶇О闈炵┖锛岃嚦灏戞湁杈撳叆鎴栬緭鍑轰环鏍?
// 2. 鏁板€艰寖鍥达細浠锋牸鍦ㄥ悎鐞嗗尯闂村唴
// 3. 闃舵椤哄簭锛歵oken 鍖洪棿閫掑锛屼环鏍奸€掑噺鎴栫浉绛?
// 4. 闆跺€艰繃婊わ細杈撳叆杈撳嚭浠锋牸涓嶈兘閮戒负 0
func ValidateScrapedData(data *ScrapedPriceData) []ValidationError {
	var errors []ValidationError

	if data == nil {
		errors = append(errors, ValidationError{
			ModelName: "",
			Field:     "data",
			Message:   "鐖彇鏁版嵁涓虹┖",
			Severity:  "error",
		})
		return errors
	}

	if len(data.Models) == 0 {
		errors = append(errors, ValidationError{
			ModelName: "",
			Field:     "models",
			Message:   "鏈埇鍙栧埌浠讳綍妯″瀷鏁版嵁",
			Severity:  "error",
		})
		return errors
	}

	for i, m := range data.Models {
		modelErrors := validateSingleModel(m, i)
		errors = append(errors, modelErrors...)

		// 灏嗛獙璇佽鍛婁篃鍐欏叆妯″瀷鐨?Warnings 瀛楁
		for _, e := range modelErrors {
			data.Models[i].Warnings = append(data.Models[i].Warnings, fmt.Sprintf("[%s] %s: %s", e.Severity, e.Field, e.Message))
		}
	}

	return errors
}

// validateSingleModel 楠岃瘉鍗曚釜妯″瀷鐨勪环鏍兼暟鎹?
func validateSingleModel(m ScrapedModel, index int) []ValidationError {
	var errors []ValidationError
	nameForLog := m.ModelName
	if nameForLog == "" {
		nameForLog = fmt.Sprintf("妯″瀷#%d", index)
	}

	// 1. 妯″瀷鍚嶇О闈炵┖妫€鏌?
	if m.ModelName == "" {
		errors = append(errors, ValidationError{
			ModelName: nameForLog,
			Field:     "model_name",
			Message:   "妯″瀷鍚嶇О涓虹┖",
			Severity:  "error",
		})
	}

	// 2. 杈撳叆杈撳嚭浠锋牸涓嶈兘閮戒负 0
	if m.InputPrice == 0 && m.OutputPrice == 0 && len(m.PriceTiers) == 0 {
		errors = append(errors, ValidationError{
			ModelName: nameForLog,
			Field:     "price",
			Message:   "input and output prices are both zero and no tiered pricing is configured",
			Severity:  "warning",
		})
	}

	// 3. 浠锋牸鑼冨洿妫€鏌ワ紙鏍规嵁璁¤垂鍗曚綅閫夋嫨鍚堢悊鑼冨洿锛?
	minP, maxP := getPriceRange(m.PricingUnit)
	unitLabel := pricingUnitLabel(m.PricingUnit)

	if m.InputPrice != 0 {
		if m.InputPrice < minP || m.InputPrice > maxP {
			errors = append(errors, ValidationError{
				ModelName: nameForLog,
				Field:     "input_price",
				Message:   fmt.Sprintf("杈撳叆浠锋牸 %.4f 瓒呭嚭鍚堢悊鑼冨洿 [%.4f, %.1f] %s", m.InputPrice, minP, maxP, unitLabel),
				Severity:  "warning",
			})
		}
	}
	if m.OutputPrice != 0 {
		if m.OutputPrice < minP || m.OutputPrice > maxP {
			errors = append(errors, ValidationError{
				ModelName: nameForLog,
				Field:     "output_price",
				Message:   fmt.Sprintf("杈撳嚭浠锋牸 %.4f 瓒呭嚭鍚堢悊鑼冨洿 [%.4f, %.1f] %s", m.OutputPrice, minP, maxP, unitLabel),
				Severity:  "warning",
			})
		}
	}

	// 4. 闃舵浠锋牸楠岃瘉
	if len(m.PriceTiers) > 0 {
		tierErrors := validatePriceTiers(nameForLog, m.PriceTiers)
		errors = append(errors, tierErrors...)
	}

	return errors
}

// validatePriceTiers 楠岃瘉闃舵浠锋牸鐨勫悎鐞嗘€?
// 瑙勫垯锛歵oken 鍖洪棿蹇呴』閫掑锛屽悓闃舵鍐呬环鏍煎簲閫掑噺鎴栫浉绛夛紙閲忓ぇ浼樻儬锛?
func validatePriceTiers(modelName string, tiers []model.PriceTier) []ValidationError {
	var errors []ValidationError

	for i, tier := range tiers {
		// 妫€鏌ラ樁姊唴浠锋牸鑼冨洿
		if tier.InputPrice != 0 && (tier.InputPrice < minPriceRMB || tier.InputPrice > maxPriceRMB) {
			errors = append(errors, ValidationError{
				ModelName: modelName,
				Field:     fmt.Sprintf("tier[%d].input_price", i),
				Message:   fmt.Sprintf("闃舵 %s 杈撳叆浠锋牸 %.4f 瓒呭嚭鍚堢悊鑼冨洿", tier.Name, tier.InputPrice),
				Severity:  "warning",
			})
		}
		if tier.OutputPrice != 0 && (tier.OutputPrice < minPriceRMB || tier.OutputPrice > maxPriceRMB) {
			errors = append(errors, ValidationError{
				ModelName: modelName,
				Field:     fmt.Sprintf("tier[%d].output_price", i),
				Message:   fmt.Sprintf("闃舵 %s 杈撳嚭浠锋牸 %.4f 瓒呭嚭鍚堢悊鑼冨洿", tier.Name, tier.OutputPrice),
				Severity:  "warning",
			})
		}

		// Check token ranges using the current input_min field only.
		if i > 0 {
			prevTier := tiers[i-1]
			curMin := tier.InputMin
			prevMin := prevTier.InputMin
			if curMin > 0 && prevMin > 0 && curMin <= prevMin {
				errors = append(errors, ValidationError{
					ModelName: modelName,
					Field:     fmt.Sprintf("tier[%d].input_min", i),
					Message:   fmt.Sprintf("闃舵 %s 鐨勪笅鐣?%d) 搴斿ぇ浜庝笂涓€闃舵 %s 鐨勪笅鐣?%d)", tier.Name, curMin, prevTier.Name, prevMin),
					Severity:  "error",
				})
			}

			// 闀夸笂涓嬫枃妯″瀷鐨勯樁姊环鏍煎彲鑳介殢 token 鏁伴噺涓婂崌锛堥暱鏂囨。璁¤垂鏇磋吹锛夛紝涓嶅彂鍑鸿鍛?
		}
	}

	return errors
}

// DetectAnomalies 妫€娴嬩环鏍煎紓甯稿彉鍔?
// current: 褰撳墠鏁版嵁搴撲腑鐨勪环鏍?
// newPrice: 鏂扮埇鍙栫殑浠锋牸
// threshold: 鍙樺姩闃堝€硷紙0.5 = 50%锛夛紝瓒呰繃姝ゆ瘮渚嬭涓哄紓甯?
// 杩斿洖锛氭槸鍚﹀紓甯搞€佸彉鍔ㄦ瘮鐜囥€佽鍛婁俊鎭?
func DetectAnomalies(current, newPrice float64, threshold float64) (isAnomaly bool, ratio float64, warning string) {
	// 濡傛灉褰撳墠浠锋牸涓?0锛屾棤娉曡绠楀彉鍔ㄦ瘮鐜?
	if current == 0 {
		if newPrice > 0 {
			return false, 0, "current price is zero; new price is the initial value"
		}
		return false, 0, ""
	}

	// 濡傛灉鏂颁环鏍间负 0锛屾爣璁颁负寮傚父锛堝彲鑳界埇鍙栧け璐ワ級
	if newPrice == 0 {
		return true, -1.0, fmt.Sprintf("new price is zero while current price is %.4f; scraping may have failed", current)
	}

	// 璁＄畻鍙樺姩姣旂巼: (new - current) / current
	ratio = (newPrice - current) / current

	// 鍒ゆ柇鏄惁瓒呰繃闃堝€?
	if math.Abs(ratio) > threshold {
		direction := "涓婃定"
		if ratio < 0 {
			direction = "涓嬮檷"
		}
		warning = fmt.Sprintf("price changed %s %.1f%% (%.4f -> %.4f), exceeding %.0f%% threshold",
			direction, math.Abs(ratio)*100, current, newPrice, threshold*100)
		return true, ratio, warning
	}

	return false, ratio, ""
}
