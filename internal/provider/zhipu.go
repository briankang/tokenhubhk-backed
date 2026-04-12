// 智谱GLM适配器
//
// 支持的模型:
//   - glm-4
//   - glm-4-flash
//   - glm-3-turbo
//
// API reference: https://open.bigmodel.cn/dev/api
// 特殊处理: 使用API Key进行JWT签名认证
package provider

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"tokenhub-server/internal/pkg/logger"

	"go.uber.org/zap"
)

var _ Provider = (*ZhipuProvider)(nil)

const zhipuDefaultBaseURL = "https://open.bigmodel.cn/api/paas/v4"

var zhipuModels = []string{
	"glm-4",
	"glm-4-flash",
	"glm-3-turbo",
}

// ZhipuProvider 实现Provider接口的智谱GLM适配器
type ZhipuProvider struct {
	apiKey  string // format: {id}.{secret}
	baseURL string
	client  *http.Client
}

// NewZhipuProvider 创建智谱GLM提供商实例
func NewZhipuProvider(cfg ProviderConfig) *ZhipuProvider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = zhipuDefaultBaseURL
	}
	return &ZhipuProvider{
		apiKey:  cfg.APIKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  newHTTPClient(cfg.TimeoutDuration()),
	}
}

func (p *ZhipuProvider) Name() string      { return "zhipu" }
func (p *ZhipuProvider) ModelList() []string { return zhipuModels }

func (p *ZhipuProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider zhipu: %w", err)
	}

	oaiReq := convertToOpenAIFormat(req, false)
	body, err := json.Marshal(oaiReq)
	if err != nil {
		return nil, fmt.Errorf("provider zhipu: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider zhipu: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider zhipu: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider zhipu: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var oaiResp openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&oaiResp); err != nil {
		return nil, fmt.Errorf("provider zhipu: decode response: %w", err)
	}

	return convertFromOpenAIResponse(&oaiResp), nil
}

func (p *ZhipuProvider) StreamChat(ctx context.Context, req *ChatRequest) (StreamReader, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider zhipu: %w", err)
	}

	oaiReq := convertToOpenAIFormat(req, true)
	body, err := json.Marshal(oaiReq)
	if err != nil {
		return nil, fmt.Errorf("provider zhipu: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider zhipu: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider zhipu: do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("provider zhipu: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return &openAIStreamReader{
		reader: bufio.NewReader(resp.Body),
		body:   resp.Body,
	}, nil
}

func (p *ZhipuProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	token, err := p.generateToken()
	if err != nil {
		logger.L.Error("provider zhipu: failed to generate JWT token", zap.Error(err))
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
}

// generateToken creates a JWT-like token for Zhipu API authentication.
// API Key格式为 "{id}.{secret}"
func (p *ZhipuProvider) generateToken() (string, error) {
	parts := strings.SplitN(p.apiKey, ".", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid API key format, expected {id}.{secret}")
	}

	id := parts[0]
	secret := parts[1]

	now := time.Now()
	timestamp := now.UnixMilli()
	exp := now.Add(30 * time.Minute).UnixMilli()

	// Build JWT header and payload.
	header := base64URLEncode([]byte(fmt.Sprintf(`{"alg":"HS256","sign_type":"SIGN","typ":"JWT"}`)))
	payload := base64URLEncode([]byte(fmt.Sprintf(`{"api_key":"%s","exp":%d,"timestamp":%d}`, id, exp, timestamp)))

	signingInput := header + "." + payload
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signingInput))
	signature := base64URLEncode(mac.Sum(nil))

	return signingInput + "." + signature, nil
}

// base64URLEncode encodes data using base64url encoding without padding.
func base64URLEncode(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}
