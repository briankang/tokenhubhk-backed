// 火山引擎 Coding Plan 适配器
//
// 支持的模型:
//   - doubao-coder (豆包编码模型)
//   - doubao-coder-pro (豆包编码增强版)
//
// API参考: https://www.volcengine.com/docs/82379/1263482
// 格式: OpenAI兼容，使用火山引擎 ARK 端点
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

var _ Provider = (*CodingVolcengineProvider)(nil)

const codingVolcengineDefaultBaseURL = "https://ark.cn-beijing.volces.com/api/v3"

// codingVolcengineModels 火山引擎 Coding Plan 支持的模型列表
var codingVolcengineModels = []string{
	"doubao-coder",
	"doubao-coder-pro",
}

// CodingVolcengineProvider 火山引擎 Coding Plan 适配器
// 基于 OpenAI 兼容协议，支持代码补全和对话模式
type CodingVolcengineProvider struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewCodingVolcengineProvider 创建火山引擎 Coding Plan 提供商实例
func NewCodingVolcengineProvider(cfg ProviderConfig) *CodingVolcengineProvider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = codingVolcengineDefaultBaseURL
	}
	return &CodingVolcengineProvider{
		apiKey:  cfg.APIKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  newHTTPClient(cfg.TimeoutDuration()),
	}
}

func (p *CodingVolcengineProvider) Name() string      { return "coding_volcengine" }
func (p *CodingVolcengineProvider) ModelList() []string { return codingVolcengineModels }

// Chat 执行非流式聊天补全请求（代码补全场景）
func (p *CodingVolcengineProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider coding_volcengine: %w", err)
	}

	oaiReq := convertToOpenAIFormat(req, false)
	body, err := json.Marshal(oaiReq)
	if err != nil {
		return nil, fmt.Errorf("provider coding_volcengine: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider coding_volcengine: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider coding_volcengine: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider coding_volcengine: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var oaiResp openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&oaiResp); err != nil {
		return nil, fmt.Errorf("provider coding_volcengine: decode response: %w", err)
	}

	return convertFromOpenAIResponse(&oaiResp), nil
}

// StreamChat 执行流式聊天补全请求（代码补全场景）
func (p *CodingVolcengineProvider) StreamChat(ctx context.Context, req *ChatRequest) (StreamReader, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider coding_volcengine: %w", err)
	}

	oaiReq := convertToOpenAIFormat(req, true)
	body, err := json.Marshal(oaiReq)
	if err != nil {
		return nil, fmt.Errorf("provider coding_volcengine: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider coding_volcengine: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider coding_volcengine: do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("provider coding_volcengine: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return &openAIStreamReader{
		reader: bufio.NewReader(resp.Body),
		body:   resp.Body,
	}, nil
}

// setHeaders 设置火山引擎请求头
func (p *CodingVolcengineProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
}
