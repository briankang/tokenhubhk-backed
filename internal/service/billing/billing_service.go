package billing

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
	"tokenhub-server/internal/provider"
	balancesvc "tokenhub-server/internal/service/balance"
	"tokenhub-server/internal/service/pricing"
)

const (
	BillingStatusSettled      = "settled"
	BillingStatusNoCharge     = "no_charge"
	BillingStatusDeductFailed = "deduct_failed"
)

const billableModelCacheTTL = 2 * time.Minute

type billableModelCacheEntry struct {
	model     model.AIModel
	expiresAt time.Time
}

type billableModelCacheKey struct {
	db        *gorm.DB
	modelName string
}

var billableModelCache sync.Map

type Service struct {
	db          *gorm.DB
	pricingCalc *pricing.PricingCalculator
	balanceSvc  *balancesvc.BalanceService
	quoteSvc    *QuoteService
}

type UsageRequest struct {
	RequestID    string
	UserID       uint
	TenantID     uint
	AgentLevel   int
	ModelName    string
	Usage        provider.Usage
	ThinkingMode bool
	FreezeID     string
	// DimValues v3 引入:按维度命中 PriceMatrix 单元格(如 thinking_mode/context_tier)。
	// 留空时不参与矩阵命中,走旧 PriceTiers / 顶层售价路径。
	DimValues map[string]interface{}
}

type UnitUsageRequest struct {
	RequestID  string
	UserID     uint
	TenantID   uint
	AgentLevel int
	ModelName  string
	Usage      pricing.UsageInput
	FreezeID   string
	// DimValues v3 引入:按维度命中 PriceMatrix 单元格
	// 例如视频:{resolution: "1080p", input_has_video: true, inference_mode: "online"}
	// 例如图片:{resolution: "1024x1024", quality: "hd", mode: "generation"}
	DimValues map[string]interface{}
}

type FreezeOutcome struct {
	FreezeID             string
	EstimatedCostCredits int64
	EstimatedCostUnits   int64
	EstimatedCostRMB     float64
	Model                model.AIModel
	CostResult           *pricing.CostResult
}

type UsageOutcome struct {
	Model                  model.AIModel
	CostResult             *pricing.CostResult
	CostCredits            int64
	CostUnits              int64
	CostRMB                float64
	PlatformCostRMB        float64
	PlatformCostUnits      int64
	BillingStatus          string
	ActualCostCredits      int64
	ActualCostUnits        int64
	UnderCollectedCredits  int64
	UnderCollectedUnits    int64
	UsageSource            string
	UsageEstimated         bool
	ThinkingModeApplied    bool
	ThinkingOutputPriceRMB float64
	Snapshot               map[string]interface{}
	SnapshotJSON           string
}

func NewService(db *gorm.DB, pricingCalc *pricing.PricingCalculator, balanceSvc *balancesvc.BalanceService) *Service {
	if db == nil {
		panic("billing service: db is nil")
	}
	return &Service{
		db:          db,
		pricingCalc: pricingCalc,
		balanceSvc:  balanceSvc,
		quoteSvc:    NewQuoteService(db, pricingCalc),
	}
}

// QuoteService 暴露内部统一计价服务，供 handler 层（如试算端点）共享真相源。
func (s *Service) QuoteService() *QuoteService { return s.quoteSvc }

func (s *Service) loadBillableModel(ctx context.Context, modelName string) (model.AIModel, error) {
	cacheKey := billableModelCacheKey{db: s.db, modelName: modelName}
	if raw, ok := billableModelCache.Load(cacheKey); ok {
		if cached, ok := raw.(*billableModelCacheEntry); ok && time.Now().Before(cached.expiresAt) {
			return cached.model, nil
		}
		billableModelCache.Delete(cacheKey)
	}

	var aiModel model.AIModel
	err := s.db.WithContext(ctx).Where("model_name = ? AND is_active = true", modelName).First(&aiModel).Error
	if err == nil {
		billableModelCache.Store(cacheKey, &billableModelCacheEntry{
			model:     aiModel,
			expiresAt: time.Now().Add(billableModelCacheTTL),
		})
		return aiModel, nil
	}
	err = s.db.WithContext(ctx).Where("model_name = ?", modelName).Order("id ASC").First(&aiModel).Error
	if err == nil {
		billableModelCache.Store(cacheKey, &billableModelCacheEntry{
			model:     aiModel,
			expiresAt: time.Now().Add(billableModelCacheTTL),
		})
	}
	return aiModel, err
}

func (s *Service) SettleUsage(ctx context.Context, req UsageRequest) (*UsageOutcome, error) {
	if s.pricingCalc == nil {
		return &UsageOutcome{BillingStatus: BillingStatusNoCharge, UsageSource: "provider"}, nil
	}
	aiModel, err := s.loadBillableModel(ctx, req.ModelName)
	if err != nil {
		if req.FreezeID != "" {
			_ = s.ReleaseFrozen(ctx, req.FreezeID)
		}
		return &UsageOutcome{BillingStatus: BillingStatusNoCharge, UsageSource: "provider"}, nil
	}

	costResult, err := s.calculateCost(ctx, req, &aiModel)
	if err != nil || costResult == nil {
		if req.FreezeID != "" {
			_ = s.ReleaseFrozen(ctx, req.FreezeID)
		}
		return nil, err
	}

	out := &UsageOutcome{
		Model:             aiModel,
		CostResult:        costResult,
		CostCredits:       costResult.TotalCost,
		CostUnits:         costResult.TotalCostUnits,
		CostRMB:           costResult.TotalCostRMB,
		PlatformCostRMB:   credits.BillingUnitsToRMB(costResult.PlatformCostUnits),
		PlatformCostUnits: costResult.PlatformCostUnits,
		BillingStatus:     BillingStatusNoCharge,
		UsageSource:       "provider",
		UsageEstimated:    false,
	}

	if req.ThinkingMode {
		s.applyThinkingSurcharge(ctx, req, &aiModel, costResult, out)
		out.CostCredits = costResult.TotalCost
		out.CostUnits = costResult.TotalCostUnits
		out.CostRMB = costResult.TotalCostRMB
	}

	var deductErr error
	if s.balanceSvc != nil {
		if req.FreezeID != "" {
			deductErr = s.balanceSvc.SettleBalanceUnits(ctx, req.FreezeID, costResult.TotalCostUnits)
		} else if costResult.TotalCostUnits > 0 {
			deductErr = s.balanceSvc.DeductUnitsForRequest(ctx, req.UserID, req.TenantID, costResult.TotalCostUnits, req.ModelName, req.RequestID)
		}
	}

	if deductErr != nil {
		out.BillingStatus = BillingStatusDeductFailed
		out.ActualCostCredits = 0
		out.ActualCostUnits = 0
		out.UnderCollectedCredits = costResult.TotalCost
		out.UnderCollectedUnits = costResult.TotalCostUnits
		out.Snapshot = s.buildSnapshot(req, out)
		out.SnapshotJSON = encodeSnapshot(out.Snapshot)
		return out, deductErr
	}

	if costResult.TotalCostUnits > 0 {
		out.BillingStatus = BillingStatusSettled
		out.ActualCostCredits = costResult.TotalCost
		out.ActualCostUnits = costResult.TotalCostUnits
	} else {
		out.BillingStatus = BillingStatusNoCharge
	}
	out.UnderCollectedCredits = 0
	out.Snapshot = s.buildSnapshot(req, out)
	out.SnapshotJSON = encodeSnapshot(out.Snapshot)
	return out, nil
}

func (s *Service) FreezeUsage(ctx context.Context, req UsageRequest) (*FreezeOutcome, error) {
	if s.pricingCalc == nil || s.balanceSvc == nil {
		return &FreezeOutcome{}, nil
	}
	aiModel, err := s.loadBillableModel(ctx, req.ModelName)
	if err != nil {
		return &FreezeOutcome{}, nil
	}
	costResult, err := s.calculateCost(ctx, req, &aiModel)
	if err != nil || costResult == nil {
		return nil, err
	}
	out := &UsageOutcome{Model: aiModel, CostResult: costResult}
	if req.ThinkingMode {
		s.applyThinkingSurcharge(ctx, req, &aiModel, costResult, out)
	}
	freeze := &FreezeOutcome{
		EstimatedCostCredits: costResult.TotalCost,
		EstimatedCostUnits:   costResult.TotalCostUnits,
		EstimatedCostRMB:     costResult.TotalCostRMB,
		Model:                aiModel,
		CostResult:           costResult,
	}
	if costResult.TotalCostUnits <= 0 {
		return freeze, nil
	}
	freezeID, err := s.balanceSvc.FreezeBalanceUnits(ctx, req.UserID, req.TenantID, costResult.TotalCostUnits, req.ModelName, req.RequestID)
	if err != nil {
		return freeze, err
	}
	freeze.FreezeID = freezeID
	return freeze, nil
}

func (s *Service) SettleUnitUsage(ctx context.Context, req UnitUsageRequest) (*UsageOutcome, error) {
	if s.pricingCalc == nil {
		return &UsageOutcome{BillingStatus: BillingStatusNoCharge, UsageSource: "provider"}, nil
	}
	aiModel, err := s.loadBillableModel(ctx, req.ModelName)
	if err != nil {
		if req.FreezeID != "" {
			_ = s.ReleaseFrozen(ctx, req.FreezeID)
		}
		return &UsageOutcome{BillingStatus: BillingStatusNoCharge, UsageSource: "provider"}, nil
	}

	// v3: PriceMatrix 命中预处理
	matrixCell, matrixHit := s.tryMatchPriceMatrix(ctx, aiModel.ID, req.DimValues)

	costResult, err := s.pricingCalc.CalculateCostByUnit(ctx, req.UserID, aiModel.ID, req.TenantID, req.AgentLevel, req.Usage)
	if err != nil || costResult == nil {
		return nil, err
	}
	if matrixHit && matrixCell != nil {
		costResult.MatchedDimValues = matrixCell.DimValues
		if matrixCell.Note != "" {
			costResult.MatchedMatrixCellNote = matrixCell.Note
		}
	}

	out := &UsageOutcome{
		Model:             aiModel,
		CostResult:        costResult,
		CostCredits:       costResult.TotalCost,
		CostUnits:         costResult.TotalCostUnits,
		CostRMB:           costResult.TotalCostRMB,
		PlatformCostRMB:   credits.BillingUnitsToRMB(costResult.PlatformCostUnits),
		PlatformCostUnits: costResult.PlatformCostUnits,
		BillingStatus:     BillingStatusNoCharge,
		UsageSource:       "provider",
		UsageEstimated:    false,
	}

	var deductErr error
	if s.balanceSvc != nil {
		if req.FreezeID != "" {
			deductErr = s.balanceSvc.SettleBalanceUnits(ctx, req.FreezeID, costResult.TotalCostUnits)
		} else if costResult.TotalCostUnits > 0 {
			deductErr = s.balanceSvc.DeductUnitsForRequest(ctx, req.UserID, req.TenantID, costResult.TotalCostUnits, req.ModelName, req.RequestID)
		}
	}
	if deductErr != nil {
		out.BillingStatus = BillingStatusDeductFailed
		out.ActualCostCredits = 0
		out.ActualCostUnits = 0
		out.UnderCollectedCredits = costResult.TotalCost
		out.UnderCollectedUnits = costResult.TotalCostUnits
		out.Snapshot = s.buildUnitSnapshot(req, out)
		out.SnapshotJSON = encodeSnapshot(out.Snapshot)
		return out, deductErr
	}

	if costResult.TotalCostUnits > 0 {
		out.BillingStatus = BillingStatusSettled
		out.ActualCostCredits = costResult.TotalCost
		out.ActualCostUnits = costResult.TotalCostUnits
	} else {
		out.BillingStatus = BillingStatusNoCharge
	}
	out.UnderCollectedCredits = 0
	out.Snapshot = s.buildUnitSnapshot(req, out)
	out.SnapshotJSON = encodeSnapshot(out.Snapshot)
	return out, nil
}

func (s *Service) FreezeUnitUsage(ctx context.Context, req UnitUsageRequest) (*FreezeOutcome, error) {
	if s.pricingCalc == nil || s.balanceSvc == nil {
		return &FreezeOutcome{}, nil
	}
	aiModel, err := s.loadBillableModel(ctx, req.ModelName)
	if err != nil {
		return &FreezeOutcome{}, nil
	}

	costResult, err := s.pricingCalc.CalculateCostByUnit(ctx, req.UserID, aiModel.ID, req.TenantID, req.AgentLevel, req.Usage)
	if err != nil || costResult == nil {
		return nil, err
	}
	out := &FreezeOutcome{
		EstimatedCostCredits: costResult.TotalCost,
		EstimatedCostUnits:   costResult.TotalCostUnits,
		EstimatedCostRMB:     costResult.TotalCostRMB,
		Model:                aiModel,
		CostResult:           costResult,
	}
	if costResult.TotalCostUnits <= 0 {
		return out, nil
	}
	freezeID, err := s.balanceSvc.FreezeBalanceUnits(ctx, req.UserID, req.TenantID, costResult.TotalCostUnits, req.ModelName, req.RequestID)
	if err != nil {
		return out, err
	}
	out.FreezeID = freezeID
	return out, nil
}

func (s *Service) ReleaseFrozen(ctx context.Context, freezeID string) error {
	if s.balanceSvc == nil || freezeID == "" {
		return nil
	}
	return s.balanceSvc.ReleaseFrozen(ctx, freezeID)
}

// toQuoteRequest 把内部 UsageRequest 折算为 QuoteService.Calculate 的入参，
// 便于试算预览、replay 等路径走同一份输入结构。
func toQuoteRequest(req UsageRequest, aiModelID uint, scenario string) QuoteRequest {
	return QuoteRequest{
		Scenario:     scenario,
		RequestID:    req.RequestID,
		ModelID:      aiModelID,
		ModelName:    req.ModelName,
		UserID:       req.UserID,
		TenantID:     req.TenantID,
		AgentLevel:   req.AgentLevel,
		Usage:        QuoteUsageFromProvider(req.Usage),
		ThinkingMode: req.ThinkingMode,
		FreezeID:     req.FreezeID,
		DimValues:    req.DimValues,
	}
}

// toQuoteRequestUnit 把内部 UnitUsageRequest 折算为 QuoteService.Calculate 的入参。
func toQuoteRequestUnit(req UnitUsageRequest, aiModelID uint, scenario string) QuoteRequest {
	return QuoteRequest{
		Scenario:   scenario,
		RequestID:  req.RequestID,
		ModelID:    aiModelID,
		ModelName:  req.ModelName,
		UserID:     req.UserID,
		TenantID:   req.TenantID,
		AgentLevel: req.AgentLevel,
		Usage:      QuoteUsageFromUnit(req.Usage),
		FreezeID:   req.FreezeID,
		DimValues:  req.DimValues,
	}
}

// finalizeQuoteForOutcome 在扣费结束后把 actual/under/billing_status 灌入 BillingQuote。
//
// quote_hash 不重算（hash 仅依赖定价输入,不依赖结算结果)。
func finalizeQuoteForOutcome(q *BillingQuote, out *UsageOutcome) {
	if q == nil || out == nil {
		return
	}
	q.ActualCostCredits = out.ActualCostCredits
	q.ActualCostUnits = out.ActualCostUnits
	q.ActualCreditsDecimal = credits.BillingUnitsToCreditAmount(out.ActualCostUnits)
	q.UnderCollectedCredits = out.UnderCollectedCredits
	q.UnderCollectedUnits = out.UnderCollectedUnits
	q.UnderCollectedCreditsDecimal = credits.BillingUnitsToCreditAmount(out.UnderCollectedUnits)
	q.BillingStatus = out.BillingStatus
}

func (s *Service) calculateCost(ctx context.Context, req UsageRequest, aiModel *model.AIModel) (*pricing.CostResult, error) {
	// v3: PriceMatrix 命中优先(若有维度命中,矩阵 cell 价格写回 ModelPricing 顶层后再算)
	matrixCell, matrixHit := s.tryMatchPriceMatrix(ctx, aiModel.ID, req.DimValues)

	var (
		cr  *pricing.CostResult
		err error
	)
	if req.Usage.CacheReadTokens > 0 || req.Usage.CacheWriteTokens > 0 {
		cr, err = s.pricingCalc.CalculateCostWithCache(ctx, req.UserID, aiModel, req.TenantID, req.AgentLevel, pricing.CacheUsageInput{
			InputTokens:        req.Usage.PromptTokens,
			OutputTokens:       req.Usage.CompletionTokens,
			CacheReadTokens:    req.Usage.CacheReadTokens,
			CacheWriteTokens:   req.Usage.CacheWriteTokens,
			CacheWrite1hTokens: req.Usage.CacheWrite1hTokens,
		})
	} else {
		cr, err = s.pricingCalc.CalculateCost(ctx, req.UserID, aiModel.ID, req.TenantID, req.AgentLevel, req.Usage.PromptTokens, req.Usage.CompletionTokens)
	}
	if err != nil || cr == nil {
		return cr, err
	}
	if matrixHit && matrixCell != nil {
		cr.MatchedDimValues = matrixCell.DimValues
		if matrixCell.Note != "" {
			cr.MatchedMatrixCellNote = matrixCell.Note
		}
	}
	return cr, nil
}

// tryMatchPriceMatrix 是 SettleUsage / SettleUnitUsage 用的便捷包装,
// 内部委托给 pricing.MatchCellByModelID — 该函数也被 QuoteService.Calculate(预览路径)
// 与 GetCostBreakdown(重放回退路径)共用,以保证三方在同一份矩阵数据上做匹配。
func (s *Service) tryMatchPriceMatrix(ctx context.Context, modelID uint, dimValues map[string]interface{}) (*model.PriceMatrixCell, bool) {
	return pricing.MatchCellByModelID(ctx, s.db, modelID, dimValues)
}

func (s *Service) applyThinkingSurcharge(ctx context.Context, req UsageRequest, aiModel *model.AIModel, costResult *pricing.CostResult, out *UsageOutcome) {
	applied, sellRMB := applyThinkingSurchargeToCostResult(s.db, ctx, aiModel, costResult, req.Usage.CompletionTokens)
	if applied {
		out.ThinkingModeApplied = true
		out.ThinkingOutputPriceRMB = sellRMB
	}
}

func (s *Service) buildSnapshot(req UsageRequest, out *UsageOutcome) map[string]interface{} {
	cr := out.CostResult
	calculatorType := billingCalculatorType(out.Model)
	snapshot := map[string]interface{}{
		"schema_version":          1,
		"generated_at":            time.Now().UTC().Format(time.RFC3339Nano),
		"request_id":              req.RequestID,
		"model_id":                out.Model.ID,
		"model_name":              req.ModelName,
		"pricing_unit":            out.Model.PricingUnit,
		"calculator_type":         calculatorType,
		"calculator_version":      "v1",
		"calculator_source":       "billing_service",
		"calculator_formula":      billingCalculatorFormula(calculatorType),
		"prompt_tokens":           req.Usage.PromptTokens,
		"completion_tokens":       req.Usage.CompletionTokens,
		"total_tokens":            req.Usage.TotalTokens,
		"cache_read_tokens":       req.Usage.CacheReadTokens,
		"cache_write_tokens":      req.Usage.CacheWriteTokens,
		"reasoning_tokens":        req.Usage.ReasoningTokens,
		"freeze_id":               req.FreezeID,
		"estimated_cost_credits":  out.CostCredits,
		"sell_input_per_million":  cr.PriceDetail.InputPricePerMillion,
		"sell_output_per_million": cr.PriceDetail.OutputPricePerMillion,
		"sell_input_rmb":          cr.PriceDetail.InputPriceRMB,
		"sell_output_rmb":         cr.PriceDetail.OutputPriceRMB,
		"input_cost_credits":      cr.InputCost,
		"input_cost_units":        cr.InputCostUnits,
		"output_cost_credits":     cr.OutputCost,
		"output_cost_units":       cr.OutputCostUnits,
		"total_cost_credits":      cr.TotalCost,
		"total_cost_units":        out.CostUnits,
		"total_cost_rmb":          cr.TotalCostRMB,
		"platform_cost_credits":   cr.PlatformCost,
		"platform_cost_units":     out.PlatformCostUnits,
		"platform_cost_rmb":       out.PlatformCostRMB,
		"pricing_source":          cr.PriceDetail.Source,
		"matched_price_tier":      cr.MatchedTier,
		"matched_price_tier_idx":  cr.MatchedTierIdx,
		"user_discount_type":      cr.UserDiscountType,
		"billing_status":          out.BillingStatus,
		"actual_cost_credits":     out.ActualCostCredits,
		"actual_cost_units":       out.ActualCostUnits,
		"under_collected_credits": out.UnderCollectedCredits,
		"under_collected_units":   out.UnderCollectedUnits,
		"cache_read_cost":         cr.CacheReadCost,
		"cache_read_cost_units":   cr.CacheReadCostUnits,
		"cache_write_cost":        cr.CacheWriteCost,
		"cache_write_cost_units":  cr.CacheWriteCostUnits,
		"regular_input_cost":      cr.RegularInputCost,
		"regular_input_cost_units": cr.RegularInputCostUnits,
		"cache_saving_credits":    cr.CacheSavingCredits,
		"cache_saving_units":      cr.CacheSavingUnits,
		"thinking_mode":           req.ThinkingMode,
		"thinking_mode_applied":   out.ThinkingModeApplied,
	}
	if cr.UserDiscountID != nil {
		snapshot["user_discount_id"] = *cr.UserDiscountID
	}
	if cr.UserDiscountRate != nil {
		snapshot["user_discount_rate"] = *cr.UserDiscountRate
	}
	if out.ThinkingOutputPriceRMB > 0 {
		snapshot["thinking_output_price_rmb"] = out.ThinkingOutputPriceRMB
	}
	// 通过 canonical formatter 构造 BillingQuote(与 QuoteService.Calculate 同源)。
	quote := buildBillingQuote(toQuoteRequest(req, out.Model.ID, QuoteScenarioCharge), &out.Model, cr, out.ThinkingModeApplied)
	finalizeQuoteForOutcome(quote, out)
	if quoteMap := quote.ToSnapshotMap(); quoteMap != nil {
		snapshot["quote"] = quoteMap
		snapshot["quote_hash"] = quote.QuoteHash
		snapshot["quote_schema_version"] = quoteSchemaVersion
	}
	return snapshot
}

func (s *Service) buildUnitSnapshot(req UnitUsageRequest, out *UsageOutcome) map[string]interface{} {
	cr := out.CostResult
	calculatorType := billingCalculatorType(out.Model)
	snapshot := map[string]interface{}{
		"schema_version":          1,
		"generated_at":            time.Now().UTC().Format(time.RFC3339Nano),
		"request_id":              req.RequestID,
		"model_id":                out.Model.ID,
		"model_name":              req.ModelName,
		"model_type":              out.Model.ModelType,
		"pricing_unit":            out.Model.PricingUnit,
		"calculator_type":         calculatorType,
		"calculator_version":      "v1",
		"calculator_source":       "billing_service",
		"calculator_formula":      billingCalculatorFormula(calculatorType),
		"input_tokens":            req.Usage.InputTokens,
		"output_tokens":           req.Usage.OutputTokens,
		"image_count":             req.Usage.ImageCount,
		"char_count":              req.Usage.CharCount,
		"duration_sec":            req.Usage.DurationSec,
		"call_count":              req.Usage.CallCount,
		"freeze_id":               req.FreezeID,
		"estimated_cost_credits":  out.CostCredits,
		"sell_input_rmb":          cr.PriceDetail.InputPriceRMB,
		"sell_output_rmb":         cr.PriceDetail.OutputPriceRMB,
		"input_cost_credits":      cr.InputCost,
		"input_cost_units":        cr.InputCostUnits,
		"output_cost_credits":     cr.OutputCost,
		"output_cost_units":       cr.OutputCostUnits,
		"total_cost_credits":      cr.TotalCost,
		"total_cost_units":        out.CostUnits,
		"total_cost_rmb":          cr.TotalCostRMB,
		"platform_cost_credits":   cr.PlatformCost,
		"platform_cost_units":     out.PlatformCostUnits,
		"platform_cost_rmb":       out.PlatformCostRMB,
		"pricing_source":          cr.PriceDetail.Source,
		"matched_price_tier":      cr.MatchedTier,
		"matched_price_tier_idx":  cr.MatchedTierIdx,
		"user_discount_type":      cr.UserDiscountType,
		"billing_status":          out.BillingStatus,
		"actual_cost_credits":     out.ActualCostCredits,
		"actual_cost_units":       out.ActualCostUnits,
		"under_collected_credits": out.UnderCollectedCredits,
		"under_collected_units":   out.UnderCollectedUnits,
	}
	if cr.UserDiscountID != nil {
		snapshot["user_discount_id"] = *cr.UserDiscountID
	}
	if cr.UserDiscountRate != nil {
		snapshot["user_discount_rate"] = *cr.UserDiscountRate
	}
	// 通过 canonical formatter 构造 BillingQuote(与 QuoteService.Calculate 同源)。
	quote := buildBillingQuote(toQuoteRequestUnit(req, out.Model.ID, QuoteScenarioCharge), &out.Model, cr, false)
	finalizeQuoteForOutcome(quote, out)
	if quoteMap := quote.ToSnapshotMap(); quoteMap != nil {
		snapshot["quote"] = quoteMap
		snapshot["quote_hash"] = quote.QuoteHash
		snapshot["quote_schema_version"] = quoteSchemaVersion
	}
	return snapshot
}

func billingCalculatorType(aiModel model.AIModel) string {
	name := strings.ToLower(aiModel.ModelName)
	switch aiModel.PricingUnit {
	case model.UnitPerImage:
		if strings.Contains(name, "vision") || strings.Contains(name, "vl") {
			return "vision_image_unit"
		}
		return "image_unit"
	case model.UnitPerSecond, model.UnitPerMinute, model.UnitPerHour:
		if aiModel.ModelType == model.ModelTypeVideoGeneration {
			return "video_duration_resolution"
		}
		return "asr_duration"
	case model.UnitPer10kCharacters, model.UnitPerMillionCharacters, model.UnitPerKChars:
		return "tts_characters"
	case model.UnitPerCall:
		return "rerank_call"
	default:
		if strings.Contains(name, "seedance-2") {
			return "volc_seedance_2_video_formula"
		}
		if strings.Contains(name, "embed") || aiModel.ModelType == model.ModelTypeEmbedding {
			return "embedding_token"
		}
		if strings.Contains(name, "rerank") || aiModel.ModelType == model.ModelTypeRerank {
			return "rerank_token"
		}
		if aiModel.SupportsCache || len(aiModel.PriceTiers) > 2 {
			return "token_io_tiered_cache"
		}
		return "token_io"
	}
}

func billingCalculatorFormula(calculatorType string) []string {
	switch calculatorType {
	case "volc_seedance_2_video_formula":
		return []string{
			"估算 tokens = max(min_tokens, (input_seconds + output_seconds) * width * height * fps / 1024)",
			"金额 = token 单价 * 估算 tokens / 1,000,000",
		}
	case "video_duration_resolution":
		return []string{"金额 = 视频条数 * 分辨率/音频/草稿档位单价"}
	case "image_unit", "vision_image_unit":
		return []string{"金额 = 图片张数 * 图片档位单价"}
	case "tts_characters":
		return []string{"金额 = 字符数 / 计费字符单位 * 字符单价"}
	case "asr_duration":
		return []string{"金额 = 音频时长 / 计费时长单位 * 时长单价"}
	case "rerank_call":
		return []string{"金额 = 调用次数 * 单次调用价格"}
	default:
		return []string{
			"输入金额 = 输入单价 * 输入 tokens / 1,000,000",
			"输出金额 = 输出单价 * 输出 tokens / 1,000,000",
		}
	}
}

func encodeSnapshot(snapshot map[string]interface{}) string {
	if len(snapshot) == 0 {
		return ""
	}
	b, err := json.Marshal(snapshot)
	if err != nil {
		return ""
	}
	return string(b)
}

func resolveThinkingOutputSellRMB(db *gorm.DB, ctx context.Context, aiModel *model.AIModel, costResult *pricing.CostResult) float64 {
	if aiModel == nil || costResult == nil {
		return 0
	}

	normalSellRMB := costResult.PriceDetail.OutputPriceRMB
	normalCostRMB := aiModel.OutputCostRMB

	var mp model.ModelPricing
	hasMP := false
	if err := db.WithContext(ctx).Where("model_id = ?", aiModel.ID).First(&mp).Error; err == nil {
		hasMP = true
	}

	if costResult.MatchedTierIdx >= 0 {
		if hasMP && len(mp.PriceTiers) > 0 {
			var mpData model.PriceTiersData
			if json.Unmarshal(mp.PriceTiers, &mpData) == nil && costResult.MatchedTierIdx < len(mpData.Tiers) {
				t := mpData.Tiers[costResult.MatchedTierIdx]
				if t.SellingOutputThinkingPrice != nil && *t.SellingOutputThinkingPrice > 0 {
					return *t.SellingOutputThinkingPrice
				}
			}
		}
		if len(aiModel.PriceTiers) > 0 {
			var amData model.PriceTiersData
			if json.Unmarshal(aiModel.PriceTiers, &amData) == nil && costResult.MatchedTierIdx < len(amData.Tiers) {
				t := amData.Tiers[costResult.MatchedTierIdx]
				if t.OutputPriceThinking > 0 && t.OutputPrice > 0 && normalSellRMB > 0 {
					return t.OutputPriceThinking * (normalSellRMB / t.OutputPrice)
				}
			}
		}
	}

	if hasMP && mp.OutputPriceThinkingRMB > 0 {
		return mp.OutputPriceThinkingRMB
	}
	if aiModel.OutputCostThinkingRMB > 0 && normalCostRMB > 0 && normalSellRMB > 0 {
		return aiModel.OutputCostThinkingRMB * (normalSellRMB / normalCostRMB)
	}
	return 0
}
