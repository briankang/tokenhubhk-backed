package database

import (
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/service/pricing"
)

// RunPriceMatrixMigration 把现有 ModelPricing 数据自动迁移为 PriceMatrix(v3 引入)。
//
// 幂等保证:
//   - 跳过已有 PriceMatrix 数据的记录(已迁移 / 管理员手编)
//   - 仅处理 PriceMatrix 为空但 ModelPricing 顶层有售价的记录
//   - 单次迁移,不阻塞业务,失败行单独记录日志
//
// 迁移规则:
//
//	无阶梯模型: 单 cell + 顶层 input/output 价
//	有 PriceTiers: N cell + dim_values = {context_tier: tier.Name}
//	单价模型(image/video/tts/asr/rerank): 单 cell + selling_per_unit
//
// 调用入口:bootstrap.RunSeeds(),仅在 ShouldRunMigrations()=true 时执行。
func RunPriceMatrixMigration(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("price matrix migration: db is nil")
	}
	log := logger.L.With(zap.String("migration", "price_matrix"))

	// 取所有需要迁移的 ModelPricing(price_matrix 为 NULL/empty)
	var rows []model.ModelPricing
	if err := db.Where("price_matrix IS NULL OR JSON_TYPE(price_matrix) = 'NULL' OR JSON_LENGTH(price_matrix) = 0").
		Find(&rows).Error; err != nil {
		// SQLite 不支持 JSON_TYPE,降级
		if err := db.Where("price_matrix IS NULL").Find(&rows).Error; err != nil {
			return fmt.Errorf("query model pricings: %w", err)
		}
	}
	if len(rows) == 0 {
		log.Info("price matrix migration: no rows to migrate")
		return nil
	}

	migrated, skipped, failed := 0, 0, 0
	for _, mp := range rows {
		if len(mp.PriceMatrix) > 0 && string(mp.PriceMatrix) != "null" {
			skipped++
			continue
		}
		var aiModel model.AIModel
		if err := db.Where("id = ?", mp.ModelID).First(&aiModel).Error; err != nil {
			log.Warn("price matrix migration: ai model not found", zap.Uint("model_id", mp.ModelID), zap.Error(err))
			failed++
			continue
		}
		matrix, err := convertModelPricingToMatrix(&aiModel, &mp)
		if err != nil || matrix == nil {
			log.Warn("price matrix migration: convert failed",
				zap.Uint("model_id", mp.ModelID),
				zap.String("model_name", aiModel.ModelName),
				zap.Error(err))
			failed++
			continue
		}
		raw, err := json.Marshal(matrix)
		if err != nil {
			log.Warn("price matrix migration: marshal failed", zap.Uint("model_id", mp.ModelID), zap.Error(err))
			failed++
			continue
		}
		if err := db.Model(&model.ModelPricing{}).
			Where("id = ?", mp.ID).
			Update("price_matrix", raw).Error; err != nil {
			log.Warn("price matrix migration: update failed", zap.Uint("model_id", mp.ModelID), zap.Error(err))
			failed++
			continue
		}
		migrated++
	}
	log.Info("price matrix migration: complete",
		zap.Int("migrated", migrated),
		zap.Int("skipped", skipped),
		zap.Int("failed", failed),
		zap.Int("total", len(rows)))
	return nil
}

// convertModelPricingToMatrix 将一条 ModelPricing 数据转成 PriceMatrix。
//
// 决策树:
//  1. 模型有 PriceTiers(平台售价已分阶梯) → 每档一 cell,dim_values = {context_tier: tier.Name}
//  2. AIModel 是 Image/Video/TTS/ASR/Rerank 单价类 → 单 cell,selling_per_unit
//  3. 其他(LLM/Embedding 单价) → 单 cell,selling_input + selling_output
func convertModelPricingToMatrix(aiModel *model.AIModel, mp *model.ModelPricing) (*model.PriceMatrix, error) {
	// 迁移路径只关心 dimensions 结构(不依赖预填价格,因为本函数自己构建 cells),
	// 这里传 nil 让 BuildDefaultMatrix 不做 selling 预填,等价于旧行为。
	dims := pricing.BuildDefaultMatrix(aiModel, nil).Dimensions
	cells := []model.PriceMatrixCell{}

	// 单价类模型(图片/视频/字符/调用)
	if isUnitPricingType(aiModel.ModelType, aiModel.PricingUnit) {
		// 若有阶梯,每档生成 cell
		if len(mp.PriceTiers) > 0 && string(mp.PriceTiers) != "null" {
			var tiersData model.PriceTiersData
			if err := json.Unmarshal(mp.PriceTiers, &tiersData); err == nil {
				for _, t := range tiersData.Tiers {
					sellingUnit := tierSellingPerUnit(&t)
					if sellingUnit == nil {
						continue
					}
					officialUnit := pickFloat(t.InputPrice, t.OutputPrice)
					cells = append(cells, model.PriceMatrixCell{
						DimValues:       buildTierDim(&t),
						OfficialPerUnit: officialUnit,
						SellingPerUnit:  sellingUnit,
						Supported:       true,
						Note:            t.Name,
					})
				}
			}
		}
		// 无阶梯或解析失败,单 cell 兜底
		if len(cells) == 0 {
			official := pickFloat(aiModel.InputCostRMB, aiModel.OutputCostRMB)
			selling := pickFloat(mp.InputPriceRMB, mp.OutputPriceRMB)
			cells = append(cells, model.PriceMatrixCell{
				DimValues:       map[string]interface{}{},
				OfficialPerUnit: official,
				SellingPerUnit:  selling,
				Supported:       true,
			})
		}
	} else {
		// Token 类(LLM/VLM/Embedding/Reasoning)
		if len(mp.PriceTiers) > 0 && string(mp.PriceTiers) != "null" {
			var tiersData model.PriceTiersData
			if err := json.Unmarshal(mp.PriceTiers, &tiersData); err == nil && len(tiersData.Tiers) > 0 {
				// 重建 dimensions:context_tier 维度的 values 由 tier.Name 集合填充
				ctxValues := make([]interface{}, 0, len(tiersData.Tiers))
				for _, t := range tiersData.Tiers {
					ctxValues = append(ctxValues, t.Name)
				}
				dims = []model.PriceDimension{
					{Key: "context_tier", Label: "上下文档位", Type: "select", Values: ctxValues, Help: "按输入 tokens 区间分阶梯"},
				}
				if aiModel.OutputCostThinkingRMB > 0 {
					dims = append(dims, model.PriceDimension{
						Key: "thinking_mode", Label: "思考模式", Type: "select",
						Values: []interface{}{"off", "on"}, Help: "是否启用深度思考",
					})
				}
				for _, t := range tiersData.Tiers {
					tierCopy := t
					sellingIn := tierCopy.SellingInputPrice
					sellingOut := tierCopy.SellingOutputPrice
					if sellingIn == nil {
						sellingIn = ptrFloatVal(tierCopy.InputPrice)
					}
					if sellingOut == nil {
						sellingOut = ptrFloatVal(tierCopy.OutputPrice)
					}
					cells = append(cells, model.PriceMatrixCell{
						DimValues:      map[string]interface{}{"context_tier": tierCopy.Name},
						OfficialInput:  ptrFloatVal(tierCopy.InputPrice),
						OfficialOutput: ptrFloatVal(tierCopy.OutputPrice),
						SellingInput:   sellingIn,
						SellingOutput:  sellingOut,
						Supported:      true,
						Note:           tierCopy.Name,
					})
					// thinking 子 cell(若有)
					if aiModel.OutputCostThinkingRMB > 0 && tierCopy.OutputPriceThinking > 0 {
						sellingThinking := tierCopy.SellingOutputThinkingPrice
						if sellingThinking == nil {
							sellingThinking = ptrFloatVal(tierCopy.OutputPriceThinking)
						}
						cells = append(cells, model.PriceMatrixCell{
							DimValues: map[string]interface{}{
								"context_tier":  tierCopy.Name,
								"thinking_mode": "on",
							},
							OfficialInput:  ptrFloatVal(tierCopy.InputPrice),
							OfficialOutput: ptrFloatVal(tierCopy.OutputPriceThinking),
							SellingInput:   sellingIn,
							SellingOutput:  sellingThinking,
							Supported:      true,
							Note:           fmt.Sprintf("%s · thinking", tierCopy.Name),
						})
					}
				}
			}
		}
		if len(cells) == 0 {
			cells = append(cells, model.PriceMatrixCell{
				DimValues:      map[string]interface{}{},
				OfficialInput:  ptrFloatVal(aiModel.InputCostRMB),
				OfficialOutput: ptrFloatVal(aiModel.OutputCostRMB),
				SellingInput:   ptrFloatVal(mp.InputPriceRMB),
				SellingOutput:  ptrFloatVal(mp.OutputPriceRMB),
				Supported:      true,
			})
			// thinking 单独 cell
			if aiModel.OutputCostThinkingRMB > 0 && mp.OutputPriceThinkingRMB > 0 {
				cells = append(cells, model.PriceMatrixCell{
					DimValues:      map[string]interface{}{"thinking_mode": "on"},
					OfficialInput:  ptrFloatVal(aiModel.InputCostRMB),
					OfficialOutput: ptrFloatVal(aiModel.OutputCostThinkingRMB),
					SellingInput:   ptrFloatVal(mp.InputPriceRMB),
					SellingOutput:  ptrFloatVal(mp.OutputPriceThinkingRMB),
					Supported:      true,
					Note:           "thinking",
				})
				if dims == nil || !dimensionsContainKey(dims, "thinking_mode") {
					dims = append(dims, model.PriceDimension{
						Key: "thinking_mode", Label: "思考模式", Type: "select",
						Values: []interface{}{"off", "on"},
					})
				}
			}
		}
	}

	if dims == nil {
		dims = []model.PriceDimension{}
	}

	pm := &model.PriceMatrix{
		SchemaVersion:        1,
		Currency:             "RMB",
		Unit:                 aiModel.PricingUnit,
		Dimensions:           dims,
		Cells:                cells,
		GlobalDiscountRate:   mp.GlobalDiscountRate,
		PricedAtAt:           mp.PricedAtAt,
		PricedAtExchangeRate: mp.PricedAtExchangeRate,
		PricedAtRateSource:   mp.PricedAtRateSource,
	}
	return pm, nil
}

// isUnitPricingType 判断模型是否为单价类(per_image/per_second/per_call/per_minute/per_hour/per_*characters)。
func isUnitPricingType(modelType, pricingUnit string) bool {
	switch pricingUnit {
	case model.UnitPerImage, model.UnitPerSecond, model.UnitPerMinute, model.UnitPerHour,
		model.UnitPer10kCharacters, model.UnitPerMillionCharacters, model.UnitPerKChars,
		model.UnitPerCall:
		return true
	}
	switch strings.ToLower(modelType) {
	case "imagegeneration", "videogeneration", "tts", "asr", "rerank":
		return true
	}
	return false
}

func tierSellingPerUnit(t *model.PriceTier) *float64 {
	if t.SellingOutputPrice != nil && *t.SellingOutputPrice > 0 {
		v := *t.SellingOutputPrice
		return &v
	}
	if t.SellingInputPrice != nil && *t.SellingInputPrice > 0 {
		v := *t.SellingInputPrice
		return &v
	}
	if t.OutputPrice > 0 {
		v := t.OutputPrice
		return &v
	}
	if t.InputPrice > 0 {
		v := t.InputPrice
		return &v
	}
	return nil
}

func buildTierDim(t *model.PriceTier) map[string]interface{} {
	m := map[string]interface{}{}
	if t.Variant != "" {
		m["variant"] = t.Variant
	}
	if t.Name != "" {
		m["context_tier"] = t.Name
	}
	return m
}

func pickFloat(values ...float64) *float64 {
	for _, v := range values {
		if v > 0 {
			out := v
			return &out
		}
	}
	return nil
}

func ptrFloatVal(v float64) *float64 {
	if v == 0 {
		return nil
	}
	out := v
	return &out
}

func dimensionsContainKey(dims []model.PriceDimension, key string) bool {
	for _, d := range dims {
		if d.Key == key {
			return true
		}
	}
	return false
}
