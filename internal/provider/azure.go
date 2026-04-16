// Azure OpenAI适配器
//
// 支持的模型（部署名称）:
//   - gpt-4o
//   - gpt-4
//   - gpt-4-turbo
//   - gpt-35-turbo
//
// API参考: https://learn.microsoft.com/en-us/azure/ai-services/openai/reference
// 特殊处理: URL格式为 /openai/deployments/{model}/chat/completions?api-version=
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

var _ Provider = (*AzureProvider)(nil)

const azureDefaultAPIVersion = "2024-06-01"

var azureModels = []string{
	"gpt-4o",
	"gpt-4",
	"gpt-4-turbo",
	"gpt-35-turbo",
}

// AzureProvider 实现Provider接口的Azure OpenAI适配器
type AzureProvider struct {
	apiKey     string
	baseURL    string // Azure resource endpoint, e.g., https://{resource}.openai.azure.com
	apiVersion string
	client     *http.Client
}

// NewAzureProvider 创建Azure OpenAI提供商实例
func NewAzureProvider(cfg ProviderConfig) *AzureProvider {
	apiVersion := cfg.APIVersion
	if apiVersion == "" {
		apiVersion = azureDefaultAPIVersion
	}
	return &AzureProvider{
		apiKey:     cfg.APIKey,
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		apiVersion: apiVersion,
		client:     newHTTPClient(cfg.TimeoutDuration()),
	}
}

func (p *AzureProvider) Name() string      { return "azure" }
func (p *AzureProvider) ModelList() []string { return azureModels }

func (p *AzureProvider) chatURL(model string) string {
	return fmt.Sprintf("%s/openai/deployments/%s/chat/completions?api-version=%s", p.baseURL, model, p.apiVersion)
}

func (p *AzureProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider azure: %w", err)
	}
	if p.baseURL == "" {
		return nil, fmt.Errorf("provider azure: base_url (Azure endpoint) is required")
	}

	oaiReq := convertToOpenAIFormat(req, false)
	body, err := MarshalWithExtra(oaiReq, req.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider azure: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.chatURL(req.Model), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider azure: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider azure: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider azure: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var oaiResp openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&oaiResp); err != nil {
		return nil, fmt.Errorf("provider azure: decode response: %w", err)
	}

	return convertFromOpenAIResponse(&oaiResp), nil
}

func (p *AzureProvider) StreamChat(ctx context.Context, req *ChatRequest) (StreamReader, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider azure: %w", err)
	}
	if p.baseURL == "" {
		return nil, fmt.Errorf("provider azure: base_url (Azure endpoint) is required")
	}

	oaiReq := convertToOpenAIFormat(req, true)
	body, err := MarshalWithExtra(oaiReq, req.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider azure: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.chatURL(req.Model), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider azure: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider azure: do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("provider azure: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return &openAIStreamReader{
		reader: bufio.NewReader(resp.Body),
		body:   resp.Body,
	}, nil
}

func (p *AzureProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("api-key", p.apiKey)
}

// OpenAI兼容提供商的共享工具函数（Azure, DeepSeek, Qwen, Kimi）

func convertToOpenAIFormat(req *ChatRequest, stream bool) *openAIRequest {
	msgs := make([]openAIMessage, len(req.Messages))
	for i, m := range req.Messages {
		msgs[i] = openAIMessage{Role: m.Role, Content: m.Content}
	}
	oaiReq := &openAIRequest{
		Model:       req.Model,
		Messages:    msgs,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stream:      stream,
		Stop:        req.Stop,
	}
	if stream {
		oaiReq.StreamOptions = &oaiStreamOption{IncludeUsage: true}
	}
	return oaiReq
}

func convertFromOpenAIResponse(oaiResp *openAIResponse) *ChatResponse {
	choices := make([]Choice, len(oaiResp.Choices))
	for i, c := range oaiResp.Choices {
		choices[i] = Choice{
			Index:            c.Index,
			Message:          Message{Role: c.Message.Role, Content: c.Message.Content, ReasoningContent: c.Message.ReasoningContent},
			FinishReason:     c.FinishReason,
		}
	}
	resp := &ChatResponse{
		ID:      oaiResp.ID,
		Model:   oaiResp.Model,
		Choices: choices,
	}
	if oaiResp.Usage != nil {
		u := Usage{
			PromptTokens:     oaiResp.Usage.PromptTokens,
			CompletionTokens: oaiResp.Usage.CompletionTokens,
			TotalTokens:      oaiResp.Usage.TotalTokens,
		}
		// 提取缓存命中Token：OpenAI/Azure 走 prompt_tokens_details.cached_tokens，
		// DeepSeek 走顶层 prompt_cache_hit_tokens
		if oaiResp.Usage.PromptTokensDetails != nil && oaiResp.Usage.PromptTokensDetails.CachedTokens > 0 {
			u.CacheReadTokens = oaiResp.Usage.PromptTokensDetails.CachedTokens
		} else if oaiResp.Usage.PromptCacheHitTokens > 0 {
			u.CacheReadTokens = oaiResp.Usage.PromptCacheHitTokens
		}
		resp.Usage = u
	}
	return resp
}
