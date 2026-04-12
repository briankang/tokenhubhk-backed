// Package mcp - MCP 资源注册模块
// 将 TokenHub 平台数据暴露为 MCP Resources，供 AI 客户端读取
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/service/openapi"
)

// RegisterAllResources 注册所有平台数据为 MCP 资源
func RegisterAllResources(server *MCPServer, db *gorm.DB, openSvc *openapi.OpenAPIService) {
	// tokenhub://models — 模型列表资源（JSON 格式）
	server.RegisterResource(MCPResource{
		URI:         "tokenhub://models",
		Name:        "AI 模型列表",
		Description: "TokenHub 平台所有可用的 AI 模型列表及详情",
		MimeType:    "application/json",
		Handler: func(userID uint) (string, error) {
			var models []model.AIModel
			err := db.Preload("Supplier").Preload("Category").
				Where("is_active = ?", true).Order("model_name ASC").
				Find(&models).Error
			if err != nil {
				return "", fmt.Errorf("查询模型列表失败: %w", err)
			}

			result := make([]map[string]interface{}, 0, len(models))
			for _, m := range models {
				item := map[string]interface{}{
					"id":             m.ID,
					"model_name":     m.ModelName,
					"display_name":   m.DisplayName,
					"max_tokens":     m.MaxTokens,
					"context_window": m.ContextWindow,
				}
				if m.Supplier.ID > 0 {
					item["supplier"] = m.Supplier.Name
				}
				if m.Category.ID > 0 {
					item["category"] = m.Category.Name
				}
				result = append(result, item)
			}
			data, _ := json.MarshalIndent(result, "", "  ")
			return string(data), nil
		},
	})

	// tokenhub://pricing — 定价信息资源（JSON 格式）
	server.RegisterResource(MCPResource{
		URI:         "tokenhub://pricing",
		Name:        "模型定价信息",
		Description: "所有模型的输入/输出价格、币种信息",
		MimeType:    "application/json",
		Handler: func(userID uint) (string, error) {
			pricingList, err := openSvc.GetModelPricingList(context.Background())
			if err != nil {
				return "", fmt.Errorf("查询定价信息失败: %w", err)
			}
			data, _ := json.MarshalIndent(pricingList, "", "  ")
			return string(data), nil
		},
	})

	// tokenhub://balance — 用户余额资源（JSON 格式）
	server.RegisterResource(MCPResource{
		URI:         "tokenhub://balance",
		Name:        "用户余额",
		Description: "当前用户的余额、免费额度、消费总额等信息",
		MimeType:    "application/json",
		Handler: func(userID uint) (string, error) {
			info, err := openSvc.GetBalance(context.Background(), userID)
			if err != nil {
				return "", fmt.Errorf("查询余额失败: %w", err)
			}
			data, _ := json.MarshalIndent(info, "", "  ")
			return string(data), nil
		},
	})

	// tokenhub://usage/today — 今日用量资源（JSON 格式）
	server.RegisterResource(MCPResource{
		URI:         "tokenhub://usage/today",
		Name:        "今日用量统计",
		Description: "当前用户今日的 API 调用量和 Token 消耗统计",
		MimeType:    "application/json",
		Handler: func(userID uint) (string, error) {
			today := time.Now().Format("2006-01-02")
			stats, err := openSvc.GetUsageStatistics(context.Background(), userID, today, today)
			if err != nil {
				return "", fmt.Errorf("查询今日用量失败: %w", err)
			}

			// 汇总今日总量
			var totalReqs int64
			var totalTokens int64
			for _, s := range stats {
				totalReqs += s.TotalRequests
				totalTokens += s.TotalTokens
			}

			result := map[string]interface{}{
				"date":           today,
				"total_requests": totalReqs,
				"total_tokens":   totalTokens,
				"by_model":       stats,
			}
			data, _ := json.MarshalIndent(result, "", "  ")
			return string(data), nil
		},
	})

	// tokenhub://docs/{slug} — 文档内容资源（Markdown 格式）
	server.RegisterResource(MCPResource{
		URI:         "tokenhub://docs/{slug}",
		Name:        "平台文档",
		Description: "TokenHub 平台文档内容（Markdown 格式），通过 slug 访问",
		MimeType:    "text/markdown",
		Handler: func(userID uint) (string, error) {
			// 当请求模板 URI 时，返回文档列表目录
			var articles []model.DocArticle
			err := db.Where("is_published = ?", true).
				Order("sort_order ASC").Find(&articles).Error
			if err != nil {
				return "", fmt.Errorf("查询文档列表失败: %w", err)
			}

			var sb strings.Builder
			sb.WriteString("# TokenHub 文档目录\n\n")
			for _, a := range articles {
				sb.WriteString(fmt.Sprintf("- [%s](tokenhub://docs/%s) — %s\n", a.Title, a.Slug, a.Summary))
			}
			return sb.String(), nil
		},
	})

	// 为每个已发布文档注册独立的资源（用于精确 URI 匹配）
	registerDocResources(server, db)
}

// registerDocResources 为所有已发布文档注册独立的 MCP 资源
func registerDocResources(server *MCPServer, db *gorm.DB) {
	var articles []model.DocArticle
	err := db.Where("is_published = ?", true).Find(&articles).Error
	if err != nil {
		return // 启动时查询失败不阻塞
	}

	for _, article := range articles {
		// 闭包捕获 slug
		slug := article.Slug
		title := article.Title
		server.RegisterResource(MCPResource{
			URI:         "tokenhub://docs/" + slug,
			Name:        title,
			Description: fmt.Sprintf("文档: %s", title),
			MimeType:    "text/markdown",
			Handler: func(userID uint) (string, error) {
				var a model.DocArticle
				if err := db.Where("slug = ? AND is_published = ?", slug, true).First(&a).Error; err != nil {
					return "", fmt.Errorf("文档未找到: %s", slug)
				}
				return a.Content, nil
			},
		})
	}
}
