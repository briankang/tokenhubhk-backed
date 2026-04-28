package pricescraper

import "testing"

func TestNormalizeModelIDUsesSharedRules(t *testing.T) {
	cases := map[string]string{
		"qwen3.5-omni-plus-realtime-2026-03-15": "qwen3-5-omni-plus-realtime",
		"hunyuan-turbos-20250926":              "hunyuan-turbos",
		"qwen3-tts-instruct-flash-2026-01-26":  "qwen3-tts-instruct-flash",
	}
	for input, want := range cases {
		if got := normalizeModelID(input); got != want {
			t.Fatalf("normalizeModelID(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestHunyuanPrefixLookupMatchesDatedVariants(t *testing.T) {
	scraper := &HunyuanScraper{}
	models := scraper.mergeModels(
		[]hunyuanAPIModel{
			{ID: "hunyuan-turbos-20250926"},
			{ID: "hunyuan-t1-vision-20250916"},
			{ID: "hunyuan-large-role-latest"},
		},
		getHunyuanSupplementaryPrices(),
	)
	got := map[string]ScrapedModel{}
	for _, m := range models {
		got[m.ModelName] = m
	}

	if got["hunyuan-turbos-20250926"].InputPrice != 0.8 || got["hunyuan-turbos-20250926"].OutputPrice != 2 {
		t.Fatalf("turbos dated variant not priced from official base: %+v", got["hunyuan-turbos-20250926"])
	}
	if got["hunyuan-t1-vision-20250916"].InputPrice != 3 || got["hunyuan-t1-vision-20250916"].OutputPrice != 9 {
		t.Fatalf("t1 vision dated variant not priced from official base: %+v", got["hunyuan-t1-vision-20250916"])
	}
	if got["hunyuan-large-role-latest"].InputPrice != 2.4 || got["hunyuan-large-role-latest"].OutputPrice != 9.6 {
		t.Fatalf("large role latest not priced from official base: %+v", got["hunyuan-large-role-latest"])
	}
}
