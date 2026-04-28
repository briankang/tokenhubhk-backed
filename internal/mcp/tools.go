// Package mcp - MCP 工具注册模块
// 将 TokenHub 平台能力封装为 MCP Tools，供 AI 客户端调用
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
	"tokenhub-server/internal/service/apikey"
	balancesvc "tokenhub-server/internal/service/balance"
	"tokenhub-server/internal/service/openapi"
)

// RegisterAllTools 注册所有平台能力为 MCP 工具
func RegisterAllTools(server *MCPServer, db *gorm.DB, balSvc *balancesvc.BalanceService, apiKeySvc *apikey.ApiKeyService, openSvc *openapi.OpenAPIService) {
	// ========== 对话与模型类 ==========
	registerChatTools(server, db)

	// ========== Coding Plan 类 ==========
	registerCodingTools(server, db)

	// ========== 余额与用量类 ==========
	registerBalanceTools(server, db, balSvc, openSvc)

	// ========== API Key 管理类 ==========
	registerAPIKeyTools(server, db, apiKeySvc)

	// ========== 文档类 ==========
	registerDocTools(server, db)
}

// ─── 对话与模型类工具 ─────────────────────────────────────────

// registerChatTools 注册对话和模型相关工具
func registerChatTools(server *MCPServer, db *gorm.DB) {
	// list_models - 列出可用模型及定价
	server.RegisterTool(MCPTool{
		Name:        "list_models",
		Description: "列出所有可用的 AI 模型及其定价信息",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		Handler: func(params map[string]interface{}, userID uint) (interface{}, error) {
			var models []model.AIModel
			err := db.Preload("Supplier").Preload("Category").
				Where("is_active = ?", true).Order("model_name ASC").
				Find(&models).Error
			if err != nil {
				return nil, fmt.Errorf("查询模型列表失败: %w", err)
			}

			// 获取模型定价
			var pricings []model.ModelPricing
			_ = db.Find(&pricings).Error
			pricingMap := make(map[uint]*model.ModelPricing)
			for i := range pricings {
				pricingMap[pricings[i].ModelID] = &pricings[i]
			}

			result := make([]map[string]interface{}, 0, len(models))
			for _, m := range models {
				item := map[string]interface{}{
					"model_name":     m.ModelName,
					"display_name":   m.DisplayName,
					"max_tokens":     m.MaxTokens,
					"context_window": m.ContextWindow,
					"is_active":      m.IsActive,
				}
				if m.Supplier.ID > 0 {
					item["supplier"] = m.Supplier.Name
				}
				if m.Category.ID > 0 {
					item["category"] = m.Category.Name
				}
				// 优先使用 ModelPricing 表的定价
				if p, ok := pricingMap[m.ID]; ok {
					item["input_price"] = credits.CreditsToRMB(p.InputPricePerToken)
					item["output_price"] = credits.CreditsToRMB(p.OutputPricePerToken)
					item["currency"] = "CNY"
				} else {
					item["input_price"] = credits.CreditsToRMB(m.InputPricePerToken)
					item["output_price"] = credits.CreditsToRMB(m.OutputPricePerToken)
					item["currency"] = "CNY"
				}
				item["pricing_unit"] = m.PricingUnit
				result = append(result, item)
			}
			return map[string]interface{}{
				"total":  len(result),
				"models": result,
			}, nil
		},
	})

	// get_model_info - 获取指定模型详情
	server.RegisterTool(MCPTool{
		Name:        "get_model_info",
		Description: "获取指定 AI 模型的详细信息，包含定价、上下文窗口、供应商等",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"model_id": map[string]interface{}{
					"type":        "string",
					"description": "模型名称或 ID",
				},
			},
			"required": []string{"model_id"},
		},
		Handler: func(params map[string]interface{}, userID uint) (interface{}, error) {
			modelID, _ := params["model_id"].(string)
			if modelID == "" {
				return nil, fmt.Errorf("model_id 参数必填")
			}

			var m model.AIModel
			err := db.Preload("Supplier").Preload("Category").
				Where("model_name = ? OR id = ?", modelID, modelID).
				First(&m).Error
			if err != nil {
				return nil, fmt.Errorf("未找到模型: %s", modelID)
			}

			var pricing model.ModelPricing
			inputPrice := credits.CreditsToRMB(m.InputPricePerToken)
			outputPrice := credits.CreditsToRMB(m.OutputPricePerToken)
			if err := db.Where("model_id = ?", m.ID).First(&pricing).Error; err == nil {
				inputPrice = credits.CreditsToRMB(pricing.InputPricePerToken)
				outputPrice = credits.CreditsToRMB(pricing.OutputPricePerToken)
			}

			result := map[string]interface{}{
				"id":             m.ID,
				"model_name":     m.ModelName,
				"display_name":   m.DisplayName,
				"description":    m.Description,
				"max_tokens":     m.MaxTokens,
				"context_window": m.ContextWindow,
				"is_active":      m.IsActive,
				"input_price":    inputPrice,
				"output_price":   outputPrice,
				"currency":       "CNY",
				"pricing_unit":   m.PricingUnit,
			}
			if m.Supplier.ID > 0 {
				result["supplier"] = m.Supplier.Name
			}
			if m.Category.ID > 0 {
				result["category"] = m.Category.Name
			}
			return result, nil
		},
	})

	// chat_completion - 调用模型对话（非流式，返回结果文本）
	server.RegisterTool(MCPTool{
		Name:        "chat_completion",
		Description: "调用 AI 模型进行对话补全。通过 MCP 工具调用仅支持非流式返回。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"model": map[string]interface{}{
					"type":        "string",
					"description": "模型名称，如 gpt-4o、deepseek-chat",
				},
				"messages": map[string]interface{}{
					"type":        "array",
					"description": "对话消息列表，每条包含 role 和 content",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"role":    map[string]interface{}{"type": "string"},
							"content": map[string]interface{}{"type": "string"},
						},
					},
				},
				"temperature": map[string]interface{}{
					"type":        "number",
					"description": "采样温度 0-2，默认 1.0",
				},
				"max_tokens": map[string]interface{}{
					"type":        "integer",
					"description": "最大输出 token 数",
				},
			},
			"required": []string{"model", "messages"},
		},
		Handler: func(params map[string]interface{}, userID uint) (interface{}, error) {
			// MCP 工具调用的 chat_completion 返回提示信息
			// 实际的聊天功能应通过 /v1/chat/completions 端点调用
			modelName, _ := params["model"].(string)
			messages, _ := params["messages"].([]interface{})
			return map[string]interface{}{
				"message":  fmt.Sprintf("请使用 TokenHub 的 OpenAI 兼容 API 端点 /v1/chat/completions 进行对话。模型: %s, 消息数: %d", modelName, len(messages)),
				"endpoint": "/v1/chat/completions",
				"method":   "POST",
				"note":     "MCP Tools 更适合查询信息和管理操作，实际对话请使用 REST API",
			}, nil
		},
	})
}

// ─── Coding Plan 类工具 ──────────────────────────────────────

// registerCodingTools 注册代码补全相关工具
func registerCodingTools(server *MCPServer, db *gorm.DB) {
	// code_completion - 代码补全
	server.RegisterTool(MCPTool{
		Name:        "code_completion",
		Description: "代码补全（FIM - Fill in the Middle），适用于编码辅助场景",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"model": map[string]interface{}{
					"type":        "string",
					"description": "Coding 模型名称",
				},
				"prompt": map[string]interface{}{
					"type":        "string",
					"description": "代码前缀（光标前的代码）",
				},
				"suffix": map[string]interface{}{
					"type":        "string",
					"description": "代码后缀（光标后的代码，用于 FIM）",
				},
				"max_tokens": map[string]interface{}{
					"type":        "integer",
					"description": "最大输出 token 数",
				},
			},
			"required": []string{"model", "prompt"},
		},
		Handler: func(params map[string]interface{}, userID uint) (interface{}, error) {
			modelName, _ := params["model"].(string)
			return map[string]interface{}{
				"message":  fmt.Sprintf("请使用 TokenHub 的 /v1/completions 端点进行代码补全。模型: %s", modelName),
				"endpoint": "/v1/completions",
				"method":   "POST",
				"note":     "代码补全需要流式传输，请直接使用 REST API",
			}, nil
		},
	})

	// list_coding_models - 列出 Coding Plan 可用模型
	server.RegisterTool(MCPTool{
		Name:        "list_coding_models",
		Description: "列出可用于代码补全的 Coding Plan 模型",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		Handler: func(params map[string]interface{}, userID uint) (interface{}, error) {
			var channels []model.Channel
			err := db.Where("status = 'active' AND (channel_type = 'CODING' OR channel_type = 'MIXED')").
				Find(&channels).Error
			if err != nil {
				return nil, fmt.Errorf("查询 Coding 渠道失败: %w", err)
			}

			// 从渠道中提取支持的模型列表
			modelSet := make(map[string]bool)
			for _, ch := range channels {
				// Models 字段为 JSON 数组
				var modelList []string
				if ch.Models != nil {
					_ = json.Unmarshal(ch.Models, &modelList)
				}
				for _, m := range modelList {
					m = strings.TrimSpace(m)
					if m != "" {
						modelSet[m] = true
					}
				}
			}

			models := make([]string, 0, len(modelSet))
			for m := range modelSet {
				models = append(models, m)
			}
			return map[string]interface{}{
				"total":  len(models),
				"models": models,
				"note":   "使用 /v1/completions 端点进行代码补全",
			}, nil
		},
	})
}

// ─── 余额与用量类工具 ────────────────────────────────────────

// registerBalanceTools 注册余额和用量查询工具
func registerBalanceTools(server *MCPServer, db *gorm.DB, balSvc *balancesvc.BalanceService, openSvc *openapi.OpenAPIService) {
	// get_balance - 查询当前余额
	server.RegisterTool(MCPTool{
		Name:        "get_balance",
		Description: "查询当前用户的余额、免费额度和冻结额度",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		Handler: func(params map[string]interface{}, userID uint) (interface{}, error) {
			info, err := openSvc.GetBalance(context.Background(), userID)
			if err != nil {
				return nil, fmt.Errorf("查询余额失败: %w", err)
			}
			return map[string]interface{}{
				"balance":        info.Balance,
				"free_quota":     info.FreeQuota,
				"total_consumed": info.TotalConsumed,
				"frozen_amount":  info.FrozenAmount,
				"currency":       info.Currency,
			}, nil
		},
	})

	// get_usage_summary - 获取用量统计
	server.RegisterTool(MCPTool{
		Name:        "get_usage_summary",
		Description: "获取用量统计信息，按模型分组显示请求数和 Token 消耗",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"date_from": map[string]interface{}{
					"type":        "string",
					"description": "起始日期，格式 YYYY-MM-DD",
				},
				"date_to": map[string]interface{}{
					"type":        "string",
					"description": "结束日期，格式 YYYY-MM-DD",
				},
			},
		},
		Handler: func(params map[string]interface{}, userID uint) (interface{}, error) {
			dateFrom, _ := params["date_from"].(string)
			dateTo, _ := params["date_to"].(string)
			// 默认查询最近 30 天
			if dateFrom == "" {
				dateFrom = time.Now().AddDate(0, 0, -30).Format("2006-01-02")
			}
			if dateTo == "" {
				dateTo = time.Now().Format("2006-01-02")
			}

			stats, err := openSvc.GetUsageStatistics(context.Background(), userID, dateFrom, dateTo)
			if err != nil {
				return nil, fmt.Errorf("查询用量统计失败: %w", err)
			}
			return map[string]interface{}{
				"date_from":  dateFrom,
				"date_to":    dateTo,
				"statistics": stats,
			}, nil
		},
	})

	// get_consumption_details - 获取消费明细
	server.RegisterTool(MCPTool{
		Name:        "get_consumption_details",
		Description: "获取消费明细列表，支持按模型过滤和分页",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"page": map[string]interface{}{
					"type":        "integer",
					"description": "页码，默认 1",
				},
				"page_size": map[string]interface{}{
					"type":        "integer",
					"description": "每页条数，默认 20，最大 100",
				},
				"model": map[string]interface{}{
					"type":        "string",
					"description": "按模型名称过滤",
				},
			},
		},
		Handler: func(params map[string]interface{}, userID uint) (interface{}, error) {
			page := intParam(params, "page", 1)
			pageSize := intParam(params, "page_size", 20)
			modelName, _ := params["model"].(string)

			details, total, err := openSvc.GetConsumptionDetails(
				context.Background(), userID, modelName, "", "", page, pageSize,
			)
			if err != nil {
				return nil, fmt.Errorf("查询消费明细失败: %w", err)
			}
			return map[string]interface{}{
				"total":   total,
				"page":    page,
				"details": details,
			}, nil
		},
	})
}

// ─── API Key 管理类工具 ──────────────────────────────────────

// registerAPIKeyTools 注册 API Key 管理工具
func registerAPIKeyTools(server *MCPServer, db *gorm.DB, apiKeySvc *apikey.ApiKeyService) {
	// list_api_keys - 列出 API Key
	server.RegisterTool(MCPTool{
		Name:        "list_api_keys",
		Description: "列出当前用户的所有 API Key",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		Handler: func(params map[string]interface{}, userID uint) (interface{}, error) {
			keys, total, err := apiKeySvc.List(context.Background(), userID, 1, 100)
			if err != nil {
				return nil, fmt.Errorf("查询 API Key 列表失败: %w", err)
			}

			items := make([]map[string]interface{}, 0, len(keys))
			for _, k := range keys {
				item := map[string]interface{}{
					"id":         k.ID,
					"name":       k.Name,
					"key_prefix": k.KeyPrefix,
					"is_active":  k.IsActive,
					"created_at": k.CreatedAt.Format(time.RFC3339),
				}
				if k.LastUsedAt != nil {
					item["last_used_at"] = k.LastUsedAt.Format(time.RFC3339)
				}
				items = append(items, item)
			}
			return map[string]interface{}{
				"total": total,
				"keys":  items,
			}, nil
		},
	})

	// create_api_key - 创建新 Key
	server.RegisterTool(MCPTool{
		Name:        "create_api_key",
		Description: "创建一个新的 API Key（注意：完整 Key 仅返回一次，请妥善保存）",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name": map[string]interface{}{
					"type":        "string",
					"description": "Key 名称，如 'Production' 或 'Development'",
				},
			},
			"required": []string{"name"},
		},
		Handler: func(params map[string]interface{}, userID uint) (interface{}, error) {
			name, _ := params["name"].(string)
			if name == "" {
				return nil, fmt.Errorf("name 参数必填")
			}

			// 查询用户获取 tenantID
			var user model.User
			if err := db.Where("id = ?", userID).First(&user).Error; err != nil {
				return nil, fmt.Errorf("查询用户信息失败: %w", err)
			}

			result, err := apiKeySvc.Generate(context.Background(), userID, user.TenantID, name)
			if err != nil {
				return nil, fmt.Errorf("创建 API Key 失败: %w", err)
			}
			return map[string]interface{}{
				"id":         result.ID,
				"name":       result.Name,
				"key":        result.Key,
				"key_prefix": result.KeyPrefix,
				"warning":    "请立即保存此 Key，它不会再次显示！",
			}, nil
		},
	})

	// revoke_api_key - 撤销 Key
	server.RegisterTool(MCPTool{
		Name:        "revoke_api_key",
		Description: "撤销（停用）指定的 API Key",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"key_id": map[string]interface{}{
					"type":        "integer",
					"description": "要撤销的 API Key ID",
				},
			},
			"required": []string{"key_id"},
		},
		Handler: func(params map[string]interface{}, userID uint) (interface{}, error) {
			keyIDFloat, _ := params["key_id"].(float64)
			keyID := uint(keyIDFloat)
			if keyID == 0 {
				return nil, fmt.Errorf("key_id 参数必填")
			}

			err := apiKeySvc.Revoke(context.Background(), keyID, userID)
			if err != nil {
				return nil, fmt.Errorf("撤销 API Key 失败: %w", err)
			}
			return map[string]interface{}{
				"success": true,
				"message": fmt.Sprintf("API Key #%d 已成功撤销", keyID),
			}, nil
		},
	})
}

// ─── 文档类工具 ─────────────────────────────────────────────

// registerDocTools 注册文档查询工具
func registerDocTools(server *MCPServer, db *gorm.DB) {
	// search_docs - 搜索平台文档
	server.RegisterTool(MCPTool{
		Name:        "search_docs",
		Description: "搜索 TokenHub 平台文档，支持按关键词模糊匹配标题和内容",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "搜索关键词",
				},
			},
			"required": []string{"query"},
		},
		Handler: func(params map[string]interface{}, userID uint) (interface{}, error) {
			query, _ := params["query"].(string)
			if query == "" {
				return nil, fmt.Errorf("query 参数必填")
			}

			var articles []model.DocArticle
			err := db.Where("is_published = ? AND (title LIKE ? OR content LIKE ? OR tags LIKE ?)",
				true, "%"+query+"%", "%"+query+"%", "%"+query+"%").
				Order("sort_order ASC").Limit(20).
				Find(&articles).Error
			if err != nil {
				return nil, fmt.Errorf("搜索文档失败: %w", err)
			}

			results := make([]map[string]interface{}, 0, len(articles))
			for _, a := range articles {
				results = append(results, map[string]interface{}{
					"id":      a.ID,
					"title":   a.Title,
					"slug":    a.Slug,
					"summary": a.Summary,
					"tags":    a.Tags,
				})
			}
			return map[string]interface{}{
				"query":   query,
				"total":   len(results),
				"results": results,
			}, nil
		},
	})

	// get_doc_article - 获取文档内容
	server.RegisterTool(MCPTool{
		Name:        "get_doc_article",
		Description: "获取指定文档的完整 Markdown 内容",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"slug": map[string]interface{}{
					"type":        "string",
					"description": "文档的 URL 标识（slug）",
				},
			},
			"required": []string{"slug"},
		},
		Handler: func(params map[string]interface{}, userID uint) (interface{}, error) {
			slug, _ := params["slug"].(string)
			if slug == "" {
				return nil, fmt.Errorf("slug 参数必填")
			}

			var article model.DocArticle
			err := db.Preload("Category").
				Where("slug = ? AND is_published = ?", slug, true).
				First(&article).Error
			if err != nil {
				return nil, fmt.Errorf("文档未找到: %s", slug)
			}

			result := map[string]interface{}{
				"title":   article.Title,
				"slug":    article.Slug,
				"content": article.Content,
				"summary": article.Summary,
				"tags":    article.Tags,
			}
			if article.Category != nil {
				result["category"] = article.Category.Name
			}
			return result, nil
		},
	})
}

// ─── 辅助函数 ──────────────────────────────────────────────

// intParam 从参数 map 中安全提取整数值，不存在时返回默认值
func intParam(params map[string]interface{}, key string, defaultVal int) int {
	v, ok := params[key]
	if !ok {
		return defaultVal
	}
	switch val := v.(type) {
	case float64:
		return int(val)
	case json.Number:
		n, _ := val.Int64()
		return int(n)
	default:
		return defaultVal
	}
}
