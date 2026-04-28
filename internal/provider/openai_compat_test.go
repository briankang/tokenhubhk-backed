package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIProviderChatCompatibilityPayload(t *testing.T) {
	var captured map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path=%s, want /chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("Authorization=%q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl_test",
			"model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}
		}`))
	}))
	defer srv.Close()

	temp := 0.7
	topP := 0.9
	p := NewOpenAIProvider(ProviderConfig{APIKey: "test-key", BaseURL: srv.URL})
	_, err := p.Chat(context.Background(), &ChatRequest{
		Model:       "gpt-4o",
		Messages:    []Message{{Role: "system", Content: "be concise"}, {Role: "user", Content: "hello"}},
		MaxTokens:   123,
		Temperature: &temp,
		TopP:        &topP,
		Stop:        []string{"END"},
		Extra: map[string]interface{}{
			"frequency_penalty": 0.2,
			"presence_penalty":  0.3,
			"response_format":   map[string]interface{}{"type": "json_object"},
			"seed":              42,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	assertNumberField(t, captured, "max_tokens", 123)
	assertNumberField(t, captured, "frequency_penalty", 0.2)
	assertNumberField(t, captured, "presence_penalty", 0.3)
	assertNumberField(t, captured, "seed", 42)
	if _, ok := captured["response_format"].(map[string]interface{}); !ok {
		t.Fatalf("response_format not forwarded: %#v", captured["response_format"])
	}
	if _, exists := captured["max_completion_tokens"]; exists {
		t.Fatalf("max_completion_tokens should not be duplicated in canonical OpenAI payload: %#v", captured)
	}
}

func assertNumberField(t *testing.T, m map[string]interface{}, key string, want float64) {
	t.Helper()
	got, ok := m[key].(float64)
	if !ok || got != want {
		t.Fatalf("%s=%#v, want %v", key, m[key], want)
	}
}
