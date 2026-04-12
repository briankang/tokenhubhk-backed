// Package chat 提供AI对话相关的API接口处理器
package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/provider"
	"tokenhub-server/internal/service/apikey"
	balancesvc "tokenhub-server/internal/service/balance"
	channelsvc "tokenhub-server/internal/service/channel"
	orchsvc "tokenhub-server/internal/service/orchestration"
	"tokenhub-server/internal/service/pricing"
	referralsvc "tokenhub-server/internal/service/referral"

	"gorm.io/gorm"
)

// ChatHandler AI对话接口处理器，负责聊天补全、编排执行、向量嵌入和模型列表等功能
type ChatHandler struct {
	db             *gorm.DB
	registry       *provider.Registry
	router         *channelsvc.ChannelRouter
	engine         *orchsvc.OrchestrationEngine
	orchSvc        *orchsvc.OrchestrationService
	pricingCalc    *pricing.PricingCalculator
	apiKeySvc      *apikey.ApiKeyService
	balanceSvc     *balancesvc.BalanceService
	quotaLimiter   *balancesvc.QuotaLimiter
	commissionCalc *referralsvc.CommissionCalculator
	logger         *zap.Logger
}

// NewChatHandler 创建ChatHandler实例，注入所有依赖服务
func NewChatHandler(
	db *gorm.DB,
	registry *provider.Registry,
	router *channelsvc.ChannelRouter,
	engine *orchsvc.OrchestrationEngine,
	orchSvc *orchsvc.OrchestrationService,
	pricingCalc *pricing.PricingCalculator,
	apiKeySvc *apikey.ApiKeyService,
	balSvc *balancesvc.BalanceService,
	quotaLimiter *balancesvc.QuotaLimiter,
	commCalc *referralsvc.CommissionCalculator,
) *ChatHandler {
	return &ChatHandler{
		db:             db,
		registry:       registry,
		router:         router,
		engine:         engine,
		orchSvc:        orchSvc,
		pricingCalc:    pricingCalc,
		apiKeySvc:      apiKeySvc,
		balanceSvc:     balSvc,
		quotaLimiter:   quotaLimiter,
		commissionCalc: commCalc,
		logger:         logger.L,
	}
}

// Register 注册聊天相关路由到路由组
func (h *ChatHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/completions", h.Completions)
	rg.POST("/orchestrated", h.Orchestrated)
	rg.POST("/embeddings", h.Embeddings)
	rg.GET("/models", h.ListModels)
}

// completionReq OpenAI兼容的聊天补全请求结构体
type completionReq struct {
	Model       string             `json:"model" binding:"required"`
	Messages    []provider.Message `json:"messages" binding:"required"`
	MaxTokens   int                `json:"max_tokens,omitempty"`
	Temperature *float64           `json:"temperature,omitempty"`
	TopP        *float64           `json:"top_p,omitempty"`
	Stream      bool               `json:"stream"`
	Stop        []string           `json:"stop,omitempty"`
}

// orchestratedReq 编排聊天补全请求结构体
type orchestratedReq struct {
	OrchestrationID   uint               `json:"orchestration_id"`
	OrchestrationCode string             `json:"orchestration_code"`
	Messages          []provider.Message `json:"messages" binding:"required"`
	Stream            bool               `json:"stream"`
}

// Completions 处理POST /api/v1/chat/completions（OpenAI兼容格式）
func (h *ChatHandler) Completions(c *gin.Context) {
	// 步骤1：通过API Key进行身份认证
	keyInfo, err := h.authenticateAPIKey(c)
	if err != nil {
		response.Error(c, http.StatusUnauthorized, errcode.ErrApiKeyInvalid)
		return
	}

	// 步骤2：解析请求参数
	var req completionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	if len(req.Messages) == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	requestID := uuid.New().String()
	start := time.Now()

	// 步骤2.5：检查用户余额（配额校验）
	if h.balanceSvc != nil {
		if err := h.balanceSvc.CheckQuota(c.Request.Context(), keyInfo.UserID); err != nil {
			c.JSON(http.StatusPaymentRequired, gin.H{
				"code":    40004,
				"message": "insufficient balance",
			})
			return
		}
	}

	// 步骤2.6：额度限制检查（日限额/月限额/单次Token上限/并发限制）
	estimatedCost := h.estimateCost(c.Request.Context(), req.Model, req.MaxTokens)
	if h.quotaLimiter != nil {
		if err := h.quotaLimiter.CheckQuota(c.Request.Context(), keyInfo.UserID, estimatedCost, req.MaxTokens); err != nil {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"code":    20008,
				"message": err.Error(),
			})
			return
		}
	}

	// 步骤2.7：预扣费 — 冻结预估费用
	var freezeID string
	if h.balanceSvc != nil && estimatedCost > 0 {
		freezeID, err = h.balanceSvc.FreezeBalance(c.Request.Context(), keyInfo.UserID, keyInfo.TenantID, estimatedCost, req.Model, requestID)
		if err != nil {
			c.JSON(http.StatusPaymentRequired, gin.H{
				"code":    40004,
				"message": "insufficient balance for freeze",
			})
			return
		}
	}

	// 增加并发计数
	if h.quotaLimiter != nil {
		h.quotaLimiter.IncrConcurrency(c.Request.Context(), keyInfo.UserID)
		defer h.quotaLimiter.DecrConcurrency(c.Request.Context(), keyInfo.UserID)
	}

	// 步骤3：通过路由器选择最优渠道（基于 CustomChannel 统一路由）
	// 从 API Key 获取关联的自定义渠道ID，nil 表示使用默认渠道
	customChannelID := keyInfo.CustomChannelID
	result, err := h.router.SelectChannel(c.Request.Context(), req.Model, customChannelID, keyInfo.UserID)
	if err != nil {
		// 选择渠道失败，释放冻结
		if freezeID != "" {
			_ = h.balanceSvc.ReleaseFrozen(c.Request.Context(), freezeID)
		}
		h.logger.Error("channel selection failed",
			zap.String("model", req.Model), zap.Error(err))
		response.Error(c, http.StatusServiceUnavailable, errcode.ErrNoAvailableChannel)
		return
	}
	ch := result.Channel
	actualModel := result.ActualModel // 实际调用的模型名（可能是别名映射后的）

	// 步骤4：获取对应的AI提供商实例
	p, err := h.registry.GetByModel(actualModel)
	if err != nil {
		// 获取提供商失败，释放冻结
		if freezeID != "" {
			_ = h.balanceSvc.ReleaseFrozen(c.Request.Context(), freezeID)
		}
		response.Error(c, http.StatusBadRequest, errcode.ErrModelNotFound)
		return
	}

	// 如果使用别名映射，更新请求中的模型名
	if actualModel != req.Model {
		h.logger.Info("using alias model mapping",
			zap.String("requested_model", req.Model),
			zap.String("actual_model", actualModel))
		req.Model = actualModel
	}

	chatReq := &provider.ChatRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stream:      req.Stream,
		Stop:        req.Stop,
	}

	// 步骤5：执行请求（流式或非流式），传入 freezeID 用于结算
	if req.Stream {
		h.handleStreamCompletion(c, p, chatReq, ch, keyInfo, requestID, start, freezeID)
		return
	}

	h.handleCompletion(c, p, chatReq, ch, keyInfo, requestID, start, freezeID)
}

// handleCompletion 处理非流式聊天补全请求
func (h *ChatHandler) handleCompletion(
	c *gin.Context,
	p provider.Provider,
	req *provider.ChatRequest,
	ch *model.Channel,
	keyInfo *apikey.ApiKeyInfo,
	requestID string,
	start time.Time,
	freezeID string,
) {
	resp, err := p.Chat(c.Request.Context(), req)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		h.recordLog(ch.ID, req.Model, keyInfo, requestID, 0, 0, int(latency), 500, err.Error())
		h.router.RecordResult(ch.ID, false, int(latency), 500)
		// 请求失败，释放冻结额度
		if freezeID != "" {
			_ = h.balanceSvc.ReleaseFrozen(c.Request.Context(), freezeID)
		}
		response.Error(c, http.StatusBadGateway, errcode.ErrThirdParty)
		return
	}

	// 记录成功结果
	h.router.RecordResult(ch.ID, true, int(latency), 200)
	h.recordLog(ch.ID, req.Model, keyInfo, requestID,
		resp.Usage.PromptTokens, resp.Usage.CompletionTokens, int(latency), 200, "")

	// 精确结算：根据实际消耗 Token 计算费用并结算
	cost := h.calculateActualCost(c.Request.Context(), req.Model, keyInfo, resp.Usage)
	if freezeID != "" && h.balanceSvc != nil {
		_ = h.balanceSvc.SettleBalance(c.Request.Context(), freezeID, cost)
	} else if cost > 0 && h.balanceSvc != nil {
		// 无冻结时回退到直接扣减
		_ = h.balanceSvc.DeductForRequest(c.Request.Context(), keyInfo.UserID, keyInfo.TenantID, cost, req.Model, requestID)
	}

	// 异步计算推荐佣金
	if h.commissionCalc != nil && cost > 0 {
		h.commissionCalc.CalculateCommissionsAsync(keyInfo.UserID, keyInfo.TenantID, cost)
	}

	c.JSON(http.StatusOK, resp)
}

// handleStreamCompletion 处理流式聊天补全请求，通过SSE推送数据
func (h *ChatHandler) handleStreamCompletion(
	c *gin.Context,
	p provider.Provider,
	req *provider.ChatRequest,
	ch *model.Channel,
	keyInfo *apikey.ApiKeyInfo,
	requestID string,
	start time.Time,
	freezeID string,
) {
	reader, err := p.StreamChat(c.Request.Context(), req)
	if err != nil {
		latency := time.Since(start).Milliseconds()
		h.recordLog(ch.ID, req.Model, keyInfo, requestID, 0, 0, int(latency), 500, err.Error())
		h.router.RecordResult(ch.ID, false, int(latency), 500)
		// 流式请求开始失败，释放冻结
		if freezeID != "" {
			_ = h.balanceSvc.ReleaseFrozen(c.Request.Context(), freezeID)
		}
		response.Error(c, http.StatusBadGateway, errcode.ErrThirdParty)
		return
	}

	// SSE 流式转发，不包含 usage 信息（旧的 chat/completions 接口）
	usage, err := provider.SSEWriter(c, reader, false)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		h.router.RecordResult(ch.ID, false, int(latency), 500)
		h.recordLog(ch.ID, req.Model, keyInfo, requestID, 0, 0, int(latency), 500, err.Error())
		// 流式传输失败，释放冻结
		if freezeID != "" {
			_ = h.balanceSvc.ReleaseFrozen(c.Request.Context(), freezeID)
		}
		return
	}

	h.router.RecordResult(ch.ID, true, int(latency), 200)
	if usage != nil {
		h.recordLog(ch.ID, req.Model, keyInfo, requestID,
			usage.PromptTokens, usage.CompletionTokens, int(latency), 200, "")
		// 精确结算
		cost := h.calculateActualCost(c.Request.Context(), req.Model, keyInfo, *usage)
		if freezeID != "" && h.balanceSvc != nil {
			_ = h.balanceSvc.SettleBalance(c.Request.Context(), freezeID, cost)
		} else if cost > 0 && h.balanceSvc != nil {
			_ = h.balanceSvc.DeductForRequest(c.Request.Context(), keyInfo.UserID, keyInfo.TenantID, cost, req.Model, requestID)
		}
		// 异步计算推荐佣金
		if h.commissionCalc != nil && cost > 0 {
			h.commissionCalc.CalculateCommissionsAsync(keyInfo.UserID, keyInfo.TenantID, cost)
		}
	} else if freezeID != "" {
		// 无usage信息时释放冻结
		_ = h.balanceSvc.ReleaseFrozen(c.Request.Context(), freezeID)
	}
}

// Orchestrated 处理POST /api/v1/chat/orchestrated 编排式聊天补全
func (h *ChatHandler) Orchestrated(c *gin.Context) {
	// 通过API Key进行身份认证
	keyInfo, err := h.authenticateAPIKey(c)
	if err != nil {
		response.Error(c, http.StatusUnauthorized, errcode.ErrApiKeyInvalid)
		return
	}

	var req orchestratedReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	if len(req.Messages) == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	// 解析编排ID（支持通过ID或Code查询）
	orchID := req.OrchestrationID
	if orchID == 0 && req.OrchestrationCode != "" {
		orch, err := h.orchSvc.GetByCode(c.Request.Context(), req.OrchestrationCode)
		if err != nil {
			response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
			return
		}
		orchID = orch.ID
	}
	if orchID == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code,
			"orchestration_id or orchestration_code is required")
		return
	}

	if req.Stream {
		h.handleStreamOrchestrated(c, orchID, req.Messages, keyInfo)
		return
	}

	h.handleOrchestrated(c, orchID, req.Messages, keyInfo)
}

// handleOrchestrated 处理非流式编排补全请求
func (h *ChatHandler) handleOrchestrated(
	c *gin.Context,
	orchID uint,
	messages []provider.Message,
	keyInfo *apikey.ApiKeyInfo,
) {
	result, err := h.engine.Execute(c.Request.Context(), orchID, messages)
	if err != nil {
		h.logger.Error("orchestration execution failed",
			zap.Uint("orchestration_id", orchID), zap.Error(err))
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	_ = keyInfo // cost calculation can be extended here

	response.Success(c, result)
}

// handleStreamOrchestrated 处理流式编排补全请求，通过SSE推送事件
func (h *ChatHandler) handleStreamOrchestrated(
	c *gin.Context,
	orchID uint,
	messages []provider.Message,
	keyInfo *apikey.ApiKeyInfo,
) {
	events, err := h.engine.Stream(c.Request.Context(), orchID, messages)
	if err != nil {
		h.logger.Error("orchestration stream failed",
			zap.Uint("orchestration_id", orchID), zap.Error(err))
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 设置SSE响应头
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	w := c.Writer
	flusher, ok := w.(interface{ Flush() })
	if !ok {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code,
			"streaming not supported")
		return
	}

	var totalUsage provider.Usage
	ctx := c.Request.Context()

	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-events:
			if !ok {
				// 通道关闭，发送结束标记
				fmt.Fprintf(w, "data: [DONE]\n\n")
				flusher.Flush()
				_ = keyInfo
				return
			}

			if evt.Error != nil {
				errData, _ := json.Marshal(map[string]string{
					"error":  evt.Error.Error(),
					"node":   evt.NodeName,
					"status": "error",
				})
				fmt.Fprintf(w, "data: %s\n\n", errData)
				flusher.Flush()
				return
			}

			if evt.Usage != nil {
				totalUsage = orchsvc.AddUsage(totalUsage, *evt.Usage)
			}

			data, _ := json.Marshal(map[string]interface{}{
				"node":    evt.NodeName,
				"content": evt.Content,
				"status":  evt.Status,
			})
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// authenticateAPIKey 从请求头提取并验证API Key
func (h *ChatHandler) authenticateAPIKey(c *gin.Context) (*apikey.ApiKeyInfo, error) {
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
func (h *ChatHandler) recordLog(
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
			h.logger.Error("failed to record channel log", zap.Error(err))
		}
	}()
}

// Embeddings 处理POST /api/v1/chat/embeddings（OpenAI兼容向量嵌入）
func (h *ChatHandler) Embeddings(c *gin.Context) {
	// 通过API Key进行身份认证
	_, err := h.authenticateAPIKey(c)
	if err != nil {
		response.Error(c, http.StatusUnauthorized, errcode.ErrApiKeyInvalid)
		return
	}

	var req struct {
		Model string      `json:"model" binding:"required"`
		Input interface{} `json:"input" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	// 返回OpenAI格式的占位向量嵌入响应
	response.Success(c, gin.H{
		"object": "list",
		"model":  req.Model,
		"data":   []gin.H{},
		"usage": gin.H{
			"prompt_tokens": 0,
			"total_tokens":  0,
		},
		"message": "embeddings endpoint requires provider support; configure a channel with embedding capability",
	})
}

// ListModels 处理GET /api/v1/chat/models（OpenAI兼容模型列表）
func (h *ChatHandler) ListModels(c *gin.Context) {
	// 从数据库查询所有已激活的AI模型
	var models []model.AIModel
	if err := h.db.Where("is_active = ?", true).Order("model_name ASC").Find(&models).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 格式化为OpenAI兼容的模型列表
	data := make([]gin.H, 0, len(models))
	for _, m := range models {
		data = append(data, gin.H{
			"id":       m.ModelName,
			"object":   "model",
			"created":  m.CreatedAt.Unix(),
			"owned_by": "tokenhub",
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   data,
	})
}

// calculateActualCost 计算实际用量费用（不执行扣减，仅返回费用金额，单位：积分 credits）
func (h *ChatHandler) calculateActualCost(
	ctx context.Context,
	modelName string, keyInfo *apikey.ApiKeyInfo, usage provider.Usage,
) int64 {
	if h.pricingCalc == nil {
		return 0
	}
	// 根据模型名称查找模型记录
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
	return costResult.TotalCost
}

// estimateCost 预估请求费用（用于预扣费），根据模型单价和max_tokens估算，返回积分（int64）
func (h *ChatHandler) estimateCost(ctx context.Context, modelName string, maxTokens int) int64 {
	if h.pricingCalc == nil {
		return 0
	}
	if maxTokens <= 0 {
		maxTokens = 4096 // 默认预估4K tokens
	}
	// 查找模型记录
	var aiModel model.AIModel
	if err := h.db.WithContext(ctx).Where("model_name = ? AND is_active = true", modelName).First(&aiModel).Error; err != nil {
		return 0
	}
	// 预估：假设 prompt_tokens ≈ 1000（合理预估），completion_tokens = max_tokens
	costResult, err := h.pricingCalc.CalculateCost(ctx, aiModel.ID, 0, 0, 1000, maxTokens)
	if err != nil || costResult == nil {
		return 0
	}
	return costResult.TotalCost
}

// calculateAndDeductCost 计算用量费用并从余额中扣减（兼容旧调用，尽力而为模式），返回积分（int64）
func (h *ChatHandler) calculateAndDeductCost(
	ctx context.Context,
	modelName string, keyInfo *apikey.ApiKeyInfo, usage provider.Usage, requestID string,
) int64 {
	cost := h.calculateActualCost(ctx, modelName, keyInfo, usage)
	// 从用户余额中扣减费用
	if h.balanceSvc != nil && cost > 0 {
		_ = h.balanceSvc.DeductForRequest(ctx, keyInfo.UserID, keyInfo.TenantID, cost, modelName, requestID)
	}
	return cost
}
