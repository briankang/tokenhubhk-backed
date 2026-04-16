// DeepSeek Coder 代码补全适配器
//
// 支持的模型:
//   - deepseek-coder (DeepSeek Coder)
//
// API参考: https://platform.deepseek.com/api-docs
// 特殊功能: 支持 FIM (Fill-in-the-Middle) 代码补全
//   - POST /v1/completions 端点
//   - 使用 prompt + suffix 参数实现中间填充
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

var _ Provider = (*CodingDeepSeekProvider)(nil)

const codingDeepSeekDefaultBaseURL = "https://api.deepseek.com"

// codingDeepSeekModels DeepSeek Coder 支持的模型列表
var codingDeepSeekModels = []string{
	"deepseek-coder",
}

// CodingDeepSeekProvider DeepSeek Coder 代码补全适配器
// 支持 OpenAI 兼容的 chat/completions 以及 FIM completions 端点
type CodingDeepSeekProvider struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewCodingDeepSeekProvider 创建 DeepSeek Coder 提供商实例
func NewCodingDeepSeekProvider(cfg ProviderConfig) *CodingDeepSeekProvider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = codingDeepSeekDefaultBaseURL
	}
	return &CodingDeepSeekProvider{
		apiKey:  cfg.APIKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  newHTTPClient(cfg.TimeoutDuration()),
	}
}

func (p *CodingDeepSeekProvider) Name() string      { return "coding_deepseek" }
func (p *CodingDeepSeekProvider) ModelList() []string { return codingDeepSeekModels }

// Chat 执行非流式聊天补全请求（代码对话场景）
func (p *CodingDeepSeekProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider coding_deepseek: %w", err)
	}

	oaiReq := convertToOpenAIFormat(req, false)
	body, err := MarshalWithExtra(oaiReq, req.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider coding_deepseek: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider coding_deepseek: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider coding_deepseek: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider coding_deepseek: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var oaiResp openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&oaiResp); err != nil {
		return nil, fmt.Errorf("provider coding_deepseek: decode response: %w", err)
	}

	return convertFromOpenAIResponse(&oaiResp), nil
}

// StreamChat 执行流式聊天补全请求
func (p *CodingDeepSeekProvider) StreamChat(ctx context.Context, req *ChatRequest) (StreamReader, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider coding_deepseek: %w", err)
	}

	oaiReq := convertToOpenAIFormat(req, true)
	body, err := MarshalWithExtra(oaiReq, req.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider coding_deepseek: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider coding_deepseek: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider coding_deepseek: do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("provider coding_deepseek: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return &openAIStreamReader{
		reader: bufio.NewReader(resp.Body),
		body:   resp.Body,
	}, nil
}

// FIMCompletionRequest FIM (Fill-in-the-Middle) 代码补全请求结构体
// 用于 POST /v1/completions 端点
type FIMCompletionRequest struct {
	Model       string   `json:"model"`                 // 模型名称
	Prompt      string   `json:"prompt"`                // 代码前缀（光标之前的代码）
	Suffix      string   `json:"suffix,omitempty"`      // 代码后缀（光标之后的代码）
	MaxTokens   int      `json:"max_tokens,omitempty"`  // 最大生成 Token 数
	Temperature *float64 `json:"temperature,omitempty"` // 采样温度
	TopP        *float64 `json:"top_p,omitempty"`       // 核采样参数
	Stream      bool     `json:"stream"`                // 是否流式返回
	Stop        []string `json:"stop,omitempty"`        // 停止词列表
	Echo        bool     `json:"echo,omitempty"`        // 是否回显prompt
}

// FIMCompletionResponse FIM 代码补全响应结构体
type FIMCompletionResponse struct {
	ID      string      `json:"id"`
	Object  string      `json:"object"`
	Created int64       `json:"created"`
	Model   string      `json:"model"`
	Choices []FIMChoice `json:"choices"`
	Usage   *Usage      `json:"usage,omitempty"`
}

// FIMChoice FIM 补全选项
type FIMChoice struct {
	Index        int     `json:"index"`
	Text         string  `json:"text"`
	FinishReason *string `json:"finish_reason,omitempty"`
}

// FIMCompletion 执行 FIM 代码补全（非流式）
// 通过 POST /v1/completions 发送 prompt + suffix 实现中间填充
func (p *CodingDeepSeekProvider) FIMCompletion(ctx context.Context, req *FIMCompletionRequest) (*FIMCompletionResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("provider coding_deepseek: marshal FIM request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider coding_deepseek: create FIM request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider coding_deepseek: do FIM request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider coding_deepseek: FIM API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var fimResp FIMCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&fimResp); err != nil {
		return nil, fmt.Errorf("provider coding_deepseek: decode FIM response: %w", err)
	}

	return &fimResp, nil
}

// FIMStreamCompletion 执行 FIM 代码补全（流式）
func (p *CodingDeepSeekProvider) FIMStreamCompletion(ctx context.Context, req *FIMCompletionRequest) (StreamReader, error) {
	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("provider coding_deepseek: marshal FIM stream request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider coding_deepseek: create FIM stream request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider coding_deepseek: do FIM stream request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("provider coding_deepseek: FIM stream API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return &fimStreamReader{
		reader: bufio.NewReader(resp.Body),
		body:   resp.Body,
	}, nil
}

// fimStreamReader FIM 流式响应读取器
type fimStreamReader struct {
	reader *bufio.Reader
	body   io.ReadCloser
}

// Read 读取下一个 FIM 流式分片，转换为通用 StreamChunk 格式
func (s *fimStreamReader) Read() (*StreamChunk, error) {
	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return nil, io.EOF
			}
			return nil, fmt.Errorf("provider coding_deepseek: read FIM stream: %w", err)
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

		// FIM 流式响应格式与 chat completions 不同，需要特殊处理
		var fimChunk struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Choices []struct {
				Index        int     `json:"index"`
				Text         string  `json:"text"`
				FinishReason *string `json:"finish_reason,omitempty"`
			} `json:"choices"`
			Usage *Usage `json:"usage,omitempty"`
		}
		if err := json.Unmarshal([]byte(data), &fimChunk); err != nil {
			logger.L.Debug("provider coding_deepseek: skip malformed FIM chunk", zap.Error(err))
			continue
		}

		// 将 FIM 格式转换为通用 StreamChunk（text → delta.content）
		choices := make([]StreamChoice, len(fimChunk.Choices))
		for i, c := range fimChunk.Choices {
			choices[i] = StreamChoice{
				Index:        c.Index,
				Delta:        DeltaContent{Content: c.Text},
				FinishReason: c.FinishReason,
			}
		}

		return &StreamChunk{
			ID:      fimChunk.ID,
			Model:   fimChunk.Model,
			Choices: choices,
			Usage:   fimChunk.Usage,
		}, nil
	}
}

// Close 关闭 FIM 流
func (s *fimStreamReader) Close() error {
	if s.body != nil {
		return s.body.Close()
	}
	return nil
}

// setHeaders 设置 DeepSeek 请求头
func (p *CodingDeepSeekProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
}
