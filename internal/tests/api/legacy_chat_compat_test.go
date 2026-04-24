package api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestLegacyChatCompletionsUsesV1MainPath(t *testing.T) {
	apiKey := ensureCodingAPIKey(t)

	body, status, err := doRawRequest("POST", baseURL+"/api/v1/chat/completions", map[string]interface{}{
		"model": "qwen-plus",
		"messages": []map[string]string{
			{"role": "user", "content": "Say hello in one word"},
		},
		"max_tokens": 10,
	}, apiKey)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if status == http.StatusServiceUnavailable {
		t.Skip("no available channel")
	}
	if status == http.StatusPaymentRequired {
		t.Skip("insufficient balance")
	}
	if status != http.StatusOK && status != http.StatusBadGateway {
		t.Fatalf("expected 200 or 502, got %d: %s", status, string(body))
	}
}

func TestLegacyChatModelsUsesOpenAIShape(t *testing.T) {
	body, status, err := doRawRequest("GET", baseURL+"/api/v1/chat/models", nil, "")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, string(body))
	}
	text := string(body)
	if !strings.Contains(text, `"object":"list"`) || !strings.Contains(text, `"data"`) {
		t.Fatalf("expected OpenAI model list shape, got: %s", text)
	}
}

func TestLegacyChatModelsReturnsDeprecationHeaders(t *testing.T) {
	req, err := http.NewRequest("GET", baseURL+"/api/v1/chat/models", nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Deprecation") != "true" {
		t.Fatalf("expected Deprecation header, got %q", resp.Header.Get("Deprecation"))
	}
	if !strings.Contains(resp.Header.Get("Link"), "/v1/models") {
		t.Fatalf("expected successor Link header, got %q", resp.Header.Get("Link"))
	}
}
