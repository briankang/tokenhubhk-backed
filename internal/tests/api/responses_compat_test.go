package api_test

import (
	"net/http"
	"testing"
)

func TestV1ResponsesCompat(t *testing.T) {
	apiKey := ensureCodingAPIKey(t)

	body, status, err := doRawRequest("POST", baseURL+"/v1/responses", map[string]interface{}{
		"model":             "qwen-plus",
		"input":             "Say hello in one word",
		"instructions":      "Answer briefly.",
		"max_output_tokens": 10,
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
