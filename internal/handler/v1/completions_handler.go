// Package v1 提供 OpenAI 兼容的 /v1/ 路由处理器
// 支持 /v1/chat/completions、/v1/completions (FIM)、/v1/models、/v1/embeddings
package v1

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/provider"
	"tokenhub-server/internal/service/apikey"
	balancesvc "tokenhub-server/internal/service/balance"
	channelsvc "tokenhub-server/internal/service/channel"
	codingsvc "tokenhub-server/internal/service/coding"
	"tokenhub-server/internal/service/pricing"
	referralsvc "tokenhub-server/internal/service/referral"
)

// CompletionsHandler OpenAI 兼容的补全接口处理器
// 处理 /v1/chat/completions 和 /v1/completions (FIM) 请求
type CompletionsHandler struct {
	db             *gorm.DB
	codingSvc      *codingsvc.CodingService
	channelRouter  *channelsvc.ChannelRouter
	pricingCalc    *pricing.PricingCalculator
	apiKeySvc      *apikey.ApiKeyService
	balanceSvc     *balancesvc.BalanceService
	commissionCalc *referralsvc.CommissionCalculator
	logger         *zap.Logger
}

// NewCompletionsHandler 创建 CompletionsHandler 实例，注入所有依赖
func NewCompletionsHandler(
	db *gorm.DB,
	codingSvc *codingsvc.CodingService,
	channelRouter *channelsvc.ChannelRouter,
	pricingCalc *pricing.PricingCalculator,
	apiKeySvc *apikey.ApiKeyService,
	balSvc *balancesvc.BalanceService,
	commCalc *referralsvc.CommissionCalculator,
) *CompletionsHandler {
	return &CompletionsHandler{
		db:             db,
		codingSvc:      codingSvc,
		channelRouter:  channelRouter,
		pricingCalc:    pricingCalc,
		apiKeySvc:      apiKeySvc,
		balanceSvc:     balSvc,
		commissionCalc: commCalc,
		logger:         logger.L,
	}
}

// Register 注册路由到 /v1/ 路由组
func (h *CompletionsHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/chat/completions", h.ChatCompletions)
	rg.POST("/completions", h.FIMCompletions)
}

// chatCompletionRequest OpenAI 兼容的聊天补全请求结构体
type chatCompletionRequest struct {
	Model         string             `json:"model" binding:"required"`
	Messages      []provider.Message `json:"messages" binding:"required"`
	MaxTokens     int                `json:"max_tokens,omitempty"`
	Temperature   *float64           `json:"temperature,omitempty"`
	TopP          *float64           `json:"top_p,omitempty"`
	Stream        bool               `json:"stream"`
	StreamOptions *streamOptions     `json:"stream_options,omitempty"`
	Stop          []string           `json:"stop,omitempty"`
}

// streamOptions 流式响应选项
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"` // 是否在最后一个 chunk 中包含 usage 信息
}

// ChatCompletions 处理 POST /v1/chat/completions（OpenAI 兼容格式）
// 将请求映射到现有的渠道路由和提供商逻辑
func (h *CompletionsHandler) ChatCompletions(c *gin.Context) {
	// 步骤1：通过 API Key 进行身份认证
	keyInfo, err := h.authenticateAPIKey(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{
				"message": "Invalid API key",
				"type":    "invalid_request_error",
				"code":    "invalid_api_key",
			},
		})
		return
	}

	// 步骤2：解析请求参数
	var req chatCompletionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"message": "Invalid request: " + err.Error(),
				"type":    "invalid_request_error",
			},
		})
		return
	}

	if len(req.Messages) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"message": "messages must not be empty",
				"type":    "invalid_request_error",
			},
		})
		return
	}

	requestID := "chatcmpl-" + uuid.New().String()
	start := time.Now()
	var ch *model.Channel
	var actualModel string

	// 步骤2.5：检查用户余额
	if h.balanceSvc != nil {
		if err := h.balanceSvc.CheckQuota(c.Request.Context(), keyInfo.UserID); err != nil {
			c.JSON(http.StatusPaymentRequired, gin.H{
				"error": gin.H{
					"message": "Insufficient balance",
					"type":    "insufficient_quota",
					"code":    "insufficient_quota",
				},
			})
			return
		}
	}

	// 步骤3：通过渠道路由选择最优渠道（基于 CustomChannel 统一路由）
	customChannelID := keyInfo.CustomChannelID
	result, err := h.channelRouter.SelectChannel(c.Request.Context(), req.Model, customChannelID, keyInfo.UserID)
	if err != nil {
		h.logger.Error("v1 chat: 渠道选择失败", zap.String("model", req.Model), zap.Error(err))
		// 尝试 Coding 渠道 fallback
		ch2, err2 := h.codingSvc.SelectCodingChannel(c.Request.Context(), req.Model)
		if err2 != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": gin.H{
					"message": "No available channel for model: " + req.Model,
					"type":    "server_error",
				},
			})
			return
		}
		ch = ch2
		actualModel = req.Model
	} else {
		ch = result.Channel
		actualModel = result.ActualModel
	}

	// 步骤4：根据渠道创建对应的提供商实例
	p := h.codingSvc.CreateProviderForChannel(ch)

	// 如果使用别名映射，更新请求中的模型名
	modelForReq := req.Model
	if actualModel != "" && actualModel != req.Model {
		h.logger.Info("v1 chat: using alias model mapping",
			zap.String("requested_model", req.Model),
			zap.String("actual_model", actualModel))
		modelForReq = actualModel
	}

	chatReq := &provider.ChatRequest{
		Model:       modelForReq,
		Messages:    req.Messages,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stream:      req.Stream,
		Stop:        req.Stop,
	}

	// 步骤5：执行请求（流式或非流式）
	if req.Stream {
		includeUsage := req.StreamOptions != nil && req.StreamOptions.IncludeUsage
		h.handleStreamChat(c, p, chatReq, ch, keyInfo, requestID, start, includeUsage)
		return
	}
	h.handleChat(c, p, chatReq, ch, keyInfo, requestID, start)
}

// handleChat 处理非流式聊天补全请求
func (h *CompletionsHandler) handleChat(
	c *gin.Context,
	p provider.Provider,
	req *provider.ChatRequest,
	ch *model.Channel,
	keyInfo *apikey.ApiKeyInfo,
	requestID string,
	start time.Time,
) {
	resp, err := p.Chat(c.Request.Context(), req)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		h.recordLog(ch.ID, req.Model, keyInfo, requestID, 0, 0, int(latency), 500, err.Error())
		h.channelRouter.RecordResult(ch.ID, false, int(latency), 500)
		c.JSON(http.StatusBadGateway, gin.H{
			"error": gin.H{
				"message": "Upstream provider error",
				"type":    "server_error",
			},
		})
		return
	}

	// 记录成功结果
	h.channelRouter.RecordResult(ch.ID, true, int(latency), 200)
	h.recordLog(ch.ID, req.Model, keyInfo, requestID,
		resp.Usage.PromptTokens, resp.Usage.CompletionTokens, int(latency), 200, "")

	// 计算费用并扣减余额（返回积分 int64）
	cost := h.calculateAndDeductCost(c, req.Model, keyInfo, resp.Usage, requestID)
	if h.commissionCalc != nil && cost > 0 {
		h.commissionCalc.CalculateCommissionsAsync(keyInfo.UserID, keyInfo.TenantID, cost)
	}

	c.JSON(http.StatusOK, resp)
}

// handleStreamChat 处理流式聊天补全请求，通过SSE推送数据
func (h *CompletionsHandler) handleStreamChat(
	c *gin.Context,
	p provider.Provider,
	req *provider.ChatRequest,
	ch *model.Channel,
	keyInfo *apikey.ApiKeyInfo,
	requestID string,
	start time.Time,
	includeUsage bool, // 是否在最后一个 chunk 中包含 usage 信息
) {
	reader, err := p.StreamChat(c.Request.Context(), req)
	if err != nil {
		latency := time.Since(start).Milliseconds()
		h.recordLog(ch.ID, req.Model, keyInfo, requestID, 0, 0, int(latency), 500, err.Error())
		h.channelRouter.RecordResult(ch.ID, false, int(latency), 500)
		c.JSON(http.StatusBadGateway, gin.H{
			"error": gin.H{
				"message": "Upstream provider error",
				"type":    "server_error",
			},
		})
		return
	}

	// 使用 SSE 写入器转发流式响应
	usage, err := provider.SSEWriter(c, reader, includeUsage)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		h.channelRouter.RecordResult(ch.ID, false, int(latency), 500)
		h.recordLog(ch.ID, req.Model, keyInfo, requestID, 0, 0, int(latency), 500, err.Error())
		return
	}

	h.channelRouter.RecordResult(ch.ID, true, int(latency), 200)
	if usage != nil {
		h.recordLog(ch.ID, req.Model, keyInfo, requestID,
			usage.PromptTokens, usage.CompletionTokens, int(latency), 200, "")
		cost := h.calculateAndDeductCost(c, req.Model, keyInfo, *usage, requestID)
		if h.commissionCalc != nil && cost > 0 {
			h.commissionCalc.CalculateCommissionsAsync(keyInfo.UserID, keyInfo.TenantID, cost)
		}
	}
}

// fimCompletionRequest FIM (Fill-in-the-Middle) 代码补全请求结构体
type fimCompletionRequest struct {
	Model       string   `json:"model" binding:"required"`
	Prompt      string   `json:"prompt" binding:"required"`
	Suffix      string   `json:"suffix,omitempty"`
	MaxTokens   int      `json:"max_tokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	Stream      bool     `json:"stream"`
	Stop        []string `json:"stop,omitempty"`
	Echo        bool     `json:"echo,omitempty"`
}

// FIMCompletions 处理 POST /v1/completions（FIM 代码补全）
// 支持 prompt + suffix 参数实现中间填充代码补全
func (h *CompletionsHandler) FIMCompletions(c *gin.Context) {
	// 认证
	keyInfo, err := h.authenticateAPIKey(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{
				"message": "Invalid API key",
				"type":    "invalid_request_error",
				"code":    "invalid_api_key",
			},
		})
		return
	}

	var req fimCompletionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"message": "Invalid request: " + err.Error(),
				"type":    "invalid_request_error",
			},
		})
		return
	}

	// 检查余额
	if h.balanceSvc != nil {
		if err := h.balanceSvc.CheckQuota(c.Request.Context(), keyInfo.UserID); err != nil {
			c.JSON(http.StatusPaymentRequired, gin.H{
				"error": gin.H{
					"message": "Insufficient balance",
					"type":    "insufficient_quota",
				},
			})
			return
		}
	}

	requestID := "cmpl-" + uuid.New().String()
	start := time.Now()

	// 选择 Coding 渠道
	ch, err := h.codingSvc.SelectCodingChannel(c.Request.Context(), req.Model)
	if err != nil {
		// 尝试通过通用渠道路由选择
		result, err2 := h.channelRouter.SelectChannel(c.Request.Context(), req.Model, keyInfo.CustomChannelID, keyInfo.UserID)
		if err2 != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": gin.H{
					"message": "No available channel for model: " + req.Model,
					"type":    "server_error",
				},
			})
			return
		}
		ch = result.Channel
	}

	// 创建提供商实例
	p := h.codingSvc.CreateProviderForChannel(ch)

	// 检查是否是 DeepSeek 提供商（支持原生 FIM）
	if deepseekP, ok := p.(*provider.CodingDeepSeekProvider); ok {
		h.handleDeepSeekFIM(c, deepseekP, &req, ch, keyInfo, requestID, start)
		return
	}

	// 其他提供商：将 FIM 请求转换为 chat/completions 格式
	// 使用 system prompt 指示模型进行代码补全
	messages := []provider.Message{
		{Role: "system", Content: "You are a code completion assistant. Complete the code between the given prefix and suffix. Only output the completion code, no explanations."},
		{Role: "user", Content: fmt.Sprintf("Complete the code:\n\nPrefix:\n```\n%s\n```\n\nSuffix:\n```\n%s\n```\n\nOutput only the code that goes between prefix and suffix:", req.Prompt, req.Suffix)},
	}

	chatReq := &provider.ChatRequest{
		Model:       req.Model,
		Messages:    messages,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stream:      req.Stream,
		Stop:        req.Stop,
	}

	if req.Stream {
		h.handleStreamChat(c, p, chatReq, ch, keyInfo, requestID, start, false) // FIM 不支持 include_usage
		return
	}

	// 非流式：执行 chat 请求并转换为 completions 响应格式
	resp, err := p.Chat(c.Request.Context(), chatReq)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		h.recordLog(ch.ID, req.Model, keyInfo, requestID, 0, 0, int(latency), 500, err.Error())
		c.JSON(http.StatusBadGateway, gin.H{
			"error": gin.H{
				"message": "Upstream provider error",
				"type":    "server_error",
			},
		})
		return
	}

	h.recordLog(ch.ID, req.Model, keyInfo, requestID,
		resp.Usage.PromptTokens, resp.Usage.CompletionTokens, int(latency), 200, "")

	// 转换为 OpenAI completions 响应格式
	completionText := ""
	finishReason := "stop"
	if len(resp.Choices) > 0 {
		completionText = resp.Choices[0].Message.Content
		finishReason = resp.Choices[0].FinishReason
	}

	c.JSON(http.StatusOK, gin.H{
		"id":      requestID,
		"object":  "text_completion",
		"created": time.Now().Unix(),
		"model":   req.Model,
		"choices": []gin.H{
			{
				"index":         0,
				"text":          completionText,
				"finish_reason": finishReason,
			},
		},
		"usage": gin.H{
			"prompt_tokens":     resp.Usage.PromptTokens,
			"completion_tokens": resp.Usage.CompletionTokens,
			"total_tokens":      resp.Usage.TotalTokens,
		},
	})
}

// handleDeepSeekFIM 使用 DeepSeek 原生 FIM 端点处理代码补全
func (h *CompletionsHandler) handleDeepSeekFIM(
	c *gin.Context,
	p *provider.CodingDeepSeekProvider,
	req *fimCompletionRequest,
	ch *model.Channel,
	keyInfo *apikey.ApiKeyInfo,
	requestID string,
	start time.Time,
) {
	fimReq := &provider.FIMCompletionRequest{
		Model:       req.Model,
		Prompt:      req.Prompt,
		Suffix:      req.Suffix,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stream:      req.Stream,
		Stop:        req.Stop,
		Echo:        req.Echo,
	}

	if req.Stream {
		// 流式 FIM
		reader, err := p.FIMStreamCompletion(c.Request.Context(), fimReq)
		if err != nil {
			latency := time.Since(start).Milliseconds()
			h.recordLog(ch.ID, req.Model, keyInfo, requestID, 0, 0, int(latency), 500, err.Error())
			c.JSON(http.StatusBadGateway, gin.H{
				"error": gin.H{
					"message": "Upstream provider error",
					"type":    "server_error",
				},
			})
			return
		}
		usage, err := provider.SSEWriter(c, reader, false)
		latency := time.Since(start).Milliseconds()
		if err != nil {
			h.recordLog(ch.ID, req.Model, keyInfo, requestID, 0, 0, int(latency), 500, err.Error())
			return
		}
		if usage != nil {
			h.recordLog(ch.ID, req.Model, keyInfo, requestID,
				usage.PromptTokens, usage.CompletionTokens, int(latency), 200, "")
		}
		return
	}

	// 非流式 FIM
	fimResp, err := p.FIMCompletion(c.Request.Context(), fimReq)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		h.recordLog(ch.ID, req.Model, keyInfo, requestID, 0, 0, int(latency), 500, err.Error())
		c.JSON(http.StatusBadGateway, gin.H{
			"error": gin.H{
				"message": "Upstream provider error",
				"type":    "server_error",
			},
		})
		return
	}

	if fimResp.Usage != nil {
		h.recordLog(ch.ID, req.Model, keyInfo, requestID,
			fimResp.Usage.PromptTokens, fimResp.Usage.CompletionTokens, int(latency), 200, "")
	}

	c.JSON(http.StatusOK, fimResp)
}

// authenticateAPIKey 从请求头提取并验证 API Key
func (h *CompletionsHandler) authenticateAPIKey(c *gin.Context) (*apikey.ApiKeyInfo, error) {
	auth := c.GetHeader("Authorization")
	if auth == "" {
		return nil, fmt.Errorf("missing authorization header")
	}
	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return nil, fmt.Errorf("invalid authorization format")
	}
	key := strings.TrimSpace(parts[1])
	if key == "" {
		return nil, fmt.Errorf("empty api key")
	}
	return h.apiKeySvc.Verify(c.Request.Context(), key)
}

// recordLog 异步保存渠道调用日志
func (h *CompletionsHandler) recordLog(
	channelID uint, modelName string, keyInfo *apikey.ApiKeyInfo,
	requestID string, promptTokens, completionTokens, latencyMs, statusCode int, errMsg string,
) {
	go func() {
		log := &model.ChannelLog{
			ChannelID:      channelID,
			ModelName:      modelName,
			TenantID:       keyInfo.TenantID,
			UserID:         keyInfo.UserID,
			RequestTokens:  promptTokens,
			ResponseTokens: completionTokens,
			LatencyMs:      latencyMs,
			StatusCode:     statusCode,
			ErrorMessage:   errMsg,
			RequestID:      requestID,
		}
		if err := h.db.Create(log).Error; err != nil {
			h.logger.Error("v1: 记录渠道日志失败", zap.Error(err))
		}
	}()
}

// calculateAndDeductCost 计算用量费用并从余额中扣减，返回积分（int64）
func (h *CompletionsHandler) calculateAndDeductCost(
	c *gin.Context,
	modelName string, keyInfo *apikey.ApiKeyInfo, usage provider.Usage, requestID string,
) int64 {
	if h.pricingCalc == nil {
		return 0
	}
	ctx := c.Request.Context()
	var aiModel model.AIModel
	if err := h.db.WithContext(ctx).Where("model_name = ? AND is_active = true", modelName).First(&aiModel).Error; err != nil {
		return 0
	}
	costResult, err := h.pricingCalc.CalculateCost(
		ctx,
		aiModel.ID, keyInfo.TenantID, 0,
		usage.PromptTokens, usage.CompletionTokens,
	)
	if err != nil || costResult == nil {
		return 0
	}
	if h.balanceSvc != nil && costResult.TotalCost > 0 {
		_ = h.balanceSvc.DeductForRequest(ctx, keyInfo.UserID, keyInfo.TenantID, costResult.TotalCost, modelName, requestID)
	}
	return costResult.TotalCost
}
