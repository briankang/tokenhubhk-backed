// Package provider 定义统一的LLM提供商抽象层
// 提供多平台LLM交互的通用接口和提供商实例注册中心
package provider

import (
	"context"
	"fmt"
	"net/http"
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
type Message struct {
	Role    string `json:"role"`    // system/user/assistant
	Content string `json:"content"`
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
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
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
		"doubao":    func(c ProviderConfig) Provider { return NewDoubaoProvider(c) },
		"kimi":      func(c ProviderConfig) Provider { return NewKimiProvider(c) },
		"zhipu":     func(c ProviderConfig) Provider { return NewZhipuProvider(c) },
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

// newHTTPClient 创建带指定超时时间的标准HTTP客户端
func newHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
	}
}
