package admin

import (
	"strings"
	"testing"

	"tokenhub-server/internal/model"
)

func TestPublicModelDescriptionRemovesSupplierProxyCopy(t *testing.T) {
	item := model.AIModel{
		ModelName:     "gemini-3.1-pro-preview",
		DisplayName:   "Gemini 3.1 Pro (Preview)",
		Description:   "Gemini 3.1 Pro (Preview) - 经网宿网关代理。官网价 $2.0000/$12.0000 × 汇率 7.10；合同折扣 0.795 单独体现在折后成本。",
		ModelType:     "LLM",
		ContextWindow: 128000,
		IsActive:      true,
		Status:        "online",
		Supplier: model.Supplier{
			Name: "网宿网关",
			Code: "wangsu_aigw",
		},
	}

	resp := toPublicResponse(item)
	got := resp.Description
	for _, forbidden := range []string{"网宿", "供应商", "代理", "提供的", "/v1/chat/completions", "OpenAI 兼容协议", "官网价", "汇率", "合同折扣", "官方对标", "official"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("public description leaked %q: %s", forbidden, got)
		}
	}
	if strings.TrimSpace(got) == "" || strings.EqualFold(strings.TrimSpace(got), item.DisplayName) {
		t.Fatalf("public description should fall back to model-only intro, got %q", got)
	}
	if !strings.Contains(got, "Gemini") {
		t.Fatalf("public description should still describe model family, got %q", got)
	}
	if resp.DescriptionI18n["zh"] == "" || resp.DescriptionI18n["en"] == "" {
		t.Fatalf("description_i18n should include zh and en, got %#v", resp.DescriptionI18n)
	}
	for _, text := range []string{resp.DescriptionI18n["zh"], resp.DescriptionI18n["en"]} {
		for _, forbidden := range []string{"网宿", "代理", "官网价", "汇率", "合同折扣", "官方对标", "official", "exchange rate"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("localized public description leaked %q: %s", forbidden, text)
			}
		}
	}
	if hasCJK(resp.DescriptionI18n["en"]) {
		t.Fatalf("english public description should not contain CJK text: %s", resp.DescriptionI18n["en"])
	}
}
