package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWangsuProviderGenerateImageOpenAICompat(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/gateway/coze-gpt-image/images/generations" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1710000000,"model":"gpt-image-2","data":[{"url":"https://example.test/image.png","revised_prompt":"ok"}]}`))
	}))
	defer server.Close()

	p := NewWangsuProvider("test-key", server.URL+"/v1/gateway/coze-gpt-image/images/generations", WangsuProtoOpenAI, 5)
	resp, err := p.GenerateImage(context.Background(), &ImageRequest{
		Model:   "gpt-image-2",
		Prompt:  "draw a clean product photo",
		Size:    "1024x1024",
		Quality: "medium",
		Extra: map[string]interface{}{
			"background": "opaque",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("auth=%q", gotAuth)
	}
	if gotBody["model"] != "gpt-image-2" || gotBody["prompt"] != "draw a clean product photo" {
		t.Fatalf("unexpected request body: %#v", gotBody)
	}
	if gotBody["n"].(float64) != 1 {
		t.Fatalf("default n=%v, want 1", gotBody["n"])
	}
	if gotBody["background"] != "opaque" {
		t.Fatalf("extra params not merged: %#v", gotBody)
	}
	if resp.Model != "gpt-image-2" || len(resp.Data) != 1 || resp.Data[0].URL == "" {
		t.Fatalf("unexpected response: %#v", resp)
	}
}
