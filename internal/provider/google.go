// Google Gemini适配器，支持Gemini模型系列
//
// 支持的模型:
//   - gemini-1.5-pro
//   - gemini-1.5-flash
//   - gemini-2.0-flash
//   - gemini-2.0-pro
//
// API参考: https://ai.google.dev/api/generate-content
// 特殊处理: 消息转换为"contents"格式，角色映射为 user/model
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

var _ Provider = (*GoogleProvider)(nil)

const googleDefaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"

var googleModels = []string{
	"gemini-1.5-pro",
	"gemini-1.5-flash",
	"gemini-2.0-flash",
	"gemini-2.0-pro",
}

// GoogleProvider 实现Provider接口的Google Gemini适配器
type GoogleProvider struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewGoogleProvider 创建Google Gemini提供商实例
func NewGoogleProvider(cfg ProviderConfig) *GoogleProvider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = googleDefaultBaseURL
	}
	return &GoogleProvider{
		apiKey:  cfg.APIKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  newHTTPClient(cfg.TimeoutDuration()),
	}
}

func (p *GoogleProvider) Name() string      { return "google" }
func (p *GoogleProvider) ModelList() []string { return googleModels }

// Google Gemini原生请求/响应类型定义
type geminiRequest struct {
	Contents         []geminiContent        `json:"contents"`
	SystemInstruct   *geminiContent         `json:"systemInstruction,omitempty"`
	GenerationConfig *geminiGenerationCfg   `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiGenerationCfg struct {
	MaxOutputTokens int      `json:"maxOutputTokens,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"topP,omitempty"`
	StopSequences   []string `json:"stopSequences,omitempty"`
}

type geminiResponse struct {
	Candidates    []geminiCandidate `json:"candidates"`
	UsageMetadata *geminiUsage      `json:"usageMetadata,omitempty"`
}

type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason"`
	Index        int           `json:"index"`
}

type geminiUsage struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	TotalTokenCount         int `json:"totalTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount,omitempty"`
}

func (p *GoogleProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider google: %w", err)
	}

	gReq := p.convertRequest(req)
	body, err := MarshalWithExtra(gReq, req.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider google: marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", p.baseURL, req.Model, p.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider google: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider google: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider google: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var gResp geminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&gResp); err != nil {
		return nil, fmt.Errorf("provider google: decode response: %w", err)
	}

	return p.convertResponse(req.Model, &gResp), nil
}

func (p *GoogleProvider) StreamChat(ctx context.Context, req *ChatRequest) (StreamReader, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider google: %w", err)
	}

	gReq := p.convertRequest(req)
	body, err := MarshalWithExtra(gReq, req.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider google: marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/models/%s:streamGenerateContent?alt=sse&key=%s", p.baseURL, req.Model, p.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider google: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider google: do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("provider google: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return &googleStreamReader{
		reader: bufio.NewReader(resp.Body),
		body:   resp.Body,
		model:  req.Model,
	}, nil
}

func (p *GoogleProvider) convertRequest(req *ChatRequest) *geminiRequest {
	var systemContent *geminiContent
	contents := make([]geminiContent, 0, len(req.Messages))

	for _, m := range req.Messages {
		if m.Role == "system" {
			systemContent = &geminiContent{
				Parts: []geminiPart{{Text: TextContent(m.Content)}},
			}
			continue
		}
		role := mapGeminiRole(m.Role)
		contents = append(contents, geminiContent{
			Role:  role,
			Parts: []geminiPart{{Text: TextContent(m.Content)}},
		})
	}

	gReq := &geminiRequest{
		Contents:       contents,
		SystemInstruct: systemContent,
	}

	if req.MaxTokens > 0 || req.Temperature != nil || req.TopP != nil || len(req.Stop) > 0 {
		gReq.GenerationConfig = &geminiGenerationCfg{
			MaxOutputTokens: req.MaxTokens,
			Temperature:     req.Temperature,
			TopP:            req.TopP,
			StopSequences:   req.Stop,
		}
	}

	return gReq
}

func mapGeminiRole(role string) string {
	switch role {
	case "assistant":
		return "model"
	default:
		return role // "user" stays "user"
	}
}

func (p *GoogleProvider) convertResponse(model string, gResp *geminiResponse) *ChatResponse {
	choices := make([]Choice, len(gResp.Candidates))
	for i, c := range gResp.Candidates {
		text := ""
		for _, part := range c.Content.Parts {
			text += part.Text
		}
		choices[i] = Choice{
			Index:        c.Index,
			Message:      Message{Role: "assistant", Content: text},
			FinishReason: mapGeminiFinishReason(c.FinishReason),
		}
	}

	resp := &ChatResponse{
		ID:      fmt.Sprintf("gemini-%s", model),
		Model:   model,
		Choices: choices,
	}
	if gResp.UsageMetadata != nil {
		resp.Usage = Usage{
			PromptTokens:     gResp.UsageMetadata.PromptTokenCount,
			CompletionTokens: gResp.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      gResp.UsageMetadata.TotalTokenCount,
			CacheReadTokens:  gResp.UsageMetadata.CachedContentTokenCount,
		}
	}
	return resp
}

func mapGeminiFinishReason(reason string) string {
	switch reason {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY":
		return "content_filter"
	default:
		return reason
	}
}

// googleStreamReader reads SSE events from the Google Gemini streaming response.
type googleStreamReader struct {
	reader *bufio.Reader
	body   io.ReadCloser
	model  string
	index  int
}

func (s *googleStreamReader) Read() (*StreamChunk, error) {
	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return nil, io.EOF
			}
			return nil, fmt.Errorf("provider google: read stream: %w", err)
		}

		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			return nil, io.EOF
		}

		var gResp geminiResponse
		if err := json.Unmarshal([]byte(data), &gResp); err != nil {
			logger.L.Debug("provider google: skip malformed chunk", zap.Error(err))
			continue
		}

		chunk := &StreamChunk{
			ID:    fmt.Sprintf("gemini-%s-%d", s.model, s.index),
			Model: s.model,
		}
		s.index++

		for _, candidate := range gResp.Candidates {
			text := ""
			for _, part := range candidate.Content.Parts {
				text += part.Text
			}
			var finishReason *string
			if candidate.FinishReason != "" {
				r := mapGeminiFinishReason(candidate.FinishReason)
				finishReason = &r
			}
			chunk.Choices = append(chunk.Choices, StreamChoice{
				Index:        candidate.Index,
				Delta:        DeltaContent{Content: text},
				FinishReason: finishReason,
			})
		}

		if gResp.UsageMetadata != nil {
			chunk.Usage = &Usage{
				PromptTokens:     gResp.UsageMetadata.PromptTokenCount,
				CompletionTokens: gResp.UsageMetadata.CandidatesTokenCount,
				TotalTokens:      gResp.UsageMetadata.TotalTokenCount,
				CacheReadTokens:  gResp.UsageMetadata.CachedContentTokenCount,
			}
		}

		return chunk, nil
	}
}

func (s *googleStreamReader) Close() error {
	if s.body != nil {
		return s.body.Close()
	}
	return nil
}
