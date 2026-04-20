package pricing

import (
	"context"
	"encoding/json"
	"fmt"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
	"tokenhub-server/internal/pkg/logger"

	"go.uber.org/zap"
)

// TierPriceSelection 阶梯价格选择结果
type TierPriceSelection struct {
	InputPricePerMillion  int64   // 命中阶梯的输入售价（积分/百万 token）
	OutputPricePerMillion int64   // 命中阶梯的输出售价（积分/百万 token）
	InputPriceRMB         float64 // 每百万 token 输入价（人民币）
	OutputPriceRMB        float64 // 每百万 token 输出价（人民币）
	MatchedTier           string  // 命中阶梯名称
	MatchedTierIdx        int     // 命中阶梯下标（-1=未命中）
	FromTier              bool    // true=从阶梯得出；false=回退到单价
	// SellingOverride=true 表示阶梯明确指定了售价（非成本价推导），
	// 此时跳过 FIXED/MARKUP 折扣链路，仅叠加 DISCOUNT 类型
	SellingOverride bool
}

// selectPriceForTokens 根据 (inputTokens, outputTokens) 选择阶梯价格
// 优先级：
//  1. ModelPricing.PriceTiers（平台售价阶梯，SellingInputPrice 非 nil 时视为终价）
//  2. AIModel.PriceTiers（成本价阶梯，用于 supplier 侧计价）
//  3. 都未命中 → FromTier=false，调用方走旧单价路径
func (c *PricingCalculator) selectPriceForTokens(
	ctx context.Context,
	modelID uint,
	inputTokens, outputTokens int64,
) (*TierPriceSelection, error) {
	result := &TierPriceSelection{MatchedTierIdx: -1}

	// 1. 先查 ModelPricing.PriceTiers（售价阶梯）
	mp, mpErr := c.getPlatformPrice(ctx, modelID)
	if mpErr == nil && mp != nil {
		tierData := parseTiersJSON(mp.PriceTiers)
		if tierData != nil && len(tierData.Tiers) > 0 {
			idx, tier := model.SelectTier(tierData.Tiers, inputTokens, outputTokens)
			if tier != nil {
				// 优先用 SellingPrice 覆盖
				if tier.SellingInputPrice != nil || tier.SellingOutputPrice != nil {
					inputRMB := mp.InputPriceRMB
					outputRMB := mp.OutputPriceRMB
					if tier.SellingInputPrice != nil {
						inputRMB = *tier.SellingInputPrice
					}
					if tier.SellingOutputPrice != nil {
						outputRMB = *tier.SellingOutputPrice
					}
					result.InputPriceRMB = inputRMB
					result.OutputPriceRMB = outputRMB
					result.InputPricePerMillion = credits.RMBToCredits(inputRMB)
					result.OutputPricePerMillion = credits.RMBToCredits(outputRMB)
					result.MatchedTier = tier.Name
					result.MatchedTierIdx = idx
					result.FromTier = true
					result.SellingOverride = true
					return result, nil
				}
				// 未覆盖售价 → 使用 tier 的 Input/OutputPrice 作为售价基础
				if tier.InputPrice > 0 || tier.OutputPrice > 0 {
					result.InputPriceRMB = tier.InputPrice
					result.OutputPriceRMB = tier.OutputPrice
					result.InputPricePerMillion = credits.RMBToCredits(tier.InputPrice)
					result.OutputPricePerMillion = credits.RMBToCredits(tier.OutputPrice)
					result.MatchedTier = tier.Name
					result.MatchedTierIdx = idx
					result.FromTier = true
					return result, nil
				}
			}
		}
	}

	// 2. 再查 AIModel.PriceTiers（成本阶梯）
	var aiModel model.AIModel
	if err := c.db.WithContext(ctx).Select("id, price_tiers, input_cost_rmb, output_cost_rmb").First(&aiModel, modelID).Error; err != nil {
		if err != gorm.ErrRecordNotFound {
			logger.L.Debug("selectPriceForTokens: load ai_model failed",
				zap.Uint("model_id", modelID), zap.Error(err))
		}
		return result, nil
	}

	tierData := parseTiersJSON(aiModel.PriceTiers)
	if tierData != nil && len(tierData.Tiers) > 0 {
		idx, tier := model.SelectTier(tierData.Tiers, inputTokens, outputTokens)
		if tier != nil && (tier.InputPrice > 0 || tier.OutputPrice > 0) {
			// AI 模型阶梯是"成本价"，按 ModelPricing 的成本→售价比例推算售价
			// 比例计算：若 aiModel.InputCostRMB>0，scale = mp.InputPriceRMB / aiModel.InputCostRMB；否则无倍率
			inputScale := 1.0
			outputScale := 1.0
			if mp != nil && aiModel.InputCostRMB > 0 {
				inputScale = mp.InputPriceRMB / aiModel.InputCostRMB
			}
			if mp != nil && aiModel.OutputCostRMB > 0 {
				outputScale = mp.OutputPriceRMB / aiModel.OutputCostRMB
			}
			inputRMB := tier.InputPrice * inputScale
			outputRMB := tier.OutputPrice * outputScale
			result.InputPriceRMB = inputRMB
			result.OutputPriceRMB = outputRMB
			result.InputPricePerMillion = credits.RMBToCredits(inputRMB)
			result.OutputPricePerMillion = credits.RMBToCredits(outputRMB)
			result.MatchedTier = tier.Name
			result.MatchedTierIdx = idx
			result.FromTier = true
			return result, nil
		}
	}

	// 未命中 → FromTier=false，由调用方回退
	return result, nil
}

// parseTiersJSON 从 JSON 字段解析 PriceTiersData
// 返回 nil 表示空/失败，调用方应 fallback
func parseTiersJSON(raw model.JSON) *model.PriceTiersData {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var data model.PriceTiersData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil
	}
	return &data
}

// GetPlatformPriceForDisplay 导出 getPlatformPrice 用于外部（handler 层）
func (c *PricingCalculator) GetPlatformPriceForDisplay(ctx context.Context, modelID uint) (*model.ModelPricing, error) {
	return c.getPlatformPrice(ctx, modelID)
}

// Ensure fmt is used to avoid import errors when all paths return nil err
var _ = fmt.Sprintf
