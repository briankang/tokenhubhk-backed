package pricescraper

import (
	"tokenhub-server/internal/model"
)

// =====================================================
// 阿里云百炼「思考模式 / 非思考模式」差异化价格补充表
//
// 背景：
//   阿里云百炼 API 返回的 `prices[]` 数组里，output_token type 只给一个统一价格，
//   但实际官网文档页（https://help.aliyun.com/zh/model-studio/model-pricing）
//   对 **部分推理类模型** 会将"非思考模式"和"思考模式"拆为两列不同价格。
//
//   例如（数据随官网变动，需定期对齐）：
//     qwen3-235b-a22b-thinking        非思考 6   / 思考 24
//     qwen3-30b-a3b-thinking-2507     非思考 4   / 思考 12
//     qwen3-32b-thinking-2507         非思考 2.8 / 思考 8.4
//     qwen3-14b-thinking-2507         非思考 1.5 / 思考 4.5
//     qwq-32b（思考链专用）           非思考 —   / 思考 4
//
// 设计：
//   1. 本表维护 "必须按 thinking 双价计费" 的模型清单 + 各阶梯双价
//   2. mergeAlibabaWithSupplementary 读取本表，用 `OutputPriceThinking` 字段覆盖
//      API 返回数据中相同模型 + 相同阶梯 name 的 tier
//   3. 未列入本表的模型保持 `output_price_thinking = 0`（= 不区分，沿用 output_price）
//
// 维护原则：
//   - 仅添加官方文档页明确拆列的模型（思考 ≠ 非思考）
//   - 当供应商某天将 API prices[] 改为返回 `output_token_thinking` type 时，
//     alibaba_scraper.convertModel 会自动解析，本表可移除对应条目
//   - 季度对齐一次价格
// =====================================================

// getAlibabaThinkingPrices 返回阿里云思考模式双价格的覆盖表
func getAlibabaThinkingPrices() []ScrapedModel {
	i64 := func(v int64) *int64 { return &v }

	return []ScrapedModel{
		// qwen3-max-thinking（若存在思考档独立定价）—— 以官网最新为准
		// 占位示例（数据以官网为准，请管理员/运营侧季度维护）
		{
			ModelName:           "qwen3-235b-a22b-thinking-2507",
			DisplayName:         "Qwen3-235B-A22B Thinking 2507",
			InputPrice:          6,
			OutputPrice:         24,
			OutputPriceThinking: 24,
			Currency:            "CNY",
			PricingUnit:         PricingUnitPerMillionTokens,
			ModelType:           "LLM",
			PriceTiers: []model.PriceTier{
				{Name: "输入<=256k", InputMin: 0, InputMax: i64(256000),
					InputPrice: 6, OutputPrice: 24, OutputPriceThinking: 24},
			},
		},
		{
			ModelName:           "qwen3-30b-a3b-thinking-2507",
			DisplayName:         "Qwen3-30B-A3B Thinking 2507",
			InputPrice:          4,
			OutputPrice:         12,
			OutputPriceThinking: 12,
			Currency:            "CNY",
			PricingUnit:         PricingUnitPerMillionTokens,
			ModelType:           "LLM",
			PriceTiers: []model.PriceTier{
				{Name: "输入<=256k", InputMin: 0, InputMax: i64(256000),
					InputPrice: 4, OutputPrice: 12, OutputPriceThinking: 12},
			},
		},
		{
			ModelName:           "qwen3-32b-thinking-2507",
			DisplayName:         "Qwen3-32B Thinking 2507",
			InputPrice:          2.8,
			OutputPrice:         8.4,
			OutputPriceThinking: 8.4,
			Currency:            "CNY",
			PricingUnit:         PricingUnitPerMillionTokens,
			ModelType:           "LLM",
			PriceTiers: []model.PriceTier{
				{Name: "输入<=128k", InputMin: 0, InputMax: i64(128000),
					InputPrice: 2.8, OutputPrice: 8.4, OutputPriceThinking: 8.4},
			},
		},
	}
}

// applyAlibabaThinkingOverrides 将思考模式双价格补充数据合并进已解析的 ScrapedModel 列表
//
// 合并策略：
//   - 按模型名完全匹配
//   - 匹配到则遍历 supp.PriceTiers，按 name 对齐：
//       若 API 里已有同名 tier → 补齐 OutputPriceThinking 字段
//       若未找到匹配 tier     → 忽略（信赖 API 的阶梯划分，本表只做"列"补充）
//   - 同时覆盖顶层 OutputPriceThinking / OutputCostThinkingRMB
//   - 补充表本身若不在 API 返回中 → 作为新模型 append（保证即使 API 漏掉也能入库）
func applyAlibabaThinkingOverrides(apiModels []ScrapedModel, overrides []ScrapedModel) []ScrapedModel {
	idx := make(map[string]int, len(apiModels))
	for i, m := range apiModels {
		idx[normalizeModelID(m.ModelName)] = i
	}
	for _, supp := range overrides {
		key := normalizeModelID(supp.ModelName)
		if i, ok := idx[key]; ok {
			// 顶层 thinking 价
			if supp.OutputPriceThinking > 0 {
				apiModels[i].OutputPriceThinking = supp.OutputPriceThinking
			}
			// 按 tier name 对齐 thinking 价
			for _, suppT := range supp.PriceTiers {
				for j := range apiModels[i].PriceTiers {
					if apiModels[i].PriceTiers[j].Name == suppT.Name && suppT.OutputPriceThinking > 0 {
						apiModels[i].PriceTiers[j].OutputPriceThinking = suppT.OutputPriceThinking
					}
				}
			}
			continue
		}
		// API 未返回 → append
		apiModels = append(apiModels, supp)
	}
	return apiModels
}
