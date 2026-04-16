// 腾讯混元（Tencent Hunyuan）适配器 — OpenAI 兼容格式
//
// API 文档: https://cloud.tencent.com/document/product/1729/111007
// 认证方式: Bearer <api_key>（OpenAI 兼容端点，直接使用 API Key）
// 基础 URL : https://api.hunyuan.cloud.tencent.com/v1
//
// 支持模型:
//   - hunyuan-lite        (免费，256K 上下文)
//   - hunyuan-standard    (标准，32K)
//   - hunyuan-standard-256k (标准长文本，256K)
//   - hunyuan-pro         (旗舰)
//   - hunyuan-turbo       (高速)
//   - hunyuan-turbo-latest(高速最新版)
//   - hunyuan-large       (大参数量，256K)
//   - hunyuan-code        (代码生成)
//   - hunyuan-role        (角色扮演)
//   - hunyuan-functioncall(函数调用)
//   - hunyuan-vision      (多模态视觉)
//   - hunyuan-turbo-vision(多模态视觉高速)
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

var _ Provider = (*HunyuanProvider)(nil)

const hunyuanDefaultBaseURL = "https://api.hunyuan.cloud.tencent.com/v1"

// hunyuanModels 常驻模型列表（用于 Provider 初始注册；真实可用模型由模型发现服务同步）
var hunyuanModels = []string{
	"hunyuan-lite",
	"hunyuan-standard",
	"hunyuan-standard-256k",
	"hunyuan-pro",
	"hunyuan-turbo",
	"hunyuan-turbo-latest",
	"hunyuan-large",
	"hunyuan-code",
	"hunyuan-role",
	"hunyuan-functioncall",
	"hunyuan-vision",
	"hunyuan-turbo-vision",
}

// HunyuanProvider 实现 Provider 接口的腾讯混元适配器
// 混元 OpenAI 兼容接口使用 Bearer API Key 认证
type HunyuanProvider struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewHunyuanProvider 创建腾讯混元提供商实例
func NewHunyuanProvider(cfg ProviderConfig) *HunyuanProvider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = hunyuanDefaultBaseURL
	}
	return &HunyuanProvider{
		apiKey:  cfg.APIKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  newHTTPClient(cfg.TimeoutDuration()),
	}
}

func (p *HunyuanProvider) Name() string       { return "hunyuan" }
func (p *HunyuanProvider) ModelList() []string { return hunyuanModels }

// Chat 非流式聊天补全（OpenAI 兼容格式）
func (p *HunyuanProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider hunyuan: %w", err)
	}

	oaiReq := convertToOpenAIFormat(req, false)
	body, err := MarshalWithExtra(oaiReq, req.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider hunyuan: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider hunyuan: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider hunyuan: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider hunyuan: API returned status %d: %s",
			resp.StatusCode, string(respBody))
	}

	var oaiResp openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&oaiResp); err != nil {
		return nil, fmt.Errorf("provider hunyuan: decode response: %w", err)
	}

	return convertFromOpenAIResponse(&oaiResp), nil
}

// StreamChat 流式聊天补全（OpenAI SSE 格式）
func (p *HunyuanProvider) StreamChat(ctx context.Context, req *ChatRequest) (StreamReader, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider hunyuan: %w", err)
	}

	oaiReq := convertToOpenAIFormat(req, true)
	body, err := MarshalWithExtra(oaiReq, req.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider hunyuan: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider hunyuan: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider hunyuan: do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("provider hunyuan: API returned status %d: %s",
			resp.StatusCode, string(respBody))
	}

	return &openAIStreamReader{
		reader: bufio.NewReader(resp.Body),
		body:   resp.Body,
	}, nil
}

// setHeaders 设置请求 Header
// 混元 OpenAI 兼容端点使用 Bearer <API Key> 认证
func (p *HunyuanProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
}
