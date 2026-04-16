// OpenAI适配器，支持 GPT-4o, GPT-4, GPT-4-turbo, GPT-3.5-turbo 等模型
//
// 支持的模型:
//   - gpt-4o
//   - gpt-4o-mini
//   - gpt-4-turbo
//   - gpt-4
//   - gpt-3.5-turbo
//
// API参考: https://platform.openai.com/docs/api-reference/chat
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

	"tokenhub-server/internal/pkg/logger"

	"go.uber.org/zap"
)

// 编译期接口检查
var _ Provider = (*OpenAIProvider)(nil)

const openAIDefaultBaseURL = "https://api.openai.com/v1"

var openAIModels = []string{
	"gpt-4o",
	"gpt-4o-mini",
	"gpt-4-turbo",
	"gpt-4",
	"gpt-3.5-turbo",
}

// OpenAIProvider 实现Provider接口的OpenAI适配器
type OpenAIProvider struct {
	apiKey  string
	baseURL string
	orgID   string
	client  *http.Client
}

// NewOpenAIProvider 创建OpenAI提供商实例
func NewOpenAIProvider(cfg ProviderConfig) *OpenAIProvider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = openAIDefaultBaseURL
	}
	return &OpenAIProvider{
		apiKey:  cfg.APIKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		orgID:   cfg.OrgID,
		client:  newHTTPClient(cfg.TimeoutDuration()),
	}
}

func (p *OpenAIProvider) Name() string { return "openai" }

func (p *OpenAIProvider) ModelList() []string { return openAIModels }

// openAIRequest is the native OpenAI request format.
type openAIRequest struct {
	Model         string           `json:"model"`
	Messages      []openAIMessage  `json:"messages"`
	MaxTokens     int              `json:"max_tokens,omitempty"`
	Temperature   *float64         `json:"temperature,omitempty"`
	TopP          *float64         `json:"top_p,omitempty"`
	Stream        bool             `json:"stream"`
	Stop          []string         `json:"stop,omitempty"`
	StreamOptions *oaiStreamOption `json:"stream_options,omitempty"`
}

type oaiStreamOption struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIMessage struct {
	Role             string `json:"role"`
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content,omitempty"` // 深度思考内容（豆包/Qwen3/DeepSeek-R1等）
}

// openAIResponse is the native OpenAI response format.
type openAIResponse struct {
	ID      string          `json:"id"`
	Model   string          `json:"model"`
	Choices []openAIChoice  `json:"choices"`
	Usage   *openAIUsage    `json:"usage,omitempty"`
}

type openAIChoice struct {
	Index        int           `json:"index"`
	Message      openAIMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type openAIUsage struct {
	PromptTokens        int                  `json:"prompt_tokens"`
	CompletionTokens    int                  `json:"completion_tokens"`
	TotalTokens         int                  `json:"total_tokens"`
	PromptTokensDetails *openAIPromptDetails `json:"prompt_tokens_details,omitempty"`
	// DeepSeek 顶层缓存字段（与 OpenAI 的 prompt_tokens_details.cached_tokens 互补）
	PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens,omitempty"`
	PromptCacheMissTokens int `json:"prompt_cache_miss_tokens,omitempty"`
}

// openAIPromptDetails OpenAI 响应中的输入 Token 明细
type openAIPromptDetails struct {
	CachedTokens int `json:"cached_tokens"`
	AudioTokens  int `json:"audio_tokens,omitempty"`
}

// Chat 执行非流式聊天补全请求
func (p *OpenAIProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider openai: %w", err)
	}

	oaiReq := p.convertRequest(req, false)
	body, err := MarshalWithExtra(oaiReq, req.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider openai: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider openai: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider openai: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider openai: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var oaiResp openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&oaiResp); err != nil {
		return nil, fmt.Errorf("provider openai: decode response: %w", err)
	}

	return p.convertResponse(&oaiResp), nil
}

// StreamChat 执行流式聊天补全请求
func (p *OpenAIProvider) StreamChat(ctx context.Context, req *ChatRequest) (StreamReader, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider openai: %w", err)
	}

	oaiReq := p.convertRequest(req, true)
	body, err := MarshalWithExtra(oaiReq, req.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider openai: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider openai: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider openai: do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("provider openai: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return &openAIStreamReader{
		reader: bufio.NewReader(resp.Body),
		body:   resp.Body,
	}, nil
}

func (p *OpenAIProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	if p.orgID != "" {
		req.Header.Set("OpenAI-Organization", p.orgID)
	}
}

func (p *OpenAIProvider) convertRequest(req *ChatRequest, stream bool) *openAIRequest {
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

func (p *OpenAIProvider) convertResponse(oaiResp *openAIResponse) *ChatResponse {
	choices := make([]Choice, len(oaiResp.Choices))
	for i, c := range oaiResp.Choices {
		choices[i] = Choice{
			Index:        c.Index,
			Message:      Message{Role: c.Message.Role, Content: c.Message.Content},
			FinishReason: c.FinishReason,
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
		// 提取缓存命中Token：优先从 prompt_tokens_details.cached_tokens（OpenAI），
		// 其次从顶层 prompt_cache_hit_tokens（DeepSeek）
		if oaiResp.Usage.PromptTokensDetails != nil && oaiResp.Usage.PromptTokensDetails.CachedTokens > 0 {
			u.CacheReadTokens = oaiResp.Usage.PromptTokensDetails.CachedTokens
		} else if oaiResp.Usage.PromptCacheHitTokens > 0 {
			u.CacheReadTokens = oaiResp.Usage.PromptCacheHitTokens
		}
		resp.Usage = u
	}
	return resp
}

// openAIStreamReader reads SSE events from the OpenAI streaming response.
type openAIStreamReader struct {
	reader *bufio.Reader
	body   io.ReadCloser
}

func (s *openAIStreamReader) Read() (*StreamChunk, error) {
	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return nil, io.EOF
			}
			return nil, fmt.Errorf("provider openai: read stream: %w", err)
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			return nil, io.EOF
		}

		var chunk openAIStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			logger.L.Debug("provider openai: skip malformed chunk", zap.Error(err))
			continue
		}

		return convertOpenAIStreamChunk(&chunk), nil
	}
}

func (s *openAIStreamReader) Close() error {
	if s.body != nil {
		return s.body.Close()
	}
	return nil
}

type openAIStreamChunk struct {
	ID      string              `json:"id"`
	Model   string              `json:"model"`
	Choices []openAIStreamDelta `json:"choices"`
	Usage   *openAIUsage        `json:"usage,omitempty"`
}

type openAIStreamDelta struct {
	Index        int            `json:"index"`
	Delta        openAIDelta    `json:"delta"`
	FinishReason *string        `json:"finish_reason,omitempty"`
}

type openAIDelta struct {
	Role             string `json:"role,omitempty"`
	Content          string `json:"content,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"` // 深度思考内容
}

func convertOpenAIStreamChunk(chunk *openAIStreamChunk) *StreamChunk {
	choices := make([]StreamChoice, len(chunk.Choices))
	for i, c := range chunk.Choices {
		choices[i] = StreamChoice{
			Index:        c.Index,
			Delta:        DeltaContent{Role: c.Delta.Role, Content: c.Delta.Content, ReasoningContent: c.Delta.ReasoningContent},
			FinishReason: c.FinishReason,
		}
	}
	sc := &StreamChunk{
		ID:      chunk.ID,
		Model:   chunk.Model,
		Choices: choices,
	}
	if chunk.Usage != nil {
		u := &Usage{
			PromptTokens:     chunk.Usage.PromptTokens,
			CompletionTokens: chunk.Usage.CompletionTokens,
			TotalTokens:      chunk.Usage.TotalTokens,
		}
		if chunk.Usage.PromptTokensDetails != nil && chunk.Usage.PromptTokensDetails.CachedTokens > 0 {
			u.CacheReadTokens = chunk.Usage.PromptTokensDetails.CachedTokens
		} else if chunk.Usage.PromptCacheHitTokens > 0 {
			u.CacheReadTokens = chunk.Usage.PromptCacheHitTokens
		}
		sc.Usage = u
	}
	return sc
}
