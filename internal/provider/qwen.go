// 通义千问（Qwen）适配器（OpenAI兼容格式）
//
// Supported models:
//   - qwen-turbo
//   - qwen-plus
//   - qwen-max
//   - qwen-2.5-72b-instruct
//   - qwen-2.5-32b-instruct
//
// API reference: https://help.aliyun.com/zh/model-studio/developer-reference/
// 格式: OpenAI兼容，使用DashScope端点
package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

var _ Provider = (*QwenProvider)(nil)

const qwenDefaultBaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"

var qwenModels = []string{
	"qwen-turbo",
	"qwen-plus",
	"qwen-max",
	"qwen-2.5-72b-instruct",
	"qwen-2.5-32b-instruct",
}

// QwenProvider 实现Provider接口的阿里云通义千问适配器
type QwenProvider struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewQwenProvider 创建通义千问提供商实例
func NewQwenProvider(cfg ProviderConfig) *QwenProvider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = qwenDefaultBaseURL
	}
	return &QwenProvider{
		apiKey:  cfg.APIKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  newHTTPClient(cfg.TimeoutDuration()),
	}
}

func (p *QwenProvider) Name() string      { return "qwen" }
func (p *QwenProvider) ModelList() []string { return qwenModels }

func (p *QwenProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider qwen: %w", err)
	}

	oaiReq := convertToOpenAIFormat(req, false)
	body, err := json.Marshal(oaiReq)
	if err != nil {
		return nil, fmt.Errorf("provider qwen: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider qwen: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider qwen: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider qwen: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var oaiResp openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&oaiResp); err != nil {
		return nil, fmt.Errorf("provider qwen: decode response: %w", err)
	}

	return convertFromOpenAIResponse(&oaiResp), nil
}

func (p *QwenProvider) StreamChat(ctx context.Context, req *ChatRequest) (StreamReader, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider qwen: %w", err)
	}

	oaiReq := convertToOpenAIFormat(req, true)
	body, err := json.Marshal(oaiReq)
	if err != nil {
		return nil, fmt.Errorf("provider qwen: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider qwen: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider qwen: do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("provider qwen: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return &openAIStreamReader{
		reader: bufio.NewReader(resp.Body),
		body:   resp.Body,
	}, nil
}

func (p *QwenProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
}
