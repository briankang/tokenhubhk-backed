package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
	"tokenhub-server/internal/provider"
	"tokenhub-server/internal/service/pricing"
)

const (
	quoteEngineVersion = "billing_quote_v1"

	QuoteScenarioPreview = "preview"
	QuoteScenarioCharge  = "charge"
	QuoteScenarioReplay  = "replay"
)

// ErrQuoteModelNotFound 试算或扣费时模型不存在或不可计费。
var ErrQuoteModelNotFound = errors.New("billing quote: model not found")

// ErrQuotePricingMissing 模型缺售价(model_pricings)，无法生成 quote。
var ErrQuotePricingMissing = errors.New("billing quote: model has no published sale price")

// QuoteUsage 统一承载 Token 类与按量类用量。
// Calculate 内部根据 AIModel.PricingUnit 决定走哪条 PricingCalculator 路径。
type QuoteUsage struct {
	InputTokens        int     `json:"input_tokens,omitempty"`
	OutputTokens       int     `json:"output_tokens,omitempty"`
	TotalTokens        int     `json:"total_tokens,omitempty"`
	CacheReadTokens    int     `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens   int     `json:"cache_write_tokens,omitempty"`
	CacheWrite1hTokens int     `json:"cache_write_1h_tokens,omitempty"`
	ReasoningTokens    int     `json:"reasoning_tokens,omitempty"`
	ImageCount         int     `json:"image_count,omitempty"`
	DurationSec        float64 `json:"duration_sec,omitempty"`
	CharCount          int     `json:"char_count,omitempty"`
	CallCount          int     `json:"call_count,omitempty"`
}

// QuoteRequest 是 BillingQuoteService.Calculate 的统一输入。
//
// scenario 控制 quote_id 来源与持久化语义:
//   - charge:  真实扣费事件，request_id 必填。
//   - preview: 管理员试算，request_id 可空(自动留空，不写库)。
//   - replay:  对账重放，请求体来自历史 snapshot。
//
// DimValues 用于 PriceMatrix 多维命中(如 Seedance 视频的
// resolution × input_has_video × inference_mode)。
// 三方调用(preview / charge / replay)都应填充此字段以保证命中一致。
type QuoteRequest struct {
	Scenario     string
	RequestID    string
	ModelID      uint
	ModelName    string
	UserID       uint
	TenantID     uint
	AgentLevel   int
	Usage        QuoteUsage
	ThinkingMode bool
	FreezeID     string
	DimValues    map[string]interface{}
}

// QuoteLineItem quote 中的单项分账。
type QuoteLineItem struct {
	Component        string      `json:"component"`
	UsageKey         string      `json:"usage_key"`
	Quantity         interface{} `json:"quantity"`
	Unit             string      `json:"unit"`
	Denominator      int64       `json:"denominator"`
	UnitPriceCredits int64       `json:"unit_price_credits"`
	UnitPriceRMB     float64     `json:"unit_price_rmb"`
	CostCredits      int64       `json:"cost_credits"`
	CostUnits        int64       `json:"cost_units"`
	CostRMB          float64     `json:"cost_rmb"`
	Source           string      `json:"source"`
	Section          string      `json:"section,omitempty"`
}

// BillingQuote 统一计价结果。
//
// 真实扣费、价格计算器试算、成本分析三条链路必须共用此结构体。
// 同一组 (model, usage, pricing_version, discount_version) 输入下，QuoteHash 必须稳定一致。
type BillingQuote struct {
	SchemaVersion         int             `json:"schema_version"`
	EngineVersion         string          `json:"engine_version"`
	Scenario              string          `json:"scenario"`
	QuoteID               string          `json:"quote_id"`
	ModelID               uint            `json:"model_id"`
	ModelName             string          `json:"model_name"`
	ModelType             string          `json:"model_type"`
	PricingUnit           string          `json:"pricing_unit"`
	Currency              string          `json:"currency"`
	PricingSource         string          `json:"pricing_source"`
	Usage                 QuoteUsage      `json:"usage"`
	LineItems             []QuoteLineItem `json:"line_items"`
	TotalCredits          int64           `json:"total_credits"`
	TotalUnits            int64           `json:"total_units"`
	TotalCreditsDecimal   float64         `json:"total_credits_decimal"`
	TotalRMB              float64         `json:"total_rmb"`
	PlatformCostCredits   int64           `json:"platform_cost_credits"`
	PlatformCostUnits     int64           `json:"platform_cost_units"`
	PlatformCreditsDecimal float64        `json:"platform_cost_credits_decimal"`
	PlatformCostRMB       float64         `json:"platform_cost_rmb"`
	ActualCostCredits     int64           `json:"actual_cost_credits"`
	ActualCostUnits       int64           `json:"actual_cost_units"`
	ActualCreditsDecimal  float64         `json:"actual_cost_credits_decimal"`
	UnderCollectedCredits int64           `json:"under_collected_credits,omitempty"`
	UnderCollectedUnits   int64           `json:"under_collected_units,omitempty"`
	UnderCollectedCreditsDecimal float64  `json:"under_collected_credits_decimal,omitempty"`
	BillingStatus         string          `json:"billing_status,omitempty"`
	MatchedTierName       string          `json:"matched_tier_name,omitempty"`
	MatchedTierIdx        int             `json:"matched_tier_idx"`
	UserDiscountType      string          `json:"user_discount_type,omitempty"`
	UserDiscountID        *uint           `json:"user_discount_id,omitempty"`
	UserDiscountRate      *float64        `json:"user_discount_rate,omitempty"`
	ThinkingModeApplied   bool            `json:"thinking_mode_applied,omitempty"`
	// PriceMatrix 命中信息(v3):透传给前端展示「命中: 1080p × 含视频 × 在线」
	MatchedDimValues  map[string]interface{} `json:"matched_dim_values,omitempty"`
	MatchedMatrixNote string                 `json:"matched_matrix_note,omitempty"`
	Warnings          []string               `json:"warnings,omitempty"`
	QuoteHash         string                 `json:"quote_hash"`
}

// QuoteService 是统一计价引擎。它无状态、无副作用，
// 可被 preview / 真实扣费 / replay 三条链路安全调用。
//
// 真实扣费链路调用 Calculate 后，由 BalanceService 完成扣费，
// 然后将 BillingQuote 落到 api_call_logs.billing_snapshot["quote"]。
type QuoteService struct {
	db          *gorm.DB
	pricingCalc *pricing.PricingCalculator
}

// NewQuoteService 构造 QuoteService。db 必须非 nil；pricingCalc 应为已初始化的 PricingCalculator。
func NewQuoteService(db *gorm.DB, pricingCalc *pricing.PricingCalculator) *QuoteService {
	if db == nil {
		panic("billing quote service: db is nil")
	}
	return &QuoteService{db: db, pricingCalc: pricingCalc}
}

// Calculate 是计价真相源。
//
// 它读取请求中的模型与用量，调用底层 PricingCalculator 完成定价计算，
// 应用 thinking surcharge，最后产出包含 line_items 与稳定 quote_hash 的 BillingQuote。
//
// Calculate 不写库、不动余额、不写日志。它纯函数式可被预览与重放路径直接复用。
func (qs *QuoteService) Calculate(ctx context.Context, req QuoteRequest) (*BillingQuote, error) {
	if qs.pricingCalc == nil {
		return nil, errors.New("billing quote service: pricing calculator is nil")
	}
	aiModel, err := qs.loadModel(ctx, req)
	if err != nil {
		return nil, err
	}

	cr, err := qs.computeCost(ctx, req, &aiModel)
	if err != nil {
		return nil, err
	}
	if cr == nil {
		return nil, ErrQuotePricingMissing
	}

	// v3 三方一致性:Preview / Settle / Replay 均通过此函数命中 PriceMatrix cell,
	// 命中时把 dim_values + note 写入 CostResult,后续 buildBillingQuote 透传到 quote。
	if matrixCell, hit := pricing.MatchCellByModelID(ctx, qs.db, aiModel.ID, req.DimValues); hit && matrixCell != nil {
		cr.MatchedDimValues = matrixCell.DimValues
		if matrixCell.Note != "" {
			cr.MatchedMatrixCellNote = matrixCell.Note
		}
	}

	thinkingApplied := false
	if req.ThinkingMode {
		applied, _ := applyThinkingSurchargeToCostResult(qs.db, ctx, &aiModel, cr, req.Usage.OutputTokens)
		thinkingApplied = applied
	}

	quote := buildBillingQuote(req, &aiModel, cr, thinkingApplied)
	return quote, nil
}

// loadModel 优先按 ID 加载，回退到 model_name；保留与 BillingService.loadBillableModel 一致的优先级。
func (qs *QuoteService) loadModel(ctx context.Context, req QuoteRequest) (model.AIModel, error) {
	var m model.AIModel
	if req.ModelID > 0 {
		if err := qs.db.WithContext(ctx).Where("id = ?", req.ModelID).First(&m).Error; err == nil {
			return m, nil
		}
	}
	if req.ModelName != "" {
		if err := qs.db.WithContext(ctx).Where("model_name = ? AND is_active = true", req.ModelName).First(&m).Error; err == nil {
			return m, nil
		}
		if err := qs.db.WithContext(ctx).Where("model_name = ?", req.ModelName).Order("id ASC").First(&m).Error; err == nil {
			return m, nil
		}
	}
	return m, ErrQuoteModelNotFound
}

// computeCost 按 PricingUnit 选择具体计算路径，复用 pricing.PricingCalculator 不重写定价逻辑。
func (qs *QuoteService) computeCost(ctx context.Context, req QuoteRequest, aiModel *model.AIModel) (*pricing.CostResult, error) {
	u := req.Usage
	if isUnitPricingUnit(aiModel.PricingUnit) {
		return qs.pricingCalc.CalculateCostByUnit(ctx, req.UserID, aiModel.ID, req.TenantID, req.AgentLevel, pricing.UsageInput{
			InputTokens:  u.InputTokens,
			OutputTokens: u.OutputTokens,
			ImageCount:   u.ImageCount,
			DurationSec:  u.DurationSec,
			CharCount:    u.CharCount,
			CallCount:    u.CallCount,
		})
	}
	if u.CacheReadTokens > 0 || u.CacheWriteTokens > 0 {
		return qs.pricingCalc.CalculateCostWithCache(ctx, req.UserID, aiModel, req.TenantID, req.AgentLevel, pricing.CacheUsageInput{
			InputTokens:        u.InputTokens,
			OutputTokens:       u.OutputTokens,
			CacheReadTokens:    u.CacheReadTokens,
			CacheWriteTokens:   u.CacheWriteTokens,
			CacheWrite1hTokens: u.CacheWrite1hTokens,
		})
	}
	return qs.pricingCalc.CalculateCost(ctx, req.UserID, aiModel.ID, req.TenantID, req.AgentLevel, u.InputTokens, u.OutputTokens)
}

// QuoteUsageFromProvider 把上游 provider.Usage 折算为 QuoteUsage。
// 真实扣费链路构造 QuoteRequest 时使用。
func QuoteUsageFromProvider(u provider.Usage) QuoteUsage {
	return QuoteUsage{
		InputTokens:        u.PromptTokens,
		OutputTokens:       u.CompletionTokens,
		TotalTokens:        u.TotalTokens,
		CacheReadTokens:    u.CacheReadTokens,
		CacheWriteTokens:   u.CacheWriteTokens,
		CacheWrite1hTokens: u.CacheWrite1hTokens,
		ReasoningTokens:    u.ReasoningTokens,
	}
}

// QuoteUsageFromUnit 把 pricing.UsageInput 折算为 QuoteUsage。
func QuoteUsageFromUnit(u pricing.UsageInput) QuoteUsage {
	return QuoteUsage{
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
		ImageCount:   u.ImageCount,
		DurationSec:  u.DurationSec,
		CharCount:    u.CharCount,
		CallCount:    u.CallCount,
	}
}

// isUnitPricingUnit 判断模型是否为非 token 计费单位。
func isUnitPricingUnit(pu string) bool {
	switch pu {
	case "", model.UnitPerMillionTokens:
		return false
	case model.UnitPerImage,
		model.UnitPerSecond,
		model.UnitPerMinute,
		model.UnitPerHour,
		model.UnitPer10kCharacters,
		model.UnitPerKChars,
		model.UnitPerMillionCharacters,
		model.UnitPerCall:
		return true
	}
	return false
}

// applyThinkingSurchargeToCostResult 在 cr 上原位施加 thinking 加价，返回是否生效与 thinking 售价。
//
// 抽出独立函数以便 QuoteService.Calculate 与 BillingService.applyThinkingSurcharge 共享同一处口径。
// completionTokens 是输出 token 数（与 PricingCalculator 输入对齐）。
func applyThinkingSurchargeToCostResult(db *gorm.DB, ctx context.Context, aiModel *model.AIModel, cr *pricing.CostResult, completionTokens int) (bool, float64) {
	if completionTokens <= 0 {
		return false, 0
	}
	thinkingSellRMB := resolveThinkingOutputSellRMB(db, ctx, aiModel, cr)
	if thinkingSellRMB <= 0 {
		return false, 0
	}
	normalSellRMB := cr.PriceDetail.OutputPriceRMB
	diffRMB := thinkingSellRMB - normalSellRMB
	if diffRMB <= 0 {
		return false, 0
	}
	surchargeRMB := diffRMB * float64(completionTokens) / 1_000_000
	surchargeUnits := credits.RMBToBillingUnits(surchargeRMB)
	if surchargeUnits <= 0 {
		return false, 0
	}
	surchargeCredits := credits.BillingUnitsToCredits(surchargeUnits)
	cr.TotalCost += surchargeCredits
	cr.OutputCost += surchargeCredits
	cr.TotalCostUnits += surchargeUnits
	cr.OutputCostUnits += surchargeUnits
	cr.TotalCostRMB = credits.BillingUnitsToRMB(cr.TotalCostUnits)
	cr.ThinkingOutputCost = surchargeCredits
	cr.ThinkingOutputCostUnits = surchargeUnits
	cr.ThinkingOutputPriceRMB = thinkingSellRMB
	cr.ThinkingOutputPricePerMillion = credits.RMBToCredits(thinkingSellRMB - normalSellRMB)
	return true, thinkingSellRMB
}

// buildBillingQuote 把 CostResult 折算为 BillingQuote 结构体。
//
// 它是 token / cache / thinking / 单位计费 共享的格式化器，
// 真实扣费的 snapshot 与试算预览页的展示都通过它生成。
func buildBillingQuote(req QuoteRequest, aiModel *model.AIModel, cr *pricing.CostResult, thinkingApplied bool) *BillingQuote {
	usage := req.Usage
	if usage.TotalTokens == 0 && (usage.InputTokens > 0 || usage.OutputTokens > 0) {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}

	var lineItems []QuoteLineItem
	if isUnitPricingUnit(aiModel.PricingUnit) {
		lineItems = buildUnitLineItems(req, aiModel, cr)
	} else {
		lineItems = buildTokenLineItems(req, cr)
	}
	totalUnits := cr.TotalCostUnits
	if totalUnits == 0 && cr.TotalCost > 0 {
		totalUnits = credits.CreditsToBillingUnits(cr.TotalCost)
	}
	platformUnits := cr.PlatformCostUnits
	if platformUnits == 0 && cr.PlatformCost > 0 {
		platformUnits = credits.CreditsToBillingUnits(cr.PlatformCost)
	}
	lineItems = lineItemsWithAdjustmentStruct(lineItems, totalUnits)

	quote := &BillingQuote{
		SchemaVersion:       quoteSchemaVersion,
		EngineVersion:       quoteEngineVersion,
		Scenario:            defaultScenario(req.Scenario),
		QuoteID:             req.RequestID,
		ModelID:             aiModel.ID,
		ModelName:           aiModel.ModelName,
		ModelType:           aiModel.ModelType,
		PricingUnit:         aiModel.PricingUnit,
		Currency:            cr.PriceDetail.Currency,
		PricingSource:       cr.PriceDetail.Source,
		Usage:               usage,
		LineItems:           lineItems,
		TotalCredits:          cr.TotalCost,
		TotalUnits:            totalUnits,
		TotalCreditsDecimal:   credits.BillingUnitsToCreditAmount(totalUnits),
		TotalRMB:              credits.BillingUnitsToRMB(totalUnits),
		PlatformCostCredits:   cr.PlatformCost,
		PlatformCostUnits:     platformUnits,
		PlatformCreditsDecimal: credits.BillingUnitsToCreditAmount(platformUnits),
		PlatformCostRMB:       credits.BillingUnitsToRMB(platformUnits),
		MatchedTierName:     cr.MatchedTier,
		MatchedTierIdx:      cr.MatchedTierIdx,
		UserDiscountType:    cr.UserDiscountType,
		UserDiscountID:      cr.UserDiscountID,
		UserDiscountRate:    cr.UserDiscountRate,
		ThinkingModeApplied: thinkingApplied,
		MatchedDimValues:    cr.MatchedDimValues,
		MatchedMatrixNote:   cr.MatchedMatrixCellNote,
	}
	quote.QuoteHash = computeQuoteHash(quote)
	return quote
}

func defaultScenario(s string) string {
	if s == "" {
		return QuoteScenarioCharge
	}
	return s
}

// buildTokenLineItems 输入/输出/缓存读写/thinking 等 token 分项。
// 与既有 buildUsageQuoteSnapshot 的输出对齐，便于 Step B 平滑迁移。
func buildTokenLineItems(req QuoteRequest, cr *pricing.CostResult) []QuoteLineItem {
	items := make([]QuoteLineItem, 0, 6)

	hasCache := cr.CacheReadTokens > 0 || cr.CacheWriteTokens > 0 || cr.CacheWrite1hTokens > 0
	regularInputTokens := cr.RegularInputTokens
	if regularInputTokens == 0 && req.Usage.InputTokens > 0 && !hasCache {
		regularInputTokens = int64(req.Usage.InputTokens)
	}
	regularInputCost := cr.RegularInputCost
	regularInputCostUnits := cr.RegularInputCostUnits
	if !hasCache {
		regularInputCost = cr.InputCost
		regularInputCostUnits = cr.InputCostUnits
	}
	if regularInputCostUnits == 0 && regularInputCost > 0 {
		regularInputCostUnits = credits.CreditsToBillingUnits(regularInputCost)
	}
	if regularInputTokens > 0 || regularInputCost > 0 {
		items = append(items, makeTokenLineItem(
			quoteComponentRegularInput,
			"input_tokens",
			regularInputTokens,
			cr.PriceDetail.InputPricePerMillion,
			cr.PriceDetail.InputPriceRMB,
			regularInputCostUnits,
			cr.PriceDetail.Source,
		))
	}

	if cr.CacheReadTokens > 0 || cr.CacheReadCost > 0 {
		items = append(items, makeTokenLineItem(
			quoteComponentCacheRead,
			"cache_read_tokens",
			cr.CacheReadTokens,
			cr.CacheReadPricePerMillion,
			credits.CreditsToRMB(cr.CacheReadPricePerMillion),
			nonZeroUnits(cr.CacheReadCostUnits, cr.CacheReadCost),
			cr.PriceDetail.Source,
		))
	}

	write1hCost := int64(0)
	write1hCostUnits := int64(0)
	if cr.CacheWrite1hTokens > 0 && cr.CacheWrite1hPricePerMillion > 0 {
		write1hCost = cr.CacheWrite1hPricePerMillion * cr.CacheWrite1hTokens / 1_000_000
		write1hCostUnits = credits.CostUnitsFromCreditsPerMillion(cr.CacheWrite1hPricePerMillion, cr.CacheWrite1hTokens)
	}
	write5mTokens := cr.CacheWriteTokens - cr.CacheWrite1hTokens
	if write5mTokens < 0 {
		write5mTokens = 0
	}
	write5mCost := cr.CacheWriteCost - write1hCost
	if write5mCost < 0 {
		write5mCost = 0
	}
	write5mCostUnits := cr.CacheWriteCostUnits - write1hCostUnits
	if write5mCostUnits < 0 {
		write5mCostUnits = 0
	}
	if write5mCostUnits == 0 && write5mCost > 0 {
		write5mCostUnits = credits.CreditsToBillingUnits(write5mCost)
	}
	if write5mTokens > 0 || write5mCost > 0 {
		items = append(items, makeTokenLineItem(
			quoteComponentCacheWrite,
			"cache_write_tokens",
			write5mTokens,
			cr.CacheWritePricePerMillion,
			credits.CreditsToRMB(cr.CacheWritePricePerMillion),
			write5mCostUnits,
			cr.PriceDetail.Source,
		))
	}
	if cr.CacheWrite1hTokens > 0 || write1hCost > 0 {
		items = append(items, makeTokenLineItem(
			quoteComponentCacheWrite1h,
			"cache_write_1h_tokens",
			cr.CacheWrite1hTokens,
			cr.CacheWrite1hPricePerMillion,
			credits.CreditsToRMB(cr.CacheWrite1hPricePerMillion),
			nonZeroUnits(write1hCostUnits, write1hCost),
			cr.PriceDetail.Source,
		))
	}

	outputCost := cr.OutputCost - cr.ThinkingOutputCost
	if outputCost < 0 {
		outputCost = 0
	}
	outputCostUnits := cr.OutputCostUnits - cr.ThinkingOutputCostUnits
	if outputCostUnits < 0 {
		outputCostUnits = 0
	}
	if outputCostUnits == 0 && outputCost > 0 {
		outputCostUnits = credits.CreditsToBillingUnits(outputCost)
	}
	if req.Usage.OutputTokens > 0 || outputCost > 0 {
		items = append(items, makeTokenLineItem(
			quoteComponentOutput,
			"output_tokens",
			int64(req.Usage.OutputTokens),
			cr.PriceDetail.OutputPricePerMillion,
			cr.PriceDetail.OutputPriceRMB,
			outputCostUnits,
			cr.PriceDetail.Source,
		))
	}

	if cr.ThinkingOutputCost > 0 {
		thinkingUnitRMB := cr.ThinkingOutputPriceRMB - cr.PriceDetail.OutputPriceRMB
		if thinkingUnitRMB < 0 {
			thinkingUnitRMB = 0
		}
		items = append(items, makeTokenLineItem(
			quoteComponentThinking,
			"completion_tokens",
			int64(req.Usage.OutputTokens),
			cr.ThinkingOutputPricePerMillion,
			thinkingUnitRMB,
			nonZeroUnits(cr.ThinkingOutputCostUnits, cr.ThinkingOutputCost),
			cr.PriceDetail.Source,
		))
	}

	return items
}

// buildUnitLineItems 单条按量分项（图片/视频时长/字符/按次）。
func buildUnitLineItems(req QuoteRequest, aiModel *model.AIModel, cr *pricing.CostResult) []QuoteLineItem {
	component, usageKey, quantity, unit, denominator := unitQuoteComponentForUsage(aiModel.PricingUnit, req.Usage)
	return []QuoteLineItem{
		{
			Component:        component,
			UsageKey:         usageKey,
			Quantity:         quantity,
			Unit:             unit,
			Denominator:      denominator,
			UnitPriceCredits: cr.PriceDetail.InputPricePerMillion,
			UnitPriceRMB:     cr.PriceDetail.InputPriceRMB,
			CostCredits:      cr.TotalCost,
			CostUnits:        nonZeroUnits(cr.TotalCostUnits, cr.TotalCost),
			CostRMB:          credits.BillingUnitsToRMB(nonZeroUnits(cr.TotalCostUnits, cr.TotalCost)),
			Source:           cr.PriceDetail.Source,
		},
	}
}

// unitQuoteComponentForUsage 与既有 unitQuoteComponent 等价，但接受 QuoteUsage。
func unitQuoteComponentForUsage(pricingUnit string, u QuoteUsage) (component, usageKey string, quantity interface{}, unit string, denominator int64) {
	switch pricingUnit {
	case model.UnitPerImage:
		return quoteComponentImage, "image_count", int64(u.ImageCount), "image", 1
	case model.UnitPerSecond:
		return quoteComponentDurationSec, "duration_sec", u.DurationSec, "second", 1
	case model.UnitPerMinute:
		return quoteComponentDurationMin, "duration_sec", roundFloat(u.DurationSec/60.0, 6), "minute", 1
	case model.UnitPer10kCharacters, model.UnitPerKChars:
		return quoteComponentCharacter10K, "char_count", roundFloat(float64(u.CharCount)/10_000.0, 6), "10k_characters", 1
	case model.UnitPerMillionCharacters:
		return quoteComponentCharacter1M, "char_count", roundFloat(float64(u.CharCount)/1_000_000.0, 6), "million_characters", 1
	case model.UnitPerCall:
		return quoteComponentCall, "call_count", int64(u.CallCount), "call", 1
	case model.UnitPerHour:
		return quoteComponentDurationHour, "duration_sec", roundFloat(u.DurationSec/3600.0, 6), "hour", 1
	default:
		return quoteComponentRegularInput, "input_tokens", int64(u.InputTokens), "token", 1_000_000
	}
}

// makeTokenLineItem 构造一条 token 分项。
func makeTokenLineItem(component, usageKey string, quantity int64, unitPriceCredits int64, unitPriceRMB float64, costUnits int64, source string) QuoteLineItem {
	return QuoteLineItem{
		Component:        component,
		UsageKey:         usageKey,
		Quantity:         quantity,
		Unit:             "token",
		Denominator:      1_000_000,
		UnitPriceCredits: unitPriceCredits,
		UnitPriceRMB:     unitPriceRMB,
		CostCredits:      credits.BillingUnitsToCredits(costUnits),
		CostUnits:        costUnits,
		CostRMB:          credits.BillingUnitsToRMB(costUnits),
		Source:           source,
	}
}

func nonZeroUnits(units int64, legacyCredits int64) int64 {
	if units > 0 {
		return units
	}
	if legacyCredits > 0 {
		return credits.CreditsToBillingUnits(legacyCredits)
	}
	return 0
}

// lineItemsWithAdjustmentStruct 与 lineItemsWithAdjustment 等价，作用在 []QuoteLineItem 上。
//
// 当 line items 累加 ≠ TotalCost 时（小额保底/取整等），
// 追加一条 minimum_charge_adjustment 行将差额补齐，保证 sum(line_items.cost_credits) == TotalCost。
func lineItemsWithAdjustmentStruct(items []QuoteLineItem, totalUnits int64) []QuoteLineItem {
	var sum int64
	for _, item := range items {
		sum += item.CostUnits
	}
	if delta := totalUnits - sum; delta != 0 {
		items = append(items, QuoteLineItem{
			Component:        quoteComponentMinAdjustment,
			UsageKey:         "minimum_charge",
			Quantity:         int64(1),
			Unit:             "request",
			Denominator:      1,
			UnitPriceCredits: credits.BillingUnitsToCredits(delta),
			UnitPriceRMB:     credits.BillingUnitsToRMB(delta),
			CostCredits:      credits.BillingUnitsToCredits(delta),
			CostUnits:        delta,
			CostRMB:          credits.BillingUnitsToRMB(delta),
			Source:           "billing_minimum",
		})
	}
	return items
}

// computeQuoteHash 在 BillingQuote 上产生稳定 SHA256 hash。
//
// 哈希排除 QuoteHash 字段、actual/under/billing_status 等结算时字段，
// 仅覆盖定价输入与产出，保证同样定价版本下相同输入产生相同 hash。
func computeQuoteHash(q *BillingQuote) string {
	if q == nil {
		return ""
	}
	payload := map[string]interface{}{
		"schema_version":   q.SchemaVersion,
		"engine_version":   q.EngineVersion,
		"scenario":         q.Scenario,
		"quote_id":         q.QuoteID,
		"model_id":         q.ModelID,
		"model_name":       q.ModelName,
		"pricing_unit":     q.PricingUnit,
		"pricing_source":   q.PricingSource,
		"usage":            q.Usage,
		"line_items":       q.LineItems,
		"total_units":      q.TotalUnits,
		"platform_units":   q.PlatformCostUnits,
		"matched_tier":     q.MatchedTierName,
		"matched_tier_idx": q.MatchedTierIdx,
		"discount_type":    q.UserDiscountType,
		"discount_id":      q.UserDiscountID,
		"discount_rate":    q.UserDiscountRate,
		"thinking_applied": q.ThinkingModeApplied,
		"matched_dim":      q.MatchedDimValues,
	}
	raw, err := canonicalJSON(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// canonicalJSON 把对象先 JSON 化再以排序后的 key 重新序列化，保证 hash 与 map 遍历顺序无关。
func canonicalJSON(v interface{}) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var generic interface{}
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	return marshalSorted(generic)
}

func marshalSorted(v interface{}) ([]byte, error) {
	switch x := v.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf := []byte{'{'}
		for i, k := range keys {
			if i > 0 {
				buf = append(buf, ',')
			}
			kb, _ := json.Marshal(k)
			buf = append(buf, kb...)
			buf = append(buf, ':')
			vb, err := marshalSorted(x[k])
			if err != nil {
				return nil, err
			}
			buf = append(buf, vb...)
		}
		buf = append(buf, '}')
		return buf, nil
	case []interface{}:
		buf := []byte{'['}
		for i, item := range x {
			if i > 0 {
				buf = append(buf, ',')
			}
			ib, err := marshalSorted(item)
			if err != nil {
				return nil, err
			}
			buf = append(buf, ib...)
		}
		buf = append(buf, ']')
		return buf, nil
	default:
		return json.Marshal(x)
	}
}

// ToSnapshotMap 把 BillingQuote 转成 map[string]interface{}，
// 用于嵌入 api_call_logs.billing_snapshot["quote"]。
//
// 显式构造 map 而非 JSON 转换，确保 line_items 的类型仍是 []map[string]interface{}，
// 与既有测试 helper（quoteFromSnapshot/findQuoteLine 等）保持兼容。
func (q *BillingQuote) ToSnapshotMap() map[string]interface{} {
	if q == nil {
		return nil
	}
	items := make([]map[string]interface{}, 0, len(q.LineItems))
	for _, li := range q.LineItems {
		items = append(items, li.ToMap())
	}
	usageMap := q.Usage.ToMap()
	m := map[string]interface{}{
		"schema_version":        q.SchemaVersion,
		"engine_version":        q.EngineVersion,
		"scenario":              q.Scenario,
		"quote_id":              q.QuoteID,
		"model_id":              q.ModelID,
		"model_name":            q.ModelName,
		"model_type":            q.ModelType,
		"pricing_unit":          q.PricingUnit,
		"currency":              q.Currency,
		"pricing_source":        q.PricingSource,
		"usage":                 usageMap,
		"line_items":            items,
		"total_credits":         q.TotalCredits,
		"total_units":           q.TotalUnits,
		"total_credits_decimal": q.TotalCreditsDecimal,
		"total_rmb":             q.TotalRMB,
		"platform_cost_credits": q.PlatformCostCredits,
		"platform_cost_units":   q.PlatformCostUnits,
		"platform_cost_credits_decimal": q.PlatformCreditsDecimal,
		"platform_cost_rmb":     q.PlatformCostRMB,
		"matched_tier_name":     q.MatchedTierName,
		"matched_tier_idx":      q.MatchedTierIdx,
		"user_discount_type":    q.UserDiscountType,
		"actual_cost_credits":   q.ActualCostCredits,
		"actual_cost_units":     q.ActualCostUnits,
		"actual_cost_credits_decimal": q.ActualCreditsDecimal,
		"billing_status":        q.BillingStatus,
		"thinking_mode_applied": q.ThinkingModeApplied,
		"quote_hash":            q.QuoteHash,
	}
	if q.UnderCollectedCredits != 0 {
		m["under_collected_credits"] = q.UnderCollectedCredits
	}
	if q.UnderCollectedUnits != 0 {
		m["under_collected_units"] = q.UnderCollectedUnits
		m["under_collected_credits_decimal"] = q.UnderCollectedCreditsDecimal
	}
	if q.UserDiscountID != nil {
		m["user_discount_id"] = *q.UserDiscountID
	}
	if q.UserDiscountRate != nil {
		m["user_discount_rate"] = *q.UserDiscountRate
	}
	if len(q.Warnings) > 0 {
		m["warnings"] = q.Warnings
	}
	if len(q.MatchedDimValues) > 0 {
		m["matched_dim_values"] = q.MatchedDimValues
	}
	if q.MatchedMatrixNote != "" {
		m["matched_matrix_note"] = q.MatchedMatrixNote
	}
	return m
}

// ToMap 把单条 line item 转为 map，便于嵌入 snapshot。
func (li QuoteLineItem) ToMap() map[string]interface{} {
	m := map[string]interface{}{
		"component":          li.Component,
		"usage_key":          li.UsageKey,
		"quantity":           li.Quantity,
		"unit":               li.Unit,
		"denominator":        li.Denominator,
		"unit_price_credits": li.UnitPriceCredits,
		"unit_price_rmb":     li.UnitPriceRMB,
		"cost_credits":       li.CostCredits,
		"cost_units":         li.CostUnits,
		"cost_credits_decimal": credits.BillingUnitsToCreditAmount(li.CostUnits),
		"cost_rmb":           li.CostRMB,
		"source":             li.Source,
	}
	if li.Section != "" {
		m["section"] = li.Section
	}
	return m
}

// ToMap 把 QuoteUsage 转为可序列化的 map（仅保留非零字段）。
func (u QuoteUsage) ToMap() map[string]interface{} {
	m := map[string]interface{}{}
	if u.InputTokens != 0 {
		m["input_tokens"] = u.InputTokens
	}
	if u.OutputTokens != 0 {
		m["output_tokens"] = u.OutputTokens
	}
	if u.TotalTokens != 0 {
		m["total_tokens"] = u.TotalTokens
	}
	if u.CacheReadTokens != 0 {
		m["cache_read_tokens"] = u.CacheReadTokens
	}
	if u.CacheWriteTokens != 0 {
		m["cache_write_tokens"] = u.CacheWriteTokens
	}
	if u.CacheWrite1hTokens != 0 {
		m["cache_write_1h_tokens"] = u.CacheWrite1hTokens
	}
	if u.ReasoningTokens != 0 {
		m["reasoning_tokens"] = u.ReasoningTokens
	}
	if u.ImageCount != 0 {
		m["image_count"] = u.ImageCount
	}
	if u.DurationSec != 0 {
		m["duration_sec"] = u.DurationSec
	}
	if u.CharCount != 0 {
		m["char_count"] = u.CharCount
	}
	if u.CallCount != 0 {
		m["call_count"] = u.CallCount
	}
	return m
}
