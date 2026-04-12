package database

import (
	"fmt"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// RunSeedDocs 填充文档种子数据（分类 + 文档文章）
// 幂等性：仅当 doc_categories 表为空时才执行
func RunSeedDocs(db *gorm.DB) {
	var count int64
	if err := db.Model(&model.DocCategory{}).Count(&count).Error; err != nil {
		logger.L.Warn("seed_docs: 检查分类数量失败", zap.Error(err))
		return
	}
	if count > 0 {
		logger.L.Info("seed_docs: 文档数据已存在，跳过")
		return
	}

	logger.L.Info("seed_docs: 开始填充文档种子数据...")

	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := seedDocCategories(tx); err != nil {
			return fmt.Errorf("分类: %w", err)
		}
		if err := seedDocArticles(tx); err != nil {
			return fmt.Errorf("文章: %w", err)
		}
		return nil
	}); err != nil {
		logger.L.Error("seed_docs: 事务失败", zap.Error(err))
		return
	}

	logger.L.Info("seed_docs: 文档种子数据填充完成")
}

// seedDocCategories 创建文档分类树（7大一级分类 + 3个二级分类）
func seedDocCategories(tx *gorm.DB) error {
	// 一级分类定义
	type catDef struct {
		Name, Slug, Icon, Description string
		SortOrder                     int
	}
	topCats := []catDef{
		{"快速入门", "getting-started", "rocket", "快速上手 TokenHub 平台", 10},
		{"平台使用指南", "platform-guide", "book", "详细的平台功能使用说明", 20},
		{"AI 工具接入", "tool-integration", "plug", "各类 AI 工具接入 TokenHub 指南", 30},
		{"Coding Plan", "coding-plan", "code", "AI 编码助手聚合方案", 40},
		{"管理员指南", "admin-guide", "settings", "系统管理与配置指南", 50},
		{"API 参考", "api-reference", "terminal", "接口文档与错误码", 60},
		{"部署指南", "deployment", "server", "部署与运维指南", 70},
	}

	for _, c := range topCats {
		cat := model.DocCategory{
			Name:        c.Name,
			Slug:        c.Slug,
			Icon:        c.Icon,
			Description: c.Description,
			SortOrder:   c.SortOrder,
		}
		if err := tx.Create(&cat).Error; err != nil {
			return fmt.Errorf("创建分类 %s: %w", c.Slug, err)
		}
	}

	// 二级分类：AI 工具接入的子分类
	var toolCat model.DocCategory
	if err := tx.Where("slug = ?", "tool-integration").First(&toolCat).Error; err != nil {
		return fmt.Errorf("查找 tool-integration 分类: %w", err)
	}

	subCats := []catDef{
		{"编码工具", "coding-tools", "code", "AI 编码工具接入指南", 31},
		{"对话客户端", "chat-clients", "message-circle", "AI 对话客户端接入指南", 32},
		{"AI Agent 框架", "agent-frameworks", "cpu", "AI Agent 框架接入指南", 33},
	}
	for _, c := range subCats {
		cat := model.DocCategory{
			Name:        c.Name,
			Slug:        c.Slug,
			Icon:        c.Icon,
			Description: c.Description,
			ParentID:    &toolCat.ID,
			SortOrder:   c.SortOrder,
		}
		if err := tx.Create(&cat).Error; err != nil {
			return fmt.Errorf("创建子分类 %s: %w", c.Slug, err)
		}
	}

	return nil
}

// seedDocArticles 创建文档文章内容（15+ 篇）
func seedDocArticles(tx *gorm.DB) error {
	// 辅助函数：根据 slug 查找分类 ID
	findCat := func(slug string) uint {
		var cat model.DocCategory
		if err := tx.Where("slug = ?", slug).First(&cat).Error; err != nil {
			return 0
		}
		return cat.ID
	}

	type articleDef struct {
		CatSlug string
		Title   string
		Slug    string
		Summary string
		Tags    string
		Sort    int
		Content string
	}

	articles := []articleDef{
		// ========== 快速入门 ==========
		{
			CatSlug: "getting-started",
			Title:   "5 分钟快速开始",
			Slug:    "quick-start",
			Summary: "从注册到发送第一个 API 请求，5 分钟上手 TokenHub",
			Tags:    "快速开始,入门,API Key",
			Sort:    10,
			Content: contentQuickStart,
		},

		// ========== 平台使用指南 ==========
		{
			CatSlug: "platform-guide",
			Title:   "API Key 创建与管理",
			Slug:    "api-keys",
			Summary: "创建、管理和安全使用 API Key",
			Tags:    "API Key,密钥,安全",
			Sort:    10,
			Content: contentApiKeys,
		},
		{
			CatSlug: "platform-guide",
			Title:   "Playground 使用指南",
			Slug:    "playground",
			Summary: "在线测试 AI 模型的交互式工具",
			Tags:    "Playground,测试,模型",
			Sort:    20,
			Content: contentPlayground,
		},
		{
			CatSlug: "platform-guide",
			Title:   "余额充值与额度说明",
			Slug:    "balance",
			Summary: "了解余额体系、充值方式和额度规则",
			Tags:    "余额,充值,额度",
			Sort:    30,
			Content: contentBalance,
		},

		// ========== 平台使用指南 - 分销文档 ==========
		{
			CatSlug: "platform-guide",
			Title:   "代理商分销指南",
			Slug:    "agent-distribution",
			Summary: "三级代理佣金体系、代理申请流程、佣金结算规则",
			Tags:    "代理,分销,佣金,三级",
			Sort:    40,
			Content: contentAgentDistribution,
		},
		{
			CatSlug: "platform-guide",
			Title:   "个人邀请返现指南",
			Slug:    "personal-referral",
			Summary: "邀请码获取、分享方式、返现比例、提现规则",
			Tags:    "邀请,返现,推荐,奖励",
			Sort:    50,
			Content: contentPersonalReferral,
		},

		// ========== AI 工具接入 - 编码工具 ==========
		{
			CatSlug: "coding-tools",
			Title:   "Cursor 接入 TokenHub",
			Slug:    "cursor-guide",
			Summary: "在 Cursor 中配置 TokenHub 自定义模型端点",
			Tags:    "Cursor,编码,IDE",
			Sort:    10,
			Content: contentCursor,
		},
		{
			CatSlug: "coding-tools",
			Title:   "Continue.dev 接入指南",
			Slug:    "continue-guide",
			Summary: "在 Continue.dev 中配置 TokenHub 作为模型提供商",
			Tags:    "Continue,VS Code,编码",
			Sort:    20,
			Content: contentContinue,
		},
		{
			CatSlug: "coding-tools",
			Title:   "Cline 接入指南",
			Slug:    "cline-guide",
			Summary: "在 Cline (Claude Dev) 中使用 TokenHub API",
			Tags:    "Cline,VS Code,Agent",
			Sort:    30,
			Content: contentCline,
		},

		// ========== AI 工具接入 - 对话客户端 ==========
		{
			CatSlug: "chat-clients",
			Title:   "LobeChat 接入指南",
			Slug:    "lobechat-guide",
			Summary: "在 LobeChat 中配置 TokenHub 作为 OpenAI 兼容端点",
			Tags:    "LobeChat,对话,部署",
			Sort:    10,
			Content: contentLobeChat,
		},
		{
			CatSlug: "chat-clients",
			Title:   "ChatGPT-Next-Web 接入指南",
			Slug:    "nextchat-guide",
			Summary: "在 NextChat 中配置 TokenHub API 端点",
			Tags:    "NextChat,ChatGPT,Web",
			Sort:    20,
			Content: contentNextChat,
		},
		{
			CatSlug: "chat-clients",
			Title:   "Open WebUI 接入指南",
			Slug:    "openwebui-guide",
			Summary: "在 Open WebUI 中添加 TokenHub 作为 OpenAI 连接",
			Tags:    "Open WebUI,Docker,Ollama",
			Sort:    30,
			Content: contentOpenWebUI,
		},

		// ========== Coding Plan ==========
		{
			CatSlug: "coding-plan",
			Title:   "Coding Plan 产品介绍",
			Slug:    "coding-plan-intro",
			Summary: "TokenHub Coding Plan：为编码工具定制的 AI 模型聚合方案",
			Tags:    "Coding Plan,产品,编码",
			Sort:    10,
			Content: contentCodingPlan,
		},

		// ========== API 参考 ==========
		{
			CatSlug: "api-reference",
			Title:   "认证方式",
			Slug:    "authentication",
			Summary: "JWT Token、API Key 和 Bearer Token 认证说明",
			Tags:    "认证,JWT,API Key,Bearer",
			Sort:    10,
			Content: contentAuth,
		},
		{
			CatSlug: "api-reference",
			Title:   "Chat Completions API",
			Slug:    "chat-api",
			Summary: "OpenAI 兼容的 Chat Completions 接口参考",
			Tags:    "Chat,API,OpenAI,流式",
			Sort:    20,
			Content: contentChatAPI,
		},
		{
			CatSlug: "api-reference",
			Title:   "Open API 企业接口",
			Slug:    "open-api",
			Summary: "企业级 Open API 接口：消费、用量、余额查询",
			Tags:    "Open API,企业,消费,用量",
			Sort:    30,
			Content: contentOpenAPI,
		},
		{
			CatSlug: "api-reference",
			Title:   "错误码参考",
			Slug:    "error-codes",
			Summary: "API 错误码与状态码说明",
			Tags:    "错误码,HTTP,状态码",
			Sort:    40,
			Content: contentErrorCodes,
		},

		// ========== 部署指南 ==========
		{
			CatSlug: "deployment",
			Title:   "Docker Compose 一键部署",
			Slug:    "docker-deploy",
			Summary: "使用 Docker Compose 快速部署 TokenHub 平台",
			Tags:    "Docker,部署,Compose",
			Sort:    10,
			Content: contentDockerDeploy,
		},
		{
			CatSlug: "deployment",
			Title:   "手动部署与环境变量",
			Slug:    "manual-deploy",
			Summary: "手动部署步骤与完整环境变量参考",
			Tags:    "手动部署,环境变量,配置",
			Sort:    20,
			Content: contentManualDeploy,
		},
	}

	for _, a := range articles {
		catID := findCat(a.CatSlug)
		if catID == 0 {
			return fmt.Errorf("分类 %s 不存在", a.CatSlug)
		}
		article := model.DocArticle{
			CategoryID:  catID,
			Title:       a.Title,
			Slug:        a.Slug,
			Content:     a.Content,
			Summary:     a.Summary,
			Tags:        a.Tags,
			Locale:      "zh",
			SortOrder:   a.Sort,
			IsPublished: true,
		}
		if err := tx.Create(&article).Error; err != nil {
			return fmt.Errorf("创建文章 %s: %w", a.Slug, err)
		}
	}

	return nil
}
