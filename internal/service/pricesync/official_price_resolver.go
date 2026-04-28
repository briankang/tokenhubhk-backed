// Package pricesync 提供与供应商官方定价页相关的解析能力。
//
// 主入口 ResolveOfficialPriceURL 按以下优先级返回模型的官方定价页 URL:
//  1. AIModel.OfficialPriceURL  - 管理员在模型级别手动覆盖
//  2. Supplier.PricingURLs      - 多页配置中按 type_hint 匹配模型类型
//  3. Supplier.PricingURL       - 供应商默认定价页
//
// 全部为空时返回 "",前端隐藏「打开官网」按钮。
package pricesync

import (
	"context"
	"encoding/json"
	"strings"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

// OfficialPriceURLResult 解析结果。
type OfficialPriceURLResult struct {
	URL    string `json:"url"`    // 解析得到的最终 URL,空表示未配置
	Source string `json:"source"` // 来源标记: model_override / supplier_typed / supplier_default / unset
	Hint   string `json:"hint"`   // 命中的 type_hint(仅 supplier_typed 时有值)
}

// fallbackBySupplierCode 提供主流供应商的官方定价页兜底 URL。
//
// 当数据库 Supplier.PricingURL 为空时使用,确保运营点击「↗ 官网定价页」总能跳到正确位置。
// Key 是 Supplier.Code 的小写形式;扩展时只需追加条目。
var fallbackBySupplierCode = map[string]string{
	"openai":            "https://platform.openai.com/docs/pricing",
	"anthropic":         "https://www.anthropic.com/pricing",
	"google":            "https://ai.google.dev/pricing",
	"google_gemini":     "https://ai.google.dev/pricing",
	"deepseek":          "https://api-docs.deepseek.com/quick_start/pricing/",
	"moonshot":          "https://platform.moonshot.cn/docs/pricing",
	"zhipu":             "https://open.bigmodel.cn/pricing",
	"alibaba_dashscope": "https://help.aliyun.com/zh/model-studio/getting-started/models",
	"aliyun_dashscope":  "https://help.aliyun.com/zh/model-studio/getting-started/models",
	"aliyun":            "https://help.aliyun.com/zh/model-studio/getting-started/models",
	"qwen":              "https://help.aliyun.com/zh/model-studio/getting-started/models",
	"volcengine":        "https://www.volcengine.com/docs/82379/1099320",
	"doubao":            "https://www.volcengine.com/docs/82379/1099320",
	"baidu_qianfan":     "https://cloud.baidu.com/doc/WENXINWORKSHOP/s/Qm0jiijso",
	"baidu":             "https://cloud.baidu.com/doc/WENXINWORKSHOP/s/Qm0jiijso",
	"qianfan":           "https://cloud.baidu.com/doc/WENXINWORKSHOP/s/Qm0jiijso",
	"tencent_hunyuan":   "https://cloud.tencent.com/document/product/1729/97731",
	"hunyuan":           "https://cloud.tencent.com/document/product/1729/97731",
	"wangsu":            "https://www.wangsu.com/ai-gateway",
	"wangsu_aigateway":  "https://www.wangsu.com/ai-gateway",
	"minimax":           "https://www.minimaxi.com/document/price",
	"siliconflow":       "https://siliconflow.cn/pricing",
	"yi":                "https://platform.lingyiwanwu.com/docs#pricing",
	"baichuan":          "https://platform.baichuan-ai.com/price",
	"stepfun":           "https://platform.stepfun.com/docs/pricing/intro",
	"xai":               "https://docs.x.ai/docs/models#models-and-pricing",
	"openrouter":        "https://openrouter.ai/models",
	"groq":              "https://groq.com/pricing/",
	"together":          "https://www.together.ai/pricing",
	"replicate":         "https://replicate.com/pricing",
	"perplexity":        "https://docs.perplexity.ai/guides/pricing",
}

// ResolveOfficialPriceURL 按 model 解析官方定价页 URL。
//
// 解析优先级:
//  1. model_override     - AIModel.OfficialPriceURL 管理员手动覆盖
//  2. supplier_typed     - Supplier.PricingURLs 多页按 type_hint 匹配
//  3. supplier_default   - Supplier.PricingURL 供应商默认页
//  4. supplier_fallback  - 内置 fallbackBySupplierCode 字典(主流供应商兜底)
//  5. unset              - 全部为空,前端隐藏按钮
//
// db 用于在 m.Supplier 未预加载时按需查询 supplier。
// 调用方可传入 nil ctx,内部使用 context.Background。
func ResolveOfficialPriceURL(ctx context.Context, db *gorm.DB, m *model.AIModel) OfficialPriceURLResult {
	if m == nil {
		return OfficialPriceURLResult{Source: "unset"}
	}
	// 1) 模型级覆盖
	if strings.TrimSpace(m.OfficialPriceURL) != "" {
		return OfficialPriceURLResult{URL: m.OfficialPriceURL, Source: "model_override"}
	}

	// 加载 supplier(若未预加载)
	supplier := m.Supplier
	if supplier.ID == 0 && m.SupplierID != 0 && db != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		_ = db.WithContext(ctx).Where("id = ?", m.SupplierID).First(&supplier).Error
	}

	// 2) 多页配置按 type_hint 匹配
	if supplier.ID != 0 && len(supplier.PricingURLs) > 0 && string(supplier.PricingURLs) != "null" {
		var entries []model.PricingURLEntry
		if err := json.Unmarshal(supplier.PricingURLs, &entries); err == nil {
			matched := matchPricingEntry(entries, m.ModelType, m.ModelName)
			if matched != nil && strings.TrimSpace(matched.URL) != "" {
				return OfficialPriceURLResult{
					URL:    matched.URL,
					Source: "supplier_typed",
					Hint:   matched.TypeHint,
				}
			}
		}
	}

	// 3) 供应商默认 URL
	if supplier.ID != 0 && strings.TrimSpace(supplier.PricingURL) != "" {
		return OfficialPriceURLResult{URL: supplier.PricingURL, Source: "supplier_default"}
	}

	// 4) 兜底字典(按 Supplier.Code 小写匹配)
	if supplier.Code != "" {
		code := strings.ToLower(strings.TrimSpace(supplier.Code))
		if url, ok := fallbackBySupplierCode[code]; ok {
			return OfficialPriceURLResult{URL: url, Source: "supplier_fallback", Hint: code}
		}
		// 也尝试按子串匹配(如 "wangsu_xxx" 匹配到 "wangsu")
		for prefix, url := range fallbackBySupplierCode {
			if strings.Contains(code, prefix) {
				return OfficialPriceURLResult{URL: url, Source: "supplier_fallback", Hint: prefix}
			}
		}
	}

	return OfficialPriceURLResult{Source: "unset"}
}

// matchPricingEntry 在 PricingURLs 数组中按 type_hint 寻找最匹配的一条。
//
// 匹配规则(按优先级):
//  1. type_hint 完全等于 model_type
//  2. type_hint 包含模型名子串(如 "qwen-vl" 命中 type_hint="qwen")
//  3. 第一条无 type_hint 的兜底
func matchPricingEntry(entries []model.PricingURLEntry, modelType, modelName string) *model.PricingURLEntry {
	if len(entries) == 0 {
		return nil
	}
	mtLower := strings.ToLower(modelType)
	mnLower := strings.ToLower(modelName)

	var fallback *model.PricingURLEntry
	for i := range entries {
		entry := &entries[i]
		hint := strings.ToLower(strings.TrimSpace(entry.TypeHint))
		if hint == "" {
			if fallback == nil {
				fallback = entry
			}
			continue
		}
		if hint == mtLower {
			return entry
		}
		if strings.Contains(mnLower, hint) {
			return entry
		}
	}
	return fallback
}
