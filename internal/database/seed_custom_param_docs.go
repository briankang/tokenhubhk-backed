package database

import (
	"strings"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

const customParamsPassthroughSlug = "custom-params-passthrough"

// RunCleanPlaceholderDocCategories removes generated placeholder doc categories
// such as "Category cat_1776..." when they do not own any article.
func RunCleanPlaceholderDocCategories(db *gorm.DB) {
	if db == nil {
		return
	}
	var cats []model.DocCategory
	if err := db.Where("slug LIKE ? OR name LIKE ?", "cat_%", "Category cat_%").Find(&cats).Error; err != nil {
		logger.L.Warn("clean placeholder doc categories: query failed", zap.Error(err))
		return
	}
	for _, cat := range cats {
		if !isGeneratedPlaceholderDocCategory(cat) {
			continue
		}
		var articles, docs int64
		db.Model(&model.DocArticle{}).Where("category_id = ?", cat.ID).Count(&articles)
		db.Model(&model.Doc{}).Where("category_id = ?", cat.ID).Count(&docs)
		if articles == 0 && docs == 0 {
			if err := db.Delete(&cat).Error; err != nil {
				logger.L.Warn("clean placeholder doc category: delete failed", zap.Uint("id", cat.ID), zap.Error(err))
			}
		}
	}
}

func isGeneratedPlaceholderDocCategory(cat model.DocCategory) bool {
	slug := strings.TrimSpace(strings.ToLower(cat.Slug))
	name := strings.TrimSpace(strings.ToLower(cat.Name))
	return strings.HasPrefix(slug, "cat_") || strings.HasPrefix(name, "category cat_")
}

func RunDocArticleLocaleMigration(db *gorm.DB) {
	if db == nil {
		return
	}
	if db.Migrator().HasIndex(&model.DocArticle{}, "idx_doc_articles_slug") {
		if err := db.Migrator().DropIndex(&model.DocArticle{}, "idx_doc_articles_slug"); err != nil {
			logger.L.Warn("doc article locale migration: drop old slug index failed", zap.Error(err))
		}
	}
	if err := db.AutoMigrate(&model.DocArticle{}); err != nil {
		logger.L.Warn("doc article locale migration: automigrate failed", zap.Error(err))
	}
}

// RunSeedCustomParamDocs keeps the legacy passthrough page unpublished.
// The current user-facing docs include this guide as "custom-params" inside
// the Playground category, so the old api-reference page must not recreate an
// unsupported public docs category after RunSeedDocs synchronizes the catalog.
func RunSeedCustomParamDocs(db *gorm.DB) {
	if db == nil {
		return
	}
	if err := db.Model(&model.DocArticle{}).
		Where("slug = ?", customParamsPassthroughSlug).
		Update("is_published", false).Error; err != nil {
		logger.L.Warn("seed custom params doc: unpublish legacy page failed", zap.Error(err))
	}
}

const contentCustomParamsPassthrough = `## 自定义参数透传逻辑

TokenHubHK 对外保持 OpenAI 兼容请求形态，同时允许开发者把少量供应商特有参数透传给上游。透传只改变请求体里的扩展字段，不改变用户侧认证方式。

### 认证边界

所有用户请求都必须使用 TokenHubHK 平台密钥：

` + "```http" + `
Authorization: Bearer sk-your-tokenhubhk-api-key
Content-Type: application/json
` + "```" + `

不要在用户请求里放供应商 API Key、签名字段、Secret Key 或上游 Authorization。供应商密钥只由管理员在渠道中配置，平台路由到渠道后再按渠道认证方式生成上游请求。

### 三层参数来源

TokenHubHK 合并上游请求体时按以下顺序处理：

1. 平台标准参数：例如 ` + "`model`" + `、` + "`messages`" + `、` + "`temperature`" + `、` + "`top_p`" + `、` + "`stream`" + `、` + "`stream_options`" + `、` + "`max_tokens`" + ` 或 ` + "`max_completion_tokens`" + `。
2. 平台标准参数映射：对深度思考、联网搜索、JSON 输出、工具调用等能力，平台会先按模型和供应商映射成上游可识别字段。
3. 显式透传参数：` + "`provider_extra`" + `、` + "`extra_body`" + `、` + "`custom_params`" + ` 里的键值会在映射后合并，因此字段名和值会尽量原样到达上游。

当同名字段重复时，后合并的显式透传参数会覆盖前面映射出的同名字段。生产环境建议只透传供应商特有字段，避免覆盖平台已经管理的计费、模型、消息和流式控制字段。

### 用户请求级透传

开发者可以在 ` + "`/v1/chat/completions`" + ` 请求体中使用下面三个包装字段之一：

` + "```json" + `
{
  "model": "your-model",
  "messages": [
    {"role": "user", "content": "总结这段内容"}
  ],
  "provider_extra": {
    "enable_search": true,
    "thinking_budget": 2048
  }
}
` + "```" + `

` + "`provider_extra`" + `、` + "`extra_body`" + `、` + "`custom_params`" + ` 的效果一致：它们必须是 JSON object，平台会移除包装层，把内部字段合并到最终上游请求体。

### 渠道级 CustomParams

管理员也可以在渠道配置里设置 ` + "`custom_params`" + `，用于固定上游请求头或请求体参数：

` + "```json" + `
{
  "headers": {
    "X-Custom-Trace": "tokenhubhk"
  },
  "extra_body": {
    "metadata": {
      "source": "tokenhubhk"
    }
  }
}
` + "```" + `

渠道级 ` + "`headers`" + ` 会合并到上游 HTTP Header，但不会覆盖平台生成的认证 Header。渠道级 ` + "`extra_body`" + ` 会被展开并合并到上游请求体。百度千帆等需要 ` + "`client_secret`" + ` 的供应商，由管理员放在渠道级 ` + "`custom_params`" + `，不要由普通用户传入。

### 保留字段

这些字段由平台控制，不建议通过透传覆盖：

| 字段 | 原因 |
| --- | --- |
| ` + "`model`" + ` | 决定路由、计费、模型权限和可用性校验 |
| ` + "`messages`" + ` | 平台需要用于上下文、审计、计费和协议适配 |
| ` + "`stream`" + ` / ` + "`stream_options`" + ` | 影响 SSE 输出和 usage 回传 |
| ` + "`max_tokens`" + ` / ` + "`max_completion_tokens`" + ` | 影响额度预估、TPM 和费用 |
| ` + "`Authorization`" + ` / ` + "`api_key`" + ` / ` + "`secret_key`" + ` | 用户侧禁止传上游密钥或签名材料 |

如果确实需要供应商特有能力，应优先使用模型文档页中标记为 ` + "`platform_mapped`" + ` 或 ` + "`official_confirmed`" + ` 的字段；只有官方文档已确认且平台尚未标准化的字段，才放入透传对象。

### 合并示例

用户请求：

` + "```json" + `
{
  "model": "qwen-plus",
  "messages": [{"role": "user", "content": "联网查一下最新信息"}],
  "temperature": 0.3,
  "provider_extra": {
    "enable_search": true
  }
}
` + "```" + `

平台处理结果：

1. 校验 TokenHubHK API Key。
2. 根据模型选择可用渠道。
3. 提取标准字段用于权限、余额、限流和计费。
4. 将深度思考、联网搜索等平台标准能力按供应商映射。
5. 把 ` + "`provider_extra.enable_search`" + ` 合并到上游请求体。
6. 使用管理员配置的渠道密钥和认证方式请求上游。

### 验证标准

- 字段必须来自供应商官方文档，或在 TokenHubHK 参数核验表中标记为可用。
- 能走平台标准参数映射的能力，不要重复走透传。
- 透传参数必须是 JSON object，空 key 会被忽略。
- 不要透传认证、签名、计费、路由、模型选择等平台治理字段。
- 上线前用 Playground 或模型 API 文档页提供的示例请求验证真实返回。
`
