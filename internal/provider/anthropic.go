// Anthropic适配器，支持Claude模型系列
//
// 支持的模型:
//   - claude-3-5-sonnet-20241022
//   - claude-3-sonnet-20240229
//   - claude-3-opus-20240229
//   - claude-3-haiku-20240307
//
// API参考: https://docs.anthropic.com/en/api/messages
// 特殊处理: system消息被提取到顶层"system"参数
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

var _ Provider = (*AnthropicProvider)(nil)

const (
	anthropicDefaultBaseURL = "https://api.anthropic.com/v1"
	anthropicAPIVersion     = "2023-06-01"
)

var anthropicModels = []string{
	"claude-3-5-sonnet-20241022",
	"claude-3-sonnet-20240229",
	"claude-3-opus-20240229",
	"claude-3-haiku-20240307",
}

// AnthropicProvider 实现Provider接口的Anthropic适配器
type AnthropicProvider struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewAnthropicProvider 创建Anthropic提供商实例
func NewAnthropicProvider(cfg ProviderConfig) *AnthropicProvider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = anthropicDefaultBaseURL
	}
	return &AnthropicProvider{
		apiKey:  cfg.APIKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  newHTTPClient(cfg.TimeoutDuration()),
	}
}

func (p *AnthropicProvider) Name() string      { return "anthropic" }
func (p *AnthropicProvider) ModelList() []string { return anthropicModels }

// Anthropic原生请求类型定义
type anthropicRequest struct {
	Model     string             `json:"model"`
	Messages  []anthropicMessage `json:"messages"`
	System    string             `json:"system,omitempty"`
	MaxTokens int                `json:"max_tokens"`
	Stream    bool               `json:"stream"`
	Stop      []string           `json:"stop_sequences,omitempty"`
	Temp      *float64           `json:"temperature,omitempty"`
	TopP      *float64           `json:"top_p,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	ID      string             `json:"id"`
	Model   string             `json:"model"`
	Type    string             `json:"type"`
	Content []anthropicContent `json:"content"`
	Usage   anthropicUsage     `json:"usage"`
	Stop    string             `json:"stop_reason"`
}

type anthropicContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func (p *AnthropicProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider anthropic: %w", err)
	}

	aReq := p.convertRequest(req, false)
	body, err := json.Marshal(aReq)
	if err != nil {
		return nil, fmt.Errorf("provider anthropic: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider anthropic: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider anthropic: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider anthropic: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var aResp anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&aResp); err != nil {
		return nil, fmt.Errorf("provider anthropic: decode response: %w", err)
	}

	return p.convertResponse(&aResp), nil
}

func (p *AnthropicProvider) StreamChat(ctx context.Context, req *ChatRequest) (StreamReader, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider anthropic: %w", err)
	}

	aReq := p.convertRequest(req, true)
	body, err := json.Marshal(aReq)
	if err != nil {
		return nil, fmt.Errorf("provider anthropic: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider anthropic: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider anthropic: do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("provider anthropic: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return &anthropicStreamReader{
		reader: bufio.NewReader(resp.Body),
		body:   resp.Body,
		model:  req.Model,
	}, nil
}

func (p *AnthropicProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", anthropicAPIVersion)
}

func (p *AnthropicProvider) convertRequest(req *ChatRequest, stream bool) *anthropicRequest {
	var system string
	msgs := make([]anthropicMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		if m.Role == "system" {
			system = m.Content
			continue
		}
		msgs = append(msgs, anthropicMessage{Role: m.Role, Content: m.Content})
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	return &anthropicRequest{
		Model:     req.Model,
		Messages:  msgs,
		System:    system,
		MaxTokens: maxTokens,
		Stream:    stream,
		Stop:      req.Stop,
		Temp:      req.Temperature,
		TopP:      req.TopP,
	}
}

func (p *AnthropicProvider) convertResponse(aResp *anthropicResponse) *ChatResponse {
	content := ""
	for _, c := range aResp.Content {
		if c.Type == "text" {
			content += c.Text
		}
	}
	return &ChatResponse{
		ID:    aResp.ID,
		Model: aResp.Model,
		Choices: []Choice{
			{
				Index:        0,
				Message:      Message{Role: "assistant", Content: content},
				FinishReason: mapAnthropicStopReason(aResp.Stop),
			},
		},
		Usage: Usage{
			PromptTokens:     aResp.Usage.InputTokens,
			CompletionTokens: aResp.Usage.OutputTokens,
			TotalTokens:      aResp.Usage.InputTokens + aResp.Usage.OutputTokens,
		},
	}
}

func mapAnthropicStopReason(reason string) string {
	switch reason {
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	default:
		return reason
	}
}

// anthropicStreamReader handles Anthropic's SSE stream events.
type anthropicStreamReader struct {
	reader *bufio.Reader
	body   io.ReadCloser
	model  string
	msgID  string
}

// Anthropic SSE事件类型定义
type anthropicSSEEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"-"`
}

type anthropicMsgStart struct {
	Type    string `json:"type"`
	Message struct {
		ID    string         `json:"id"`
		Model string         `json:"model"`
		Usage anthropicUsage `json:"usage"`
	} `json:"message"`
}

type anthropicContentDelta struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
}

type anthropicMsgDelta struct {
	Type  string `json:"type"`
	Delta struct {
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage anthropicUsage `json:"usage"`
}

func (s *anthropicStreamReader) Read() (*StreamChunk, error) {
	for {
		eventType, data, err := s.readSSEEvent()
		if err != nil {
			return nil, err
		}

		switch eventType {
		case "message_start":
			var evt anthropicMsgStart
			if err := json.Unmarshal(data, &evt); err != nil {
				logger.L.Debug("provider anthropic: skip malformed message_start", zap.Error(err))
				continue
			}
			s.msgID = evt.Message.ID
			s.model = evt.Message.Model
			continue

		case "content_block_delta":
			var evt anthropicContentDelta
			if err := json.Unmarshal(data, &evt); err != nil {
				logger.L.Debug("provider anthropic: skip malformed content_block_delta", zap.Error(err))
				continue
			}
			return &StreamChunk{
				ID:    s.msgID,
				Model: s.model,
				Choices: []StreamChoice{
					{
						Index: 0,
						Delta: DeltaContent{Content: evt.Delta.Text},
					},
				},
			}, nil

		case "message_delta":
			var evt anthropicMsgDelta
			if err := json.Unmarshal(data, &evt); err != nil {
				logger.L.Debug("provider anthropic: skip malformed message_delta", zap.Error(err))
				continue
			}
			reason := "stop"
			if evt.Delta.StopReason != "" {
				reason = mapAnthropicStopReason(evt.Delta.StopReason)
			}
			return &StreamChunk{
				ID:    s.msgID,
				Model: s.model,
				Choices: []StreamChoice{
					{
						Index:        0,
						FinishReason: &reason,
					},
				},
				Usage: &Usage{
					CompletionTokens: evt.Usage.OutputTokens,
				},
			}, nil

		case "message_stop":
			return nil, io.EOF

		default:
			continue
		}
	}
}

func (s *anthropicStreamReader) readSSEEvent() (string, []byte, error) {
	var eventType string
	var data []byte

	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return "", nil, io.EOF
			}
			return "", nil, fmt.Errorf("provider anthropic: read stream: %w", err)
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if eventType != "" || data != nil {
				return eventType, data, nil
			}
			continue
		}

		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			data = []byte(strings.TrimPrefix(line, "data: "))
		}
	}
}

func (s *anthropicStreamReader) Close() error {
	if s.body != nil {
		return s.body.Close()
	}
	return nil
}
