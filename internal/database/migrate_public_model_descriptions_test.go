package database

import "testing"

func TestCleanPublicModelDescriptionRemovesProxyCopy(t *testing.T) {
	got := cleanPublicModelDescription(
		"Gemini 3.1 Pro (Preview) - 经网宿网关代理。官网价 $2.0000/$12.0000 × 汇率 7.10；合同折扣 0.795 单独体现在折后成本。官方对标：gemini-3.1-pro-preview (official tiered, keep only for Wangsu compatibility)。",
		"Gemini 3.1 Pro (Preview)",
		"gemini.gemini-3.1-pro-preview",
	)
	if got != "Gemini 3.1 Pro (Preview)官方对标：gemini-3.1-pro-preview (official tiered)" {
		t.Fatalf("cleaned description = %q", got)
	}
}

func TestCleanPublicModelDescriptionDropsIdentifierOnlyCopy(t *testing.T) {
	got := cleanPublicModelDescription(
		"Gemini 3.1 Pro (Preview) - 经网宿网关代理。",
		"Gemini 3.1 Pro (Preview)",
		"gemini.gemini-3.1-pro-preview",
	)
	if got != "" {
		t.Fatalf("identifier-only cleaned description = %q, want empty fallback marker", got)
	}
}

func TestCleanPublicModelDescriptionRemovesEnglishProviderAndCostCopy(t *testing.T) {
	got := cleanPublicModelDescription(
		"Vidu Q3 Pro video generation via Wangsu AI Gateway. Default price uses official 720p general video price; cost is official API price x 0.8 and selling price equals official API price",
		"Vidu Q3 Pro",
		"viduq3-pro",
	)
	if got != "Vidu Q3 Pro video generation" {
		t.Fatalf("english cleaned description = %q", got)
	}
}

func TestCleanPublicModelDescriptionRemovesImageGatewayAndPricingCopy(t *testing.T) {
	got := cleanPublicModelDescription(
		"GPT Image 1.5 AI 网关（网关ID rg66wsl2，通道 coze-gpt-image）。官网价 $0.034000/张（1024x1024 medium），成本按 8 折=0.193120 元/张，售价与官网价一致=0.241400 元/张。来源：https://developers.openai.com/api/docs/models/gpt-image-1.5；OpenAI official per-image price for medium 1024x1024 generation",
		"GPT Image 1.5",
		"gpt-image-1.5",
	)
	if got != "" {
		t.Fatalf("image gateway cleaned description = %q, want empty fallback marker", got)
	}

	got = cleanPublicModelDescription(
		"FLUX.1 [schnell] AI 网关（网关ID rg66wsl2，通道 coze-gpt-image）。官网价 $0.002831/张（1024x1024），来源：https://www.together.ai/pricing；Together AI official price is $0.0027/MP; 1024x1024 is about 1.048576MP",
		"FLUX.1 [schnell]",
		"flux.1-schnell",
	)
	if got != "" {
		t.Fatalf("image dimension pricing cleaned description = %q, want empty fallback marker", got)
	}
}

func TestCleanPublicModelDescriptionRemovesWangsuNamingCopy(t *testing.T) {
	got := cleanPublicModelDescription(
		"GPT-5.4 mini官方对标：gpt-5-mini (official, Wangsu 自有命名)",
		"GPT-5.4 mini",
		"gpt-5.4-mini",
	)
	if got != "GPT-5.4 mini官方对标：gpt-5-mini (official)" {
		t.Fatalf("wangsu naming cleaned description = %q", got)
	}
}
