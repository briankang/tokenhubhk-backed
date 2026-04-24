// Package provider 定义统一的LLM提供商抽象层
// 提供多平台LLM交互的通用接口和提供商实例注册中心
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"tokenhub-server/internal/pkg/logger"

	"go.uber.org/zap"
)

// ChatRequest 统一请求格式（OpenAI兼容）
type ChatRequest struct {
	Model       string                 `json:"model"`
	Messages    []Message              `json:"messages"`
	MaxTokens   int                    `json:"max_tokens,omitempty"`
	Temperature *float64               `json:"temperature,omitempty"`
	TopP        *float64               `json:"top_p,omitempty"`
	Stream      bool                   `json:"stream"`
	Stop        []string               `json:"stop,omitempty"`
	Extra       map[string]interface{} `json:"extra,omitempty"`
	// InjectCacheControl 指示 Provider 层自动注入缓存断点（handler 内部标志，不序列化到上游请求）
	// 仅对支持显式缓存的供应商（Anthropic）生效；true 时自动将 system 或首条 user 消息转换为 content-block 格式
	InjectCacheControl bool `json:"-"`
}

// Validate 对聊天请求进行基本参数校验
func (r *ChatRequest) Validate() error {
	if r.Model == "" {
		return fmt.Errorf("model is required")
	}
	if len(r.Messages) == 0 {
		return fmt.Errorf("messages cannot be empty")
	}
	for i, msg := range r.Messages {
		if msg.Role == "" {
			return fmt.Errorf("message[%d]: role is required", i)
		}
	}
	if r.Temperature != nil && (*r.Temperature < 0 || *r.Temperature > 2) {
		return fmt.Errorf("temperature must be between 0 and 2")
	}
	if r.TopP != nil && (*r.TopP < 0 || *r.TopP > 1) {
		return fmt.Errorf("top_p must be between 0 and 1")
	}
	return nil
}

// Message 单条聊天消息结构体
//
// Content 字段类型为 interface{} 以兼容 OpenAI 多模态规范：
//   - string：普通文本消息（绝大多数场景）
//   - []interface{} / []map[string]interface{}：多模态数组，如
//     [{"type":"text","text":"..."}, {"type":"image_url","image_url":{"url":"..."}}]
//
// 下游供应商适配器应通过 TextContent(m.Content) 取扁平文本（用于日志/缓存判断/Token 估算），
// 通过 m.Content 原样序列化给上游（支持多模态的供应商如豆包/Qwen-VL/GPT-4V 会自动识别数组格式）。
type Message struct {
	Role             string      `json:"role"`              // system/user/assistant
	Content          interface{} `json:"content"`
	ReasoningContent string      `json:"reasoning_content,omitempty"` // 深度思考内容（豆包/Qwen3/DeepSeek-R1等）
}

// TextContent 从 Message.Content 中提取扁平文本。
//   - string → 原样返回
//   - []interface{} → 遍历元素，拼接所有 {"type":"text","text":"..."} 的 text 字段
//   - 其它类型 → 返回空串
//
// 用于日志、cache_control 判断、Token 估算等需要纯文本的场景。不影响上游请求的多模态透传。
func TextContent(c interface{}) string {
	if c == nil {
		return ""
	}
	switch v := c.(type) {
	case string:
		return v
	case []interface{}:
		var sb strings.Builder
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				if t, _ := m["type"].(string); t == "text" {
					if txt, ok := m["text"].(string); ok {
						if sb.Len() > 0 {
							sb.WriteString("\n")
						}
						sb.WriteString(txt)
					}
				}
			}
		}
		return sb.String()
	default:
		return ""
	}
}

// ChatResponse 统一响应格式
type ChatResponse struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Choice 单个补全选项
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Usage Token用量统计
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	// 缓存相关字段（不支持缓存的供应商保持零值）
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`  // 缓存命中Token数（OpenAI cached_tokens / Anthropic cache_read_input_tokens）
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"` // 缓存写入Token数（Anthropic cache_creation_input_tokens）
	// 思考模式 Token（阿里云 qwen3.x-plus / deepseek-r1 / qwq 系列等；未使用思考时 0）
	// 从上游返回的 usage.completion_tokens_details.reasoning_tokens / response.reasoning_content 解析
	ReasoningTokens  int `json:"reasoning_tokens,omitempty"`
}

// StreamChunk 单个流式响应分片
type StreamChunk struct {
	ID      string         `json:"id"`
	Model   string         `json:"model"`
	Choices []StreamChoice `json:"choices"`
	Usage   *Usage         `json:"usage,omitempty"`
}

// StreamChoice 单个流式选项
type StreamChoice struct {
	Index        int          `json:"index"`
	Delta        DeltaContent `json:"delta"`
	FinishReason *string      `json:"finish_reason,omitempty"`
}

// DeltaContent 流式响应中的增量内容
type DeltaContent struct {
	Role             string `json:"role,omitempty"`
	Content          string `json:"content,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"` // 深度思考内容（Qwen3/DeepSeek-R1等）
}

// Provider 统一的LLM提供商适配器接口，所有提供商必须实现此接口
type Provider interface {
	// Name 返回提供商标识符
	Name() string
	// Chat 执行非流式聊天补全请求
	Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
	// StreamChat 执行流式聊天补全请求
	StreamChat(ctx context.Context, req *ChatRequest) (StreamReader, error)
	// ModelList 返回支持的模型列表
	ModelList() []string
}

// StreamReader 流式响应读取器接口
type StreamReader interface {
	// Read 读取下一个分片，流结束时返回io.EOF
	Read() (*StreamChunk, error)
	// Close 关闭流并释放资源
	Close() error
}

// ProviderConfig 单个提供商的配置结构体
type ProviderConfig struct {
	APIKey     string `mapstructure:"api_key"`
	BaseURL    string `mapstructure:"base_url"`
	OrgID      string `mapstructure:"org_id"`
	APIVersion string `mapstructure:"api_version"`
	Timeout    int    `mapstructure:"timeout"` // seconds, default 120
}

// TimeoutDuration 将配置的超时时间转换为time.Duration
func (c *ProviderConfig) TimeoutDuration() time.Duration {
	if c.Timeout <= 0 {
		return 120 * time.Second
	}
	return time.Duration(c.Timeout) * time.Second
}

// Registry 提供商注册中心，管理所有提供商实例和模型映射
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
	modelMap  map[string]string // model name -> provider name
}

// NewRegistry 创建新的空提供商注册中心
func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[string]Provider),
		modelMap:  make(map[string]string),
	}
}

// Register 添加提供商到注册中心并索引其支持的模型
func (r *Registry) Register(name string, p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.providers[name] = p
	for _, model := range p.ModelList() {
		r.modelMap[model] = name
	}
	logger.L.Info("provider registered", zap.String("provider", name), zap.Strings("models", p.ModelList()))
}

// Get 根据名称获取提供商实例
func (r *Registry) Get(name string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	p, ok := r.providers[name]
	return p, ok
}

// GetByModel 根据模型名称获取对应的提供商
func (r *Registry) GetByModel(model string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	name, ok := r.modelMap[model]
	if !ok {
		return nil, fmt.Errorf("no provider found for model %q", model)
	}
	p, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("provider %q registered for model %q but not found", name, model)
	}
	return p, nil
}

// ListProviders 返回所有已注册的提供商名称
func (r *Registry) ListProviders() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	return names
}

// ListModels 返回所有已注册的模型名称
func (r *Registry) ListModels() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	models := make([]string, 0, len(r.modelMap))
	for model := range r.modelMap {
		models = append(models, model)
	}
	return models
}

// InitAllProviders 根据配置初始化所有提供商并注册到中心
func InitAllProviders(cfg map[string]ProviderConfig) *Registry {
	registry := NewRegistry()

	factories := map[string]func(ProviderConfig) Provider{
		"openai":    func(c ProviderConfig) Provider { return NewOpenAIProvider(c) },
		"anthropic": func(c ProviderConfig) Provider { return NewAnthropicProvider(c) },
		"google":    func(c ProviderConfig) Provider { return NewGoogleProvider(c) },
		"azure":     func(c ProviderConfig) Provider { return NewAzureProvider(c) },
		"deepseek":  func(c ProviderConfig) Provider { return NewDeepSeekProvider(c) },
		"qwen":      func(c ProviderConfig) Provider { return NewQwenProvider(c) },
		"wenxin":    func(c ProviderConfig) Provider { return NewWenxinProvider(c) },
		"qianfan":   func(c ProviderConfig) Provider { return NewQianfanProvider(c) },
		"doubao":    func(c ProviderConfig) Provider { return NewDoubaoProvider(c) },
		"kimi":      func(c ProviderConfig) Provider { return NewKimiProvider(c) },
		"zhipu":     func(c ProviderConfig) Provider { return NewZhipuProvider(c) },
		"hunyuan":   func(c ProviderConfig) Provider { return NewHunyuanProvider(c) },
	}

	for name, providerCfg := range cfg {
		if providerCfg.APIKey == "" {
			logger.L.Warn("skipping provider with empty API key", zap.String("provider", name))
			continue
		}
		factory, ok := factories[name]
		if !ok {
			logger.L.Warn("unknown provider, skipping", zap.String("provider", name))
			continue
		}
		p := factory(providerCfg)
		registry.Register(name, p)
	}

	logger.L.Info("all providers initialized",
		zap.Int("provider_count", len(registry.providers)),
		zap.Int("model_count", len(registry.modelMap)),
	)
	return registry
}

// newHTTPClient 创建用于上游LLM调用的HTTP客户端
//
// 关键设计：将 timeout 作为 ResponseHeaderTimeout 而非 Client.Timeout
//   - Client.Timeout 包含整个 body 读取期，对长流式响应（思考型模型可达数分钟）会被强制掐断，
//     表现为前端"network error"或不完整流。
//   - ResponseHeaderTimeout 仅约束"建立连接 + 收到响应头"的耗时，body 读取由调用方的
//     context（c.Request.Context()）控制，符合流式语义。
//
// 同时显式设置：
//   - DialContext 30s 连接超时 + 30s keep-alive
//   - TLSHandshakeTimeout 10s
//   - IdleConnTimeout 90s（连接池复用）
//   - ExpectContinueTimeout 1s
func newHTTPClient(timeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: timeout, // 仅约束响应头返回时间
	}
	return &http.Client{
		Transport: transport,
		// 不再设置全局 Timeout，避免长流式响应被掐断；取消由请求 context 控制
	}
}

// MarshalWithExtra 将请求体序列化为 JSON，并合并 Extra 参数到顶层
// 用于将自定义参数（如 enable_thinking）透传给上游供应商
func MarshalWithExtra(reqBody interface{}, extra map[string]interface{}) ([]byte, error) {
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	if len(extra) == 0 {
		return body, nil
	}
	// 解析原始 JSON 为 map
	var merged map[string]interface{}
	if err := json.Unmarshal(body, &merged); err != nil {
		return nil, err
	}
	// 合并 Extra 参数（Extra 优先，覆盖同名字段）
	for k, v := range extra {
		merged[k] = v
	}
	return json.Marshal(merged)
}
