// 百度千帆（Qianfan V2）适配器 — OpenAI 兼容格式
//
// API 文档: https://cloud.baidu.com/doc/qianfan-api/s/3m9b5lqft
// 认证方式: Bearer <bce-v3/ALTAK-xxx/xxx>（V2 接口直接使用完整密钥，无需换取 access_token）
// 基础 URL : https://qianfan.baidubce.com/v2
//
// 支持模型（已通过 API 验证的实际 model_id）:
//   - ernie-4.5-8k-preview  (旗舰)
//   - ernie-4.5-turbo-32k
//   - ernie-4.5-turbo-128k
//   - ernie-x1.1            (推理, 最新旗舰)
//   - ernie-x1-32k          (推理)
//   - ernie-x1-turbo-32k    (推理)
//   - ernie-4.0-8k-latest
//   - ernie-4.0-8k
//   - ernie-4.0-turbo-8k
//   - ernie-3.5-8k
//   - ernie-3.5-128k
//   - ernie-speed-pro-128k
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

var _ Provider = (*QianfanProvider)(nil)

const qianfanDefaultBaseURL = "https://qianfan.baidubce.com/v2"

// qianfanModels 常驻模型列表（用于 Provider 初始注册；真实可用模型由模型发现服务同步）
var qianfanModels = []string{
	"ernie-4.5-8k-preview",
	"ernie-4.5-turbo-32k",
	"ernie-4.5-turbo-128k",
	"ernie-x1.1",
	"ernie-x1-32k",
	"ernie-x1-turbo-32k",
	"ernie-4.0-8k-latest",
	"ernie-4.0-8k",
	"ernie-4.0-turbo-8k",
	"ernie-3.5-8k",
	"ernie-3.5-128k",
	"ernie-speed-pro-128k",
}

// QianfanProvider 实现 Provider 接口的百度千帆 V2 适配器
// Qianfan V2 接口完全兼容 OpenAI 格式；认证使用 Bearer bce-v3/ALTAK-xxx 格式密钥
type QianfanProvider struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewQianfanProvider 创建百度千帆 V2 提供商实例
func NewQianfanProvider(cfg ProviderConfig) *QianfanProvider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = qianfanDefaultBaseURL
	}
	return &QianfanProvider{
		apiKey:  cfg.APIKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  newHTTPClient(cfg.TimeoutDuration()),
	}
}

func (p *QianfanProvider) Name() string       { return "qianfan" }
func (p *QianfanProvider) ModelList() []string { return qianfanModels }

// Chat 非流式聊天补全（OpenAI 兼容格式）
func (p *QianfanProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider qianfan: %w", err)
	}

	oaiReq := convertToOpenAIFormat(req, false)
	body, err := MarshalWithExtra(oaiReq, req.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider qianfan: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider qianfan: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider qianfan: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider qianfan: API returned status %d: %s",
			resp.StatusCode, string(respBody))
	}

	var oaiResp openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&oaiResp); err != nil {
		return nil, fmt.Errorf("provider qianfan: decode response: %w", err)
	}

	return convertFromOpenAIResponse(&oaiResp), nil
}

// StreamChat 流式聊天补全（OpenAI SSE 格式）
func (p *QianfanProvider) StreamChat(ctx context.Context, req *ChatRequest) (StreamReader, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider qianfan: %w", err)
	}

	oaiReq := convertToOpenAIFormat(req, true)
	body, err := MarshalWithExtra(oaiReq, req.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider qianfan: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider qianfan: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider qianfan: do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("provider qianfan: API returned status %d: %s",
			resp.StatusCode, string(respBody))
	}

	return &openAIStreamReader{
		reader: bufio.NewReader(resp.Body),
		body:   resp.Body,
	}, nil
}

// setHeaders 设置请求 Header
// 千帆 V2 使用 Bearer <完整密钥> 认证（密钥已是 bce-v3/ALTAK-xxx 格式，无需额外处理）
func (p *QianfanProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
}
