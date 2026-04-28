// Package pricing 中的 PriceMatrix 默认维度模板(v3 引入)。
//
// 每种模型类型有一组默认 Dimensions,管理员可在编辑器中调整。
// BuildDefaultMatrix(aiModel, mp) 在模型首次配置时自动生成对应模板,
// 并按已有的 AIModel 成本字段 + ModelPricing 售价字段为每个 cell 预填默认价格,
// 让管理员进入编辑器时不必从零录入。
package pricing

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"tokenhub-server/internal/model"
)

// defaultGlobalDiscountRate 当 ModelPricing 没有显式配置 GlobalDiscountRate 时,
// 用此默认折扣率从官网价推导售价默认值。
// 与前端 PriceMatrixEditor 的 applyGlobalDiscount 默认 0.85 保持一致。
const defaultGlobalDiscountRate = 0.85

// BuildDefaultMatrix 为给定 AIModel 生成默认 PriceMatrix。
//
// mp 可为 nil(模型尚未保存任何售价时)。当 mp 非 nil 时,会按其顶层
// InputPriceRMB / OutputPriceRMB / OutputPriceThinkingRMB / PriceTiers / GlobalDiscountRate
// 为各 cell 预填 Selling* 字段;Official* 字段始终从 aiModel 的成本字段填充。
//
// 预填策略允许管理员进入编辑器时已经看到合理的初始值,只需校对/微调。
func BuildDefaultMatrix(aiModel *model.AIModel, mp *model.ModelPricing) *model.PriceMatrix {
	if aiModel == nil {
		return nil
	}
	dims := defaultDimensionsFor(aiModel)
	cells := buildDefaultCells(aiModel, mp, dims)
	return &model.PriceMatrix{
		SchemaVersion: 1,
		Currency:      "RMB",
		Unit:          aiModel.PricingUnit,
		Dimensions:    dims,
		Cells:         cells,
	}
}

// defaultDimensionsFor 按 ModelType + ModelName pattern 选择默认维度集。
//
// 选择规则:
//   - LLM:基础情况无维度(单 cell);若 supports_thinking,加 thinking_mode 维度
//   - Embedding / Rerank:无维度(单 cell)
//   - Image:resolution + quality + mode
//   - Video:按模型名 pattern 区分 Seedance 2.0/1.5/1.0 三种
//   - TTS:voice_tier + stream_mode
//   - ASR:recognition_type + inference_mode
func defaultDimensionsFor(aiModel *model.AIModel) []model.PriceDimension {
	mt := aiModel.ModelType
	name := strings.ToLower(aiModel.ModelName)
	switch mt {
	case model.ModelTypeImageGeneration:
		return []model.PriceDimension{
			{Key: "resolution", Label: "分辨率", Type: "select", Values: []interface{}{"512x512", "1024x1024", "2048x2048"}, Help: "输出图片分辨率档位"},
			{Key: "quality", Label: "质量", Type: "select", Values: []interface{}{"standard", "hd"}, Help: "图片质量档"},
			{Key: "mode", Label: "模式", Type: "select", Values: []interface{}{"generation", "edit", "variation"}, Help: "生成/编辑/变体"},
		}
	case model.ModelTypeVideoGeneration:
		// Seedance 2.0(标准 / 2.0-fast)按 resolution × input_has_video × inference_mode
		if strings.Contains(name, "seedance-2.0") || strings.Contains(name, "seedance-2-0") {
			dims := []model.PriceDimension{
				{Key: "resolution", Label: "输出分辨率", Type: "select", Values: []interface{}{"480p", "720p", "1080p"}, Help: "Seedance 输出视频分辨率"},
				{Key: "input_has_video", Label: "输入是否含视频", Type: "boolean", Values: []interface{}{false, true}, Help: "输入含视频(图生视频)价格更低"},
				{Key: "inference_mode", Label: "推理模式", Type: "select", Values: []interface{}{"online", "offline"}, Help: "Seedance 2.0 暂不支持离线"},
			}
			// 2.0-fast 不支持 1080p
			if strings.Contains(name, "fast") {
				dims[0] = model.PriceDimension{Key: "resolution", Label: "输出分辨率", Type: "select", Values: []interface{}{"480p", "720p"}, Help: "Seedance 2.0-fast 不支持 1080p"}
			}
			return dims
		}
		// Seedance 1.5 按 audio_mode × inference_mode
		if strings.Contains(name, "seedance-1.5") || strings.Contains(name, "seedance-1-5") {
			return []model.PriceDimension{
				{Key: "audio_mode", Label: "输出有声/无声", Type: "select", Values: []interface{}{"audio", "silent"}, Help: "Seedance 1.5 按是否含音轨分价"},
				{Key: "inference_mode", Label: "推理模式", Type: "select", Values: []interface{}{"online", "offline"}, Help: "在线/离线推理"},
			}
		}
		// Seedance 1.0 系列(pro / pro-fast / lite)按 inference_mode
		if strings.Contains(name, "seedance-1.0") || strings.Contains(name, "seedance-1-0") {
			return []model.PriceDimension{
				{Key: "inference_mode", Label: "推理模式", Type: "select", Values: []interface{}{"online", "offline"}, Help: "在线/离线推理"},
			}
		}
		// 其他视频模型(Vidu/Kling/Veo 等)默认按 resolution
		return []model.PriceDimension{
			{Key: "resolution", Label: "输出分辨率", Type: "select", Values: []interface{}{"480p", "720p", "1080p"}, Help: "视频输出分辨率"},
		}
	case model.ModelTypeTTS:
		return []model.PriceDimension{
			{Key: "voice_tier", Label: "音色档", Type: "select", Values: []interface{}{"standard", "hd", "large_model"}, Help: "标准/高清/大模型音色"},
			{Key: "stream_mode", Label: "流式模式", Type: "select", Values: []interface{}{"non_stream", "stream"}, Help: "是否流式输出"},
		}
	case model.ModelTypeASR:
		return []model.PriceDimension{
			{Key: "recognition_type", Label: "识别类型", Type: "select", Values: []interface{}{"realtime", "file", "long"}, Help: "实时/录音文件/长语音"},
			{Key: "inference_mode", Label: "推理模式", Type: "select", Values: []interface{}{"online", "offline"}, Help: "在线/离线推理"},
		}
	case model.ModelTypeRerank, model.ModelTypeEmbedding:
		// 单 cell 即可,无维度
		return []model.PriceDimension{}
	default:
		// LLM / Vision / Reasoning:context_tier(若有阶梯)+ thinking_mode(若支持思考)
		dims := []model.PriceDimension{}
		if aiModel.OutputCostThinkingRMB > 0 {
			dims = append(dims, model.PriceDimension{
				Key: "thinking_mode", Label: "思考模式", Type: "select",
				Values: []interface{}{"off", "on"},
				Help:   "是否启用深度思考(思考模式输出价不同)",
			})
		}
		// 若 PriceTiers JSON 含具体阶梯,添加「输入 token 区间」维度,
		// 并把每条 tier 的可读标签作为 select Values。
		// 解析失败或 tiers 为空时,**完全不加该维度**,避免编辑器出现一个空 select 列。
		if tierValues := extractTierLabelsFromAIModel(aiModel); len(tierValues) > 0 {
			dims = append(dims, model.PriceDimension{
				Key: "context_tier", Label: "输入 token 区间", Type: "select",
				Values: tierValues,
				Help:   "按输入 tokens 长度划分阶梯,每段对应不同输入/输出价",
			})
		}
		return dims
	}
}

// extractTierLabelsFromAIModel 从 AIModel.PriceTiers JSON 中提取每条 tier 的人类可读标签,
// 用作 PriceMatrix 的 context_tier 维度 select 值。
//
// 标签格式优先级:
//  1. tier.Name 非空 → 直接用 Name(如 "tier1" / "tier2")
//  2. 否则按 InputMin/InputMax 范围生成,如 "0-32K tokens" / "32K-200K tokens" / "200K+ tokens"
//
// 失败 / 无 tiers 时返回 nil,调用方应丢弃该维度。
func extractTierLabelsFromAIModel(aiModel *model.AIModel) []interface{} {
	if aiModel == nil || len(aiModel.PriceTiers) == 0 || string(aiModel.PriceTiers) == "null" {
		return nil
	}
	var data model.PriceTiersData
	if err := json.Unmarshal(aiModel.PriceTiers, &data); err != nil || len(data.Tiers) == 0 {
		return nil
	}
	out := make([]interface{}, 0, len(data.Tiers))
	for i, t := range data.Tiers {
		out = append(out, tierDisplayLabel(t, i))
	}
	return out
}

// tierDisplayLabel 给一条 PriceTier 生成展示标签(用于矩阵 select Values)。
// idx 仅作为最终兜底标签 "阶梯 N",当 Name 为空且范围数据缺失时使用。
func tierDisplayLabel(t model.PriceTier, idx int) string {
	if strings.TrimSpace(t.Name) != "" {
		return t.Name
	}
	if t.InputMin == 0 && t.InputMax == nil {
		return fmt.Sprintf("阶梯 %d", idx+1)
	}
	minLabel := formatTokenSizeLabel(t.InputMin)
	if t.InputMax == nil {
		return fmt.Sprintf("%s+ tokens", minLabel)
	}
	maxLabel := formatTokenSizeLabel(*t.InputMax)
	if t.InputMin == 0 {
		return fmt.Sprintf("0-%s tokens", maxLabel)
	}
	return fmt.Sprintf("%s-%s tokens", minLabel, maxLabel)
}

// formatTokenSizeLabel 把 token 数字格式化为短标签(1024→"1K",32000→"32K",200000→"200K",1500000→"1.5M")。
func formatTokenSizeLabel(n int64) string {
	switch {
	case n >= 1_000_000:
		if n%1_000_000 == 0 {
			return fmt.Sprintf("%dM", n/1_000_000)
		}
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1000:
		if n%1000 == 0 {
			return fmt.Sprintf("%dK", n/1000)
		}
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// buildDefaultCells 为默认维度集生成 cells,并按已有数据为每个 cell 预填价格。
//
// 策略:
//   - 无维度时:返回 1 个 cell,空 dim_values
//   - 有维度时:返回所有维度值的笛卡尔积
//   - 每个 cell 通过 applyDefaultCellPrices 按 dim_values 命中规则预填价格
//
// 这样管理员打开编辑器时已经看到合理的初始值(成本价 + 推导售价),
// 只需校对/微调,而不必从零录入。
func buildDefaultCells(aiModel *model.AIModel, mp *model.ModelPricing, dims []model.PriceDimension) []model.PriceMatrixCell {
	// 解析 AIModel.PriceTiers 一次,在所有 cell 之间共用
	// 注:parseTiersJSON 来自 tier_calculator.go,空/null/解析失败均返回 nil
	aiTiers := parseTiersJSONSafe(aiModelTiersBytes(aiModel))
	mpTiers := parseTiersJSONSafe(modelPricingTiersBytes(mp))
	perUnit := isPerUnitMatrixUnit(aiModel)
	discountRate := effectiveDiscountRate(mp)

	if len(dims) == 0 {
		cell := model.PriceMatrixCell{
			DimValues: map[string]interface{}{},
			Supported: true,
		}
		applyDefaultCellPrices(&cell, aiModel, mp, aiTiers, mpTiers, perUnit, discountRate)
		return []model.PriceMatrixCell{cell}
	}
	// 笛卡尔积
	combos := cartesianValues(dims)
	cells := make([]model.PriceMatrixCell, 0, len(combos))
	for _, combo := range combos {
		cell := model.PriceMatrixCell{
			DimValues: combo,
			Supported: true,
		}
		applyDefaultCellPrices(&cell, aiModel, mp, aiTiers, mpTiers, perUnit, discountRate)
		cells = append(cells, cell)
	}
	return cells
}

// applyDefaultCellPrices 按 cell 的 dim_values 推断价格来源,填充 Official*/Selling* 字段。
//
// 维度命中规则:
//  1. 含 context_tier:按 dim_values["context_tier"] 标签反查 AIModel.PriceTiers 中匹配的 tier,
//     取其 InputPrice/OutputPrice 作 Official;同步从 mp.PriceTiers 取 selling override 或 fallback
//  2. 含 thinking_mode=on:输出价用 OutputCostThinkingRMB / OutputPriceThinkingRMB;off 用普通输出价
//  3. 单价类(per_unit)模型:统一用 aiModel.InputCostRMB / mp.InputPriceRMB(因为目前没有按维度
//     细分的成本/售价数据,先统一打底,管理员可后续按需差异化)
//  4. 无维度(单 cell):直接用 aiModel + mp 顶层字段
//
// Selling 字段优先级:
//   tier 级 Selling override → tier 级 cost 价(老路径) → 顶层 mp.*PriceRMB → Official × discountRate → nil
//
// 来源值为 0 时不写入(留 nil),让前端显示空白,提示管理员真没数据需要手动填。
func applyDefaultCellPrices(
	cell *model.PriceMatrixCell,
	aiModel *model.AIModel,
	mp *model.ModelPricing,
	aiTiers, mpTiers *model.PriceTiersData,
	perUnit bool,
	discountRate float64,
) {
	if cell == nil || aiModel == nil {
		return
	}

	// 解析 cell 维度命中
	thinkingOn := false
	if v, ok := cell.DimValues["thinking_mode"]; ok {
		thinkingOn = isThinkingOn(v)
	}
	tierLabel, _ := cell.DimValues["context_tier"].(string)

	var aiTier *model.PriceTier
	var mpTier *model.PriceTier
	if tierLabel != "" {
		aiTier = lookupTierByLabel(aiTiers, tierLabel)
		mpTier = lookupTierByLabel(mpTiers, tierLabel)
	}

	if perUnit {
		// 单价类:Official/Selling 都走 PerUnit 字段
		official := pickFirstPositive(
			func() float64 {
				if aiTier != nil {
					return pickFirstPositive(
						func() float64 { return aiTier.OutputPrice },
						func() float64 { return aiTier.InputPrice },
					)
				}
				return 0
			},
			func() float64 { return aiModel.OutputCostRMB },
			func() float64 { return aiModel.InputCostRMB },
		)
		if official > 0 {
			cell.OfficialPerUnit = ptrFloat64(roundTo6(official))
		}
		selling := resolveSellingPerUnit(mp, mpTier, official, discountRate)
		if selling > 0 {
			cell.SellingPerUnit = ptrFloat64(roundTo6(selling))
		}
		return
	}

	// 双价(input/output)类型
	officialIn := pickFirstPositive(
		func() float64 {
			if aiTier != nil {
				return aiTier.InputPrice
			}
			return 0
		},
		func() float64 { return aiModel.InputCostRMB },
	)
	officialOut := pickFirstPositive(
		func() float64 {
			if aiTier != nil {
				if thinkingOn && aiTier.OutputPriceThinking > 0 {
					return aiTier.OutputPriceThinking
				}
				return aiTier.OutputPrice
			}
			return 0
		},
		func() float64 {
			if thinkingOn && aiModel.OutputCostThinkingRMB > 0 {
				return aiModel.OutputCostThinkingRMB
			}
			return aiModel.OutputCostRMB
		},
	)

	if officialIn > 0 {
		cell.OfficialInput = ptrFloat64(roundTo6(officialIn))
	}
	if officialOut > 0 {
		cell.OfficialOutput = ptrFloat64(roundTo6(officialOut))
	}

	sellingIn := resolveSellingInput(mp, mpTier, officialIn, discountRate)
	sellingOut := resolveSellingOutput(mp, mpTier, officialOut, discountRate, thinkingOn)
	if sellingIn > 0 {
		cell.SellingInput = ptrFloat64(roundTo6(sellingIn))
	}
	if sellingOut > 0 {
		cell.SellingOutput = ptrFloat64(roundTo6(sellingOut))
	}
}

// resolveSellingInput 决定输入售价,按优先级返回首个 > 0 的值。
// 顺序:tier 级 SellingInputPrice override → tier 级 InputPrice(老路径) → mp.InputPriceRMB → official × rate
func resolveSellingInput(mp *model.ModelPricing, mpTier *model.PriceTier, official, rate float64) float64 {
	if mpTier != nil {
		if mpTier.SellingInputPrice != nil && *mpTier.SellingInputPrice > 0 {
			return *mpTier.SellingInputPrice
		}
		if mpTier.InputPrice > 0 {
			return mpTier.InputPrice
		}
	}
	if mp != nil && mp.InputPriceRMB > 0 {
		return mp.InputPriceRMB
	}
	if official > 0 && rate > 0 {
		return official * rate
	}
	return 0
}

// resolveSellingOutput 决定输出售价(区分 thinking 模式)。
func resolveSellingOutput(mp *model.ModelPricing, mpTier *model.PriceTier, official, rate float64, thinkingOn bool) float64 {
	if mpTier != nil {
		if thinkingOn && mpTier.SellingOutputThinkingPrice != nil && *mpTier.SellingOutputThinkingPrice > 0 {
			return *mpTier.SellingOutputThinkingPrice
		}
		if mpTier.SellingOutputPrice != nil && *mpTier.SellingOutputPrice > 0 {
			return *mpTier.SellingOutputPrice
		}
		if thinkingOn && mpTier.OutputPriceThinking > 0 {
			return mpTier.OutputPriceThinking
		}
		if mpTier.OutputPrice > 0 {
			return mpTier.OutputPrice
		}
	}
	if mp != nil {
		if thinkingOn && mp.OutputPriceThinkingRMB > 0 {
			return mp.OutputPriceThinkingRMB
		}
		if mp.OutputPriceRMB > 0 {
			return mp.OutputPriceRMB
		}
	}
	if official > 0 && rate > 0 {
		return official * rate
	}
	return 0
}

// resolveSellingPerUnit 单价类售价的回退优先级。
func resolveSellingPerUnit(mp *model.ModelPricing, mpTier *model.PriceTier, official, rate float64) float64 {
	if mpTier != nil {
		if mpTier.SellingOutputPrice != nil && *mpTier.SellingOutputPrice > 0 {
			return *mpTier.SellingOutputPrice
		}
		if mpTier.SellingInputPrice != nil && *mpTier.SellingInputPrice > 0 {
			return *mpTier.SellingInputPrice
		}
		if mpTier.OutputPrice > 0 {
			return mpTier.OutputPrice
		}
		if mpTier.InputPrice > 0 {
			return mpTier.InputPrice
		}
	}
	if mp != nil {
		if mp.OutputPriceRMB > 0 {
			return mp.OutputPriceRMB
		}
		if mp.InputPriceRMB > 0 {
			return mp.InputPriceRMB
		}
	}
	if official > 0 && rate > 0 {
		return official * rate
	}
	return 0
}

// effectiveDiscountRate 返回应用于 selling fallback 的折扣率。
// 优先用 ModelPricing.GlobalDiscountRate(>0),否则用系统默认 0.85。
func effectiveDiscountRate(mp *model.ModelPricing) float64 {
	if mp != nil && mp.GlobalDiscountRate > 0 {
		return mp.GlobalDiscountRate
	}
	return defaultGlobalDiscountRate
}

// lookupTierByLabel 按 tierDisplayLabel 反查 PriceTier。
// 标签来源与 extractTierLabelsFromAIModel 保持一致,确保 cell.dim_values["context_tier"] 能命中。
func lookupTierByLabel(data *model.PriceTiersData, label string) *model.PriceTier {
	if data == nil || label == "" {
		return nil
	}
	for i := range data.Tiers {
		if tierDisplayLabel(data.Tiers[i], i) == label {
			return &data.Tiers[i]
		}
	}
	return nil
}

// parseTiersJSONSafe 在 parseTiersJSON(tier_calculator.go) 之上加一层「空 tiers 数组也算 nil」的语义,
// 让下游 lookupTierByLabel 直接 nil-safe 短路,避免空 Tiers 走完循环。
func parseTiersJSONSafe(raw model.JSON) *model.PriceTiersData {
	data := parseTiersJSON(raw)
	if data == nil || len(data.Tiers) == 0 {
		return nil
	}
	return data
}

func aiModelTiersBytes(aiModel *model.AIModel) model.JSON {
	if aiModel == nil {
		return nil
	}
	return aiModel.PriceTiers
}

func modelPricingTiersBytes(mp *model.ModelPricing) model.JSON {
	if mp == nil {
		return nil
	}
	return mp.PriceTiers
}

// isPerUnitMatrixUnit 判断该模型在矩阵中应使用 *PerUnit 字段还是 Input/Output 双列。
//
// 规则与 defaultDimensionsFor 的模型类型分支保持一致:
//   - ImageGeneration / VideoGeneration / TTS / ASR → per_unit
//   - LLM / VLM / Vision / Embedding / Rerank / Reasoning → input_output(双列)
func isPerUnitMatrixUnit(aiModel *model.AIModel) bool {
	if aiModel == nil {
		return false
	}
	switch aiModel.ModelType {
	case model.ModelTypeImageGeneration, model.ModelTypeVideoGeneration,
		model.ModelTypeTTS, model.ModelTypeASR:
		return true
	}
	return false
}

// isThinkingOn 兼容 JSON 反序列化后的 bool / 字符串两种形式。
func isThinkingOn(v interface{}) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		s := strings.ToLower(strings.TrimSpace(x))
		return s == "on" || s == "true" || s == "1" || s == "yes"
	}
	return false
}

// pickFirstPositive 依次执行 fns,返回首个 > 0 的结果;全部 0/负则返回 0。
func pickFirstPositive(fns ...func() float64) float64 {
	for _, fn := range fns {
		if v := fn(); v > 0 {
			return v
		}
	}
	return 0
}

func ptrFloat64(v float64) *float64 { return &v }

// roundTo6 与前端 PriceMatrixEditor.roundTo6 保持一致,避免浮点尾数。
func roundTo6(v float64) float64 {
	return math.Round(v*1e6) / 1e6
}

// cartesianValues 计算所有维度值的笛卡尔积。
func cartesianValues(dims []model.PriceDimension) []map[string]interface{} {
	if len(dims) == 0 {
		return []map[string]interface{}{{}}
	}
	// 跳过没有 Values 的维度
	used := make([]model.PriceDimension, 0, len(dims))
	for _, d := range dims {
		if len(d.Values) > 0 {
			used = append(used, d)
		}
	}
	if len(used) == 0 {
		return []map[string]interface{}{{}}
	}
	out := []map[string]interface{}{{}}
	for _, d := range used {
		next := make([]map[string]interface{}, 0, len(out)*len(d.Values))
		for _, prev := range out {
			for _, v := range d.Values {
				clone := make(map[string]interface{}, len(prev)+1)
				for k, val := range prev {
					clone[k] = val
				}
				clone[d.Key] = v
				next = append(next, clone)
			}
		}
		out = next
	}
	return out
}

// touchTime 仅为编译占位(避免 import "time" 未使用)
func touchTime() time.Time { return time.Now() }
