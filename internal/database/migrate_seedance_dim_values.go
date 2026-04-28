package database

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
	"tokenhub-server/internal/pkg/logger"
)

// =============================================================================
// Seedance 数据迁移：magic-InputMin → DimValues (S4, 2026-04-28)
//
// 目标：把 DB 中现有 Seedance 模型的 PriceTiers 升级到显式 DimValues 形态，
// 让 selectPriceForTokens（S2 升级后）能按维度精确选档，从此不再依赖 token-scale
// 黑魔法。
//
// 数据来源（火山引擎官网 2026-04 定价）：
//
//	Seedance 2.0 系列（4 档：resolution × input_has_video）
//	  720p · 不含视频:  46 ¥/M
//	  720p · 含视频:    28 ¥/M
//	  1080p · 不含视频: 51 ¥/M
//	  1080p · 含视频:   31 ¥/M
//
//	Seedance 2.0 Fast（2 档：input_has_video，无 1080p）
//	  不含视频: 37 ¥/M
//	  含视频:   22 ¥/M
//
//	Seedance 1.5 Pro（4 档：inference_mode × audio_mode + Draft 折算）
//	  在线 + 有声: 16 ¥/M
//	  在线 + 无声: 8 ¥/M
//	  离线 + 有声: 8 ¥/M
//	  离线 + 无声: 4 ¥/M
//
//	Seedance 1.0 Pro / Lite / Fast（2 档：inference_mode）
//	  Pro:       online=15  / offline=7.5
//	  Pro Fast:  online=4.2 / offline=2.1
//	  Lite:      online=10  / offline=5.0
//
// 模型映射策略：
//
//	DB 中存在多种 model_name 命名（火山带日期版本号 / TalkingData 拆 720p/1080p）。
//	本迁移按 model_name 前缀分流：
//
//	  doubao-seedance-2.0-720p          → 单档（720p）+ 含/不含视频 2 档 DimValues
//	  doubao-seedance-2.0-1080p         → 单档（1080p）+ 含/不含视频 2 档 DimValues
//	  doubao-seedance-2.0 / 2-0-260128  → 完整 4 档（resolution × input_has_video）
//	  doubao-seedance-2.0-fast / 2-0-fast-260128 → 2 档（input_has_video）
//	  doubao-seedance-1.5-pro / 1-5-pro-251215   → 4 档 + Draft VideoPricingConfig
//	  doubao-seedance-1.0-pro / 1-0-pro-250528   → 2 档 (inference_mode)
//	  doubao-seedance-1.0-pro-fast / 1-0-pro-fast-251015 → 2 档
//	  doubao-seedance-1.0-lite / 1-0-lite-i2v-250428 / -t2v-250428 → 2 档
//
// 幂等性：
//
//	若 DB 中 PriceTiers 已含 DimValues（说明本迁移已运行）→ 跳过该 model
//	若 PriceTiers 为空或不含 DimValues → 重写为新形态
//	**ai_models.output_cost_rmb 与 model_pricings.output_price_rmb 同步对齐到 base 价**
//
// 触发：bootstrap.RunDataMigrations，管理员通过 /admin/system/migrate 触发
// =============================================================================

// seedanceDimMigrationRule 单个 Seedance 模型族的迁移规则
type seedanceDimMigrationRule struct {
	// NamePrefixes DB 中可能匹配的 model_name 前缀（小写）
	NamePrefixes []string
	// Tiers 该族应有的 DimValues 阶梯（成本侧 InputPrice/OutputPrice）
	Tiers func() []model.PriceTier
	// BaseOutputCost 兜底基础价（顶层 output_cost_rmb 同步值）
	// 当请求 dims 缺失或不命中任何 tier 时，CalculateCost 走旧路径用此价
	BaseOutputCost float64
	// Notes 仅供日志说明
	Notes string
}

// seedanceDimMigrationRules 所有 Seedance 模型族的完整迁移规则
//
// 注意：BaseOutputCost 的选择策略 ——
//   - 拆分模型（-720p/-1080p）：用其唯一分辨率档的"不含视频"价（最贵档，避免 fallback 漏扣）
//   - 单一模型（2.0 / 1.5-pro / 1.0-pro 等）：用最贵档（最高分辨率 + 不含视频，或 online + 有声）
func seedanceDimMigrationRules() []seedanceDimMigrationRule {
	return []seedanceDimMigrationRule{
		// ── Seedance 2.0 拆分模型（TalkingData 路径）────────────────────────
		{
			NamePrefixes: []string{"doubao-seedance-2.0-720p"},
			Tiers: func() []model.PriceTier {
				return []model.PriceTier{
					{
						Name:        "480p/720p · 不含视频",
						DimValues:   map[string]string{model.DimKeyResolution: "720p", model.DimKeyInputHasVideo: "false"},
						InputPrice:  46, OutputPrice: 46,
					},
					{
						Name:        "480p/720p · 含视频",
						DimValues:   map[string]string{model.DimKeyResolution: "720p", model.DimKeyInputHasVideo: "true"},
						InputPrice:  28, OutputPrice: 28,
					},
				}
			},
			BaseOutputCost: 46,
			Notes:          "TalkingData 720p 拆分，2 档（含/不含视频）",
		},
		{
			NamePrefixes: []string{"doubao-seedance-2.0-1080p"},
			Tiers: func() []model.PriceTier {
				return []model.PriceTier{
					{
						Name:        "1080p · 不含视频",
						DimValues:   map[string]string{model.DimKeyResolution: "1080p", model.DimKeyInputHasVideo: "false"},
						InputPrice:  51, OutputPrice: 51,
					},
					{
						Name:        "1080p · 含视频",
						DimValues:   map[string]string{model.DimKeyResolution: "1080p", model.DimKeyInputHasVideo: "true"},
						InputPrice:  31, OutputPrice: 31,
					},
				}
			},
			BaseOutputCost: 51,
			Notes:          "TalkingData 1080p 拆分，2 档（含/不含视频）",
		},
		// ── Seedance 2.0 完整版（Volcengine 单 model 路径）────────────────────
		{
			NamePrefixes: []string{"doubao-seedance-2.0-fast", "doubao-seedance-2-0-fast"},
			Tiers: func() []model.PriceTier {
				return []model.PriceTier{
					{
						Name:        "不含视频",
						DimValues:   map[string]string{model.DimKeyInputHasVideo: "false"},
						InputPrice:  37, OutputPrice: 37,
					},
					{
						Name:        "含视频",
						DimValues:   map[string]string{model.DimKeyInputHasVideo: "true"},
						InputPrice:  22, OutputPrice: 22,
					},
				}
			},
			BaseOutputCost: 37,
			Notes:          "Seedance 2.0 Fast，2 档（含/不含视频，无 1080p）",
		},
		// 注意：必须放在 fast 之后，因为 prefix "doubao-seedance-2.0" 会匹配 fast
		// 我们用更精确的前缀 "-260128" 区分纯 2.0
		{
			NamePrefixes: []string{"doubao-seedance-2-0-260128", "doubao-seedance-2.0-260128"},
			Tiers: func() []model.PriceTier {
				return []model.PriceTier{
					{
						Name:        "480p/720p · 不含视频",
						DimValues:   map[string]string{model.DimKeyResolution: "720p", model.DimKeyInputHasVideo: "false"},
						InputPrice:  46, OutputPrice: 46,
					},
					{
						Name:        "480p/720p · 含视频",
						DimValues:   map[string]string{model.DimKeyResolution: "720p", model.DimKeyInputHasVideo: "true"},
						InputPrice:  28, OutputPrice: 28,
					},
					{
						Name:        "1080p · 不含视频",
						DimValues:   map[string]string{model.DimKeyResolution: "1080p", model.DimKeyInputHasVideo: "false"},
						InputPrice:  51, OutputPrice: 51,
					},
					{
						Name:        "1080p · 含视频",
						DimValues:   map[string]string{model.DimKeyResolution: "1080p", model.DimKeyInputHasVideo: "true"},
						InputPrice:  31, OutputPrice: 31,
					},
				}
			},
			BaseOutputCost: 46, // 默认 720p 不含视频
			Notes:          "Volcengine 单 model 4 档（resolution × input_has_video）",
		},
		// ── Seedance 1.5 Pro（4 档 + Draft）────────────────────────────────
		{
			NamePrefixes: []string{"doubao-seedance-1.5-pro", "doubao-seedance-1-5-pro"},
			Tiers: func() []model.PriceTier {
				return []model.PriceTier{
					{
						Name:        "在线推理 · 有声视频",
						DimValues:   map[string]string{model.DimKeyInferenceMode: "online", model.DimKeyAudioMode: "true"},
						InputPrice:  16, OutputPrice: 16,
					},
					{
						Name:        "在线推理 · 无声视频",
						DimValues:   map[string]string{model.DimKeyInferenceMode: "online", model.DimKeyAudioMode: "false"},
						InputPrice:  8, OutputPrice: 8,
					},
					{
						Name:        "离线推理 · 有声视频",
						DimValues:   map[string]string{model.DimKeyInferenceMode: "offline", model.DimKeyAudioMode: "true"},
						InputPrice:  8, OutputPrice: 8,
					},
					{
						Name:        "离线推理 · 无声视频",
						DimValues:   map[string]string{model.DimKeyInferenceMode: "offline", model.DimKeyAudioMode: "false"},
						InputPrice:  4, OutputPrice: 4,
					},
				}
			},
			BaseOutputCost: 16, // 默认 online + audio
			Notes:          "Seedance 1.5 Pro，4 档（inference_mode × audio_mode）+ Draft 折算保留在 VideoPricingConfig",
		},
		// ── Seedance 1.0 系列 ───────────────────────────────────────────────
		// 注：必须放在 fast 之后；前缀更长更精确
		{
			NamePrefixes: []string{"doubao-seedance-1.0-pro-fast", "doubao-seedance-1-0-pro-fast"},
			Tiers: func() []model.PriceTier {
				return seedanceOnlineOfflineDimTiers(4.2)
			},
			BaseOutputCost: 4.2,
			Notes:          "Seedance 1.0 Pro Fast，2 档",
		},
		{
			NamePrefixes: []string{"doubao-seedance-1.0-pro", "doubao-seedance-1-0-pro"},
			Tiers: func() []model.PriceTier {
				return seedanceOnlineOfflineDimTiers(15)
			},
			BaseOutputCost: 15,
			Notes:          "Seedance 1.0 Pro，2 档",
		},
		{
			NamePrefixes: []string{"doubao-seedance-1.0-lite", "doubao-seedance-1-0-lite"},
			Tiers: func() []model.PriceTier {
				return seedanceOnlineOfflineDimTiers(10)
			},
			BaseOutputCost: 10,
			Notes:          "Seedance 1.0 Lite，2 档",
		},
	}
}

// seedanceOnlineOfflineDimTiers 构造 1.0 系列 2 档（online/offline）
//
// 离线价 = 在线价 × 0.5（火山引擎统一折扣率）
func seedanceOnlineOfflineDimTiers(onlinePrice float64) []model.PriceTier {
	return []model.PriceTier{
		{
			Name:        "在线推理",
			DimValues:   map[string]string{model.DimKeyInferenceMode: "online"},
			InputPrice:  onlinePrice, OutputPrice: onlinePrice,
		},
		{
			Name:        "离线推理",
			DimValues:   map[string]string{model.DimKeyInferenceMode: "offline"},
			InputPrice:  onlinePrice * 0.5, OutputPrice: onlinePrice * 0.5,
		},
	}
}

// matchSeedanceRule 根据 model_name 找到对应的迁移规则（最长前缀匹配）
func matchSeedanceRule(modelName string) *seedanceDimMigrationRule {
	rules := seedanceDimMigrationRules()
	lowerName := strings.ToLower(modelName)

	var best *seedanceDimMigrationRule
	bestLen := 0
	for i := range rules {
		for _, prefix := range rules[i].NamePrefixes {
			if strings.HasPrefix(lowerName, prefix) && len(prefix) > bestLen {
				best = &rules[i]
				bestLen = len(prefix)
			}
		}
	}
	return best
}

// RunSeedanceDimValuesMigration 把现有 Seedance 模型的 PriceTiers 重写为 DimValues 形态
//
// 这是 S4 + F1 + F2(A) 的实际数据落地步骤。执行后：
//   - 每个 Seedance 模型的 ai_models.price_tiers 含 N 档（每档带 DimValues）
//   - ai_models.output_cost_rmb 同步对齐到 BaseOutputCost
//   - model_pricings.price_tiers 同步迁移（售价 = 成本 × 比例）
//   - 旧 magic-InputMin tier 数据被全量替换
//
// 触发幂等：若 DB 中已有 DimValues 字段则跳过该模型。
func RunSeedanceDimValuesMigration(db *gorm.DB) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}
	if db == nil {
		log.Warn("seedance dim_values migration: db is nil, skip")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	type aiModelRow struct {
		ID            uint
		ModelName     string
		OutputCostRMB float64
		PriceTiers    []byte
	}

	var rows []aiModelRow
	if err := db.WithContext(ctx).
		Table("ai_models").
		Select("id, model_name, output_cost_rmb, price_tiers").
		Where("model_name LIKE ?", "doubao-seedance-%").
		Find(&rows).Error; err != nil {
		log.Error("seedance dim_values migration: query failed", zap.Error(err))
		return
	}

	if len(rows) == 0 {
		log.Info("seedance dim_values migration: no Seedance models found")
		return
	}

	migrated := 0
	skipped := 0
	unmatched := 0
	failed := 0

	for _, r := range rows {
		// 幂等检查：若现有 PriceTiers 已含 DimValues → 跳过
		if hasDimValuesAlready(r.PriceTiers) {
			skipped++
			log.Debug("seedance migration: already has DimValues, skip",
				zap.Uint("id", r.ID), zap.String("model_name", r.ModelName))
			continue
		}

		rule := matchSeedanceRule(r.ModelName)
		if rule == nil {
			unmatched++
			log.Warn("seedance migration: no matching rule",
				zap.String("model_name", r.ModelName))
			continue
		}

		// 1. 构造新 PriceTiers JSON（DimValues 形态）
		newTiers := rule.Tiers()
		newTiersData := model.PriceTiersData{
			Tiers:     newTiers,
			Currency:  "CNY",
			UpdatedAt: time.Now(),
		}
		newTiersJSON, err := json.Marshal(newTiersData)
		if err != nil {
			log.Error("seedance migration: marshal tiers failed",
				zap.String("model_name", r.ModelName), zap.Error(err))
			failed++
			continue
		}

		// 2. 同步更新 ai_models 表
		updates := map[string]any{
			"price_tiers":     newTiersJSON,
			"output_cost_rmb": rule.BaseOutputCost,
		}
		// 顶层 output_cost 仅在小于规则 base 时才升级（避免覆盖管理员配置的更高值）
		if r.OutputCostRMB > rule.BaseOutputCost {
			delete(updates, "output_cost_rmb")
		}
		if err := db.WithContext(ctx).
			Table("ai_models").
			Where("id = ?", r.ID).
			Updates(updates).Error; err != nil {
			log.Error("seedance migration: update ai_models failed",
				zap.Uint("id", r.ID), zap.String("model_name", r.ModelName), zap.Error(err))
			failed++
			continue
		}

		// 3. 同步更新 model_pricings 表（售价侧）
		if err := updateModelPricingDimTiers(ctx, db, r.ID, rule); err != nil {
			log.Warn("seedance migration: update model_pricings failed",
				zap.Uint("id", r.ID), zap.Error(err))
			// 不 fail，继续
		}

		log.Info("seedance migration: migrated",
			zap.String("model_name", r.ModelName),
			zap.Int("tier_count", len(newTiers)),
			zap.Float64("base_output_cost", rule.BaseOutputCost),
			zap.String("notes", rule.Notes))
		migrated++
	}

	log.Info("seedance dim_values migration: complete",
		zap.Int("migrated", migrated),
		zap.Int("skipped_already_dims", skipped),
		zap.Int("unmatched", unmatched),
		zap.Int("failed", failed),
		zap.Int("total_seedance_models", len(rows)))
}

// hasDimValuesAlready 判断 PriceTiers JSON 是否已包含 DimValues 字段（幂等检查）
func hasDimValuesAlready(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	var data model.PriceTiersData
	if err := json.Unmarshal(raw, &data); err != nil {
		return false
	}
	for _, t := range data.Tiers {
		if len(t.DimValues) > 0 {
			return true
		}
	}
	return false
}

// updateModelPricingDimTiers 把售价侧 PriceTiers 也迁移为 DimValues 形态
//
// 售价 = 成本 × 现有 (mp.OutputPriceRMB / aiModel.OutputCostRMB) 比例
// 若比例算不出（数据残缺）→ 售价 = 成本（管理员可后续调整）
func updateModelPricingDimTiers(ctx context.Context, db *gorm.DB, modelID uint, rule *seedanceDimMigrationRule) error {
	type mpRow struct {
		ID             uint
		PriceTiers     []byte
		OutputPriceRMB float64
		InputPriceRMB  float64
	}

	var mps []mpRow
	if err := db.WithContext(ctx).
		Table("model_pricings").
		Select("id, price_tiers, output_price_rmb, input_price_rmb").
		Where("model_id = ?", modelID).
		Find(&mps).Error; err != nil {
		return err
	}

	for _, mp := range mps {
		if hasDimValuesAlready(mp.PriceTiers) {
			continue
		}

		// 计算售价倍率：mp.OutputPriceRMB / rule.BaseOutputCost
		ratio := 1.0
		if rule.BaseOutputCost > 0 && mp.OutputPriceRMB > 0 {
			ratio = mp.OutputPriceRMB / rule.BaseOutputCost
		}

		// 构造新售价 tier（DimValues 同 cost tier，价格 × ratio）
		newTiers := rule.Tiers()
		for i := range newTiers {
			sellInput := newTiers[i].InputPrice * ratio
			sellOutput := newTiers[i].OutputPrice * ratio
			newTiers[i].SellingInputPrice = &sellInput
			newTiers[i].SellingOutputPrice = &sellOutput
		}
		newTiersData := model.PriceTiersData{
			Tiers:     newTiers,
			Currency:  "CNY",
			UpdatedAt: time.Now(),
		}
		newTiersJSON, err := json.Marshal(newTiersData)
		if err != nil {
			return fmt.Errorf("marshal mp tiers: %w", err)
		}

		updates := map[string]any{
			"price_tiers": newTiersJSON,
		}
		// 同时确保顶层 output_price_rmb 不为零（确保 fallback 路径有值）
		if mp.OutputPriceRMB == 0 {
			updates["output_price_rmb"] = rule.BaseOutputCost * ratio
			updates["output_price_per_token"] = credits.RMBToCredits(rule.BaseOutputCost * ratio)
		}

		if err := db.WithContext(ctx).
			Table("model_pricings").
			Where("id = ?", mp.ID).
			Updates(updates).Error; err != nil {
			return err
		}
	}
	return nil
}
