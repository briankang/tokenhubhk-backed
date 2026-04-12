// 百度文心一言（Wenxin/ERNIE）适配器
//
// Supported models:
//   - ernie-4.0-8k
//   - ernie-3.5-8k
//   - ernie-speed-128k
//
// API reference: https://cloud.baidu.com/doc/WENXINWORKSHOP/s/Dlkm79mnx
// 特殊处理: access_token认证，消息格式略有不同
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
	"sync"
	"time"

	"tokenhub-server/internal/pkg/logger"

	"go.uber.org/zap"
)

var _ Provider = (*WenxinProvider)(nil)

const wenxinDefaultBaseURL = "https://aip.baidubce.com/rpc/2.0/ai_custom/v1/wenxinworkshop/chat"

var wenxinModels = []string{
	"ernie-4.0-8k",
	"ernie-3.5-8k",
	"ernie-speed-128k",
}

// wenxinModelEndpoints maps model names to their endpoint suffixes.
var wenxinModelEndpoints = map[string]string{
	"ernie-4.0-8k":     "completions_pro",
	"ernie-3.5-8k":     "completions",
	"ernie-speed-128k":  "ernie_speed",
}

// WenxinProvider 实现Provider接口的百度文心一言适配器
type WenxinProvider struct {
	apiKey    string // client_id
	secretKey string // client_secret (stored in OrgID field)
	baseURL   string
	client    *http.Client

	mu          sync.RWMutex
	accessToken string
	tokenExpiry time.Time
}

// NewWenxinProvider 创建百度文心一言提供商实例
// cfg.APIKey is used as client_id, cfg.OrgID as client_secret.
func NewWenxinProvider(cfg ProviderConfig) *WenxinProvider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = wenxinDefaultBaseURL
	}
	return &WenxinProvider{
		apiKey:    cfg.APIKey,
		secretKey: cfg.OrgID,
		baseURL:   strings.TrimRight(baseURL, "/"),
		client:    newHTTPClient(cfg.TimeoutDuration()),
	}
}

func (p *WenxinProvider) Name() string      { return "wenxin" }
func (p *WenxinProvider) ModelList() []string { return wenxinModels }

// 文心一言原生请求/响应类型定义
type wenxinRequest struct {
	Messages []wenxinMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Temp     *float64        `json:"temperature,omitempty"`
	TopP     *float64        `json:"top_p,omitempty"`
	System   string          `json:"system,omitempty"`
}

type wenxinMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type wenxinResponse struct {
	ID               string `json:"id"`
	Result           string `json:"result"`
	NeedClearHistory bool   `json:"need_clear_history"`
	Usage            struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	ErrorCode int    `json:"error_code"`
	ErrorMsg  string `json:"error_msg"`
}

type wenxinTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	Error       string `json:"error"`
}

func (p *WenxinProvider) getAccessToken(ctx context.Context) (string, error) {
	p.mu.RLock()
	if p.accessToken != "" && time.Now().Before(p.tokenExpiry) {
		token := p.accessToken
		p.mu.RUnlock()
		return token, nil
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()

	// Double check after acquiring write lock.
	if p.accessToken != "" && time.Now().Before(p.tokenExpiry) {
		return p.accessToken, nil
	}

	url := fmt.Sprintf("https://aip.baidubce.com/oauth/2.0/token?grant_type=client_credentials&client_id=%s&client_secret=%s",
		p.apiKey, p.secretKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", fmt.Errorf("provider wenxin: create token request: %w", err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("provider wenxin: do token request: %w", err)
	}
	defer resp.Body.Close()

	var tokenResp wenxinTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("provider wenxin: decode token response: %w", err)
	}

	if tokenResp.Error != "" {
		return "", fmt.Errorf("provider wenxin: token error: %s", tokenResp.Error)
	}

	p.accessToken = tokenResp.AccessToken
	p.tokenExpiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn-300) * time.Second) // refresh 5 min early

	return p.accessToken, nil
}

func (p *WenxinProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider wenxin: %w", err)
	}

	token, err := p.getAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	wReq := p.convertRequest(req, false)
	body, err := json.Marshal(wReq)
	if err != nil {
		return nil, fmt.Errorf("provider wenxin: marshal request: %w", err)
	}

	endpoint := p.getEndpoint(req.Model)
	url := fmt.Sprintf("%s/%s?access_token=%s", p.baseURL, endpoint, token)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider wenxin: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider wenxin: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider wenxin: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var wResp wenxinResponse
	if err := json.NewDecoder(resp.Body).Decode(&wResp); err != nil {
		return nil, fmt.Errorf("provider wenxin: decode response: %w", err)
	}

	if wResp.ErrorCode != 0 {
		return nil, fmt.Errorf("provider wenxin: API error %d: %s", wResp.ErrorCode, wResp.ErrorMsg)
	}

	return p.convertResponse(req.Model, &wResp), nil
}

func (p *WenxinProvider) StreamChat(ctx context.Context, req *ChatRequest) (StreamReader, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider wenxin: %w", err)
	}

	token, err := p.getAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	wReq := p.convertRequest(req, true)
	body, err := json.Marshal(wReq)
	if err != nil {
		return nil, fmt.Errorf("provider wenxin: marshal request: %w", err)
	}

	endpoint := p.getEndpoint(req.Model)
	url := fmt.Sprintf("%s/%s?access_token=%s", p.baseURL, endpoint, token)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider wenxin: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider wenxin: do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("provider wenxin: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return &wenxinStreamReader{
		reader: bufio.NewReader(resp.Body),
		body:   resp.Body,
		model:  req.Model,
	}, nil
}

func (p *WenxinProvider) getEndpoint(model string) string {
	if ep, ok := wenxinModelEndpoints[model]; ok {
		return ep
	}
	return "completions"
}

func (p *WenxinProvider) convertRequest(req *ChatRequest, stream bool) *wenxinRequest {
	var system string
	msgs := make([]wenxinMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		if m.Role == "system" {
			system = m.Content
			continue
		}
		msgs = append(msgs, wenxinMessage{Role: m.Role, Content: m.Content})
	}

	return &wenxinRequest{
		Messages: msgs,
		Stream:   stream,
		System:   system,
		Temp:     req.Temperature,
		TopP:     req.TopP,
	}
}

func (p *WenxinProvider) convertResponse(model string, wResp *wenxinResponse) *ChatResponse {
	return &ChatResponse{
		ID:    wResp.ID,
		Model: model,
		Choices: []Choice{
			{
				Index:        0,
				Message:      Message{Role: "assistant", Content: wResp.Result},
				FinishReason: "stop",
			},
		},
		Usage: Usage{
			PromptTokens:     wResp.Usage.PromptTokens,
			CompletionTokens: wResp.Usage.CompletionTokens,
			TotalTokens:      wResp.Usage.TotalTokens,
		},
	}
}

// wenxinStreamReader reads SSE events from the Wenxin streaming response.
type wenxinStreamReader struct {
	reader *bufio.Reader
	body   io.ReadCloser
	model  string
}

func (s *wenxinStreamReader) Read() (*StreamChunk, error) {
	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return nil, io.EOF
			}
			return nil, fmt.Errorf("provider wenxin: read stream: %w", err)
		}

		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		var wResp wenxinResponse
		if err := json.Unmarshal([]byte(data), &wResp); err != nil {
			logger.L.Debug("provider wenxin: skip malformed chunk", zap.Error(err))
			continue
		}

		if wResp.ErrorCode != 0 {
			return nil, fmt.Errorf("provider wenxin: stream error %d: %s", wResp.ErrorCode, wResp.ErrorMsg)
		}

		chunk := &StreamChunk{
			ID:    wResp.ID,
			Model: s.model,
			Choices: []StreamChoice{
				{
					Index: 0,
					Delta: DeltaContent{Content: wResp.Result},
				},
			},
		}

		if wResp.Usage.TotalTokens > 0 {
			reason := "stop"
			chunk.Choices[0].FinishReason = &reason
			chunk.Usage = &Usage{
				PromptTokens:     wResp.Usage.PromptTokens,
				CompletionTokens: wResp.Usage.CompletionTokens,
				TotalTokens:      wResp.Usage.TotalTokens,
			}
		}

		return chunk, nil
	}
}

func (s *wenxinStreamReader) Close() error {
	if s.body != nil {
		return s.body.Close()
	}
	return nil
}
