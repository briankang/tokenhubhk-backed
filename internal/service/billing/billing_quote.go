package billing

import "math"

// quote schema 与组件常量。
//
// 说明:
//   - 数据流:`QuoteService.Calculate` 与 `Service.SettleUsage`/`SettleUnitUsage`
//     都通过 `buildBillingQuote` 这一份 canonical formatter 产出 quote,
//     保证试算预览、真实扣费、成本分析三方一致。
//   - 旧版 `buildUsageQuoteSnapshot` / `buildUnitQuoteSnapshot` 已迁移到
//     `billing_quote_service.go` 内的 `buildTokenLineItems` / `buildUnitLineItems`,
//     由 `BillingQuote` 结构 + `ToSnapshotMap()` 替代输出。
const (
	quoteSchemaVersion = 2

	quoteComponentRegularInput  = "regular_input"
	quoteComponentOutput        = "output"
	quoteComponentCacheRead     = "cache_read_input"
	quoteComponentCacheWrite    = "cache_write_input"
	quoteComponentCacheWrite1h  = "cache_write_1h_input"
	quoteComponentThinking      = "thinking_output"
	quoteComponentImage         = "image_unit"
	quoteComponentDurationSec   = "duration_second"
	quoteComponentDurationMin   = "duration_minute"
	quoteComponentDurationHour  = "duration_hour"
	quoteComponentCharacter10K  = "character_10k"
	quoteComponentCharacter1M   = "character_million"
	quoteComponentCall          = "call"
	quoteComponentMinAdjustment = "minimum_charge_adjustment"
)

// roundFloat 四舍五入到指定小数位,共享给 unitQuoteComponentForUsage 等使用。
func roundFloat(v float64, places int) float64 {
	if places <= 0 {
		return math.Round(v)
	}
	scale := math.Pow10(places)
	return math.Round(v*scale) / scale
}
