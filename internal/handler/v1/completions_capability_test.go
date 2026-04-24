package v1

import (
	"encoding/json"
	"testing"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/provider"
)

func TestRequiredChatFeatureKeys(t *testing.T) {
	raw := mustRawMap(t, `{
		"model":"qwen-turbo",
		"messages":[{"role":"user","content":[
			{"type":"text","text":"describe"},
			{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}
		]}],
		"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],
		"response_format":{"type":"json_object"},
		"enable_thinking":true
	}`)
	req := chatCompletionRequest{
		Model: "qwen-turbo",
		Messages: []provider.Message{{
			Role: "user",
			Content: []interface{}{
				map[string]interface{}{"type": "text", "text": "describe"},
				map[string]interface{}{"type": "image_url", "image_url": map[string]interface{}{"url": "https://example.com/a.png"}},
			},
		}},
	}

	got := requiredChatFeatureKeys(&req, raw, true)
	want := []string{"supports_vision", "supports_function_call", "supports_json_mode", "supports_thinking"}
	if len(got) != len(want) {
		t.Fatalf("required features=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("required features=%v, want %v", got, want)
		}
	}
}

func TestModelMetaMissingRequiredFeatures(t *testing.T) {
	meta := &modelMeta{
		Features: model.JSON(`{"supports_function_call":true,"supports_json_mode":true,"supports_vision":false}`),
	}
	missing := meta.MissingRequiredFeatures([]string{"supports_function_call", "supports_vision", "supports_thinking"})

	want := []string{"supports_vision", "supports_thinking"}
	if len(missing) != len(want) {
		t.Fatalf("missing=%v, want %v", missing, want)
	}
	for i := range want {
		if missing[i] != want[i] {
			t.Fatalf("missing=%v, want %v", missing, want)
		}
	}
}

func TestRequiredChatFeatureKeysTextOnly(t *testing.T) {
	raw := mustRawMap(t, `{"model":"qwen-turbo","messages":[{"role":"user","content":"hello"}],"response_format":{"type":"text"}}`)
	req := chatCompletionRequest{
		Model:    "qwen-turbo",
		Messages: []provider.Message{{Role: "user", Content: "hello"}},
	}

	if got := requiredChatFeatureKeys(&req, raw, false); len(got) != 0 {
		t.Fatalf("text-only request should not require special features: %v", got)
	}
}

func TestExtractChatExtraParamsKeepsOpenAIAdvancedFields(t *testing.T) {
	raw := mustRawMap(t, `{
		"model":"qwen-turbo",
		"messages":[{"role":"user","content":"hello"}],
		"max_tokens":16,
		"response_format":{"type":"json_object"},
		"tools":[{"type":"function","function":{"name":"lookup"}}],
		"tool_choice":"auto",
		"frequency_penalty":0.2,
		"presence_penalty":0.3,
		"seed":42,
		"top_k":20,
		"reasoning":{"effort":"high"}
	}`)

	extra := extractChatExtraParams(raw)
	if _, exists := extra["max_tokens"]; exists {
		t.Fatalf("max_tokens is handled by chatCompletionRequest and should not be duplicated: %#v", extra)
	}
	for _, key := range []string{
		"response_format", "tools", "tool_choice", "frequency_penalty",
		"presence_penalty", "seed", "top_k", "reasoning", "reasoning_effort",
	} {
		if _, exists := extra[key]; !exists {
			t.Fatalf("extra params missing %q: %#v", key, extra)
		}
	}
	if got := extra["reasoning_effort"]; got != "high" {
		t.Fatalf("reasoning_effort=%#v, want high", got)
	}
}

func mustRawMap(t *testing.T, body string) map[string]json.RawMessage {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatal(err)
	}
	return raw
}
