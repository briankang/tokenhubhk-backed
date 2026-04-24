package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestV1ChatCapabilityGuardRejectsVisionOnTextModel(t *testing.T) {
	apiKey := ensureCodingAPIKey(t)

	body, status, err := doRawRequest("POST", baseURL+"/v1/chat/completions", map[string]interface{}{
		"model": "ernie-x1-32k",
		"messages": []map[string]interface{}{
			{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": "describe this image"},
					{"type": "image_url", "image_url": map[string]string{"url": "https://example.com/a.png"}},
				},
			},
		},
		"max_tokens": 8,
	}, apiKey)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if status != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", status, string(body))
	}
	assertOpenAIErrorCode(t, body, "unsupported_model_capability")
	if !strings.Contains(string(body), "supports_vision") {
		t.Fatalf("expected supports_vision in response body, got: %s", string(body))
	}
}

func TestV1ChatCapabilityGuardAllowsSupportedJSONMode(t *testing.T) {
	apiKey := ensureCodingAPIKey(t)

	body, status, err := doRawRequest("POST", baseURL+"/v1/chat/completions", map[string]interface{}{
		"model": "ernie-x1-32k",
		"messages": []map[string]string{
			{"role": "user", "content": "Return {\"ok\":true} only"},
		},
		"response_format": map[string]string{"type": "json_object"},
		"max_tokens":      8,
	}, apiKey)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if status == http.StatusBadRequest && strings.Contains(string(body), "unsupported_model_capability") {
		t.Fatalf("json-capable model was rejected by capability guard: %s", string(body))
	}
}

func assertOpenAIErrorCode(t *testing.T, body []byte, want string) {
	t.Helper()
	var resp openAIErrorResp
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("parse OpenAI error: %v, body=%s", err, string(body))
	}
	if resp.Error.Code != want {
		t.Fatalf("error code=%q, want %q; body=%s", resp.Error.Code, want, string(body))
	}
}
