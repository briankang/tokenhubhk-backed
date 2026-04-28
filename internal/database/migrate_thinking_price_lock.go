package database

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
	"tokenhub-server/internal/pkg/logger"
)

// =============================================================================
// 思考模式价格锁定迁移 (F4)
//
// 背景：
//   阿里云、火山、百度、腾讯、DeepSeek、Moonshot、Anthropic 等多家供应商的"思考模型"
//   都支持 reasoning_content / thinking 链路。但只有少数模型（如 qwen3.5-flash 0.8/2/8、
//   qvq-72b-preview 等）思考价 ≠ 非思考价。**绝大多数思考模型的两价相同**。
//
//   现状（DB 调查 2026-04-28）：
//   - 22+ 个活跃 supports_thinking=true 的模型，全部 output_cost_thinking_rmb=0
//   - alibaba_thinking_supplementary 中的 3 个硬编码 model_name 在 DB 中**完全不匹配**
//   - 运行时 thinkingMode 检测正常，但 extractThinkingPriceRMB 走 ratio 反推 → 与非思考价相同
//   - 实际漏扣风险：**对那些思考价 ≠ 非思考价的模型**，长期按非思考价计费
//
// 设计：
//   1. **Explicit Overrides Table**（thinkingPriceOverrides）：
//      - 列出已知思考价 ≠ 非思考价的模型（按真实 DB model_name 匹配，不依赖 supplier）
//      - 支持按阶梯名匹配（tier 级别覆盖）
//      - 未来新模型只需在此表中追加一行
//   2. **Default Lock Policy**：
//      - 对 features.supports_thinking=true 且不在 overrides 表中的模型：
//        thinking 价 = output 价（"显式锁定同价"，防 API 漂移导致静默漏扣）
//   3. **幂等性**：
//      - 仅更新 output_cost_thinking_rmb=0 OR NULL 的行（不覆盖管理员已配置）
//      - 同步更新 ModelPricing.OutputPriceThinkingRMB / OutputPriceThinkingPerToken
//      - PriceTiers JSON 内每个 tier 加 output_price_thinking 字段（若缺失）
//
// 触发：在 bootstrap.RunDataMigrations 中调用（管理员通过 /admin/system/migrate 触发，
//
//	或安装向导首次部署时执行；不在每次容器启动时运行）。幂等设计，重复执行安全。
//
// =============================================================================

// thinkingPriceOverride 单个模型的思考价覆盖
//
// 优先级（从高到低）：
//
//	1. ModelName + TierName 完全匹配 → 用该 tier 的 OutputThinkingRMB
//	2. ModelName 完全匹配（无 TierName）→ 用顶层 OutputThinkingRMB
//	3. 未匹配 → 走默认锁定（thinking == output）
type thinkingPriceOverride struct {
	ModelName             string  // 必填：DB 中 ai_models.model_name 完全匹配（大小写敏感）
	OutputThinkingRMB     float64 // 顶层思考价 (¥/M)，0=不覆盖顶层
	TierName              string  // 可选：仅对该 tier 生效
	TierOutputThinkingRMB float64 // 该 tier 的思考价
}

// thinkingPriceOverrides 思考价显式覆盖表
//
// 维护原则：
//   - 仅对官网文档明确"思考输出价 ≠ 非思考输出价"的模型添加
//   - 同一模型多档需要分别声明（每个 TierName 一行）
//   - 数据每季度对齐一次官网定价页
//   - 模型上线时也应在此处添加（即使同价，可显式锁定意图；不在此处则走默认）
//
// 当前已知差价模型（数据来源：阿里云 2026-04 文档页 https://help.aliyun.com/zh/model-studio/model-pricing）：
//
//	qwen3.5-flash:    0-128k 输入 0.8 / 输出非思考 2 / 输出思考 8
//	qwen-flash:       0-128k 输入 0.6 / 输出非思考 1.5 / 输出思考 6
//	qvq-72b-preview:  0-128k 输入 2 / 输出非思考 8 / 输出思考 24
//	qvq-max-preview:  0-128k 输入 4 / 输出非思考 12 / 输出思考 36
//	qwen3-omni-flash: 0-128k 输入 0.5 / 输出非思考 1.5 / 输出思考 6
//	qwen3-coder-flash:0-128k 输入 0.8 / 输出非思考 2 / 输出思考 8（待官方确认）
func thinkingPriceOverrides() []thinkingPriceOverride {
	return []thinkingPriceOverride{
		// ── 阿里云 Flash / Preview 系列：思考价显著高于非思考 ───────────────
		{ModelName: "qwen3.5-flash", TierName: "0-128k", TierOutputThinkingRMB: 8, OutputThinkingRMB: 8},
		{ModelName: "qwen3.5-flash-2026-02-23", TierName: "0-128k", TierOutputThinkingRMB: 8, OutputThinkingRMB: 8},
		{ModelName: "qwen3-flash", TierName: "0-128k", TierOutputThinkingRMB: 8, OutputThinkingRMB: 8},
		{ModelName: "qwen-flash", TierName: "0-128k", TierOutputThinkingRMB: 6, OutputThinkingRMB: 6},
		{ModelName: "qvq-72b-preview", TierName: "0-128k", TierOutputThinkingRMB: 24, OutputThinkingRMB: 24},
		{ModelName: "qvq-max-preview", TierName: "0-128k", TierOutputThinkingRMB: 36, OutputThinkingRMB: 36},
		{ModelName: "qwen3-omni-flash", TierName: "0-128k", TierOutputThinkingRMB: 6, OutputThinkingRMB: 6},

		// 同名带日期版本（独立 AIModel 记录，需要显式重复覆盖）
		{ModelName: "qwen3.5-flash-2026-02-23", TierName: "0-128k", TierOutputThinkingRMB: 8, OutputThinkingRMB: 8},

		// 注：未列入此表的所有 features.supports_thinking=true 模型 → 走默认 thinking == output 锁定
		// 包括：qwen3-max（同价）/ qwq-plus（思考链专用）/ qvq-max（同价）/ qwen3-coder-plus（同价）/
		//      deepseek-r1 / hunyuan-t1-latest / kimi-k2-thinking / ernie-x1-turbo /
		//      ernie-5.0-thinking-latest / claude-* / gpt-* / qwen3-vl-* 等
	}
}

// RunThinkingPriceLockMigration 思考模式价格锁定迁移
//
// 流程：
//
//	1. 读取所有 ai_models WHERE features.supports_thinking=true
//	2. 对每个模型：
//	   a. 在 overrides 表中查找匹配的覆盖规则
//	   b. 应用规则到顶层 output_cost_thinking_rmb
//	   c. 应用规则到 price_tiers JSON 中每个 tier 的 output_price_thinking
//	   d. 同步更新 model_pricings 表（OutputPriceThinkingRMB / OutputPriceThinkingPerToken）
//	   e. 同步更新 model_pricings.price_tiers JSON 中的 selling_output_thinking_price
//	3. 仅更新 output_cost_thinking_rmb=0 或 NULL 的行（不覆盖管理员配置）
//
// 错误处理：单行失败不阻塞其他行，记 warning 继续
func RunThinkingPriceLockMigration(db *gorm.DB) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}
	if db == nil {
		log.Warn("thinking price lock: db is nil, skip")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	overrides := thinkingPriceOverrides()
	overrideByModel := make(map[string][]thinkingPriceOverride, len(overrides))
	for _, o := range overrides {
		overrideByModel[o.ModelName] = append(overrideByModel[o.ModelName], o)
	}

	type aiModelRow struct {
		ID                     uint
		ModelName              string
		OutputCostRMB          float64
		OutputCostThinkingRMB  float64
		PriceTiers             []byte
		Features               []byte
	}

	var rows []aiModelRow
	// 注：MySQL JSON_EXTRACT 返回 JSON true（非整数 1），GORM 占位符 ? 绑定 Go bool=1
	// 不会命中。这里直接用裸 SQL TRUE 字面量。
	if err := db.WithContext(ctx).
		Table("ai_models").
		Select("id, model_name, output_cost_rmb, output_cost_thinking_rmb, price_tiers, features").
		Where("JSON_EXTRACT(features, '$.supports_thinking') = TRUE").
		Where("is_active = 1").
		Where("output_cost_rmb > 0").
		Find(&rows).Error; err != nil {
		log.Error("thinking price lock: query ai_models failed", zap.Error(err))
		return
	}

	if len(rows) == 0 {
		log.Info("thinking price lock: no supports_thinking models found, skip")
		return
	}

	updatedTopLevel := 0
	updatedTiers := 0
	updatedPricings := 0
	skipped := 0
	failed := 0

	for _, r := range rows {
		// 已配置 thinking 价 → 跳过（不覆盖管理员）
		if r.OutputCostThinkingRMB > 0 {
			skipped++
			continue
		}

		// 1. 决定顶层 thinking 价
		ovs := overrideByModel[r.ModelName]
		var topThinkingRMB float64
		hasOverride := false
		for _, ov := range ovs {
			if ov.TierName == "" && ov.OutputThinkingRMB > 0 {
				topThinkingRMB = ov.OutputThinkingRMB
				hasOverride = true
				break
			}
		}
		// 顶层 fallback：覆盖表中第一条 tier 级 OutputThinkingRMB（向上传播），或 output_cost
		if !hasOverride && len(ovs) > 0 && ovs[0].OutputThinkingRMB > 0 {
			topThinkingRMB = ovs[0].OutputThinkingRMB
			hasOverride = true
		}
		// 默认锁定：thinking == output
		if !hasOverride {
			topThinkingRMB = r.OutputCostRMB
		}

		// 2. 更新 ai_models 顶层
		if err := db.WithContext(ctx).
			Table("ai_models").
			Where("id = ?", r.ID).
			Update("output_cost_thinking_rmb", topThinkingRMB).Error; err != nil {
			log.Warn("thinking price lock: update ai_models top-level failed",
				zap.Uint("model_id", r.ID), zap.String("model_name", r.ModelName), zap.Error(err))
			failed++
			continue
		}
		updatedTopLevel++

		// 3. 更新 ai_models.price_tiers JSON（如果有）
		if len(r.PriceTiers) > 0 {
			updatedTierJSON, n, perr := patchTiersWithThinkingPrice(r.PriceTiers, ovs, r.OutputCostRMB)
			if perr == nil && n > 0 {
				if err := db.WithContext(ctx).
					Table("ai_models").
					Where("id = ?", r.ID).
					Update("price_tiers", updatedTierJSON).Error; err != nil {
					log.Warn("thinking price lock: update ai_models.price_tiers failed",
						zap.Uint("model_id", r.ID), zap.String("model_name", r.ModelName), zap.Error(err))
				} else {
					updatedTiers += n
				}
			}
		}

		// 4. 更新 model_pricings 表（售价侧）
		updatedMP, err := patchModelPricingThinking(ctx, db, r.ID, topThinkingRMB, ovs, r.OutputCostRMB)
		if err != nil {
			log.Warn("thinking price lock: update model_pricings failed",
				zap.Uint("model_id", r.ID), zap.Error(err))
		}
		updatedPricings += updatedMP
	}

	log.Info("thinking price lock migration: complete",
		zap.Int("scanned", len(rows)),
		zap.Int("updated_top_level", updatedTopLevel),
		zap.Int("updated_tiers", updatedTiers),
		zap.Int("updated_model_pricings", updatedPricings),
		zap.Int("skipped_already_configured", skipped),
		zap.Int("failed", failed),
		zap.Int("explicit_overrides_loaded", len(overrides)))
}

// patchTiersWithThinkingPrice 把思考价写入 PriceTiers JSON（成本侧）
//
// 策略：
//
//	1. 查找匹配的 override（按 tier name）
//	2. override 命中 → 用 override 的 TierOutputThinkingRMB
//	3. override 未命中 → output_price_thinking = output_price（同价锁定）
//	4. 仅当 tier 中 output_price_thinking 缺失或为 0 时才写入
//
// 返回更新后的 JSON、被修改的 tier 数量、错误
func patchTiersWithThinkingPrice(raw []byte, overrides []thinkingPriceOverride, fallbackOutput float64) ([]byte, int, error) {
	if len(raw) == 0 {
		return nil, 0, nil
	}
	var data model.PriceTiersData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, 0, fmt.Errorf("unmarshal tiers: %w", err)
	}
	if len(data.Tiers) == 0 {
		return nil, 0, nil
	}

	tierOverrideByName := make(map[string]float64, len(overrides))
	for _, ov := range overrides {
		if ov.TierName != "" && ov.TierOutputThinkingRMB > 0 {
			tierOverrideByName[ov.TierName] = ov.TierOutputThinkingRMB
		}
	}

	updatedCount := 0
	for i := range data.Tiers {
		t := &data.Tiers[i]
		// 已设置 → 跳过
		if t.OutputPriceThinking > 0 {
			continue
		}
		var thinkingPrice float64
		if v, ok := tierOverrideByName[t.Name]; ok {
			thinkingPrice = v
		} else {
			// 默认锁定：thinking == output_price（该 tier 的输出价）
			if t.OutputPrice > 0 {
				thinkingPrice = t.OutputPrice
			} else {
				thinkingPrice = fallbackOutput
			}
		}
		t.OutputPriceThinking = thinkingPrice
		updatedCount++
	}

	if updatedCount == 0 {
		return nil, 0, nil
	}

	out, err := json.Marshal(data)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal tiers: %w", err)
	}
	return out, updatedCount, nil
}

// patchModelPricingThinking 同步更新 model_pricings 表的思考价字段
//
// 流程：
//  1. 找到所有 model_id=X 的 model_pricings 行
//  2. 仅更新 output_price_thinking_rmb=0 或 NULL 的行（不覆盖管理员）
//  3. 顶层：output_price_thinking_rmb = topThinkingRMB
//  4. PriceTiers JSON 中每 tier：selling_output_thinking_price 同步（对齐成本侧 output_price_thinking）
func patchModelPricingThinking(ctx context.Context, db *gorm.DB, modelID uint, topThinkingRMB float64, overrides []thinkingPriceOverride, fallbackOutput float64) (int, error) {
	type mpRow struct {
		ID                          uint
		PriceTiers                  []byte
		OutputPriceThinkingRMB      float64
		OutputPriceRMB              float64
	}

	var mps []mpRow
	if err := db.WithContext(ctx).
		Table("model_pricings").
		Select("id, price_tiers, output_price_thinking_rmb, output_price_rmb").
		Where("model_id = ?", modelID).
		Find(&mps).Error; err != nil {
		return 0, err
	}

	updated := 0
	for _, mp := range mps {
		updates := map[string]any{}

		// 1. 顶层售价侧 thinking 价
		if mp.OutputPriceThinkingRMB <= 0 {
			// 推算：如果 ModelPricing.OutputPriceRMB > 0 而 AIModel.OutputCostRMB > 0
			// 则 thinking 售价 = topThinkingRMB × (mp.OutputPriceRMB / fallbackOutput)
			// 否则直接用 topThinkingRMB（保底）
			thinkingSell := topThinkingRMB
			if fallbackOutput > 0 && mp.OutputPriceRMB > 0 {
				ratio := mp.OutputPriceRMB / fallbackOutput
				thinkingSell = topThinkingRMB * ratio
			}
			updates["output_price_thinking_rmb"] = thinkingSell
			updates["output_price_thinking_per_token"] = credits.RMBToCredits(thinkingSell)
		}

		// 2. PriceTiers JSON 中 selling_output_thinking_price 同步
		if len(mp.PriceTiers) > 0 {
			newTiers, n, perr := patchModelPricingTiers(mp.PriceTiers, overrides, fallbackOutput, mp.OutputPriceRMB)
			if perr == nil && n > 0 {
				updates["price_tiers"] = newTiers
			}
		}

		if len(updates) == 0 {
			continue
		}

		if err := db.WithContext(ctx).
			Table("model_pricings").
			Where("id = ?", mp.ID).
			Updates(updates).Error; err != nil {
			return updated, err
		}
		updated++
	}
	return updated, nil
}

// patchModelPricingTiers 把售价侧 thinking 阶梯价写入 ModelPricing.PriceTiers JSON
//
// 策略：
//
//	1. 查找 override 中匹配的 tier name
//	2. override 命中 → selling_output_thinking_price = override.TierOutputThinkingRMB × (mp.OutputPriceRMB / aiModel.OutputCostRMB)
//	   （把成本价的覆盖通过比例传播到售价）
//	3. override 未命中 → selling_output_thinking_price = tier.SellingOutputPrice（同价锁定）
//	4. 仅当 tier.SellingOutputThinkingPrice 为 nil 时才写入
func patchModelPricingTiers(raw []byte, overrides []thinkingPriceOverride, costOutput, sellOutput float64) ([]byte, int, error) {
	if len(raw) == 0 {
		return nil, 0, nil
	}
	var data model.PriceTiersData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, 0, fmt.Errorf("unmarshal selling tiers: %w", err)
	}
	if len(data.Tiers) == 0 {
		return nil, 0, nil
	}

	overrideByTier := make(map[string]float64, len(overrides))
	for _, ov := range overrides {
		if ov.TierName != "" && ov.TierOutputThinkingRMB > 0 {
			overrideByTier[ov.TierName] = ov.TierOutputThinkingRMB
		}
	}

	updatedCount := 0
	for i := range data.Tiers {
		t := &data.Tiers[i]
		if t.SellingOutputThinkingPrice != nil {
			continue
		}

		var sellThinking float64
		if costThink, ok := overrideByTier[t.Name]; ok {
			// 覆盖：把成本侧 thinking 通过比例传播到售价
			ratio := 1.0
			if costOutput > 0 && t.OutputPrice > 0 {
				ratio = t.OutputPrice / costOutput
			} else if t.SellingOutputPrice != nil && *t.SellingOutputPrice > 0 && t.OutputPrice > 0 {
				ratio = *t.SellingOutputPrice / t.OutputPrice
			}
			sellThinking = costThink * ratio
		} else {
			// 同价锁定：selling_output_thinking_price = selling_output_price
			if t.SellingOutputPrice != nil {
				sellThinking = *t.SellingOutputPrice
			} else if t.OutputPrice > 0 {
				sellThinking = t.OutputPrice
			} else if sellOutput > 0 {
				sellThinking = sellOutput
			}
		}

		if sellThinking > 0 {
			v := sellThinking
			t.SellingOutputThinkingPrice = &v
			updatedCount++
		}
	}

	if updatedCount == 0 {
		return nil, 0, nil
	}

	out, err := json.Marshal(data)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal selling tiers: %w", err)
	}
	return out, updatedCount, nil
}

