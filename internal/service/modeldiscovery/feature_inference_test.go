package modeldiscovery

import "testing"

func TestInferFeaturesForModelConservativeQwen(t *testing.T) {
	features := map[string]interface{}{
		"supports_web_search": true,
		"supports_vision":     true,
	}
	InferFeaturesForModel("aliyun_dashscope", "qwen-turbo", "LLM", nil, nil, features)

	if features["supports_function_call"] != true || features["supports_json_mode"] != true {
		t.Fatalf("qwen-turbo should support function/json: %#v", features)
	}
	if features["supports_web_search"] != false || features["supports_vision"] != false || features["supports_thinking"] != false {
		t.Fatalf("qwen-turbo should not inherit broad web/vision/thinking: %#v", features)
	}
}

func TestInferFeaturesForModelSpecialCapabilities(t *testing.T) {
	cases := []struct {
		supplier string
		name     string
		modelTyp string
		key      string
	}{
		{"aliyun_dashscope", "qwen-vl-plus", "VLM", "supports_vision"},
		{"aliyun_dashscope", "qwen3-30b-a3b-thinking-2507", "LLM", "supports_thinking"},
		{"aliyun_dashscope", "qwen-deep-search-planning", "LLM", "supports_web_search"},
		{"tencent_hunyuan", "hunyuan-2.0-thinking-20251109", "LLM", "supports_web_search"},
		{"baidu_qianfan", "ernie-x1-turbo", "LLM", "supports_thinking"},
		{"wangsu_aigw", "anthropic.claude-sonnet-4-6", "LLM", "supports_web_search"},
	}

	for _, tc := range cases {
		features := map[string]interface{}{}
		InferFeaturesForModel(tc.supplier, tc.name, tc.modelTyp, nil, nil, features)
		if features[tc.key] != true {
			t.Fatalf("%s should set %s; features=%#v", tc.name, tc.key, features)
		}
	}
}

func TestInferFeaturesForModelQwenVLSupportsTools(t *testing.T) {
	features := map[string]interface{}{}
	InferFeaturesForModel("aliyun_dashscope", "qwen-vl-plus", "VLM", nil, nil, features)

	if features["supports_vision"] != true || features["supports_function_call"] != true {
		t.Fatalf("qwen-vl-plus should support vision and function calling: %#v", features)
	}
	if features["supports_thinking"] != false || features["supports_web_search"] != false {
		t.Fatalf("qwen-vl-plus should not inherit thinking/web search: %#v", features)
	}
}

func TestInferFeaturesForModelNonChatClearsCapabilities(t *testing.T) {
	features := map[string]interface{}{
		"supports_function_call": true,
		"supports_json_mode":     true,
		"supports_vision":        true,
	}
	InferFeaturesForModel("aliyun_dashscope", "qwen-image-plus", "ImageGeneration", nil, nil, features)
	for _, key := range capabilityKeys {
		if features[key] != false {
			t.Fatalf("%s=%v, want false; features=%#v", key, features[key], features)
		}
	}
}
