package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWangsuGenerateVideo(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotBody map[string]interface{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"created": 1710000000,
			"model": "sora-2",
			"task_id": "task_123",
			"data": [{"url": "https://cdn.example/video.mp4"}]
		}`))
	}))
	defer srv.Close()

	p := NewWangsuProvider("secret", srv.URL+"/gateway", WangsuProtoOpenAI, 5)
	resp, err := p.GenerateVideo(context.Background(), &VideoRequest{
		Model:       "sora-2",
		Prompt:      "city skyline",
		Duration:    8,
		Resolution:  "720p",
		AspectRatio: "16:9",
		Extra: map[string]interface{}{
			"audio": true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/gateway/videos" {
		t.Fatalf("path=%q, want /gateway/videos", gotPath)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("auth=%q, want bearer", gotAuth)
	}
	if gotBody["model"] != "sora-2" || gotBody["prompt"] != "city skyline" || gotBody["audio"] != true {
		t.Fatalf("unexpected body: %#v", gotBody)
	}
	if resp.TaskID != "task_123" || len(resp.Data) != 1 || resp.Data[0].URL == "" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if resp.Data[0].DurationSec != 8 {
		t.Fatalf("duration=%d, want fallback 8", resp.Data[0].DurationSec)
	}
}

func TestParseWangsuVideoResponseAcceptsTaskOnly(t *testing.T) {
	resp, err := parseWangsuVideoResponse([]byte(`{"id":"queued_1","model":"viduq3-pro"}`), "fallback", 0)
	if err != nil {
		t.Fatal(err)
	}
	if resp.TaskID != "queued_1" || resp.Model != "viduq3-pro" {
		t.Fatalf("unexpected task response: %#v", resp)
	}
}

func TestWangsuQueryVideoTask(t *testing.T) {
	var gotPath string
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"task_id": "task_123",
			"status": "completed",
			"result": {"video_url": "https://cdn.example/final.mp4"}
		}`))
	}))
	defer srv.Close()

	p := NewWangsuProvider("secret", srv.URL+"/gateway", WangsuProtoOpenAI, 5)
	resp, err := p.QueryVideoTask(context.Background(), "task_123")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/gateway/videos/tasks/task_123" {
		t.Fatalf("path=%q, want /gateway/videos/tasks/task_123", gotPath)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("auth=%q, want bearer", gotAuth)
	}
	if resp.Status != "succeeded" || len(resp.Data) != 1 || resp.Data[0].URL == "" {
		t.Fatalf("unexpected response: %#v", resp)
	}
}
