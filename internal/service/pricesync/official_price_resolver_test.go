package pricesync

import (
	"context"
	"encoding/json"
	"testing"

	"tokenhub-server/internal/model"
)

// TestResolveOfficialPriceURL_ModelOverride 模型级覆盖优先级最高。
func TestResolveOfficialPriceURL_ModelOverride(t *testing.T) {
	m := &model.AIModel{
		BaseModel:        model.BaseModel{ID: 1},
		OfficialPriceURL: "https://example.com/model-specific",
		Supplier: model.Supplier{
			BaseModel:  model.BaseModel{ID: 2},
			PricingURL: "https://example.com/supplier-default",
		},
	}
	got := ResolveOfficialPriceURL(context.Background(), nil, m)
	if got.URL != "https://example.com/model-specific" {
		t.Fatalf("URL = %q, want model_specific", got.URL)
	}
	if got.Source != "model_override" {
		t.Fatalf("Source = %q, want model_override", got.Source)
	}
}

// TestResolveOfficialPriceURL_SupplierTypedMatch 多页配置按 type_hint 匹配模型类型。
func TestResolveOfficialPriceURL_SupplierTypedMatch(t *testing.T) {
	urls := []model.PricingURLEntry{
		{URL: "https://example.com/text", TypeHint: "LLM"},
		{URL: "https://example.com/image", TypeHint: "ImageGeneration"},
		{URL: "https://example.com/video", TypeHint: "VideoGeneration"},
	}
	urlsJSON, _ := json.Marshal(urls)

	cases := []struct {
		modelType string
		wantURL   string
	}{
		{"LLM", "https://example.com/text"},
		{"ImageGeneration", "https://example.com/image"},
		{"VideoGeneration", "https://example.com/video"},
	}
	for _, c := range cases {
		m := &model.AIModel{
			ModelType: c.modelType,
			Supplier: model.Supplier{
				BaseModel:   model.BaseModel{ID: 1},
				PricingURLs: model.JSON(urlsJSON),
			},
		}
		got := ResolveOfficialPriceURL(context.Background(), nil, m)
		if got.URL != c.wantURL {
			t.Errorf("modelType=%s, URL = %q, want %q", c.modelType, got.URL, c.wantURL)
		}
		if got.Source != "supplier_typed" {
			t.Errorf("modelType=%s, Source = %q, want supplier_typed", c.modelType, got.Source)
		}
	}
}

// TestResolveOfficialPriceURL_NameSubstringMatch type_hint 作为模型名子串匹配。
func TestResolveOfficialPriceURL_NameSubstringMatch(t *testing.T) {
	urls := []model.PricingURLEntry{
		{URL: "https://example.com/qwen", TypeHint: "qwen"},
		{URL: "https://example.com/default"},
	}
	urlsJSON, _ := json.Marshal(urls)
	m := &model.AIModel{
		ModelName: "qwen-vl-plus",
		ModelType: "ImageGeneration",
		Supplier: model.Supplier{
			BaseModel:   model.BaseModel{ID: 1},
			PricingURLs: model.JSON(urlsJSON),
		},
	}
	got := ResolveOfficialPriceURL(context.Background(), nil, m)
	if got.URL != "https://example.com/qwen" {
		t.Fatalf("URL = %q, want qwen-matched", got.URL)
	}
}

// TestResolveOfficialPriceURL_FallbackToDefault 多页无匹配 → 回退 PricingURL。
func TestResolveOfficialPriceURL_FallbackToDefault(t *testing.T) {
	urls := []model.PricingURLEntry{
		{URL: "https://example.com/image", TypeHint: "ImageGeneration"},
	}
	urlsJSON, _ := json.Marshal(urls)
	m := &model.AIModel{
		ModelType: "LLM",
		Supplier: model.Supplier{
			BaseModel:   model.BaseModel{ID: 1},
			PricingURL:  "https://example.com/supplier-default",
			PricingURLs: model.JSON(urlsJSON),
		},
	}
	got := ResolveOfficialPriceURL(context.Background(), nil, m)
	if got.URL != "https://example.com/supplier-default" {
		t.Fatalf("URL = %q, want supplier_default", got.URL)
	}
	if got.Source != "supplier_default" {
		t.Fatalf("Source = %q, want supplier_default", got.Source)
	}
}

// TestResolveOfficialPriceURL_AllEmpty 全部为空 → unset。
func TestResolveOfficialPriceURL_AllEmpty(t *testing.T) {
	m := &model.AIModel{Supplier: model.Supplier{BaseModel: model.BaseModel{ID: 1}}}
	got := ResolveOfficialPriceURL(context.Background(), nil, m)
	if got.URL != "" {
		t.Fatalf("URL = %q, want empty", got.URL)
	}
	if got.Source != "unset" {
		t.Fatalf("Source = %q, want unset", got.Source)
	}
}

// TestResolveOfficialPriceURL_NilModel nil-safe。
func TestResolveOfficialPriceURL_NilModel(t *testing.T) {
	got := ResolveOfficialPriceURL(context.Background(), nil, nil)
	if got.Source != "unset" {
		t.Fatalf("Source = %q, want unset", got.Source)
	}
}
