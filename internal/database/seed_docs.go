package database

import (
	"errors"
	"fmt"
	"strings"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

type docCategoryDef struct {
	Name        string
	Slug        string
	Icon        string
	Description string
	ParentSlug  string
	SortOrder   int
}

type docArticleDef struct {
	CatSlug string
	Locale  string
	Title   string
	Slug    string
	Summary string
	Tags    string
	Sort    int
	Content string
}

var userDocCategories = []docCategoryDef{
	{Name: "快速开始", Slug: "getting-started", Icon: "rocket", Description: "从注册账号到完成第一次调用", SortOrder: 10},
	{Name: "账号与计费", Slug: "account-billing", Icon: "book", Description: "账号安全、API Key、充值、余额和账单", SortOrder: 20},
	{Name: "模型与价格", Slug: "models-pricing", Icon: "plug", Description: "查看模型、选择模型并理解计费方式", SortOrder: 30},
	{Name: "Playground", Slug: "playground", Icon: "message-circle", Description: "在网页中调试模型和生成可复用请求", SortOrder: 40},
	{Name: "API 调用", Slug: "api-usage", Icon: "terminal", Description: "使用 OpenAI 兼容接口接入 TokenHub", SortOrder: 50},
	{Name: "第三方客户端", Slug: "client-integration", Icon: "cpu", Description: "把 TokenHub 配置到常用聊天和开发客户端", SortOrder: 60},
	{Name: "常见问题", Slug: "help", Icon: "book", Description: "排查认证、余额、模型和响应问题", SortOrder: 70},
}

var userDocArticles = []docArticleDef{
	{
		CatSlug: "getting-started",
		Locale:  "zh",
		Title:   "新用户快速开始",
		Slug:    "quick-start",
		Summary: "完成注册、充值、创建 API Key，并发起第一条模型请求。",
		Tags:    "入门,注册,API Key,首次调用",
		Sort:    10,
		Content: contentQuickStart,
	},
	{
		CatSlug: "account-billing",
		Locale:  "zh",
		Title:   "注册、登录与账号安全",
		Slug:    "account-security",
		Summary: "说明账号创建、登录、密码安全和 OAuth 登录注意事项。",
		Tags:    "账号,登录,安全,OAuth",
		Sort:    10,
		Content: contentAccountSecurity,
	},
	{
		CatSlug: "account-billing",
		Locale:  "zh",
		Title:   "API Key 创建与管理",
		Slug:    "api-keys",
		Summary: "创建、保存、轮换和删除 API Key 的完整步骤。",
		Tags:    "API Key,密钥,安全",
		Sort:    20,
		Content: contentApiKeys,
	},
	{
		CatSlug: "account-billing",
		Locale:  "zh",
		Title:   "充值、余额与账单",
		Slug:    "balance",
		Summary: "了解充值入口、余额扣费、体验额度、账单记录和余额不足处理。",
		Tags:    "充值,余额,账单,额度",
		Sort:    30,
		Content: contentBalance,
	},
	{
		CatSlug: "models-pricing",
		Locale:  "zh",
		Title:   "查看模型与选择模型",
		Slug:    "choose-models",
		Summary: "按用途筛选模型，确认上下文、能力标签和价格。",
		Tags:    "模型,价格,能力,上下文",
		Sort:    10,
		Content: contentChooseModels,
	},
	{
		CatSlug: "models-pricing",
		Locale:  "zh",
		Title:   "价格与用量规则",
		Slug:    "pricing-usage",
		Summary: "解释输入、输出、流式响应、失败请求和余额扣减的常见规则。",
		Tags:    "价格,用量,Token,扣费",
		Sort:    20,
		Content: contentPricingUsage,
	},
	{
		CatSlug: "playground",
		Locale:  "zh",
		Title:   "Playground 调试模型",
		Slug:    "playground",
		Summary: "在网页中选择模型、调整参数、发送消息并查看请求结果。",
		Tags:    "Playground,调试,参数,请求",
		Sort:    10,
		Content: contentPlayground,
	},
	{
		CatSlug: "playground",
		Locale:  "zh",
		Title:   "高级参数与自定义透传",
		Slug:    "custom-params",
		Summary: "使用 temperature、top_p、extra_body 等参数控制请求。",
		Tags:    "高级参数,extra_body,custom_params",
		Sort:    20,
		Content: contentCustomParams,
	},
	{
		CatSlug: "api-usage",
		Locale:  "zh",
		Title:   "认证方式",
		Slug:    "authentication",
		Summary: "用户登录 Token 与模型调用 API Key 的区别和使用位置。",
		Tags:    "认证,Bearer,JWT,API Key",
		Sort:    10,
		Content: contentAuth,
	},
	{
		CatSlug: "api-usage",
		Locale:  "zh",
		Title:   "Chat Completions API",
		Slug:    "chat-api",
		Summary: "使用 /v1/chat/completions 发起非流式和流式对话请求。",
		Tags:    "Chat API,OpenAI,stream",
		Sort:    20,
		Content: contentChatAPI,
	},
	{
		CatSlug: "api-usage",
		Locale:  "zh",
		Title:   "查询模型列表",
		Slug:    "models-api",
		Summary: "通过 /v1/models 获取当前账号可调用的模型。",
		Tags:    "模型列表,/v1/models",
		Sort:    30,
		Content: contentModelsAPI,
	},
	{
		CatSlug: "client-integration",
		Locale:  "zh",
		Title:   "通用 OpenAI 兼容配置",
		Slug:    "openai-compatible-clients",
		Summary: "把 TokenHub 作为 OpenAI Compatible 服务配置到第三方客户端。",
		Tags:    "客户端,OpenAI Compatible,Base URL",
		Sort:    10,
		Content: contentOpenAICompatibleClients,
	},
	{
		CatSlug: "client-integration",
		Locale:  "zh",
		Title:   "LobeChat / Open WebUI 接入",
		Slug:    "chat-clients",
		Summary: "常用聊天客户端的 Base URL、API Key 和模型配置步骤。",
		Tags:    "LobeChat,Open WebUI,NextChat",
		Sort:    20,
		Content: contentChatClients,
	},
	{
		CatSlug: "client-integration",
		Locale:  "zh",
		Title:   "Cursor / Continue 接入",
		Slug:    "dev-clients",
		Summary: "在开发客户端中使用 TokenHub 的通用对话接口。",
		Tags:    "Cursor,Continue,开发客户端",
		Sort:    30,
		Content: contentDevClients,
	},
	{
		CatSlug: "help",
		Locale:  "zh",
		Title:   "错误码与排查",
		Slug:    "error-codes",
		Summary: "按状态码排查认证失败、余额不足、限流和模型不可用。",
		Tags:    "错误码,排查,401,402,429",
		Sort:    10,
		Content: contentErrorCodes,
	},
	{
		CatSlug: "help",
		Locale:  "zh",
		Title:   "常见问题",
		Slug:    "faq",
		Summary: "用户使用过程中最常见问题的处理方法。",
		Tags:    "FAQ,问题,帮助",
		Sort:    20,
		Content: contentFAQ,
	},
}

var userDocArticlesAll = append(userDocArticles, userDocArticlesEN...)

// RunSeedDocs 同步公开文档为面向用户的使用手册。
func RunSeedDocs(db *gorm.DB) {
	logger.L.Info("seed_docs: 开始同步用户文档")

	if err := ensureDocArticleLocaleIndex(db); err != nil {
		logger.L.Error("seed_docs: 文档多语言索引修复失败", zap.Error(err))
		return
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := seedDocCategories(tx); err != nil {
			return fmt.Errorf("categories: %w", err)
		}
		if err := seedDocArticles(tx); err != nil {
			return fmt.Errorf("articles: %w", err)
		}
		if err := pruneUnsupportedPublicDocs(tx); err != nil {
			return fmt.Errorf("prune: %w", err)
		}
		return nil
	}); err != nil {
		logger.L.Error("seed_docs: 用户文档同步失败", zap.Error(err))
		return
	}

	logger.L.Info("seed_docs: 用户文档同步完成", zap.Int("articles", len(userDocArticlesAll)))
}

func ensureDocArticleLocaleIndex(db *gorm.DB) error {
	if db.Migrator().HasIndex(&model.DocArticle{}, "idx_doc_articles_slug") {
		if err := db.Migrator().DropIndex(&model.DocArticle{}, "idx_doc_articles_slug"); err != nil {
			return fmt.Errorf("drop legacy slug index: %w", err)
		}
	}
	if !db.Migrator().HasIndex(&model.DocArticle{}, "uidx_doc_article_slug_locale") {
		if err := db.Migrator().CreateIndex(&model.DocArticle{}, "uidx_doc_article_slug_locale"); err != nil {
			return fmt.Errorf("create slug locale index: %w", err)
		}
	}
	return nil
}

func seedDocCategories(tx *gorm.DB) error {
	managedIDs := map[string]uint{}
	for _, c := range userDocCategories {
		var parentID *uint
		if c.ParentSlug != "" {
			id, ok := managedIDs[c.ParentSlug]
			if !ok {
				return fmt.Errorf("parent category %s not found", c.ParentSlug)
			}
			parentID = &id
		}

		var cat model.DocCategory
		err := tx.Where("slug = ?", c.Slug).First(&cat).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				cat = model.DocCategory{Slug: c.Slug}
				applyDocCategoryDef(&cat, c, parentID)
				if err := tx.Create(&cat).Error; err != nil {
					return fmt.Errorf("create category %s: %w", c.Slug, err)
				}
				managedIDs[c.Slug] = cat.ID
				continue
			}
			return fmt.Errorf("find category %s: %w", c.Slug, err)
		}

		applyDocCategoryDef(&cat, c, parentID)
		if err := tx.Save(&cat).Error; err != nil {
			return fmt.Errorf("update category %s: %w", c.Slug, err)
		}
		managedIDs[c.Slug] = cat.ID
	}
	return nil
}

func applyDocCategoryDef(cat *model.DocCategory, def docCategoryDef, parentID *uint) {
	cat.Name = def.Name
	cat.Slug = def.Slug
	cat.Icon = def.Icon
	cat.Description = def.Description
	cat.ParentID = parentID
	cat.SortOrder = def.SortOrder
}

func seedDocArticles(tx *gorm.DB) error {
	categoryIDs := map[string]uint{}
	var cats []model.DocCategory
	if err := tx.Where("slug IN ?", managedCategorySlugs()).Find(&cats).Error; err != nil {
		return fmt.Errorf("load categories: %w", err)
	}
	for _, cat := range cats {
		categoryIDs[cat.Slug] = cat.ID
	}

	for _, a := range userDocArticlesAll {
		catID, ok := categoryIDs[a.CatSlug]
		if !ok {
			return fmt.Errorf("category %s not found", a.CatSlug)
		}

		var article model.DocArticle
		locale := normalizeDocSeedLocale(a.Locale)
		err := tx.Where("slug = ? AND locale = ?", a.Slug, locale).First(&article).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				article = model.DocArticle{Slug: a.Slug}
				applyDocArticleDef(&article, a, catID)
				if err := tx.Create(&article).Error; err != nil {
					return fmt.Errorf("create article %s: %w", a.Slug, err)
				}
				continue
			}
			return fmt.Errorf("find article %s: %w", a.Slug, err)
		}

		applyDocArticleDef(&article, a, catID)
		if err := tx.Save(&article).Error; err != nil {
			return fmt.Errorf("update article %s: %w", a.Slug, err)
		}
	}
	return nil
}

func applyDocArticleDef(article *model.DocArticle, def docArticleDef, catID uint) {
	article.CategoryID = catID
	article.Title = def.Title
	article.Content = def.Content
	article.Summary = def.Summary
	article.Tags = def.Tags
	article.Locale = normalizeDocSeedLocale(def.Locale)
	article.SortOrder = def.Sort
	article.IsPublished = true
}

func normalizeDocSeedLocale(locale string) string {
	switch locale {
	case "en":
		return "en"
	case "zh-TW":
		return "zh-TW"
	default:
		return "zh"
	}
}

func pruneUnsupportedPublicDocs(tx *gorm.DB) error {
	conditions := make([]string, 0, len(userDocArticlesAll))
	args := make([]interface{}, 0, len(userDocArticlesAll)*2)
	for _, a := range userDocArticlesAll {
		conditions = append(conditions, "(slug = ? AND locale = ?)")
		args = append(args, a.Slug, normalizeDocSeedLocale(a.Locale))
	}
	if err := tx.Model(&model.DocArticle{}).
		Where("NOT ("+strings.Join(conditions, " OR ")+")", args...).
		Update("is_published", false).Error; err != nil {
		return err
	}

	if err := tx.Where("slug NOT IN ?", managedCategorySlugs()).Delete(&model.DocCategory{}).Error; err != nil {
		return err
	}
	return nil
}

func managedCategorySlugs() []string {
	slugs := make([]string, 0, len(userDocCategories))
	for _, c := range userDocCategories {
		slugs = append(slugs, c.Slug)
	}
	return slugs
}

func managedArticleSlugs() []string {
	slugs := make([]string, 0, len(userDocArticlesAll))
	for _, a := range userDocArticlesAll {
		slugs = append(slugs, a.Slug)
	}
	return slugs
}
