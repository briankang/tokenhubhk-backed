// 阿里云百炼 Coding Plan 适配器
//
// 支持的模型:
//   - qwen-coder-plus (通义千问编码增强版)
//   - qwen-coder-turbo (通义千问编码极速版)
//
// API参考: https://help.aliyun.com/zh/model-studio/developer-reference/
// 格式: OpenAI兼容，使用DashScope端点，支持 /v1/chat/completions
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

var _ Provider = (*CodingAlibabaProvider)(nil)

const codingAlibabaDefaultBaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"

// codingAlibabaModels 阿里云百炼 Coding Plan 支持的模型列表
var codingAlibabaModels = []string{
	"qwen-coder-plus",
	"qwen-coder-turbo",
}

// CodingAlibabaProvider 阿里云百炼 Coding Plan 适配器
// 基于 OpenAI 兼容协议，支持代码补全和对话模式
type CodingAlibabaProvider struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewCodingAlibabaProvider 创建阿里云百炼 Coding Plan 提供商实例
func NewCodingAlibabaProvider(cfg ProviderConfig) *CodingAlibabaProvider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = codingAlibabaDefaultBaseURL
	}
	return &CodingAlibabaProvider{
		apiKey:  cfg.APIKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  newHTTPClient(cfg.TimeoutDuration()),
	}
}

func (p *CodingAlibabaProvider) Name() string      { return "coding_alibaba" }
func (p *CodingAlibabaProvider) ModelList() []string { return codingAlibabaModels }

// Chat 执行非流式聊天补全请求（代码补全场景）
func (p *CodingAlibabaProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider coding_alibaba: %w", err)
	}

	oaiReq := convertToOpenAIFormat(req, false)
	body, err := MarshalWithExtra(oaiReq, req.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider coding_alibaba: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider coding_alibaba: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider coding_alibaba: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider coding_alibaba: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var oaiResp openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&oaiResp); err != nil {
		return nil, fmt.Errorf("provider coding_alibaba: decode response: %w", err)
	}

	return convertFromOpenAIResponse(&oaiResp), nil
}

// StreamChat 执行流式聊天补全请求（代码补全场景）
func (p *CodingAlibabaProvider) StreamChat(ctx context.Context, req *ChatRequest) (StreamReader, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider coding_alibaba: %w", err)
	}

	oaiReq := convertToOpenAIFormat(req, true)
	body, err := MarshalWithExtra(oaiReq, req.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider coding_alibaba: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider coding_alibaba: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider coding_alibaba: do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("provider coding_alibaba: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return &openAIStreamReader{
		reader: bufio.NewReader(resp.Body),
		body:   resp.Body,
	}, nil
}

// setHeaders 设置阿里云百炼请求头
func (p *CodingAlibabaProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
}
