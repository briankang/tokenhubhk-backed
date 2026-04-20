package pricescraper

import (
	"testing"
)

// TestAliyunCacheOnlyForLLMVLM 验收项 V4 (Phase 5.J)：
// 阿里云缓存定价仅对 LLM / VLM / Vision 类型生效，
// Embedding / Image / Video / TTS / ASR / Rerank 不应自动启用缓存
// （即便输入价 > 0）
func TestAliyunCacheOnlyForLLMVLM(t *testing.T) {
	scraper := &AlibabaScraper{}

	cases := []struct {
		name         string
		apiModel     alibabaModel
		wantType     string
		wantCache    bool // 期望 SupportsCache
		wantMech     string
	}{
		// ---- 应启用缓存的类型 ----
		{
			name: "LLM qwen-max",
			apiModel: alibabaModel{
				Model: "qwen-max", Name: "Qwen Max",
				Prices: []alibabaPriceRange{
					{RangeName: "input_token_default", Prices: []alibabaPriceItem{
						{Type: "input_token", Price: "2.4"},
						{Type: "output_token", Price: "9.6"},
					}},
				},
			},
			wantType: "LLM", wantCache: true, wantMech: "both",
		},
		{
			name: "VLM qwen-vl-max",
			apiModel: alibabaModel{
				Model: "qwen-vl-max", Name: "Qwen-VL-Max",
				Prices: []alibabaPriceRange{
					{RangeName: "input_token_default", Prices: []alibabaPriceItem{
						{Type: "input_token", Price: "1.6"},
						{Type: "output_token", Price: "4.0"},
					}},
				},
			},
			wantType: "VLM", wantCache: true, wantMech: "both",
		},

		// ---- 不应启用缓存的类型 ----
		{
			name: "Embedding text-embedding-v4",
			apiModel: alibabaModel{
				Model: "text-embedding-v4", Name: "Text Embedding V4",
				Prices: []alibabaPriceRange{
					{RangeName: "default", Prices: []alibabaPriceItem{
						{Type: "input_token", Price: "0.7"},
					}},
				},
			},
			wantType: "Embedding", wantCache: false, wantMech: "",
		},
		{
			name: "ImageGeneration wan2.6-t2i",
			apiModel: alibabaModel{
				Model: "wan2.6-t2i", Name: "万相 2.6",
				Prices: []alibabaPriceRange{
					{RangeName: "default", Prices: []alibabaPriceItem{
						{Type: "per_image", Price: "0.2"},
					}},
				},
			},
			wantType: "ImageGeneration", wantCache: false, wantMech: "",
		},
		{
			name: "VideoGeneration wanx2.1-t2v-turbo",
			apiModel: alibabaModel{
				Model: "wanx2.1-t2v-turbo", Name: "通义万相视频 Turbo",
				Prices: []alibabaPriceRange{
					{RangeName: "default", Prices: []alibabaPriceItem{
						{Type: "per_second", Price: "0.24"},
					}},
				},
			},
			wantType: "VideoGeneration", wantCache: false, wantMech: "",
		},
		{
			name: "TTS cosyvoice-v3.5",
			apiModel: alibabaModel{
				Model: "cosyvoice-v3.5-plus", Name: "CosyVoice 3.5",
				Prices: []alibabaPriceRange{
					{RangeName: "default", Prices: []alibabaPriceItem{
						{Type: "per_10k_characters", Price: "1.5"},
					}},
				},
			},
			wantType: "TTS", wantCache: false, wantMech: "",
		},
		{
			name: "ASR paraformer-v2",
			apiModel: alibabaModel{
				Model: "paraformer-v2", Name: "Paraformer V2",
				Prices: []alibabaPriceRange{
					{RangeName: "default", Prices: []alibabaPriceItem{
						{Type: "per_second", Price: "0.00008"},
					}},
				},
			},
			wantType: "ASR", wantCache: false, wantMech: "",
		},
		{
			name: "Rerank gte-rerank-v2",
			apiModel: alibabaModel{
				Model: "gte-rerank-v2", Name: "GTE Rerank V2",
				Prices: []alibabaPriceRange{
					{RangeName: "default", Prices: []alibabaPriceItem{
						{Type: "input_token", Price: "0.8"},
					}},
				},
			},
			wantType: "Rerank", wantCache: false, wantMech: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sm := scraper.convertModel(tc.apiModel)
			if sm == nil {
				t.Fatalf("convertModel returned nil for %q", tc.apiModel.Model)
			}
			if sm.ModelType != tc.wantType {
				t.Errorf("ModelType=%q, want %q", sm.ModelType, tc.wantType)
			}
			if sm.SupportsCache != tc.wantCache {
				t.Errorf("SupportsCache=%v, want %v (type=%s)",
					sm.SupportsCache, tc.wantCache, sm.ModelType)
			}
			if sm.CacheMechanism != tc.wantMech {
				t.Errorf("CacheMechanism=%q, want %q", sm.CacheMechanism, tc.wantMech)
			}
		})
	}
}

// TestInferAlibabaModelType 单独验证类型推断
func TestInferAlibabaModelType(t *testing.T) {
	cases := []struct {
		name   string
		caps   []string
		want   string
	}{
		{"qwen-max", nil, "LLM"},
		{"qwen-plus", []string{"TG"}, "LLM"},
		{"qwen-vl-max", []string{"Vision"}, "VLM"},
		{"qvq-max", nil, "VLM"}, // qvq 是视觉推理模型
		{"text-embedding-v4", nil, "Embedding"},
		{"gte-rerank-v2", nil, "Rerank"},
		{"cosyvoice-v3.5-plus", nil, "TTS"},
		{"qwen3-tts-flash", nil, "TTS"},
		{"paraformer-v2", nil, "ASR"},
		{"qwen3-asr-flash", nil, "ASR"},
		{"wan2.6-t2i", nil, "ImageGeneration"},
		{"qwen-image-2.0", nil, "ImageGeneration"},
		{"wanx2.1-t2v-turbo", nil, "VideoGeneration"},
		{"wan2.7-t2v", nil, "VideoGeneration"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := inferAlibabaModelType(c.name, c.caps)
			if got != c.want {
				t.Errorf("inferAlibabaModelType(%q)=%q, want %q", c.name, got, c.want)
			}
		})
	}
}
