package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnthropicConvertResponseSplitsOneHourCacheWrites(t *testing.T) {
	var resp anthropicResponse
	body := []byte(`{
		"id": "msg_123",
		"model": "claude-opus-4-6",
		"type": "message",
		"content": [{"type": "text", "text": "ok"}],
		"usage": {
			"input_tokens": 2048,
			"cache_read_input_tokens": 1800,
			"cache_creation_input_tokens": 556,
			"cache_creation": {
				"ephemeral_5m_input_tokens": 456,
				"ephemeral_1h_input_tokens": 100
			},
			"output_tokens": 503
		},
		"stop_reason": "end_turn"
	}`)
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}

	got := (&AnthropicProvider{}).convertResponse(&resp)
	if got.Usage.CacheWriteTokens != 556 {
		t.Fatalf("cache write tokens=%d, want 556", got.Usage.CacheWriteTokens)
	}
	if got.Usage.CacheWrite1hTokens != 100 {
		t.Fatalf("cache write 1h tokens=%d, want 100", got.Usage.CacheWrite1hTokens)
	}
}

func TestAnthropicConvertRequestConcatenatesSystemAndDeveloper(t *testing.T) {
	req := &ChatRequest{
		Model:     "claude-sonnet-4-5",
		MaxTokens: 64,
		Messages: []Message{
			{Role: "system", Content: "system one"},
			{Role: "developer", Content: "developer instruction"},
			{Role: "user", Content: "hello"},
		},
	}

	got := (&AnthropicProvider{}).convertRequest(req, false)

	if got.System != "system one\ndeveloper instruction" {
		t.Fatalf("system=%#v, want concatenated system/developer prompt", got.System)
	}
	if len(got.Messages) != 1 || got.Messages[0].Role != "user" {
		t.Fatalf("messages=%#v, want only user/assistant messages after system extraction", got.Messages)
	}
}

func TestSanitizeAnthropicExtraFiltersOpenAIOnlyParams(t *testing.T) {
	extra := map[string]interface{}{
		"frequency_penalty": 0.2,
		"presence_penalty":  0.3,
		"response_format":   map[string]interface{}{"type": "json_object"},
		"seed":              42,
		"reasoning_effort":  "high",
		"top_k":             20,
		"thinking":          map[string]interface{}{"type": "enabled", "budget_tokens": 1024},
		"metadata":          map[string]interface{}{"trace": "ok"},
	}

	got := sanitizeAnthropicExtra(extra)

	for _, blocked := range []string{"frequency_penalty", "presence_penalty", "response_format", "seed", "reasoning_effort"} {
		if _, exists := got[blocked]; exists {
			t.Fatalf("blocked param %q leaked into Anthropic extra: %#v", blocked, got)
		}
	}
	if got["top_k"] != 20 {
		t.Fatalf("top_k=%#v, want preserved", got["top_k"])
	}
	if _, exists := got["thinking"]; !exists {
		t.Fatalf("thinking should be preserved: %#v", got)
	}
	if _, exists := got["metadata"]; !exists {
		t.Fatalf("metadata should be preserved: %#v", got)
	}
}

func TestAnthropicProviderMessagesCompatibilityPayload(t *testing.T) {
	var captured map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			t.Fatalf("path=%s, want /messages", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Fatalf("x-api-key=%q", got)
		}
		if got := r.Header.Get("anthropic-version"); got == "" {
			t.Fatalf("missing anthropic-version header")
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_test",
			"model":"claude-sonnet-4-5",
			"type":"message",
			"content":[{"type":"text","text":"ok"}],
			"usage":{"input_tokens":5,"output_tokens":2},
			"stop_reason":"end_turn"
		}`))
	}))
	defer srv.Close()

	temp := 0.7
	topP := 0.9
	p := NewAnthropicProvider(ProviderConfig{APIKey: "test-key", BaseURL: srv.URL})
	_, err := p.Chat(context.Background(), &ChatRequest{
		Model:       "claude-sonnet-4-5",
		Messages:    []Message{{Role: "system", Content: "system one"}, {Role: "developer", Content: "developer instruction"}, {Role: "user", Content: "hello"}},
		MaxTokens:   123,
		Temperature: &temp,
		TopP:        &topP,
		Stop:        []string{"END"},
		Extra: map[string]interface{}{
			"frequency_penalty": 0.2,
			"presence_penalty":  0.3,
			"response_format":   map[string]interface{}{"type": "json_object"},
			"seed":              42,
			"reasoning_effort":  "high",
			"top_k":             20,
			"thinking":          map[string]interface{}{"type": "enabled", "budget_tokens": 1024},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if captured["system"] != "system one\ndeveloper instruction" {
		t.Fatalf("system=%#v, want concatenated system/developer prompt", captured["system"])
	}
	assertNumberField(t, captured, "max_tokens", 123)
	assertNumberField(t, captured, "top_k", 20)
	if _, ok := captured["thinking"].(map[string]interface{}); !ok {
		t.Fatalf("thinking not forwarded: %#v", captured["thinking"])
	}
	for _, blocked := range []string{"frequency_penalty", "presence_penalty", "response_format", "seed", "reasoning_effort", "max_completion_tokens"} {
		if _, exists := captured[blocked]; exists {
			t.Fatalf("blocked OpenAI-only param %q leaked into Anthropic payload: %#v", blocked, captured)
		}
	}
	msgs, ok := captured["messages"].([]interface{})
	if !ok || len(msgs) != 1 {
		t.Fatalf("messages=%#v, want one user message after system extraction", captured["messages"])
	}
	first, _ := msgs[0].(map[string]interface{})
	if first["role"] != "user" {
		t.Fatalf("first message=%#v, want user role", first)
	}
}
