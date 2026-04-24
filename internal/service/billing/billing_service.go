package billing

import (
	"context"
	"encoding/json"
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

type Service struct {
	db          *gorm.DB
	pricingCalc *pricing.PricingCalculator
	balanceSvc  *balancesvc.BalanceService
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
}

type UnitUsageRequest struct {
	RequestID  string
	UserID     uint
	TenantID   uint
	AgentLevel int
	ModelName  string
	Usage      pricing.UsageInput
	FreezeID   string
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
	return &Service{db: db, pricingCalc: pricingCalc, balanceSvc: balanceSvc}
}

func (s *Service) SettleUsage(ctx context.Context, req UsageRequest) (*UsageOutcome, error) {
	if s.pricingCalc == nil {
		return &UsageOutcome{BillingStatus: BillingStatusNoCharge, UsageSource: "provider"}, nil
	}
	var aiModel model.AIModel
	if err := s.db.WithContext(ctx).Where("model_name = ? AND is_active = true", req.ModelName).First(&aiModel).Error; err != nil {
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
		CostUnits:         credits.CreditsToBillingUnits(costResult.TotalCost),
		CostRMB:           costResult.TotalCostRMB,
		PlatformCostRMB:   float64(costResult.PlatformCost) / 10000.0,
		PlatformCostUnits: credits.CreditsToBillingUnits(costResult.PlatformCost),
		BillingStatus:     BillingStatusNoCharge,
		UsageSource:       "provider",
		UsageEstimated:    false,
	}

	if req.ThinkingMode {
		s.applyThinkingSurcharge(ctx, req, &aiModel, costResult, out)
		out.CostCredits = costResult.TotalCost
		out.CostUnits = credits.CreditsToBillingUnits(costResult.TotalCost)
		out.CostRMB = costResult.TotalCostRMB
	}

	var deductErr error
	if s.balanceSvc != nil {
		if req.FreezeID != "" {
			deductErr = s.balanceSvc.SettleBalance(ctx, req.FreezeID, costResult.TotalCost)
		} else if costResult.TotalCost > 0 {
			deductErr = s.balanceSvc.DeductForRequest(ctx, req.UserID, req.TenantID, costResult.TotalCost, req.ModelName, req.RequestID)
		}
	}

	if deductErr != nil {
		out.BillingStatus = BillingStatusDeductFailed
		out.ActualCostCredits = 0
		out.ActualCostUnits = 0
		out.UnderCollectedCredits = costResult.TotalCost
		out.UnderCollectedUnits = credits.CreditsToBillingUnits(costResult.TotalCost)
		out.Snapshot = s.buildSnapshot(req, out)
		out.SnapshotJSON = encodeSnapshot(out.Snapshot)
		return out, deductErr
	}

	if costResult.TotalCost > 0 {
		out.BillingStatus = BillingStatusSettled
		out.ActualCostCredits = costResult.TotalCost
		out.ActualCostUnits = credits.CreditsToBillingUnits(costResult.TotalCost)
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
	var aiModel model.AIModel
	if err := s.db.WithContext(ctx).Where("model_name = ? AND is_active = true", req.ModelName).First(&aiModel).Error; err != nil {
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
		EstimatedCostUnits:   credits.CreditsToBillingUnits(costResult.TotalCost),
		EstimatedCostRMB:     costResult.TotalCostRMB,
		Model:                aiModel,
		CostResult:           costResult,
	}
	if costResult.TotalCost <= 0 {
		return freeze, nil
	}
	freezeID, err := s.balanceSvc.FreezeBalance(ctx, req.UserID, req.TenantID, costResult.TotalCost, req.ModelName, req.RequestID)
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
	var aiModel model.AIModel
	if err := s.db.WithContext(ctx).Where("model_name = ? AND is_active = true", req.ModelName).First(&aiModel).Error; err != nil {
		if req.FreezeID != "" {
			_ = s.ReleaseFrozen(ctx, req.FreezeID)
		}
		return &UsageOutcome{BillingStatus: BillingStatusNoCharge, UsageSource: "provider"}, nil
	}

	costResult, err := s.pricingCalc.CalculateCostByUnit(ctx, req.UserID, aiModel.ID, req.TenantID, req.AgentLevel, req.Usage)
	if err != nil || costResult == nil {
		return nil, err
	}

	out := &UsageOutcome{
		Model:             aiModel,
		CostResult:        costResult,
		CostCredits:       costResult.TotalCost,
		CostUnits:         credits.CreditsToBillingUnits(costResult.TotalCost),
		CostRMB:           costResult.TotalCostRMB,
		PlatformCostRMB:   float64(costResult.PlatformCost) / 10000.0,
		PlatformCostUnits: credits.CreditsToBillingUnits(costResult.PlatformCost),
		BillingStatus:     BillingStatusNoCharge,
		UsageSource:       "provider",
		UsageEstimated:    false,
	}

	var deductErr error
	if s.balanceSvc != nil {
		if req.FreezeID != "" {
			deductErr = s.balanceSvc.SettleBalance(ctx, req.FreezeID, costResult.TotalCost)
		} else if costResult.TotalCost > 0 {
			deductErr = s.balanceSvc.DeductForRequest(ctx, req.UserID, req.TenantID, costResult.TotalCost, req.ModelName, req.RequestID)
		}
	}
	if deductErr != nil {
		out.BillingStatus = BillingStatusDeductFailed
		out.ActualCostCredits = 0
		out.ActualCostUnits = 0
		out.UnderCollectedCredits = costResult.TotalCost
		out.UnderCollectedUnits = credits.CreditsToBillingUnits(costResult.TotalCost)
		out.Snapshot = s.buildUnitSnapshot(req, out)
		out.SnapshotJSON = encodeSnapshot(out.Snapshot)
		return out, deductErr
	}

	if costResult.TotalCost > 0 {
		out.BillingStatus = BillingStatusSettled
		out.ActualCostCredits = costResult.TotalCost
		out.ActualCostUnits = credits.CreditsToBillingUnits(costResult.TotalCost)
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
	var aiModel model.AIModel
	if err := s.db.WithContext(ctx).Where("model_name = ? AND is_active = true", req.ModelName).First(&aiModel).Error; err != nil {
		return &FreezeOutcome{}, nil
	}

	costResult, err := s.pricingCalc.CalculateCostByUnit(ctx, req.UserID, aiModel.ID, req.TenantID, req.AgentLevel, req.Usage)
	if err != nil || costResult == nil {
		return nil, err
	}
	out := &FreezeOutcome{
		EstimatedCostCredits: costResult.TotalCost,
		EstimatedCostUnits:   credits.CreditsToBillingUnits(costResult.TotalCost),
		EstimatedCostRMB:     costResult.TotalCostRMB,
		Model:                aiModel,
		CostResult:           costResult,
	}
	if costResult.TotalCost <= 0 {
		return out, nil
	}
	freezeID, err := s.balanceSvc.FreezeBalance(ctx, req.UserID, req.TenantID, costResult.TotalCost, req.ModelName, req.RequestID)
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

func (s *Service) calculateCost(ctx context.Context, req UsageRequest, aiModel *model.AIModel) (*pricing.CostResult, error) {
	if req.Usage.CacheReadTokens > 0 || req.Usage.CacheWriteTokens > 0 {
		return s.pricingCalc.CalculateCostWithCache(ctx, req.UserID, aiModel, req.TenantID, req.AgentLevel, pricing.CacheUsageInput{
			InputTokens:      req.Usage.PromptTokens,
			OutputTokens:     req.Usage.CompletionTokens,
			CacheReadTokens:  req.Usage.CacheReadTokens,
			CacheWriteTokens: req.Usage.CacheWriteTokens,
		})
	}
	return s.pricingCalc.CalculateCost(ctx, req.UserID, aiModel.ID, req.TenantID, req.AgentLevel, req.Usage.PromptTokens, req.Usage.CompletionTokens)
}

func (s *Service) applyThinkingSurcharge(ctx context.Context, req UsageRequest, aiModel *model.AIModel, costResult *pricing.CostResult, out *UsageOutcome) {
	if req.Usage.CompletionTokens <= 0 {
		return
	}
	thinkingSellRMB := resolveThinkingOutputSellRMB(s.db, ctx, aiModel, costResult)
	if thinkingSellRMB <= 0 {
		return
	}
	normalSellRMB := costResult.PriceDetail.OutputPriceRMB
	diffRMB := thinkingSellRMB - normalSellRMB
	if diffRMB <= 0 {
		return
	}
	surchargeRMB := diffRMB * float64(req.Usage.CompletionTokens) / 1_000_000
	surchargeCredits := int64(surchargeRMB*10000 + 0.5)
	if surchargeCredits <= 0 {
		return
	}
	costResult.TotalCost += surchargeCredits
	costResult.OutputCost += surchargeCredits
	costResult.TotalCostRMB += surchargeRMB
	out.ThinkingModeApplied = true
	out.ThinkingOutputPriceRMB = thinkingSellRMB
}

func (s *Service) buildSnapshot(req UsageRequest, out *UsageOutcome) map[string]interface{} {
	cr := out.CostResult
	snapshot := map[string]interface{}{
		"schema_version":          1,
		"generated_at":            time.Now().UTC().Format(time.RFC3339Nano),
		"request_id":              req.RequestID,
		"model_id":                out.Model.ID,
		"model_name":              req.ModelName,
		"pricing_unit":            out.Model.PricingUnit,
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
		"output_cost_credits":     cr.OutputCost,
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
		"cache_write_cost":        cr.CacheWriteCost,
		"regular_input_cost":      cr.RegularInputCost,
		"cache_saving_credits":    cr.CacheSavingCredits,
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
	return snapshot
}

func (s *Service) buildUnitSnapshot(req UnitUsageRequest, out *UsageOutcome) map[string]interface{} {
	cr := out.CostResult
	snapshot := map[string]interface{}{
		"schema_version":          1,
		"generated_at":            time.Now().UTC().Format(time.RFC3339Nano),
		"request_id":              req.RequestID,
		"model_id":                out.Model.ID,
		"model_name":              req.ModelName,
		"model_type":              out.Model.ModelType,
		"pricing_unit":            out.Model.PricingUnit,
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
		"output_cost_credits":     cr.OutputCost,
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
	return snapshot
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
