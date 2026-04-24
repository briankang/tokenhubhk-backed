package database

import (
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// RunSeedParams 初始化/更新平台标准参数和供应商映射
// v4.2 起：每次重启均执行 upsert，确保所有环境拥有最新的参数定义和供应商映射。
// 更新策略：
//   - PlatformParam：按 param_name 匹配，存在则更新 display_name/description/param_type/category/sort_order，不存在则创建
//   - SupplierParamMapping：按 (platform_param_id, supplier_code) 匹配，存在则更新转换规则/支持状态/notes，不存在则创建
func RunSeedParams() {
	logger.L.Info("seeding platform params and supplier mappings (upsert mode)...")

	// ── 平台标准参数定义 ──
	// 命名规范参考 OpenAI API 参数风格
	params := []model.PlatformParam{
		// ── 思考/推理类 ──
		{
			ParamName: "enable_thinking", ParamType: "bool",
			DisplayName: "启用深度思考", Description: "开启模型的深度思考/推理模式，适用于复杂逻辑和数学问题",
			DefaultValue: "false", Category: "thinking", SortOrder: 1, IsActive: true,
		},
		{
			ParamName: "thinking_budget", ParamType: "int",
			DisplayName: "思考Token预算", Description: "深度思考模式下允许使用的最大Token数",
			DefaultValue: "10000", Category: "thinking", SortOrder: 2, IsActive: true,
		},
		{
			ParamName: "reasoning_effort", ParamType: "string",
			DisplayName: "推理强度", Description: "推理努力程度，可选值：low / medium / high",
			DefaultValue: "\"medium\"", Category: "thinking", SortOrder: 3, IsActive: true,
		},

		// ── 搜索类 ──
		{
			ParamName: "enable_search", ParamType: "bool",
			DisplayName: "启用联网搜索", Description: "允许模型在回答时进行互联网搜索以获取最新信息",
			DefaultValue: "false", Category: "search", SortOrder: 10, IsActive: true,
		},

		// ── 输出格式类 ──
		{
			ParamName: "response_format", ParamType: "json",
			DisplayName: "响应格式", Description: "指定输出格式，如 {\"type\": \"json_object\"} 强制 JSON 输出",
			DefaultValue: "", Category: "format", SortOrder: 20, IsActive: true,
		},

		// ── 惩罚/采样类 ──
		{
			ParamName: "frequency_penalty", ParamType: "float",
			DisplayName: "频率惩罚", Description: "降低模型重复相同词语的倾向，范围 -2.0 到 2.0",
			DefaultValue: "0", Category: "penalty", SortOrder: 30, IsActive: true,
		},
		{
			ParamName: "presence_penalty", ParamType: "float",
			DisplayName: "存在惩罚", Description: "增加模型讨论新话题的倾向，范围 -2.0 到 2.0",
			DefaultValue: "0", Category: "penalty", SortOrder: 31, IsActive: true,
		},
		{
			ParamName: "seed", ParamType: "int",
			DisplayName: "随机种子", Description: "固定随机种子以获得可重现的输出结果",
			DefaultValue: "", Category: "penalty", SortOrder: 32, IsActive: true,
		},
		{
			ParamName: "top_k", ParamType: "int",
			DisplayName: "Top-K 采样", Description: "每步只从概率最高的 K 个候选中采样",
			DefaultValue: "", Category: "penalty", SortOrder: 33, IsActive: true,
		},

		// ── 安全/内容过滤类 ──
		{
			ParamName: "safe_mode", ParamType: "bool",
			DisplayName: "安全模式", Description: "启用内容安全过滤，屏蔽不当内容",
			DefaultValue: "false", Category: "safety", SortOrder: 40, IsActive: true,
		},
	}

	// ── Upsert 平台参数定义 ──
	paramCreated, paramUpdated := 0, 0
	for i := range params {
		var existing model.PlatformParam
		if err := DB.Where("param_name = ?", params[i].ParamName).First(&existing).Error; err != nil {
			// 不存在，创建
			if err2 := DB.Create(&params[i]).Error; err2 != nil {
				logger.L.Error("seed platform param failed",
					zap.String("param", params[i].ParamName), zap.Error(err2))
				continue
			}
			paramCreated++
		} else {
			// 已存在，更新非 ID 字段，保留 ID 以便后续映射
			params[i].ID = existing.ID
			updates := map[string]interface{}{
				"display_name":  params[i].DisplayName,
				"description":   params[i].Description,
				"param_type":    params[i].ParamType,
				"default_value": params[i].DefaultValue,
				"category":      params[i].Category,
				"sort_order":    params[i].SortOrder,
				"is_active":     params[i].IsActive,
			}
			if err2 := DB.Model(&model.PlatformParam{}).Where("id = ?", existing.ID).
				Updates(updates).Error; err2 != nil {
				logger.L.Error("update platform param failed",
					zap.String("param", params[i].ParamName), zap.Error(err2))
			} else {
				paramUpdated++
			}
		}
	}
	logger.L.Info("platform params upserted",
		zap.Int("created", paramCreated), zap.Int("updated", paramUpdated))

	// ── 供应商映射 ──
	// 从 DB 重新读取 ID（确保 FirstOrCreate 后 ID 正确）
	paramIDMap := make(map[string]uint)
	for _, p := range params {
		if p.ID > 0 {
			paramIDMap[p.ParamName] = p.ID
		}
	}

	mappings := []model.SupplierParamMapping{
		// ════════════════════════════════════════
		// OpenAI
		// ════════════════════════════════════════
		{PlatformParamID: paramIDMap["enable_thinking"], SupplierCode: "openai", VendorParamName: "reasoning_effort", TransformType: "none", Supported: false, Notes: "OpenAI 通过模型选择(o1/o3)启用推理，不使用此参数"},
		{PlatformParamID: paramIDMap["thinking_budget"], SupplierCode: "openai", VendorParamName: "max_completion_tokens", TransformType: "rename", Supported: true, Notes: "o1/o3 模型使用 max_completion_tokens 控制思考长度"},
		{PlatformParamID: paramIDMap["reasoning_effort"], SupplierCode: "openai", VendorParamName: "reasoning_effort", TransformType: "direct", Supported: true, Notes: "o1/o3 模型原生支持"},
		{PlatformParamID: paramIDMap["enable_search"], SupplierCode: "openai", VendorParamName: "enable_search", TransformType: "none", Supported: false, Notes: "OpenAI 不支持联网搜索参数"},
		{PlatformParamID: paramIDMap["response_format"], SupplierCode: "openai", VendorParamName: "response_format", TransformType: "direct", Supported: true},
		{PlatformParamID: paramIDMap["frequency_penalty"], SupplierCode: "openai", VendorParamName: "frequency_penalty", TransformType: "direct", Supported: true},
		{PlatformParamID: paramIDMap["presence_penalty"], SupplierCode: "openai", VendorParamName: "presence_penalty", TransformType: "direct", Supported: true},
		{PlatformParamID: paramIDMap["seed"], SupplierCode: "openai", VendorParamName: "seed", TransformType: "direct", Supported: true},
		{PlatformParamID: paramIDMap["top_k"], SupplierCode: "openai", VendorParamName: "top_k", TransformType: "none", Supported: false, Notes: "OpenAI 不支持 top_k"},

		// ════════════════════════════════════════
		// Anthropic (Claude)
		// ════════════════════════════════════════
		{PlatformParamID: paramIDMap["enable_thinking"], SupplierCode: "anthropic", VendorParamName: "thinking", TransformType: "nested", Supported: true,
			TransformRule: `{"when_true": {"type": "enabled", "budget_tokens": 10000}, "when_false": {"type": "disabled"}}`,
			Notes:         "Claude 使用 thinking 对象结构，budget_tokens 取 thinking_budget 参数值"},
		{PlatformParamID: paramIDMap["thinking_budget"], SupplierCode: "anthropic", VendorParamName: "thinking.budget_tokens", TransformType: "nested", Supported: true,
			TransformRule: `{"path": "thinking", "field": "budget_tokens"}`,
			Notes:         "嵌套在 thinking 对象内，需与 enable_thinking 配合使用"},
		{PlatformParamID: paramIDMap["reasoning_effort"], SupplierCode: "anthropic", VendorParamName: "reasoning_effort", TransformType: "none", Supported: false, Notes: "Anthropic 不支持 reasoning_effort"},
		{PlatformParamID: paramIDMap["enable_search"], SupplierCode: "anthropic", VendorParamName: "enable_search", TransformType: "none", Supported: false},
		{PlatformParamID: paramIDMap["response_format"], SupplierCode: "anthropic", VendorParamName: "response_format", TransformType: "none", Supported: false, Notes: "Anthropic 通过 system prompt 控制 JSON 输出"},
		{PlatformParamID: paramIDMap["frequency_penalty"], SupplierCode: "anthropic", VendorParamName: "frequency_penalty", TransformType: "none", Supported: false},
		{PlatformParamID: paramIDMap["presence_penalty"], SupplierCode: "anthropic", VendorParamName: "presence_penalty", TransformType: "none", Supported: false},
		{PlatformParamID: paramIDMap["seed"], SupplierCode: "anthropic", VendorParamName: "seed", TransformType: "none", Supported: false},
		{PlatformParamID: paramIDMap["top_k"], SupplierCode: "anthropic", VendorParamName: "top_k", TransformType: "direct", Supported: true},

		// ════════════════════════════════════════
		// 阿里云通义千问 (Qwen)
		// ════════════════════════════════════════
		{PlatformParamID: paramIDMap["enable_thinking"], SupplierCode: "aliyun_dashscope", VendorParamName: "enable_thinking", TransformType: "direct", Supported: true, Notes: "qwen3 系列模型支持，直接透传"},
		{PlatformParamID: paramIDMap["thinking_budget"], SupplierCode: "aliyun_dashscope", VendorParamName: "thinking_budget", TransformType: "direct", Supported: true, Notes: "qwen3 系列配合 enable_thinking 使用"},
		{PlatformParamID: paramIDMap["reasoning_effort"], SupplierCode: "aliyun_dashscope", VendorParamName: "reasoning_effort", TransformType: "none", Supported: false},
		{PlatformParamID: paramIDMap["enable_search"], SupplierCode: "aliyun_dashscope", VendorParamName: "enable_search", TransformType: "direct", Supported: true, Notes: "通义千问支持联网搜索"},
		{PlatformParamID: paramIDMap["response_format"], SupplierCode: "aliyun_dashscope", VendorParamName: "response_format", TransformType: "direct", Supported: true},
		{PlatformParamID: paramIDMap["frequency_penalty"], SupplierCode: "aliyun_dashscope", VendorParamName: "frequency_penalty", TransformType: "direct", Supported: true},
		{PlatformParamID: paramIDMap["presence_penalty"], SupplierCode: "aliyun_dashscope", VendorParamName: "presence_penalty", TransformType: "direct", Supported: true},
		{PlatformParamID: paramIDMap["seed"], SupplierCode: "aliyun_dashscope", VendorParamName: "seed", TransformType: "direct", Supported: true},
		{PlatformParamID: paramIDMap["top_k"], SupplierCode: "aliyun_dashscope", VendorParamName: "top_k", TransformType: "direct", Supported: true},

		// ════════════════════════════════════════
		// 火山引擎豆包 (Volcengine Doubao)
		// ════════════════════════════════════════
		{PlatformParamID: paramIDMap["enable_thinking"], SupplierCode: "volcengine", VendorParamName: "thinking", TransformType: "nested", Supported: true,
			TransformRule: `{"when_true": {"type": "enabled", "budget_tokens": 10000}, "when_false": {"type": "disabled"}}`,
			Notes:         "seed-2.0 系列使用 thinking 对象格式：{type: enabled, budget_tokens: N}"},
		{PlatformParamID: paramIDMap["thinking_budget"], SupplierCode: "volcengine", VendorParamName: "thinking.budget_tokens", TransformType: "nested", Supported: true,
			TransformRule: `{"path": "thinking", "field": "budget_tokens"}`,
			Notes:         "设置 thinking 对象中的 budget_tokens 字段"},
		{PlatformParamID: paramIDMap["reasoning_effort"], SupplierCode: "volcengine", VendorParamName: "reasoning_effort", TransformType: "none", Supported: false},
		{PlatformParamID: paramIDMap["enable_search"], SupplierCode: "volcengine", VendorParamName: "enable_search", TransformType: "none", Supported: false, Notes: "seed-2.0 系列暂不支持联网搜索"},
		{PlatformParamID: paramIDMap["response_format"], SupplierCode: "volcengine", VendorParamName: "response_format", TransformType: "direct", Supported: true},
		{PlatformParamID: paramIDMap["frequency_penalty"], SupplierCode: "volcengine", VendorParamName: "frequency_penalty", TransformType: "direct", Supported: true},
		{PlatformParamID: paramIDMap["presence_penalty"], SupplierCode: "volcengine", VendorParamName: "presence_penalty", TransformType: "direct", Supported: true},
		{PlatformParamID: paramIDMap["seed"], SupplierCode: "volcengine", VendorParamName: "seed", TransformType: "direct", Supported: true},
		{PlatformParamID: paramIDMap["top_k"], SupplierCode: "volcengine", VendorParamName: "top_k", TransformType: "none", Supported: false},

		// ════════════════════════════════════════
		// DeepSeek
		// ════════════════════════════════════════
		{PlatformParamID: paramIDMap["enable_thinking"], SupplierCode: "deepseek", VendorParamName: "enable_thinking", TransformType: "none", Supported: false, Notes: "DeepSeek 通过选择 deepseek-reasoner 模型启用推理"},
		{PlatformParamID: paramIDMap["thinking_budget"], SupplierCode: "deepseek", VendorParamName: "thinking_budget", TransformType: "none", Supported: false},
		{PlatformParamID: paramIDMap["reasoning_effort"], SupplierCode: "deepseek", VendorParamName: "reasoning_effort", TransformType: "none", Supported: false},
		{PlatformParamID: paramIDMap["enable_search"], SupplierCode: "deepseek", VendorParamName: "enable_search", TransformType: "none", Supported: false},
		{PlatformParamID: paramIDMap["response_format"], SupplierCode: "deepseek", VendorParamName: "response_format", TransformType: "direct", Supported: true},
		{PlatformParamID: paramIDMap["frequency_penalty"], SupplierCode: "deepseek", VendorParamName: "frequency_penalty", TransformType: "direct", Supported: true},
		{PlatformParamID: paramIDMap["presence_penalty"], SupplierCode: "deepseek", VendorParamName: "presence_penalty", TransformType: "direct", Supported: true},
		{PlatformParamID: paramIDMap["seed"], SupplierCode: "deepseek", VendorParamName: "seed", TransformType: "none", Supported: false},
		{PlatformParamID: paramIDMap["top_k"], SupplierCode: "deepseek", VendorParamName: "top_k", TransformType: "none", Supported: false},

		// ════════════════════════════════════════
		// Google Gemini
		// ════════════════════════════════════════
		{PlatformParamID: paramIDMap["enable_thinking"], SupplierCode: "google_gemini", VendorParamName: "thinkingConfig", TransformType: "nested", Supported: true,
			TransformRule: `{"when_true": {"thinkingBudget": 10000}, "when_false": null}`,
			Notes:         "Gemini 2.5 使用 thinkingConfig 对象"},
		{PlatformParamID: paramIDMap["thinking_budget"], SupplierCode: "google_gemini", VendorParamName: "thinkingConfig.thinkingBudget", TransformType: "nested", Supported: true,
			TransformRule: `{"path": "thinkingConfig", "field": "thinkingBudget"}`},
		{PlatformParamID: paramIDMap["reasoning_effort"], SupplierCode: "google_gemini", VendorParamName: "reasoning_effort", TransformType: "none", Supported: false},
		{PlatformParamID: paramIDMap["enable_search"], SupplierCode: "google_gemini", VendorParamName: "tools", TransformType: "nested", Supported: true,
			TransformRule: `{"when_true": [{"googleSearch": {}}], "when_false": null}`,
			Notes:         "Gemini 通过 tools 数组启用搜索"},
		{PlatformParamID: paramIDMap["response_format"], SupplierCode: "google_gemini", VendorParamName: "generationConfig.responseMimeType", TransformType: "nested", Supported: true,
			TransformRule: `{"path": "generationConfig", "field": "responseMimeType", "value_map": {"json_object": "application/json"}}`},
		{PlatformParamID: paramIDMap["frequency_penalty"], SupplierCode: "google_gemini", VendorParamName: "generationConfig.frequencyPenalty", TransformType: "nested", Supported: true,
			TransformRule: `{"path": "generationConfig", "field": "frequencyPenalty"}`},
		{PlatformParamID: paramIDMap["presence_penalty"], SupplierCode: "google_gemini", VendorParamName: "generationConfig.presencePenalty", TransformType: "nested", Supported: true,
			TransformRule: `{"path": "generationConfig", "field": "presencePenalty"}`},
		{PlatformParamID: paramIDMap["seed"], SupplierCode: "google_gemini", VendorParamName: "generationConfig.seed", TransformType: "nested", Supported: true,
			TransformRule: `{"path": "generationConfig", "field": "seed"}`},
		{PlatformParamID: paramIDMap["top_k"], SupplierCode: "google_gemini", VendorParamName: "generationConfig.topK", TransformType: "nested", Supported: true,
			TransformRule: `{"path": "generationConfig", "field": "topK"}`},

		// ════════════════════════════════════════
		// 智谱 GLM
		// ════════════════════════════════════════
		{PlatformParamID: paramIDMap["enable_thinking"], SupplierCode: "zhipu", VendorParamName: "enable_thinking", TransformType: "none", Supported: false},
		{PlatformParamID: paramIDMap["thinking_budget"], SupplierCode: "zhipu", VendorParamName: "thinking_budget", TransformType: "none", Supported: false},
		{PlatformParamID: paramIDMap["enable_search"], SupplierCode: "zhipu", VendorParamName: "tools", TransformType: "nested", Supported: true,
			TransformRule: `{"when_true": [{"type": "web_search", "web_search": {"enable": true}}], "when_false": null}`,
			Notes:         "智谱通过 tools 启用搜索"},
		{PlatformParamID: paramIDMap["response_format"], SupplierCode: "zhipu", VendorParamName: "response_format", TransformType: "direct", Supported: true},
		{PlatformParamID: paramIDMap["frequency_penalty"], SupplierCode: "zhipu", VendorParamName: "frequency_penalty", TransformType: "none", Supported: false},
		{PlatformParamID: paramIDMap["presence_penalty"], SupplierCode: "zhipu", VendorParamName: "presence_penalty", TransformType: "none", Supported: false},
		{PlatformParamID: paramIDMap["seed"], SupplierCode: "zhipu", VendorParamName: "seed", TransformType: "direct", Supported: true},
		{PlatformParamID: paramIDMap["top_k"], SupplierCode: "zhipu", VendorParamName: "top_k", TransformType: "direct", Supported: true},

		// ════════════════════════════════════════
		// Moonshot (Kimi)
		// ════════════════════════════════════════
		{PlatformParamID: paramIDMap["enable_thinking"], SupplierCode: "moonshot", VendorParamName: "enable_thinking", TransformType: "none", Supported: false},
		{PlatformParamID: paramIDMap["enable_search"], SupplierCode: "moonshot", VendorParamName: "use_search", TransformType: "rename", Supported: true, Notes: "Kimi 参数名为 use_search"},
		{PlatformParamID: paramIDMap["response_format"], SupplierCode: "moonshot", VendorParamName: "response_format", TransformType: "direct", Supported: true},
		{PlatformParamID: paramIDMap["frequency_penalty"], SupplierCode: "moonshot", VendorParamName: "frequency_penalty", TransformType: "direct", Supported: true},
		{PlatformParamID: paramIDMap["presence_penalty"], SupplierCode: "moonshot", VendorParamName: "presence_penalty", TransformType: "direct", Supported: true},
		{PlatformParamID: paramIDMap["seed"], SupplierCode: "moonshot", VendorParamName: "seed", TransformType: "none", Supported: false},
		{PlatformParamID: paramIDMap["top_k"], SupplierCode: "moonshot", VendorParamName: "top_k", TransformType: "none", Supported: false},

		// ════════════════════════════════════════
		// 百度文心一言
		// ════════════════════════════════════════
		{PlatformParamID: paramIDMap["enable_thinking"], SupplierCode: "baidu_wenxin", VendorParamName: "enable_thinking", TransformType: "none", Supported: false},
		{PlatformParamID: paramIDMap["enable_search"], SupplierCode: "baidu_wenxin", VendorParamName: "enable_search", TransformType: "direct", Supported: true},
		{PlatformParamID: paramIDMap["response_format"], SupplierCode: "baidu_wenxin", VendorParamName: "response_format", TransformType: "direct", Supported: true},
		{PlatformParamID: paramIDMap["frequency_penalty"], SupplierCode: "baidu_wenxin", VendorParamName: "penalty_score", TransformType: "rename", Supported: true, Notes: "文心使用 penalty_score（1.0-2.0）"},
		{PlatformParamID: paramIDMap["presence_penalty"], SupplierCode: "baidu_wenxin", VendorParamName: "presence_penalty", TransformType: "none", Supported: false},
		{PlatformParamID: paramIDMap["seed"], SupplierCode: "baidu_wenxin", VendorParamName: "seed", TransformType: "none", Supported: false},
		{PlatformParamID: paramIDMap["top_k"], SupplierCode: "baidu_wenxin", VendorParamName: "top_k", TransformType: "none", Supported: false},

		// ════════════════════════════════════════
		// Azure OpenAI
		// ════════════════════════════════════════
		{PlatformParamID: paramIDMap["enable_thinking"], SupplierCode: "azure_openai", VendorParamName: "enable_thinking", TransformType: "none", Supported: false},
		{PlatformParamID: paramIDMap["reasoning_effort"], SupplierCode: "azure_openai", VendorParamName: "reasoning_effort", TransformType: "direct", Supported: true},
		{PlatformParamID: paramIDMap["enable_search"], SupplierCode: "azure_openai", VendorParamName: "enable_search", TransformType: "none", Supported: false},
		{PlatformParamID: paramIDMap["response_format"], SupplierCode: "azure_openai", VendorParamName: "response_format", TransformType: "direct", Supported: true},
		{PlatformParamID: paramIDMap["frequency_penalty"], SupplierCode: "azure_openai", VendorParamName: "frequency_penalty", TransformType: "direct", Supported: true},
		{PlatformParamID: paramIDMap["presence_penalty"], SupplierCode: "azure_openai", VendorParamName: "presence_penalty", TransformType: "direct", Supported: true},
		{PlatformParamID: paramIDMap["seed"], SupplierCode: "azure_openai", VendorParamName: "seed", TransformType: "direct", Supported: true},
		{PlatformParamID: paramIDMap["top_k"], SupplierCode: "azure_openai", VendorParamName: "top_k", TransformType: "none", Supported: false},
	}

	// ── Upsert 供应商映射 ──
	mappingCreated, mappingUpdated, mappingSkipped := 0, 0, 0
	for _, m := range mappings {
		if m.PlatformParamID == 0 {
			// 参数 ID 为 0 说明对应的参数创建失败，跳过
			mappingSkipped++
			continue
		}
		var existing model.SupplierParamMapping
		if err := DB.Where("platform_param_id = ? AND supplier_code = ?",
			m.PlatformParamID, m.SupplierCode).First(&existing).Error; err != nil {
			// 不存在，创建
			if err2 := DB.Create(&m).Error; err2 != nil {
				logger.L.Error("seed supplier param mapping failed",
					zap.String("supplier", m.SupplierCode),
					zap.Uint("param_id", m.PlatformParamID), zap.Error(err2))
				mappingSkipped++
			} else {
				mappingCreated++
			}
		} else {
			// 已存在，更新转换规则/支持状态/notes
			updates := map[string]interface{}{
				"vendor_param_name": m.VendorParamName,
				"transform_type":    m.TransformType,
				"transform_rule":    m.TransformRule,
				"supported":         m.Supported,
				"notes":             m.Notes,
			}
			if err2 := DB.Model(&model.SupplierParamMapping{}).Where("id = ?", existing.ID).
				Updates(updates).Error; err2 != nil {
				logger.L.Error("update supplier param mapping failed",
					zap.String("supplier", m.SupplierCode),
					zap.Uint("param_id", m.PlatformParamID), zap.Error(err2))
				mappingSkipped++
			} else {
				mappingUpdated++
			}
		}
	}
	logger.L.Info("platform params and supplier mappings seeded successfully",
		zap.Int("params_created", paramCreated),
		zap.Int("params_updated", paramUpdated),
		zap.Int("mappings_created", mappingCreated),
		zap.Int("mappings_updated", mappingUpdated),
		zap.Int("mappings_skipped", mappingSkipped))
}

// reseedParamMappings 重新同步映射（管理员手动触发时使用）
func reseedParamMappings(db *gorm.DB, paramIDMap map[string]uint, supplierCode string, mappings []model.SupplierParamMapping) error {
	// 删除该供应商的旧映射
	if err := db.Where("supplier_code = ?", supplierCode).Delete(&model.SupplierParamMapping{}).Error; err != nil {
		return err
	}
	// 批量创建新映射
	return db.CreateInBatches(mappings, 50).Error
}
