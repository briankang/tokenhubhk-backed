package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// ─── Coding Plan Test Types ──────────────────────────────────────

type openAIModelListResp struct {
	Object string `json:"object"`
	Data   []struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	} `json:"data"`
}

type openAIErrorResp struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

type chatCompletionResp struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type fimCompletionResp struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int    `json:"index"`
		Text         string `json:"text"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// codingAPIKey 缓存 Coding Plan 测试用的 API Key
var codingAPIKey string

// ensureCodingAPIKey 确保测试用 API Key 存在
func ensureCodingAPIKey(t *testing.T) string {
	t.Helper()
	if codingAPIKey != "" {
		return codingAPIKey
	}
	// 复用 openapi_test.go 中的 ensureOpenAPIKey
	codingAPIKey = ensureOpenAPIKey(t)
	return codingAPIKey
}

// doRawRequest 发送原始 HTTP 请求并返回响应体字节（不解析为 apiResponse 格式）
func doRawRequest(method, url string, body interface{}, token string) ([]byte, int, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}

	return respBody, resp.StatusCode, nil
}

// ─── Test: /v1/models 返回 OpenAI 格式的模型列表 ─────────────────

func TestV1ModelsListFormat(t *testing.T) {
	apiKey := ensureCodingAPIKey(t)

	body, status, err := doRawRequest("GET", baseURL+"/v1/models", nil, apiKey)
	if err != nil {
		t.Fatalf("请求 /v1/models 失败: %v", err)
	}
	if status == http.StatusNotImplemented {
		t.Skip("/v1/models 未实现")
	}
	if status != http.StatusOK {
		t.Fatalf("/v1/models 返回状态码 %d, body: %s", status, string(body))
	}

	var resp openAIModelListResp
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("解析 /v1/models 响应失败: %v, body: %s", err, string(body))
	}

	// 验证 OpenAI 格式
	if resp.Object != "list" {
		t.Errorf("期望 object='list', 实际: %s", resp.Object)
	}
	if len(resp.Data) == 0 {
		t.Error("模型列表不应为空")
	}

	// 验证每个模型的格式
	for _, m := range resp.Data {
		if m.ID == "" {
			t.Error("模型 id 不应为空")
		}
		if m.Object != "model" {
			t.Errorf("模型 object 期望 'model', 实际: %s", m.Object)
		}
		if m.OwnedBy == "" {
			t.Error("模型 owned_by 不应为空")
		}
	}

	t.Logf("/v1/models 返回 %d 个模型", len(resp.Data))
}

// ─── Test: /v1/models 不带认证返回 401 ────────────────────────────

func TestV1ModelsUnauthorized(t *testing.T) {
	body, status, err := doRawRequest("GET", baseURL+"/v1/models", nil, "")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	if status != http.StatusUnauthorized {
		t.Errorf("期望 401, 实际: %d, body: %s", status, string(body))
	}
}

// ─── Test: /v1/models 无效 Key 返回 401 ──────────────────────────

func TestV1ModelsInvalidKey(t *testing.T) {
	body, status, err := doRawRequest("GET", baseURL+"/v1/models", nil, "sk-invalid-key-12345")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	if status != http.StatusUnauthorized {
		t.Errorf("期望 401, 实际: %d, body: %s", status, string(body))
	}
}

// ─── Test: /v1/chat/completions Bearer Token 认证 ────────────────

func TestV1ChatCompletionsAuth(t *testing.T) {
	// 无 Token 应返回 401
	body, status, err := doRawRequest("POST", baseURL+"/v1/chat/completions", map[string]interface{}{
		"model": "qwen-plus",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	}, "")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	if status != http.StatusUnauthorized {
		t.Errorf("无 Token 期望 401, 实际: %d, body: %s", status, string(body))
	}
}

// ─── Test: /v1/chat/completions 请求转发 ─────────────────────────

func TestV1ChatCompletionsForward(t *testing.T) {
	apiKey := ensureCodingAPIKey(t)

	reqBody := map[string]interface{}{
		"model": "qwen-plus",
		"messages": []map[string]string{
			{"role": "user", "content": "Say hello in one word"},
		},
		"max_tokens": 10,
		"stream":     false,
	}

	body, status, err := doRawRequest("POST", baseURL+"/v1/chat/completions", reqBody, apiKey)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}

	// 如果没有可用渠道，跳过
	if status == http.StatusServiceUnavailable {
		t.Skip("没有可用渠道，跳过 chat completions 测试")
	}
	if status == http.StatusPaymentRequired {
		t.Skip("余额不足，跳过测试")
	}

	if status != http.StatusOK {
		t.Fatalf("期望 200, 实际: %d, body: %s", status, string(body))
	}

	var resp chatCompletionResp
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("解析响应失败: %v, body: %s", err, string(body))
	}

	if len(resp.Choices) == 0 {
		t.Error("choices 不应为空")
	}
	if resp.Choices[0].Message.Content == "" {
		t.Error("响应内容不应为空")
	}

	t.Logf("chat completions 成功: model=%s, content=%s", resp.Model, resp.Choices[0].Message.Content)
}

// ─── Test: /v1/chat/completions 流式响应 ─────────────────────────

func TestV1ChatCompletionsStream(t *testing.T) {
	apiKey := ensureCodingAPIKey(t)

	reqBody := map[string]interface{}{
		"model": "qwen-plus",
		"messages": []map[string]string{
			{"role": "user", "content": "Say hi"},
		},
		"max_tokens": 5,
		"stream":     true,
	}

	data, _ := json.Marshal(reqBody)
	req, err := http.NewRequest("POST", baseURL+"/v1/chat/completions", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("创建请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusServiceUnavailable {
		t.Skip("没有可用渠道")
	}
	if resp.StatusCode == http.StatusPaymentRequired {
		t.Skip("余额不足")
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("期望 200, 实际: %d, body: %s", resp.StatusCode, string(body))
	}

	// 检查 SSE 内容类型
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		t.Errorf("期望 Content-Type 包含 text/event-stream, 实际: %s", ct)
	}

	// 读取部分 SSE 数据验证格式
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	content := string(buf[:n])

	if !strings.Contains(content, "data:") {
		t.Error("SSE 响应应包含 'data:' 前缀")
	}

	t.Logf("流式响应前 %d 字节: %s", n, content[:min(200, len(content))])
}

// ─── Test: /v1/completions FIM 代码补全 ──────────────────────────

func TestV1CompletionsFIM(t *testing.T) {
	apiKey := ensureCodingAPIKey(t)

	reqBody := map[string]interface{}{
		"model":      "qwen-coder-plus",
		"prompt":     "def fibonacci(n):\n    if n <= 1:\n        return n\n    ",
		"suffix":     "\n    return fibonacci(n-1) + fibonacci(n-2)",
		"max_tokens": 50,
		"stream":     false,
	}

	body, status, err := doRawRequest("POST", baseURL+"/v1/completions", reqBody, apiKey)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}

	if status == http.StatusServiceUnavailable {
		t.Skip("没有可用渠道，跳过 FIM 测试")
	}
	if status == http.StatusPaymentRequired {
		t.Skip("余额不足")
	}

	// FIM 可能返回 200 或 502（上游不支持）
	if status != http.StatusOK && status != http.StatusBadGateway {
		t.Fatalf("期望 200 或 502, 实际: %d, body: %s", status, string(body))
	}

	if status == http.StatusOK {
		t.Logf("FIM completions 成功: %s", string(body))
	} else {
		t.Logf("FIM completions 上游错误（预期情况）: %s", string(body))
	}
}

// ─── Test: /v1/embeddings 返回 501 ──────────────────────────────

func TestV1EmbeddingsNotImplemented(t *testing.T) {
	apiKey := ensureCodingAPIKey(t)

	body, status, err := doRawRequest("POST", baseURL+"/v1/embeddings", map[string]interface{}{
		"model": "text-embedding-ada-002",
		"input": "Hello world",
	}, apiKey)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	if status != http.StatusNotImplemented {
		t.Errorf("期望 501, 实际: %d, body: %s", status, string(body))
	}

	var errResp openAIErrorResp
	if err := json.Unmarshal(body, &errResp); err == nil {
		if errResp.Error.Type != "not_implemented" {
			t.Errorf("期望 error.type='not_implemented', 实际: %s", errResp.Error.Type)
		}
	}

	t.Log("/v1/embeddings 正确返回 501 Not Implemented")
}

// ─── Test: /v1/chat/completions 无效请求体 ──────────────────────

func TestV1ChatCompletionsBadRequest(t *testing.T) {
	apiKey := ensureCodingAPIKey(t)

	// 缺少 messages
	body, status, err := doRawRequest("POST", baseURL+"/v1/chat/completions", map[string]interface{}{
		"model": "qwen-plus",
	}, apiKey)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	if status != http.StatusBadRequest {
		t.Errorf("缺少 messages 期望 400, 实际: %d, body: %s", status, string(body))
	}
}

// ─── Test: /v1/completions 无效请求体 ───────────────────────────

func TestV1CompletionsBadRequest(t *testing.T) {
	apiKey := ensureCodingAPIKey(t)

	// 缺少 prompt
	body, status, err := doRawRequest("POST", baseURL+"/v1/completions", map[string]interface{}{
		"model": "qwen-coder-plus",
	}, apiKey)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	if status != http.StatusBadRequest {
		t.Errorf("缺少 prompt 期望 400, 实际: %d, body: %s", status, string(body))
	}
}

// Go 1.21+ 内置 min 函数，不再需要自定义
