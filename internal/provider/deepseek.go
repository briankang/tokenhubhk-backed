// DeepSeek适配器（OpenAI兼容格式）
//
// Supported models:
//   - deepseek-chat (DeepSeek-V3)
//   - deepseek-reasoner (DeepSeek-R1)
//
// API reference: https://platform.deepseek.com/api-docs
// 格式: OpenAI兼容，使用不同的端点地址
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

var _ Provider = (*DeepSeekProvider)(nil)

const deepseekDefaultBaseURL = "https://api.deepseek.com/v1"

var deepseekModels = []string{
	"deepseek-chat",
	"deepseek-reasoner",
}

// DeepSeekProvider 实现Provider接口的DeepSeek适配器
type DeepSeekProvider struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewDeepSeekProvider creates a new DeepSeek provider instance.
func NewDeepSeekProvider(cfg ProviderConfig) *DeepSeekProvider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = deepseekDefaultBaseURL
	}
	return &DeepSeekProvider{
		apiKey:  cfg.APIKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  newHTTPClient(cfg.TimeoutDuration()),
	}
}

func (p *DeepSeekProvider) Name() string      { return "deepseek" }
func (p *DeepSeekProvider) ModelList() []string { return deepseekModels }

func (p *DeepSeekProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider deepseek: %w", err)
	}

	oaiReq := convertToOpenAIFormat(req, false)
	body, err := json.Marshal(oaiReq)
	if err != nil {
		return nil, fmt.Errorf("provider deepseek: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider deepseek: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider deepseek: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider deepseek: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var oaiResp openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&oaiResp); err != nil {
		return nil, fmt.Errorf("provider deepseek: decode response: %w", err)
	}

	return convertFromOpenAIResponse(&oaiResp), nil
}

func (p *DeepSeekProvider) StreamChat(ctx context.Context, req *ChatRequest) (StreamReader, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider deepseek: %w", err)
	}

	oaiReq := convertToOpenAIFormat(req, true)
	body, err := json.Marshal(oaiReq)
	if err != nil {
		return nil, fmt.Errorf("provider deepseek: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider deepseek: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider deepseek: do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("provider deepseek: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return &openAIStreamReader{
		reader: bufio.NewReader(resp.Body),
		body:   resp.Body,
	}, nil
}

func (p *DeepSeekProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
}
