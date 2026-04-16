package aimodel

import (
	"testing"

	"tokenhub-server/internal/model"
)

func TestInferModelTypeByName(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		expected string
	}{
		{name: "embedding vision", model: "doubao-embedding-vision-250615", expected: "Embedding"},
		{name: "translation", model: "doubao-seed-translation-250915", expected: "Translation"},
		{name: "seedream image", model: "doubao-seedream-5-0-lite", expected: "ImageGeneration"},
		{name: "tts", model: "doubao-tts-2-0", expected: "TextToSpeech"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inferModelTypeByName(tt.model)
			if got != tt.expected {
				t.Fatalf("inferModelTypeByName(%q) = %q, want %q", tt.model, got, tt.expected)
			}
		})
	}
}

func TestCategorizeCheckError(t *testing.T) {
	tests := []struct {
		name           string
		result         ModelCheckResult
		wantCategory   string
		wantSuggestion string
	}{
		{
			name: "product not activated",
			result: ModelCheckResult{Error: `{"error":{"message":"The product is not activated, please confirm that you have activated products and try again after activation."}}`, StatusCode: 400},
			wantCategory: "product_not_activated",
			wantSuggestion: "供应商产品未激活",
		},
		{
			name: "api mismatch",
			result: ModelCheckResult{Error: `{"error":{"code":"InvalidParameter","message":"the requested model doubao-embedding-vision-250615 does not support this api"}}`, StatusCode: 400},
			wantCategory: "api_mismatch",
			wantSuggestion: "不支持当前 API 端点",
		},
		{
			name: "model not found",
			result: ModelCheckResult{Error: `{"error":{"code":"InvalidEndpointOrModel.NotFound","message":"The model or endpoint doubao-1-5-ui-tars-250428 does not exist"}}`, StatusCode: 404},
			wantCategory: "model_not_found",
			wantSuggestion: "供应商 API 返回模型不存在",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCategory, gotSuggestion := categorizeCheckError(tt.result)
			if gotCategory != tt.wantCategory {
				t.Fatalf("categorizeCheckError() category = %q, want %q", gotCategory, tt.wantCategory)
			}
			if gotSuggestion == "" || !contains(gotSuggestion, tt.wantSuggestion) {
				t.Fatalf("categorizeCheckError() suggestion = %q, want substring %q", gotSuggestion, tt.wantSuggestion)
			}
		})
	}
}

func TestClassifyAgainstUpstream_VolcengineShutdownModel(t *testing.T) {
	m := model.AIModel{
		ID:        1,
		ModelName: "doubao-1-5-ui-tars-250428",
		ModelType: "VLM",
		SupplierID: 7,
	}

	snapshots := map[uint]*upstreamSnapshot{
		7: {
			SupplierID: 7,
			Names: map[string]bool{
				"doubao-1-5-ui-tars-250428": true,
			},
			ShutdownNames: map[string]bool{
				"doubao-1-5-ui-tars-250428": true,
			},
			Available: true,
			ReturnedModelTypes: map[string]bool{
				"LLM": true, "VLM": true, "Embedding": true, "ImageGeneration": true, "VideoGeneration": true,
			},
		},
	}

	got := classifyAgainstUpstream(m, snapshots)
	if got != UpstreamDeprecated {
		t.Fatalf("classifyAgainstUpstream() = %q, want %q", got, UpstreamDeprecated)
	}
}

func TestClassifyAgainstUpstream_VolcengineActiveModel(t *testing.T) {
	m := model.AIModel{
		ID:        1,
		ModelName: "doubao-1-5-pro-32k-250115",
		ModelType: "LLM",
		SupplierID: 7,
	}

	snapshots := map[uint]*upstreamSnapshot{
		7: {
			SupplierID: 7,
			Names: map[string]bool{
				"doubao-1-5-pro-32k-250115": true,
			},
			ShutdownNames:      map[string]bool{},
			Available:          true,
			ReturnedModelTypes: map[string]bool{"LLM": true},
		},
	}

	got := classifyAgainstUpstream(m, snapshots)
	if got != UpstreamActive {
		t.Fatalf("classifyAgainstUpstream() = %q, want %q", got, UpstreamActive)
	}
}

func TestClassifyAgainstUpstream_MissingLLMModel(t *testing.T) {
	m := model.AIModel{
		ID:        1,
		ModelName: "deepseek-v3-1-250821",
		ModelType: "LLM",
		SupplierID: 7,
	}

	snapshots := map[uint]*upstreamSnapshot{
		7: {
			SupplierID:     7,
			Names:          map[string]bool{},
			ShutdownNames:  map[string]bool{},
			Available:      true,
			ReturnedModelTypes: map[string]bool{"LLM": true},
		},
	}

	got := classifyAgainstUpstream(m, snapshots)
	if got != UpstreamDeprecated {
		t.Fatalf("classifyAgainstUpstream() = %q, want %q", got, UpstreamDeprecated)
	}
}

func contains(s, substr string) bool {
	return len(substr) == 0 || (len(s) >= len(substr) && stringContains(s, substr))
}

func stringContains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
