package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
	"tokenhub-server/internal/pkg/logger"

	"go.uber.org/zap"
)

// buildTierSelectionFromMatrixCell 把 PriceMatrix.Cell 转换为 TierPriceSelection (M1, 2026-04-28)
//
// 用途：当 selectPriceForTokens 步骤 0 命中 PriceMatrix cell 时，把 cell 的售价
// (SellingInput/SellingOutput/SellingPerUnit) 包装成 TierPriceSelection，让上层
// CalculateCost 走"阶梯命中"路径（FromTier=true + SellingOverride=true）。
//
// 返回 nil 当：
//   - cell 是 nil 或 unsupported
//   - cell 没有任何 SellingInput/SellingOutput/SellingPerUnit 字段（无效计费数据）
//
// 平台成本侧（PlatformInput/Output）：从 cell.OfficialInput/OfficialOutput 取，
// 若缺失则从 aiModel.InputCostRMB/OutputCostRMB 兜底。
func buildTierSelectionFromMatrixCell(cell *model.PriceMatrixCell, aiModel *model.AIModel) *TierPriceSelection {
	if cell == nil || !cell.Supported {
		return nil
	}

	// 取售价：优先 SellingInput/SellingOutput；单价模型用 SellingPerUnit
	var sellInputRMB, sellOutputRMB float64
	if cell.SellingInput != nil {
		sellInputRMB = *cell.SellingInput
	}
	if cell.SellingOutput != nil {
		sellOutputRMB = *cell.SellingOutput
	}
	if cell.SellingPerUnit != nil {
		// 单价模型：input/output 共用 per_unit，避免双倍扣费
		// 调用方应使用 outputTokens=0 + 单价路径，或自动用 outputPrice 计费
		if sellInputRMB == 0 {
			sellInputRMB = *cell.SellingPerUnit
		}
		if sellOutputRMB == 0 {
			sellOutputRMB = *cell.SellingPerUnit
		}
	}
	// 至少一个售价非零才视为有效命中
	if sellInputRMB == 0 && sellOutputRMB == 0 {
		return nil
	}

	// 取成本价（用于利润计算）：优先 cell.OfficialInput/Output，fallback aiModel
	var costInputRMB, costOutputRMB float64
	if cell.OfficialInput != nil {
		costInputRMB = *cell.OfficialInput
	} else if aiModel != nil {
		costInputRMB = aiModel.InputCostRMB
	}
	if cell.OfficialOutput != nil {
		costOutputRMB = *cell.OfficialOutput
	} else if aiModel != nil {
		costOutputRMB = aiModel.OutputCostRMB
	}
	if cell.OfficialPerUnit != nil {
		if costInputRMB == 0 {
			costInputRMB = *cell.OfficialPerUnit
		}
		if costOutputRMB == 0 {
			costOutputRMB = *cell.OfficialPerUnit
		}
	}

	// Cell 名称（用于 audit 日志）：优先 Note，fallback 拼接 dim_values
	tierName := cell.Note
	if tierName == "" && len(cell.DimValues) > 0 {
		// 简单拼接：key1=val1,key2=val2
		parts := make([]string, 0, len(cell.DimValues))
		for k, v := range cell.DimValues {
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
		tierName = "matrix:" + joinStrings(parts, ",")
	}

	return &TierPriceSelection{
		InputPriceRMB:                 sellInputRMB,
		OutputPriceRMB:                sellOutputRMB,
		InputPricePerMillion:          credits.RMBToCredits(sellInputRMB),
		OutputPricePerMillion:         credits.RMBToCredits(sellOutputRMB),
		PlatformInputPriceRMB:         costInputRMB,
		PlatformOutputPriceRMB:        costOutputRMB,
		PlatformInputPricePerMillion:  credits.RMBToCredits(costInputRMB),
		PlatformOutputPricePerMillion: credits.RMBToCredits(costOutputRMB),
		MatchedTier:                   tierName,
		MatchedTierIdx:                0, // PriceMatrix cell 索引以"命中即第一档"语义；不向上传播 cell 在 cells[] 中的下标
		FromTier:                      true,
		SellingOverride:               true, // PriceMatrix 售价是终价，跳过 FIXED/MARKUP 折扣链路
		MatrixCellDimValues:           cell.DimValues,
		MatrixCellNote:                cell.Note,
		FromMatrix:                    true,
	}
}

// joinStrings 简单字符串拼接 helper（避免引入 strings.Join 的 import 冲突）
func joinStrings(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for i := 1; i < len(parts); i++ {
		out += sep + parts[i]
	}
	return out
}

// selectPriceByVariant 在 PriceTiers 中按 Variant 大小写不敏感匹配单位价
//
// 用途：F3 修复 wan2.7-t2v / wan2.6-t2v 1080P 漏扣 —— 在 CalculateCostByUnit 的
// per_second / per_image / per_hour 路径，根据 usage.Variant（如 "1080P"）选档。
//
// 返回：
//   - 匹配到的单价（优先 InputPrice，其次 OutputPrice）
//   - 是否命中
//
// 未命中时调用方应回退到 m.InputCostRMB 兜底单价。
func selectPriceByVariant(tiers []model.PriceTier, variant string) (float64, bool) {
	if variant == "" || len(tiers) == 0 {
		return 0, false
	}
	for _, t := range tiers {
		if strings.EqualFold(t.Variant, variant) {
			if t.InputPrice > 0 {
				return t.InputPrice, true
			}
			if t.OutputPrice > 0 {
				return t.OutputPrice, true
			}
		}
	}
	return 0, false
}

// pickFirstNonZero 返回第一个 > 0 的浮点值，全 0 返回 0
func pickFirstNonZero(values ...float64) float64 {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

// TierPriceSelection 阶梯价格选择结果
type TierPriceSelection struct {
	InputPricePerMillion  int64   // 命中阶梯的输入售价（积分/百万 token）
	OutputPricePerMillion int64   // 命中阶梯的输出售价（积分/百万 token）
	InputPriceRMB         float64 // 每百万 token 输入价（人民币）
	OutputPriceRMB        float64 // 每百万 token 输出价（人民币）
	// 思考模式输出价（阿里云 qwen3.5-plus/qwen3.6-plus 等；0 = 不区分）
	// 调用方根据请求是否处于思考模式选择使用 OutputPriceRMB 或 OutputPriceRMBThinking
	OutputPriceRMBThinking        float64
	OutputPricePerMillionThinking int64
	MatchedTier                   string // 命中阶梯名称
	MatchedTierIdx                int    // 命中阶梯下标（-1=未命中）
	FromTier                      bool   // true=从阶梯得出；false=回退到单价
	// SellingOverride=true 表示阶梯明确指定了售价（非成本价推导），
	// 此时跳过 FIXED/MARKUP 折扣链路，仅叠加 DISCOUNT 类型
	SellingOverride         bool
	SellingOverrideThinking bool // 思考模式输出价是否为阶梯独立售价覆盖
	// P2: 阶梯命中时同步携带成本侧（平台向供应商付的钱）单价
	// 优先级：
	//   1. AIModel.PriceTiers 同范围阶梯的 InputPrice/OutputPrice（成本价）
	//   2. 按比例反推：售价阶梯比 / 售价基础比 × 成本基础价
	//   3. 0（调用方回退到 platformPrice 基础成本）
	PlatformInputPricePerMillion  int64
	PlatformOutputPricePerMillion int64
	PlatformInputPriceRMB         float64
	PlatformOutputPriceRMB        float64

	// M1 (2026-04-28): PriceMatrix 命中信息（用于 BillingService stamp 到 CostResult）
	// 仅当 selectPriceForTokens 走步骤 0 命中 PriceMatrix cell 时填充
	MatrixCellDimValues map[string]interface{} // 命中的 cell.DimValues（含 JSON 反序列化后的原始类型）
	MatrixCellNote      string                 // 命中 cell 的 note 字段（如 "1080p × 含视频"）
	FromMatrix          bool                   // 是否走 PriceMatrix 路径（true=步骤 0 命中）
}

// selectPriceForTokens 根据 (inputTokens, outputTokens [, dims]) 选择阶梯价格
// 优先级（M1 升级，2026-04-28）：
//  0. **PriceMatrix 命中**：ModelPricing.PriceMatrix 中按 dim_values 严格匹配 cell，
//     命中且 cell.SellingInput/SellingOutput 非空 → 直接作为终价（计费源 v3）
//  1. **PriceTier.DimValues 严格匹配**：dims 非空且 tier 声明 DimValues 时，全字段命中
//     （消除 magic-InputMin 编码维度的脆弱性，是 Seedance 等多档定价的正解）
//  2. ModelPricing.PriceTiers（平台售价阶梯，SellingInputPrice 非 nil 时视为终价）
//  3. AIModel.PriceTiers（成本价阶梯，用于 supplier 侧计价）
//  4. 配置了阶梯但都未严格命中 → 按最大阶梯兜底，避免成功调用低于最高档计费
//  5. 完全没有阶梯配置 → FromTier=false，调用方走旧单价路径
//
// 向后兼容：dims 为 nil/空 → 跳过步骤 0 + 1，直接走步骤 2-5，行为与升级前完全一致。
//
// PriceMatrix vs PriceTier+DimValues 关系：M1 阶段共存（PriceMatrix 优先），
// PriceMatrix 是单一权威计费源，PriceTier+DimValues 作为同步影子（迁移期写双份）。
func (c *PricingCalculator) selectPriceForTokens(
	ctx context.Context,
	modelID uint,
	inputTokens, outputTokens int64,
	dims map[string]string,
) (*TierPriceSelection, error) {
	result := &TierPriceSelection{MatchedTierIdx: -1}

	// 预先加载 AIModel（用于平台成本侧阶梯查找；P2 修复）
	var aiModel model.AIModel
	aiLoadErr := c.db.WithContext(ctx).Select("id, price_tiers, input_cost_rmb, output_cost_rmb").First(&aiModel, modelID).Error
	if aiLoadErr != nil && aiLoadErr != gorm.ErrRecordNotFound {
		logger.L.Debug("selectPriceForTokens: load ai_model failed",
			zap.Uint("model_id", modelID), zap.Error(aiLoadErr))
	}

	// M1 第 0 步（2026-04-28）：PriceMatrix 优先命中
	// 转换 dims (map[string]string) 为 map[string]interface{}（PriceMatrix 接口需要）
	if len(dims) > 0 {
		dimMap := make(map[string]interface{}, len(dims))
		for k, v := range dims {
			dimMap[k] = v
		}
		if cell, hit := MatchCellByModelID(ctx, c.db, modelID, dimMap); hit {
			// 仅当 cell 包含真实售价时才作为计费源（避免 unsupported cell 误命中）
			if matrixSel := buildTierSelectionFromMatrixCell(cell, &aiModel); matrixSel != nil {
				return matrixSel, nil
			}
		}
	}

	// 1. 先查 ModelPricing.PriceTiers（售价阶梯）
	mp, mpErr := c.getPlatformPrice(ctx, modelID)
	if mpErr == nil && mp != nil {
		tierData := parseTiersJSON(mp.PriceTiers)
		if tierData != nil && len(tierData.Tiers) > 0 {
			// S2 第 1 步：DimValues 严格匹配（仅当 dims 非空时）
			var idx int
			var tier *model.PriceTier
			if len(dims) > 0 {
				if i, t := model.SelectTierByDims(tierData.Tiers, dims); t != nil {
					idx, tier = i, t
				}
			}
			// 第 2 步：DimValues 未命中（或未提供）→ 走 token 区间 + 最大档兜底
			if tier == nil {
				idx, tier, _ = model.SelectTierOrLargest(tierData.Tiers, inputTokens, outputTokens)
			}
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
					// 思考模式输出价：优先阶梯售价覆盖 → 模型级售价 → 阶梯成本价
					thinkingRMB := mp.OutputPriceThinkingRMB
					if tier.SellingOutputThinkingPrice != nil {
						thinkingRMB = *tier.SellingOutputThinkingPrice
						result.SellingOverrideThinking = true
					} else if thinkingRMB == 0 && tier.OutputPriceThinking > 0 {
						thinkingRMB = tier.OutputPriceThinking
					}
					if thinkingRMB > 0 {
						result.OutputPriceRMBThinking = thinkingRMB
						result.OutputPricePerMillionThinking = credits.RMBToCredits(thinkingRMB)
					}
					result.MatchedTier = tier.Name
					result.MatchedTierIdx = idx
					result.FromTier = true
					result.SellingOverride = true
					c.fillPlatformTierCost(result, &aiModel, mp, inputTokens, outputTokens)
					return result, nil
				}
				// 未覆盖售价 → 使用 tier 的 Input/OutputPrice 作为售价基础
				if tier.InputPrice > 0 || tier.OutputPrice > 0 {
					result.InputPriceRMB = tier.InputPrice
					result.OutputPriceRMB = tier.OutputPrice
					result.InputPricePerMillion = credits.RMBToCredits(tier.InputPrice)
					result.OutputPricePerMillion = credits.RMBToCredits(tier.OutputPrice)
					if tier.OutputPriceThinking > 0 {
						result.OutputPriceRMBThinking = tier.OutputPriceThinking
						result.OutputPricePerMillionThinking = credits.RMBToCredits(tier.OutputPriceThinking)
					}
					result.MatchedTier = tier.Name
					result.MatchedTierIdx = idx
					result.FromTier = true
					c.fillPlatformTierCost(result, &aiModel, mp, inputTokens, outputTokens)
					return result, nil
				}
			}
		}
	}

	// 2. 再查 AIModel.PriceTiers（成本阶梯）
	if aiLoadErr == gorm.ErrRecordNotFound {
		return result, nil
	}

	tierData := parseTiersJSON(aiModel.PriceTiers)
	if tierData != nil && len(tierData.Tiers) > 0 {
		// S2 第 1 步（成本阶梯路径同样支持 DimValues 严格匹配）
		var idx int
		var tier *model.PriceTier
		if len(dims) > 0 {
			if i, t := model.SelectTierByDims(tierData.Tiers, dims); t != nil {
				idx, tier = i, t
			}
		}
		if tier == nil {
			idx, tier, _ = model.SelectTierOrLargest(tierData.Tiers, inputTokens, outputTokens)
		}
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
			// 此路径下 tier 自身就是成本价，直接填入
			result.PlatformInputPriceRMB = tier.InputPrice
			result.PlatformOutputPriceRMB = tier.OutputPrice
			result.PlatformInputPricePerMillion = credits.RMBToCredits(tier.InputPrice)
			result.PlatformOutputPricePerMillion = credits.RMBToCredits(tier.OutputPrice)
			return result, nil
		}
	}

	// 未命中 → FromTier=false，由调用方回退
	return result, nil
}

// fillPlatformTierCost 在售价阶梯命中后，尝试同步填充成本侧阶梯单价
//
// 逻辑：
//  1. 优先：在 AIModel.PriceTiers（成本阶梯）中按相同 token 范围查找命中阶梯
//  2. 兜底：按 (sellTierUnit / sellBaseUnit) 比例反推 (costBaseUnit × ratio)
//
// 找不到时保持 0，调用方应退回到 platformPrice 基础成本。
func (c *PricingCalculator) fillPlatformTierCost(
	result *TierPriceSelection,
	aiModel *model.AIModel,
	mp *model.ModelPricing,
	inputTokens, outputTokens int64,
) {
	if aiModel == nil {
		return
	}
	// 1. AIModel 成本阶梯查同范围阶梯
	aiTierData := parseTiersJSON(aiModel.PriceTiers)
	if aiTierData != nil && len(aiTierData.Tiers) > 0 {
		_, aiTier, _ := model.SelectTierOrLargest(aiTierData.Tiers, inputTokens, outputTokens)
		if aiTier != nil && (aiTier.InputPrice > 0 || aiTier.OutputPrice > 0) {
			result.PlatformInputPriceRMB = aiTier.InputPrice
			result.PlatformOutputPriceRMB = aiTier.OutputPrice
			result.PlatformInputPricePerMillion = credits.RMBToCredits(aiTier.InputPrice)
			result.PlatformOutputPricePerMillion = credits.RMBToCredits(aiTier.OutputPrice)
			return
		}
	}
	// 2. 比例反推
	if mp != nil {
		if mp.InputPriceRMB > 0 && aiModel.InputCostRMB > 0 && result.InputPriceRMB > 0 {
			scale := result.InputPriceRMB / mp.InputPriceRMB
			costRMB := aiModel.InputCostRMB * scale
			result.PlatformInputPriceRMB = costRMB
			result.PlatformInputPricePerMillion = credits.RMBToCredits(costRMB)
		}
		if mp.OutputPriceRMB > 0 && aiModel.OutputCostRMB > 0 && result.OutputPriceRMB > 0 {
			scale := result.OutputPriceRMB / mp.OutputPriceRMB
			costRMB := aiModel.OutputCostRMB * scale
			result.PlatformOutputPriceRMB = costRMB
			result.PlatformOutputPricePerMillion = credits.RMBToCredits(costRMB)
		}
	}
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
