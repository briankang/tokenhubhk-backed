// 豆包（字节跳动火山引擎）适配器
//
// Supported models:
//   - doubao-pro-32k
//   - doubao-lite-32k
//   - doubao-pro-128k
//
// API reference: https://www.volcengine.com/docs/82379/1263482
// 特殊处理: 火山引擎API，使用endpoint_id路由，OpenAI兼容格式
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

var _ Provider = (*DoubaoProvider)(nil)

const doubaoDefaultBaseURL = "https://ark.cn-beijing.volces.com/api/v3"

var doubaoModels = []string{
	"doubao-pro-32k",
	"doubao-lite-32k",
	"doubao-pro-128k",
}

// DoubaoProvider 实现Provider接口的字节豆包适配器
type DoubaoProvider struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewDoubaoProvider 创建豆包提供商实例
func NewDoubaoProvider(cfg ProviderConfig) *DoubaoProvider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = doubaoDefaultBaseURL
	}
	return &DoubaoProvider{
		apiKey:  cfg.APIKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  newHTTPClient(cfg.TimeoutDuration()),
	}
}

func (p *DoubaoProvider) Name() string      { return "doubao" }
func (p *DoubaoProvider) ModelList() []string { return doubaoModels }

func (p *DoubaoProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider doubao: %w", err)
	}

	oaiReq := convertToOpenAIFormat(req, false)
	body, err := json.Marshal(oaiReq)
	if err != nil {
		return nil, fmt.Errorf("provider doubao: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider doubao: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider doubao: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider doubao: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var oaiResp openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&oaiResp); err != nil {
		return nil, fmt.Errorf("provider doubao: decode response: %w", err)
	}

	return convertFromOpenAIResponse(&oaiResp), nil
}

func (p *DoubaoProvider) StreamChat(ctx context.Context, req *ChatRequest) (StreamReader, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider doubao: %w", err)
	}

	oaiReq := convertToOpenAIFormat(req, true)
	body, err := json.Marshal(oaiReq)
	if err != nil {
		return nil, fmt.Errorf("provider doubao: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider doubao: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider doubao: do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("provider doubao: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return &openAIStreamReader{
		reader: bufio.NewReader(resp.Body),
		body:   resp.Body,
	}, nil
}

func (p *DoubaoProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
}
