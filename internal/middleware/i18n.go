package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/config"
	pkgi18n "tokenhub-server/internal/pkg/i18n"
)

// I18n 国际化中间件，解析 Accept-Language 请求头并将翻译器写入上下文
func I18n() gin.HandlerFunc {
	return func(c *gin.Context) {
		lang := parseLang(c)
		c.Set("lang", lang)
		c.Set("i18n_msg", pkgi18n.NewTranslator(lang))
		c.Next()
	}
}

// parseLang 从请求中提取首选语言
// 优先级: query 参数 lang → Accept-Language 请求头 → 默认语言
func parseLang(c *gin.Context) string {
	// 优先检查 query 参数
	if lang := c.Query("lang"); lang != "" {
		return normalizeLang(lang)
	}

	// 然后检查 Accept-Language 请求头
	accept := c.GetHeader("Accept-Language")
	if accept == "" {
		return config.Global.I18n.DefaultLang
	}

	// 简单解析: 取第一个语言标签
	parts := strings.Split(accept, ",")
	if len(parts) > 0 {
		tag := strings.TrimSpace(parts[0])
		// 移除质量值 (如 "en-US;q=0.9" → "en-US")
		if idx := strings.Index(tag, ";"); idx > 0 {
			tag = tag[:idx]
		}
		return normalizeLang(tag)
	}

	return config.Global.I18n.DefaultLang
}

// normalizeLang 标准化语言编码，映射常见变体到主语言码
func normalizeLang(lang string) string {
	lang = strings.TrimSpace(strings.ToLower(lang))
	// 映射常见变体
	switch {
	case strings.HasPrefix(lang, "zh"):
		return "zh"
	case strings.HasPrefix(lang, "en"):
		return "en"
	default:
		return lang
	}
}
