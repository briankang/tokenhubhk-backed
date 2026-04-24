// Wangsu AI Gateway 适配器
// 封装网宿 AI 网关的三种协议模式：OpenAI 兼容、Anthropic 直连、Google Gemini 直连
//
// 关键差异（与 OpenAI/Anthropic/Google 直连适配器对比）：
//   - URL: 网宿 OpenAI/Anthropic 模式下 endpoint 为完整 URL（不追加 /chat/completions 或 /messages）
//   - URL: Gemini 模式下 endpoint 含 {MODEL} 占位符，请求时替换
//   - Auth: Gemini 模式使用 x-goog-api-key Header（非 ?key= URL 参数）
//   - 模型名转换: Gemini URL 路径中的模型名不含 "gemini." 前缀，请求体字段不传 model
//
// 文档:
//   - 网关创建:        https://www.wangsu.com/document/eca/aigateway003
//   - Chat Completions: https://www.wangsu.com/document/eca/api-chat-completions
//   - Anthropic 直连:  https://www.wangsu.com/document/eca/api-anthropic-direct-mode
package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// WangsuProtocol 表示 Wangsu 网关使用的协议模式
type WangsuProtocol string

const (
	// WangsuProtoOpenAI: 统一签名模式 / OpenAI Chat Completions 兼容
	WangsuProtoOpenAI WangsuProtocol = "openai"
	// WangsuProtoAnthropic: 直连模式 / Anthropic Messages 原生
	WangsuProtoAnthropic WangsuProtocol = "anthropic"
	// WangsuProtoGemini: 直连模式 / Google Gemini 原生（URL 含 {MODEL} 占位符）
	WangsuProtoGemini WangsuProtocol = "gemini"
)

var _ Provider = (*WangsuProvider)(nil)
var _ ImageGenerator = (*WangsuProvider)(nil)
var _ VideoGenerator = (*WangsuProvider)(nil)
var _ VideoTaskQuerier = (*WangsuProvider)(nil)

// WangsuProvider 统一封装三种 Wangsu 协议的 Provider 实现
type WangsuProvider struct {
	apiKey   string
	baseURL  string // 网宿通道 endpoint（OpenAI/Anthropic 为完整 URL；Gemini 为含 {MODEL} 占位的模板）
	protocol WangsuProtocol
	client   *http.Client
}

// NewWangsuProvider 构造 Wangsu 适配器
//
// baseURL 取自 Channel.Endpoint；protocol 取自 Channel.ApiProtocol：
//   - "openai_chat"   → WangsuProtoOpenAI
//   - "anthropic"     → WangsuProtoAnthropic
//   - "google_gemini" → WangsuProtoGemini
func NewWangsuProvider(apiKey, baseURL string, protocol WangsuProtocol, timeout int) *WangsuProvider {
	cfg := ProviderConfig{Timeout: timeout}
	return &WangsuProvider{
		apiKey:   apiKey,
		baseURL:  strings.TrimSpace(baseURL),
		protocol: protocol,
		client:   newHTTPClient(cfg.TimeoutDuration()),
	}
}

func (p *WangsuProvider) Name() string        { return "wangsu" }
func (p *WangsuProvider) ModelList() []string { return nil } // 模型清单由 DB 管理

// Chat 非流式对话
func (p *WangsuProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider wangsu: %w", err)
	}
	switch p.protocol {
	case WangsuProtoOpenAI:
		return p.chatOpenAI(ctx, req, false)
	case WangsuProtoAnthropic:
		return p.chatAnthropic(ctx, req, false)
	case WangsuProtoGemini:
		return p.chatGemini(ctx, req, false)
	default:
		return nil, fmt.Errorf("provider wangsu: unknown protocol %q", p.protocol)
	}
}

// StreamChat 流式对话
func (p *WangsuProvider) StreamChat(ctx context.Context, req *ChatRequest) (StreamReader, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider wangsu: %w", err)
	}
	switch p.protocol {
	case WangsuProtoOpenAI:
		return p.streamOpenAI(ctx, req)
	case WangsuProtoAnthropic:
		return p.streamAnthropic(ctx, req)
	case WangsuProtoGemini:
		return p.streamGemini(ctx, req)
	default:
		return nil, fmt.Errorf("provider wangsu: unknown protocol %q", p.protocol)
	}
}

// --- OpenAI 模式 ---

func (p *WangsuProvider) chatOpenAI(ctx context.Context, req *ChatRequest, isStream bool) (*ChatResponse, error) {
	body, err := p.buildOpenAIBody(req, false)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider wangsu/openai: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider wangsu/openai: do request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider wangsu/openai: status %d: %s", resp.StatusCode, string(respBody))
	}

	var oResp openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&oResp); err != nil {
		return nil, fmt.Errorf("provider wangsu/openai: decode: %w", err)
	}
	return convertOpenAIResponse(req.Model, &oResp), nil
}

func (p *WangsuProvider) streamOpenAI(ctx context.Context, req *ChatRequest) (StreamReader, error) {
	body, err := p.buildOpenAIBody(req, true)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider wangsu/openai: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider wangsu/openai: do request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("provider wangsu/openai: status %d: %s", resp.StatusCode, string(respBody))
	}

	// ⚠️ 网宿特殊行为（2026-04-22 实测）：
	// 只有 tokenhubhk_gpt 网关真正支持 SSE 流式（`Content-Type: text/event-stream`）；
	// tokenhubhk_claude / tokenhubhk_gemini 无论 stream=true 与否，都返回整段 JSON
	// （`Content-Type: application/json`），不分片。
	// 策略：按响应 Content-Type 自适应 —— 非 event-stream 时把整段 JSON 包装成单 chunk + [DONE] 输出，
	// 保证 SSEWriter / messages_handler 的 stream reader 能正常聚合 usage / content。
	ctype := strings.ToLower(resp.Header.Get("Content-Type"))
	if !strings.Contains(ctype, "event-stream") {
		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("provider wangsu/openai: read non-stream body: %w", readErr)
		}
		return newWangsuFakeStreamReader(respBody, req.Model), nil
	}

	return &openAIStreamReader{
		reader: bufio.NewReader(resp.Body),
		body:   resp.Body,
	}, nil
}

// wangsuFakeStreamReader 把一整段非流式 OpenAI JSON 响应伪装成逐 chunk 流
// 用于 Wangsu Claude/Gemini 网关的情形（它们不真正支持 SSE，stream=true 也返回整段 JSON）
type wangsuFakeStreamReader struct {
	chunks []*StreamChunk
	idx    int
}

func newWangsuFakeStreamReader(body []byte, fallbackModel string) *wangsuFakeStreamReader {
	var oResp openAIResponse
	if err := json.Unmarshal(body, &oResp); err != nil {
		// 无法解析则返回一个空终止流，避免 SSEWriter 死锁
		return &wangsuFakeStreamReader{chunks: nil}
	}
	model := oResp.Model
	if model == "" {
		model = fallbackModel
	}

	var chunks []*StreamChunk

	// chunk 1: role delta（首块）
	if len(oResp.Choices) > 0 {
		chunks = append(chunks, &StreamChunk{
			ID:    oResp.ID,
			Model: model,
			Choices: []StreamChoice{{
				Index: 0,
				Delta: DeltaContent{Role: "assistant"},
			}},
		})

		// chunk 2: content delta（整段文本作为一次 delta）
		contentStr, _ := oResp.Choices[0].Message.Content.(string)
		if contentStr != "" {
			chunks = append(chunks, &StreamChunk{
				ID:    oResp.ID,
				Model: model,
				Choices: []StreamChoice{{
					Index: 0,
					Delta: DeltaContent{Content: contentStr},
				}},
			})
		}

		// chunk 3: finish_reason
		finishReason := oResp.Choices[0].FinishReason
		if finishReason == "" {
			finishReason = "stop"
		}
		chunks = append(chunks, &StreamChunk{
			ID:    oResp.ID,
			Model: model,
			Choices: []StreamChoice{{
				Index:        0,
				Delta:        DeltaContent{},
				FinishReason: &finishReason,
			}},
		})
	}

	// chunk 4: usage（尾块，SSEWriter 需要从此处读 usage）
	if oResp.Usage != nil {
		chunks = append(chunks, &StreamChunk{
			ID:    oResp.ID,
			Model: model,
			Usage: &Usage{
				PromptTokens:     oResp.Usage.PromptTokens,
				CompletionTokens: oResp.Usage.CompletionTokens,
				TotalTokens:      oResp.Usage.TotalTokens,
			},
		})
	}

	return &wangsuFakeStreamReader{chunks: chunks}
}

func (s *wangsuFakeStreamReader) Read() (*StreamChunk, error) {
	if s.idx >= len(s.chunks) {
		return nil, io.EOF
	}
	c := s.chunks[s.idx]
	s.idx++
	return c, nil
}

func (s *wangsuFakeStreamReader) Close() error { return nil }

func (p *WangsuProvider) buildOpenAIBody(req *ChatRequest, stream bool) ([]byte, error) {
	msgs := make([]openAIMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, openAIMessage{Role: m.Role, Content: m.Content})
	}
	oReq := openAIRequest{
		Model:       req.Model,
		Messages:    msgs,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stream:      stream,
		Stop:        req.Stop,
	}
	b, err := MarshalWithExtra(oReq, req.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider wangsu/openai: marshal: %w", err)
	}
	return b, nil
}

// --- OpenAI 图片生成模式 ---

type wangsuImageRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              int    `json:"n,omitempty"`
	Size           string `json:"size,omitempty"`
	Quality        string `json:"quality,omitempty"`
	Style          string `json:"style,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"`
	NegativePrompt string `json:"negative_prompt,omitempty"`
	Seed           int64  `json:"seed,omitempty"`
}

type wangsuImageResponse struct {
	Created int64       `json:"created"`
	Model   string      `json:"model,omitempty"`
	Data    []ImageData `json:"data"`
	Error   any         `json:"error,omitempty"`
}

// GenerateImage 调用网宿统一签名模式下的 OpenAI 兼容图片生成端点。
//
// 网宿图片网关的 Channel.Endpoint 配置为完整 URL：
// https://aigateway.edgecloudapp.com/v1/{gateway}/coze-gpt-image/images/generations
func (p *WangsuProvider) GenerateImage(ctx context.Context, req *ImageRequest) (*ImageResponse, error) {
	if p.protocol != WangsuProtoOpenAI {
		return nil, fmt.Errorf("provider wangsu: protocol %s does not support image generation", p.protocol)
	}
	if req == nil {
		return nil, fmt.Errorf("provider wangsu/image: request is nil")
	}
	if strings.TrimSpace(req.Model) == "" {
		return nil, fmt.Errorf("provider wangsu/image: model is required")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, fmt.Errorf("provider wangsu/image: prompt is required")
	}

	n := req.N
	if n <= 0 {
		n = 1
	}
	bodyReq := wangsuImageRequest{
		Model:          req.Model,
		Prompt:         req.Prompt,
		N:              n,
		Size:           req.Size,
		Quality:        req.Quality,
		Style:          req.Style,
		ResponseFormat: req.ResponseFormat,
		NegativePrompt: req.NegativePrompt,
		Seed:           req.Seed,
	}
	body, err := MarshalWithExtra(bodyReq, req.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider wangsu/image: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider wangsu/image: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider wangsu/image: do request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider wangsu/image: status %d: %s", resp.StatusCode, string(respBody))
	}

	var out wangsuImageResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("provider wangsu/image: decode: %w", err)
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("provider wangsu/image: empty image response")
	}
	return &ImageResponse{
		Created: out.Created,
		Model:   pickStringProvider(out.Model, req.Model),
		Data:    out.Data,
	}, nil
}

// --- Anthropic 模式（直连，endpoint 即完整 URL）---

func (p *WangsuProvider) chatAnthropic(ctx context.Context, req *ChatRequest, isStream bool) (*ChatResponse, error) {
	body, err := p.buildAnthropicBody(req, false)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider wangsu/anthropic: create request: %w", err)
	}
	p.setAnthropicHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider wangsu/anthropic: do request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider wangsu/anthropic: status %d: %s", resp.StatusCode, string(respBody))
	}
	var aResp anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&aResp); err != nil {
		return nil, fmt.Errorf("provider wangsu/anthropic: decode: %w", err)
	}
	return convertAnthropicResponse(&aResp), nil
}

func (p *WangsuProvider) streamAnthropic(ctx context.Context, req *ChatRequest) (StreamReader, error) {
	body, err := p.buildAnthropicBody(req, true)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider wangsu/anthropic: create request: %w", err)
	}
	p.setAnthropicHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider wangsu/anthropic: do request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("provider wangsu/anthropic: status %d: %s", resp.StatusCode, string(respBody))
	}
	return &anthropicStreamReader{
		reader: bufio.NewReader(resp.Body),
		body:   resp.Body,
		model:  req.Model,
	}, nil
}

func (p *WangsuProvider) setAnthropicHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", p.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
}

func (p *WangsuProvider) buildAnthropicBody(req *ChatRequest, stream bool) ([]byte, error) {
	var systemText string
	msgs := make([]anthropicMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		if m.Role == "system" {
			systemText = TextContent(m.Content)
			continue
		}
		msgs = append(msgs, anthropicMessage{Role: m.Role, Content: m.Content})
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	aReq := &anthropicRequest{
		Model:     req.Model,
		Messages:  msgs,
		MaxTokens: maxTokens,
		Stream:    stream,
		Stop:      req.Stop,
		Temp:      req.Temperature,
		TopP:      req.TopP,
	}
	if systemText != "" {
		aReq.System = systemText
	}
	b, err := MarshalWithExtra(aReq, req.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider wangsu/anthropic: marshal: %w", err)
	}
	return b, nil
}

// --- Gemini 模式（直连，endpoint 含 {MODEL} 占位符）---

func (p *WangsuProvider) chatGemini(ctx context.Context, req *ChatRequest, isStream bool) (*ChatResponse, error) {
	url := p.buildGeminiURL(req.Model, false)
	body, err := p.buildGeminiBody(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider wangsu/gemini: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-goog-api-key", p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider wangsu/gemini: do request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider wangsu/gemini: status %d: %s", resp.StatusCode, string(respBody))
	}
	var gResp geminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&gResp); err != nil {
		return nil, fmt.Errorf("provider wangsu/gemini: decode: %w", err)
	}
	return convertGeminiResponse(req.Model, &gResp), nil
}

func (p *WangsuProvider) streamGemini(ctx context.Context, req *ChatRequest) (StreamReader, error) {
	url := p.buildGeminiURL(req.Model, true)
	body, err := p.buildGeminiBody(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider wangsu/gemini: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-goog-api-key", p.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider wangsu/gemini: do request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("provider wangsu/gemini: status %d: %s", resp.StatusCode, string(respBody))
	}
	return &googleStreamReader{
		reader: bufio.NewReader(resp.Body),
		body:   resp.Body,
		model:  req.Model,
	}, nil
}

// buildGeminiURL 将 baseURL 中的 {MODEL} 占位符替换为模型名（去前缀）
// 流式调用时将 :generateContent 替换为 :streamGenerateContent?alt=sse
func (p *WangsuProvider) buildGeminiURL(model string, stream bool) string {
	// 模型名去前缀：Wangsu URL 路径中不含 "gemini." 前缀
	modelPath := strings.TrimPrefix(model, "gemini.")
	url := strings.ReplaceAll(p.baseURL, "{MODEL}", modelPath)
	if stream {
		url = strings.ReplaceAll(url, ":generateContent", ":streamGenerateContent?alt=sse")
	}
	return url
}

func (p *WangsuProvider) buildGeminiBody(req *ChatRequest) ([]byte, error) {
	var systemContent *geminiContent
	contents := make([]geminiContent, 0, len(req.Messages))
	for _, m := range req.Messages {
		if m.Role == "system" {
			systemContent = &geminiContent{
				Parts: []geminiPart{{Text: TextContent(m.Content)}},
			}
			continue
		}
		role := "user"
		if m.Role == "assistant" {
			role = "model"
		}
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
	b, err := MarshalWithExtra(gReq, req.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider wangsu/gemini: marshal: %w", err)
	}
	return b, nil
}

// --- 响应转换助手（复用 openai/anthropic/google 已有类型）---

func convertOpenAIResponse(model string, oResp *openAIResponse) *ChatResponse {
	choices := make([]Choice, len(oResp.Choices))
	for i, c := range oResp.Choices {
		contentStr, _ := c.Message.Content.(string)
		choices[i] = Choice{
			Index:        c.Index,
			Message:      Message{Role: c.Message.Role, Content: contentStr},
			FinishReason: c.FinishReason,
		}
	}
	resp := &ChatResponse{
		ID:      oResp.ID,
		Model:   pickStringProvider(oResp.Model, model),
		Choices: choices,
	}
	if oResp.Usage != nil {
		resp.Usage = Usage{
			PromptTokens:     oResp.Usage.PromptTokens,
			CompletionTokens: oResp.Usage.CompletionTokens,
			TotalTokens:      oResp.Usage.TotalTokens,
		}
	}
	return resp
}

func convertAnthropicResponse(aResp *anthropicResponse) *ChatResponse {
	text := ""
	for _, c := range aResp.Content {
		if c.Type == "text" {
			text += c.Text
		}
	}
	finishReason := aResp.Stop
	switch finishReason {
	case "end_turn":
		finishReason = "stop"
	case "max_tokens":
		finishReason = "length"
	}
	return &ChatResponse{
		ID:    aResp.ID,
		Model: aResp.Model,
		Choices: []Choice{{
			Index:        0,
			Message:      Message{Role: "assistant", Content: text},
			FinishReason: finishReason,
		}},
		Usage: Usage{
			PromptTokens:     aResp.Usage.InputTokens,
			CompletionTokens: aResp.Usage.OutputTokens,
			TotalTokens:      aResp.Usage.InputTokens + aResp.Usage.OutputTokens,
		},
	}
}

func convertGeminiResponse(model string, gResp *geminiResponse) *ChatResponse {
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
		ID:      fmt.Sprintf("wangsu-gemini-%s", model),
		Model:   model,
		Choices: choices,
	}
	if gResp.UsageMetadata != nil {
		resp.Usage = Usage{
			PromptTokens:     gResp.UsageMetadata.PromptTokenCount,
			CompletionTokens: gResp.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      gResp.UsageMetadata.TotalTokenCount,
		}
	}
	return resp
}

func pickStringProvider(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// GenerateVideo calls Wangsu AI Gateway's /videos endpoint in unified-signature mode.
func (p *WangsuProvider) GenerateVideo(ctx context.Context, req *VideoRequest) (*VideoResponse, error) {
	if strings.TrimSpace(req.Model) == "" {
		return nil, fmt.Errorf("provider wangsu/video: model is required")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, fmt.Errorf("provider wangsu/video: prompt is required")
	}

	body := map[string]interface{}{
		"model":  req.Model,
		"prompt": req.Prompt,
	}
	if req.ImageURL != "" {
		body["image_url"] = req.ImageURL
	}
	if req.Duration > 0 {
		body["duration"] = req.Duration
	}
	if req.Resolution != "" {
		body["resolution"] = strings.ToLower(req.Resolution)
	}
	if req.AspectRatio != "" {
		body["aspect_ratio"] = req.AspectRatio
	}
	if req.FPS > 0 {
		body["fps"] = req.FPS
	}
	if req.NegativePrompt != "" {
		body["negative_prompt"] = req.NegativePrompt
	}
	if req.Seed != 0 {
		body["seed"] = req.Seed
	}
	for k, v := range req.Extra {
		if _, exists := body[k]; !exists {
			body[k] = v
		}
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("provider wangsu/video: marshal: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.videoURL(), bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("provider wangsu/video: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider wangsu/video: do request: %w", err)
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, fmt.Errorf("provider wangsu/video: read response: %w", readErr)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return nil, fmt.Errorf("provider wangsu/video: status %d: %s", resp.StatusCode, string(respBody))
	}

	return parseWangsuVideoResponse(respBody, req.Model, req.Duration)
}

// QueryVideoTask queries a Wangsu async video task using common gateway paths.
func (p *WangsuProvider) QueryVideoTask(ctx context.Context, taskID string) (*VideoResponse, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, fmt.Errorf("provider wangsu/video: task_id is required")
	}
	var lastErr error
	for _, queryURL := range p.videoTaskURLs(taskID) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, queryURL, nil)
		if err != nil {
			lastErr = err
			continue
		}
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
		resp, err := p.client.Do(httpReq)
		if err != nil {
			lastErr = err
			continue
		}
		respBody, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode >= 400 {
			if len(respBody) == 0 {
				lastErr = fmt.Errorf("status %d", resp.StatusCode)
			} else {
				lastErr = fmt.Errorf("status %d: %s", resp.StatusCode, string(respBody))
			}
			continue
		}
		out, err := parseWangsuVideoResponse(respBody, "", 0)
		if err != nil {
			lastErr = err
			continue
		}
		if out.TaskID == "" {
			out.TaskID = taskID
		}
		return out, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no query endpoint attempted")
	}
	return nil, fmt.Errorf("provider wangsu/video: query task %s failed: %w", taskID, lastErr)
}

func (p *WangsuProvider) videoURL() string {
	base := strings.TrimRight(strings.TrimSpace(p.baseURL), "/")
	if strings.HasSuffix(base, "/videos") {
		return base
	}
	return base + "/videos"
}

func (p *WangsuProvider) videoTaskURLs(taskID string) []string {
	base := strings.TrimRight(strings.TrimSpace(p.baseURL), "/")
	escaped := url.PathEscape(taskID)
	queryEscaped := url.QueryEscape(taskID)
	return []string{
		base + "/videos/tasks/" + escaped,
		base + "/videos/" + escaped,
		base + "/videos/generations/" + escaped,
		base + "/videos/status/" + escaped,
		base + "/videos?task_id=" + queryEscaped,
	}
}

type wangsuVideoResponse struct {
	ID      string `json:"id"`
	TaskID  string `json:"task_id"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Status  string `json:"status"`
	Data    []struct {
		URL           string `json:"url"`
		VideoURL      string `json:"video_url"`
		CoverURL      string `json:"cover_url"`
		DurationSec   int    `json:"duration_sec"`
		RevisedPrompt string `json:"revised_prompt"`
	} `json:"data"`
	Output struct {
		TaskID   string `json:"task_id"`
		Status   string `json:"status"`
		VideoURL string `json:"video_url"`
		URL      string `json:"url"`
		CoverURL string `json:"cover_url"`
		Videos   []struct {
			URL         string `json:"url"`
			VideoURL    string `json:"video_url"`
			CoverURL    string `json:"cover_url"`
			DurationSec int    `json:"duration_sec"`
		} `json:"videos"`
	} `json:"output"`
	Result struct {
		TaskID   string `json:"task_id"`
		Status   string `json:"status"`
		VideoURL string `json:"video_url"`
		URL      string `json:"url"`
		CoverURL string `json:"cover_url"`
	} `json:"result"`
	VideoURL string `json:"video_url"`
	URL      string `json:"url"`
}

func parseWangsuVideoResponse(raw []byte, fallbackModel string, fallbackDuration int) (*VideoResponse, error) {
	var wr wangsuVideoResponse
	if err := json.Unmarshal(raw, &wr); err != nil {
		return nil, fmt.Errorf("provider wangsu/video: decode: %w", err)
	}
	created := wr.Created
	if created == 0 {
		created = time.Now().Unix()
	}
	modelName := wr.Model
	if modelName == "" {
		modelName = fallbackModel
	}
	out := &VideoResponse{
		Created: created,
		Model:   modelName,
		TaskID:  firstNonEmpty(wr.TaskID, wr.ID, wr.Output.TaskID, wr.Result.TaskID),
		Status:  normalizeVideoStatus(firstNonEmpty(wr.Status, wr.Output.Status, wr.Result.Status)),
	}

	for _, item := range wr.Data {
		url := firstNonEmpty(item.URL, item.VideoURL)
		if url == "" {
			continue
		}
		out.Data = append(out.Data, VideoData{
			URL:           url,
			CoverURL:      item.CoverURL,
			DurationSec:   item.DurationSec,
			RevisedPrompt: item.RevisedPrompt,
		})
	}
	for _, item := range wr.Output.Videos {
		url := firstNonEmpty(item.URL, item.VideoURL)
		if url == "" {
			continue
		}
		out.Data = append(out.Data, VideoData{URL: url, CoverURL: item.CoverURL, DurationSec: item.DurationSec})
	}
	for _, url := range []string{
		firstNonEmpty(wr.Output.URL, wr.Output.VideoURL),
		firstNonEmpty(wr.Result.URL, wr.Result.VideoURL),
		firstNonEmpty(wr.URL, wr.VideoURL),
	} {
		if url != "" {
			out.Data = append(out.Data, VideoData{URL: url, CoverURL: firstNonEmpty(wr.Output.CoverURL, wr.Result.CoverURL)})
			break
		}
	}
	if fallbackDuration > 0 {
		for i := range out.Data {
			if out.Data[i].DurationSec == 0 {
				out.Data[i].DurationSec = fallbackDuration
			}
		}
	}
	if out.Status == "" {
		if len(out.Data) > 0 {
			out.Status = "succeeded"
		} else if out.TaskID != "" {
			out.Status = "processing"
		}
	}
	if len(out.Data) == 0 && out.TaskID == "" {
		return nil, fmt.Errorf("provider wangsu/video: response missing video url or task id")
	}
	return out, nil
}

func normalizeVideoStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "succeeded", "success", "completed", "complete", "done", "finished":
		return "succeeded"
	case "failed", "fail", "error", "canceled", "cancelled":
		return "failed"
	case "running", "processing", "pending", "queued", "created", "submitted", "in_progress":
		return "processing"
	default:
		return strings.ToLower(strings.TrimSpace(status))
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
