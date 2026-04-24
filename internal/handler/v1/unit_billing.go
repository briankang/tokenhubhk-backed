package v1

import (
	"context"
	"encoding/json"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
	billingsvc "tokenhub-server/internal/service/billing"
	"tokenhub-server/internal/service/pricing"
)

func findActiveModelByRequestOrActual(ctx context.Context, db *gorm.DB, requestModel, actualModel string) (*model.AIModel, string) {
	if db == nil {
		return nil, requestModel
	}
	names := []string{actualModel, requestModel}
	for _, name := range names {
		if name == "" {
			continue
		}
		var aiModel model.AIModel
		if err := db.WithContext(ctx).
			Where("model_name = ? AND is_active = true", name).
			First(&aiModel).Error; err == nil {
			return &aiModel, name
		}
	}
	return nil, requestModel
}

func resolveChannelSupplierName(ctx context.Context, db *gorm.DB, ch *model.Channel) string {
	if ch == nil {
		return ""
	}
	if ch.Supplier.Name != "" {
		return ch.Supplier.Name
	}
	if db == nil || ch.SupplierID == 0 {
		return ""
	}
	var supplier model.Supplier
	if err := db.WithContext(ctx).Select("name").Where("id = ?", ch.SupplierID).First(&supplier).Error; err == nil {
		return supplier.Name
	}
	return ""
}

func setUnitBillingContext(
	c *gin.Context,
	requestID string,
	billedModel string,
	aiModel *model.AIModel,
	costResult *pricing.CostResult,
	usage pricing.UsageInput,
	status string,
	actualCostCredits int64,
	underCollectedCredits int64,
) {
	if c == nil || aiModel == nil || costResult == nil {
		return
	}
	c.Set("billing_status", status)
	c.Set("actual_cost_credits", actualCostCredits)
	c.Set("under_collected_credits", underCollectedCredits)
	c.Set("platform_cost_rmb", credits.CreditsToRMB(costResult.PlatformCost))
	c.Set("usage_source", "provider")
	if costResult.UserDiscountID != nil {
		c.Set("user_discount_id", *costResult.UserDiscountID)
	}
	if costResult.UserDiscountRate != nil {
		c.Set("user_discount_rate", *costResult.UserDiscountRate)
	}
	if costResult.UserDiscountType != "" {
		c.Set("user_discount_type", costResult.UserDiscountType)
	}
	if costResult.MatchedTier != "" {
		c.Set("matched_price_tier", costResult.MatchedTier)
	}
	if costResult.MatchedTierIdx >= 0 {
		c.Set("matched_price_tier_idx", costResult.MatchedTierIdx)
	}

	snapshot := map[string]interface{}{
		"request_id":              requestID,
		"model":                   billedModel,
		"model_id":                aiModel.ID,
		"model_type":              aiModel.ModelType,
		"pricing_unit":            aiModel.PricingUnit,
		"billing_status":          status,
		"image_count":             usage.ImageCount,
		"char_count":              usage.CharCount,
		"duration_sec":            usage.DurationSec,
		"call_count":              usage.CallCount,
		"input_tokens":            usage.InputTokens,
		"output_tokens":           usage.OutputTokens,
		"cost_credits":            costResult.TotalCost,
		"cost_rmb":                costResult.TotalCostRMB,
		"actual_cost_credits":     actualCostCredits,
		"under_collected_credits": underCollectedCredits,
		"platform_cost_credits":   costResult.PlatformCost,
		"platform_cost_rmb":       credits.CreditsToRMB(costResult.PlatformCost),
		"input_price_rmb":         costResult.PriceDetail.InputPriceRMB,
		"output_price_rmb":        costResult.PriceDetail.OutputPriceRMB,
		"price_source":            costResult.PriceDetail.Source,
		"matched_tier":            costResult.MatchedTier,
		"matched_tier_idx":        costResult.MatchedTierIdx,
	}
	if costResult.UserDiscountID != nil {
		snapshot["user_discount_id"] = *costResult.UserDiscountID
	}
	if costResult.UserDiscountRate != nil {
		snapshot["user_discount_rate"] = *costResult.UserDiscountRate
	}
	if costResult.UserDiscountType != "" {
		snapshot["user_discount_type"] = costResult.UserDiscountType
	}
	if b, err := json.Marshal(snapshot); err == nil {
		c.Set("billing_snapshot_json", string(b))
	}
}

func applyUnitBillingOutcomeToContext(c *gin.Context, out *billingsvc.UsageOutcome) {
	if c == nil || out == nil || out.CostResult == nil {
		return
	}
	c.Set("billing_status", out.BillingStatus)
	c.Set("actual_cost_credits", out.ActualCostCredits)
	c.Set("actual_cost_units", out.ActualCostUnits)
	c.Set("under_collected_credits", out.UnderCollectedCredits)
	c.Set("under_collected_units", out.UnderCollectedUnits)
	c.Set("platform_cost_rmb", out.PlatformCostRMB)
	c.Set("platform_cost_units", out.PlatformCostUnits)
	c.Set("usage_source", out.UsageSource)
	c.Set("usage_estimated", out.UsageEstimated)
	c.Set("estimated_cost_credits", out.CostCredits)
	c.Set("estimated_cost_units", out.CostUnits)
	c.Set("cost_units", out.CostUnits)
	if out.CostResult.UserDiscountID != nil {
		c.Set("user_discount_id", *out.CostResult.UserDiscountID)
	}
	if out.CostResult.UserDiscountRate != nil {
		c.Set("user_discount_rate", *out.CostResult.UserDiscountRate)
	}
	if out.CostResult.UserDiscountType != "" {
		c.Set("user_discount_type", out.CostResult.UserDiscountType)
	}
	if out.CostResult.MatchedTier != "" {
		c.Set("matched_price_tier", out.CostResult.MatchedTier)
	}
	if out.CostResult.MatchedTierIdx >= 0 {
		c.Set("matched_price_tier_idx", out.CostResult.MatchedTierIdx)
	}
	if out.SnapshotJSON != "" {
		c.Set("billing_snapshot_json", out.SnapshotJSON)
	}
}

func releaseFrozenWithBillingService(c *gin.Context, billingSvc *billingsvc.Service, freezeID string) error {
	if billingSvc == nil || freezeID == "" {
		return nil
	}
	return billingSvc.ReleaseFrozen(c.Request.Context(), freezeID)
}
