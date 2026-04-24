// Package v1 提供 OpenAI 兼容的 /v1/ 路由处理器
// 支持 /v1/chat/completions、/v1/completions (FIM)、/v1/models、/v1/embeddings
package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/middleware"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
	pkgredis "tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/provider"
	"tokenhub-server/internal/service/apikey"
	balancesvc "tokenhub-server/internal/service/balance"
	billingsvc "tokenhub-server/internal/service/billing"
	channelsvc "tokenhub-server/internal/service/channel"
	codingsvc "tokenhub-server/internal/service/coding"
	"tokenhub-server/internal/service/parammapping"
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
	billingSvc     *billingsvc.Service
	commissionCalc *referralsvc.CommissionCalculator
	paramSvc       *parammapping.ParamMappingService
	tpmLimiter     *middleware.TPMLimiter
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
	paramSvc *parammapping.ParamMappingService,
	tpmLimiter *middleware.TPMLimiter,
) *CompletionsHandler {
	return &CompletionsHandler{
		db:             db,
		codingSvc:      codingSvc,
		channelRouter:  channelRouter,
		pricingCalc:    pricingCalc,
		apiKeySvc:      apiKeySvc,
		balanceSvc:     balSvc,
		billingSvc:     billingsvc.NewService(db, pricingCalc, balSvc),
		commissionCalc: commCalc,
		paramSvc:       paramSvc,
		tpmLimiter:     tpmLimiter,
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
	// 先读取原始请求体，以便提取标准字段之外的扩展参数（如 enable_thinking）
	rawBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"message": "Failed to read request body",
				"type":    "invalid_request_error",
			},
		})
		return
	}
	var req chatCompletionRequest
	if err := json.Unmarshal(rawBody, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"message": "Invalid request: " + err.Error(),
				"type":    "invalid_request_error",
			},
		})
		return
	}

	// 提取用户请求中的扩展参数（非标准 OpenAI 字段）
	var rawMap map[string]json.RawMessage
	json.Unmarshal(rawBody, &rawMap)
	userExtraParams := extractChatExtraParams(rawMap)

	if len(req.Messages) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"message": "messages must not be empty",
				"type":    "invalid_request_error",
			},
		})
		return
	}

	// 步骤 2.15：思考模式检测（阿里云 qwen3.x-plus / deepseek-r1 / qwq 等）
	// 用于后续扣费环节按思考模式计价（若模型有 output_cost_thinking_rmb > 0）以及 ApiCallLog 审计
	thinkingMode := false
	if v, ok := userExtraParams["enable_thinking"]; ok {
		if b, isBool := v.(bool); isBool && b {
			thinkingMode = true
		}
	}
	// 部分 qwen3 / doubao 等模型用 thinking / reasoning / extra_body 等字段
	if !thinkingMode {
		for _, k := range []string{"thinking", "reasoning", "reasoning_effort"} {
			if v, ok := userExtraParams[k]; ok {
				switch val := v.(type) {
				case bool:
					if val {
						thinkingMode = true
					}
				case map[string]interface{}:
					// {"thinking": {"enabled": true}} 或 {"thinking": {"type": "enabled"}}
					if en, ok := val["enabled"].(bool); ok && en {
						thinkingMode = true
					}
					if t, ok := val["type"].(string); ok && (t == "enabled" || t == "auto") {
						thinkingMode = true
					}
				case string:
					if val != "" && val != "none" && val != "disabled" && val != "off" {
						thinkingMode = true
					}
				}
				if thinkingMode {
					break
				}
			}
		}
	}
	if thinkingMode {
		c.Set("thinking_mode", true)
	}

	// 步骤 2.2：模型元信息前置守卫
	// 拦截 offline/非激活 模型和非 chat 类型（如 ImageGeneration/VideoGeneration/TTS 等）
	// 的请求，避免无效请求到达上游并污染渠道健康指标。
	// 若模型不存在（例如别名映射/聚合路由），则跳过守卫，后续 SelectChannel 会再次校验。
	if meta, err := h.loadModelMeta(req.Model); err == nil {
		if !meta.IsActive || strings.EqualFold(meta.Status, "offline") {
			h.logger.Warn("v1 chat: model offline or inactive",
				zap.String("model", req.Model),
				zap.String("status", meta.Status),
				zap.Bool("is_active", meta.IsActive))
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": gin.H{
					"message": fmt.Sprintf("Model %s has been disabled, please choose another model", req.Model),
					"type":    "model_unavailable",
					"code":    "model_offline",
				},
			})
			return
		}

		// v5.1: 免费层模型限制 —— Free 用户仅能调用 IsFreeTier=true 的模型
		isPaid, _ := c.Get("isPaidUser")
		if isPaidBool, ok := isPaid.(bool); ok && !isPaidBool && !meta.IsFreeTier {
			c.JSON(http.StatusForbidden, gin.H{
				"error": gin.H{
					"message": fmt.Sprintf("Model %s is a premium model. Please recharge at least ¥10 to unlock all models.", req.Model),
					"type":    "access_denied",
					"code":    "premium_model_only",
				},
			})
			return
		}

		if !isChatCompatibleModelType(meta.ModelType) {
			hint := endpointHintForModelType(meta.ModelType)
			h.logger.Warn("v1 chat: non-chat model type rejected",
				zap.String("model", req.Model),
				zap.String("type", meta.ModelType))
			c.JSON(http.StatusBadRequest, gin.H{
				"error": gin.H{
					"message": fmt.Sprintf("Model %s has type %q which is not supported on /v1/chat/completions; please use %s", req.Model, meta.ModelType, hint),
					"type":    "invalid_request_error",
					"code":    "unsupported_model_type",
				},
			})
			return
		}

		if missing := meta.MissingRequiredFeatures(requiredChatFeatureKeys(&req, rawMap, thinkingMode)); len(missing) > 0 {
			h.logger.Warn("v1 chat: model capability rejected",
				zap.String("model", req.Model),
				zap.Strings("missing_features", missing))
			c.JSON(http.StatusBadRequest, gin.H{
				"error": gin.H{
					"message": fmt.Sprintf("Model %s does not support required capabilities: %s", req.Model, strings.Join(missing, ", ")),
					"type":    "invalid_request_error",
					"code":    "unsupported_model_capability",
				},
			})
			return
		}
	}

	requestID := "chatcmpl-" + uuid.New().String()
	// 优先使用中间件生成的全局 RequestID，确保全链路可追踪
	if globalReqID, exists := c.Get("X-Request-ID"); exists {
		if rid, ok := globalReqID.(string); ok && rid != "" {
			requestID = rid
		}
	}
	start := time.Now()

	// 构建 API 调用全链路日志
	callLog := &model.ApiCallLog{
		RequestID:    requestID,
		UserID:       keyInfo.UserID,
		TenantID:     keyInfo.TenantID,
		ApiKeyID:     keyInfo.KeyID,
		ClientIP:     c.ClientIP(),
		Endpoint:     "/v1/chat/completions",
		RequestModel: req.Model,
		IsStream:     req.Stream,
		MessageCount: len(req.Messages),
		MaxTokens:    req.MaxTokens,
		Status:       "error",
	}
	// 请求体：存储完整请求 JSON（Authorization 头由 ApiKeyAuth 中间件消费，req 结构体不包含敏感字段）
	if rawBody != nil && len(rawBody) > 0 {
		callLog.RequestBody = string(rawBody)
	} else if bodyJSON, jerr := json.Marshal(req); jerr == nil {
		callLog.RequestBody = string(bodyJSON)
	} else {
		h.logger.Warn("序列化请求体失败", zap.Error(jerr), zap.String("request_id", requestID))
	}

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

	// 步骤2.6：TPM 限流预检（使用 max_tokens 估算）
	if h.tpmLimiter != nil && req.MaxTokens > 0 {
		if ok, tpmLimit := h.tpmLimiter.CheckTPM(c.Request.Context(), keyInfo.UserID, req.MaxTokens); !ok {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error": gin.H{
					"message": fmt.Sprintf("TPM limit exceeded: limit=%d tokens/min", tpmLimit),
					"type":    "rate_limit_error",
					"code":    "rate_limit_exceeded",
				},
			})
			return
		}
	}

	// 步骤2.7：校验模型已维护售价（ModelPricing）
	// 拒绝未定价模型的请求，避免"请求打上游成功但扣费失败"导致免费刷量。
	// 运维侧可通过 /admin/models/repair-pricing 一次性补齐历史数据。
	if err := h.ensureModelPriced(c.Request.Context(), req.Model); err != nil {
		if errors.Is(err, pricing.ErrModelNotPriced) {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": gin.H{
					"message": "Model is temporarily unavailable: sale price not configured",
					"type":    "model_unavailable",
					"code":    "model_not_priced",
				},
			})
			return
		}
		h.logger.Warn("v1 chat: 售价校验失败（非 ErrModelNotPriced）",
			zap.String("model", req.Model), zap.Error(err))
	}

	// 步骤3：并发计数与限速锁定 (v5.1: 记录并发到反滥用计数器)
	freezeID := ""
	if h.billingSvc != nil {
		freeze, fErr := h.billingSvc.FreezeUsage(c.Request.Context(), billingsvc.UsageRequest{
			RequestID:    requestID,
			UserID:       keyInfo.UserID,
			TenantID:     keyInfo.TenantID,
			ModelName:    req.Model,
			Usage:        estimateChatRequestUsage(&req),
			ThinkingMode: c.GetBool("thinking_mode"),
		})
		if fErr != nil {
			c.JSON(http.StatusPaymentRequired, gin.H{
				"error": gin.H{
					"message": "Insufficient balance",
					"type":    "insufficient_quota",
					"code":    "insufficient_quota",
				},
			})
			return
		}
		if freeze != nil && freeze.FreezeID != "" {
			freezeID = freeze.FreezeID
			c.Set("billing_freeze_id", freezeID)
			c.Set("estimated_cost_credits", freeze.EstimatedCostCredits)
			c.Set("estimated_cost_units", freeze.EstimatedCostUnits)
		}
	}

	concKey := fmt.Sprintf("abuse:conc:%d", keyInfo.UserID)
	if pkgredis.Client != nil {
		pkgredis.Client.Incr(c.Request.Context(), concKey)
		pkgredis.Client.Expire(c.Request.Context(), concKey, 120*time.Second) // 安全 TTL
		defer pkgredis.Client.Decr(c.Request.Context(), concKey)
	}

	// 步骤4：Failover 重试循环（最多尝试 maxRetries 个不同渠道）
	const maxRetries = 3
	customChannelID := keyInfo.CustomChannelID
	var excludeChannelIDs []uint
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		// 3.1 选择渠道（排除已失败渠道）
		var ch *model.Channel
		var actualModel string

		result, err := h.channelRouter.SelectChannelWithExcludes(
			c.Request.Context(), req.Model, customChannelID, keyInfo.UserID, excludeChannelIDs)
		if err != nil {
			h.logger.Warn("v1 chat: 渠道选择失败",
				zap.String("model", req.Model), zap.Int("attempt", attempt+1), zap.Error(err))
			// 尝试 Coding 渠道 fallback（仅在首次失败时尝试）
			if attempt == 0 {
				ch2, err2 := h.codingSvc.SelectCodingChannel(c.Request.Context(), req.Model)
				if err2 != nil {
					lastErr = fmt.Errorf("所有渠道选择失败: %w", err)
					break
				}
				ch = ch2
				actualModel = req.Model
			} else {
				lastErr = fmt.Errorf("重试 %d 次后无可用渠道: %w", attempt, err)
				break
			}
		} else {
			ch = result.Channel
			actualModel = result.ActualModel
		}

		// 3.2 创建提供商实例
		p := h.codingSvc.CreateProviderForChannel(ch)

		// 3.3 如果使用别名映射，更新请求中的模型名
		modelForReq := req.Model
		if actualModel != "" && actualModel != req.Model {
			h.logger.Info("v1 chat: using alias model mapping",
				zap.String("requested_model", req.Model),
				zap.String("actual_model", actualModel),
				zap.Int("attempt", attempt+1))
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
		// 3.3.0 尝试自动注入缓存控制标记（仅对 Anthropic explicit 机制模型）
		// 若用户在 extra 中显式传 cache_enabled=false，则跳过注入并从 extra 中剥离该键（不透传给上游）
		cacheEnabledByUser := true
		if v, ok := userExtraParams["cache_enabled"]; ok {
			if b, isBool := v.(bool); isBool {
				cacheEnabledByUser = b
			}
			delete(userExtraParams, "cache_enabled")
		}
		if aiModelForCache := h.loadAIModelForCache(c.Request.Context(), req.Model); aiModelForCache != nil {
			if cacheEnabledByUser && h.shouldInjectCacheControl(&req, aiModelForCache) {
				chatReq.InjectCacheControl = true
				h.logger.Debug("v1 chat: 自动注入缓存控制标记",
					zap.String("model", req.Model),
					zap.String("cache_mechanism", aiModelForCache.CacheMechanism))
			} else if !cacheEnabledByUser {
				h.logger.Debug("v1 chat: 用户显式关闭缓存，跳过注入",
					zap.String("model", req.Model))
			}
			// requires_stream：部分模型（如 MiniMax Pro）仅支持流式接口，强制转为流式
			if !req.Stream && aiModelForCache.RequiresStream() {
				req.Stream = true
				chatReq.Stream = true
				h.logger.Debug("v1 chat: 模型要求流式，自动升级请求",
					zap.String("model", req.Model))
			}
		}

		// 3.3.1 合并自定义参数：用户请求扩展参数（最低优先） + 模型级 ExtraParams + 渠道级 CustomParams（最高优先）
		extra := h.mergeExtraParams(req.Model, ch)
		if len(userExtraParams) > 0 {
			if extra == nil {
				extra = make(map[string]interface{})
			}
			for k, v := range userExtraParams {
				if _, exists := extra[k]; !exists {
					extra[k] = v
				}
			}
		}
		// 参数映射转换：将平台标准参数转换为供应商特定参数
		if h.paramSvc != nil && ch != nil && len(extra) > 0 {
			supplierCode := ""
			if ch.Supplier.Code != "" {
				supplierCode = ch.Supplier.Code
			} else {
				var supplier model.Supplier
				if h.db.Select("code").First(&supplier, ch.SupplierID).Error == nil {
					supplierCode = supplier.Code
				}
			}
			if supplierCode != "" {
				extra = h.paramSvc.TransformParamsWithContext(c.Request.Context(), supplierCode, extra)
			}
		}
		chatReq.Extra = extra

		// 3.4 执行请求
		if req.Stream {
			// 流式请求：仅在建立连接前可重试
			reader, streamErr := p.StreamChat(c.Request.Context(), chatReq)
			if streamErr != nil {
				// 连接阶段失败，可以重试
				latency := time.Since(start).Milliseconds()
				h.recordLog(ch.ID, chatReq.Model, keyInfo, requestID, 0, 0, int(latency), 500, streamErr.Error())
				// 使用带错误分类的记录：client_canceled / timeout 不触发熔断
				h.channelRouter.RecordResultWithError(ch.ID, streamErr, 0, int(latency))
				excludeChannelIDs = append(excludeChannelIDs, ch.ID)
				lastErr = streamErr
				h.logger.Warn("v1 chat: 流式连接失败，尝试下一渠道",
					zap.Uint("channel_id", ch.ID), zap.Int("attempt", attempt+1), zap.Error(streamErr))
				continue
			}
			// 连接已建立，开始流式传输（不可再重试）
			c.Header("X-Request-ID", requestID)
			c.Header("X-Actual-Model", chatReq.Model)
			c.Header("X-Channel-ID", fmt.Sprintf("%d", ch.ID))
			c.Header("X-Upstream-Latency-Ms", fmt.Sprintf("%d", time.Since(start).Milliseconds()))
			includeUsage := req.StreamOptions != nil && req.StreamOptions.IncludeUsage
			sseResult, writeErr := provider.SSEWriter(c, reader, includeUsage)
			latency := time.Since(start).Milliseconds()
			if writeErr != nil {
				// 使用带错误分类的记录：客户端主动断开不应触发熔断
				h.channelRouter.RecordResultWithError(ch.ID, writeErr, 0, int(latency))
				// 流式断连补扣：若 usage 已在断连前完整聚合，仍执行扣费避免丢账
				var partialCost int64
				var partialCostRMB float64
				if sseResult != nil && sseResult.Usage != nil && sseResult.Usage.TotalTokens > 0 {
					partialCost, partialCostRMB, _ = h.calculateAndDeductCostWithErr(c, req.Model, keyInfo, *sseResult.Usage, requestID)
					if h.commissionCalc != nil && partialCost > 0 {
						h.commissionCalc.CalculateCommissionsAsyncByModelName(keyInfo.UserID, keyInfo.TenantID, partialCost, req.Model)
					}
					if h.tpmLimiter != nil {
						h.tpmLimiter.RecordTPM(c.Request.Context(), keyInfo.UserID, sseResult.Usage.TotalTokens)
					}
					h.recordLog(ch.ID, chatReq.Model, keyInfo, requestID,
						sseResult.Usage.PromptTokens, sseResult.Usage.CompletionTokens,
						int(latency), 500, "warn:stream_disconnect_deducted")
					h.logger.Warn("流式断连但 usage 已聚合，已完成补扣",
						zap.String("request_id", requestID),
						zap.Int64("cost_credits", partialCost))
				} else {
					h.releaseBillingFreeze(c, freezeID)
					h.recordLog(ch.ID, chatReq.Model, keyInfo, requestID, 0, 0, int(latency), 500, writeErr.Error())
				}
				callLog.ChannelID = ch.ID
				callLog.StatusCode = 500
				callLog.TotalLatencyMs = int(latency)
				callLog.ErrorMessage = writeErr.Error()
				callLog.ErrorType = "stream_write_error"
				callLog.CostCredits = partialCost
				callLog.CostRMB = partialCostRMB
				applyMatchedTierFromCtx(c, callLog)
				if sseResult != nil && sseResult.Usage != nil {
					callLog.PromptTokens = sseResult.Usage.PromptTokens
					callLog.CompletionTokens = sseResult.Usage.CompletionTokens
					callLog.TotalTokens = sseResult.Usage.TotalTokens
				}
				h.recordApiCallLog(callLog)
			} else {
				var usage *provider.Usage
				if sseResult != nil {
					usage = sseResult.Usage
				}
				h.channelRouter.RecordResult(ch.ID, true, int(latency), 200)
				// thinking-only 异常：HTTP 层面成功但上游仅返回 reasoning 无正文 content，
				// 记录 warn 标记到 ChannelLog 便于后台监控聚合，不影响计费流程
				warnMsg := ""
				if sseResult != nil && sseResult.ThinkingOnly {
					warnMsg = "warn:thinking_only"
					if sseResult.FinishReason != "" {
						warnMsg += ":finish=" + sseResult.FinishReason
					}
				}
				if usage != nil {
					h.recordLog(ch.ID, chatReq.Model, keyInfo, requestID,
						usage.PromptTokens, usage.CompletionTokens, int(latency), 200, warnMsg)
					cost, costRMB := h.calculateAndDeductCost(c, req.Model, keyInfo, *usage, requestID)
					if h.commissionCalc != nil && cost > 0 {
						h.commissionCalc.CalculateCommissionsAsyncByModelName(keyInfo.UserID, keyInfo.TenantID, cost, req.Model)
					}
					callLog.PromptTokens = usage.PromptTokens
					callLog.CompletionTokens = usage.CompletionTokens
					callLog.TotalTokens = usage.TotalTokens
					callLog.CostCredits = cost
					callLog.CostRMB = costRMB
					callLog.CacheReadTokens = usage.CacheReadTokens
					callLog.CacheWriteTokens = usage.CacheWriteTokens
					applyMatchedTierFromCtx(c, callLog)
					// 记录实际 TPM 消耗
					if h.tpmLimiter != nil && usage.TotalTokens > 0 {
						h.tpmLimiter.RecordTPM(c.Request.Context(), keyInfo.UserID, usage.TotalTokens)
					}
				} else if warnMsg != "" {
					h.releaseBillingFreeze(c, freezeID)
					// 无 usage 但有 thinking-only 警告，仍写入一条日志
					h.recordLog(ch.ID, chatReq.Model, keyInfo, requestID, 0, 0, int(latency), 200, warnMsg)
				} else {
					h.releaseBillingFreeze(c, freezeID)
				}
				callLog.ChannelID = ch.ID
				callLog.ActualModel = chatReq.Model
				callLog.StatusCode = 200
				callLog.TotalLatencyMs = int(latency)
				callLog.RetryCount = attempt
				callLog.Status = "success"
				if warnMsg != "" {
					callLog.ErrorMessage = warnMsg
					callLog.ErrorType = "thinking_only"
				}
				h.recordApiCallLog(callLog)
			}
			return // 流式已开始传输，不论成功失败都不再重试
		}

		// 非流式请求：执行并检查结果
		resp, chatErr := p.Chat(c.Request.Context(), chatReq)
		latency := time.Since(start).Milliseconds()

		if chatErr != nil {
			// 请求失败，记录并重试下一个渠道
			h.recordLog(ch.ID, chatReq.Model, keyInfo, requestID, 0, 0, int(latency), 500, chatErr.Error())
			// 使用带错误分类的记录：timeout / canceled / 4xx 不计入熔断
			h.channelRouter.RecordResultWithError(ch.ID, chatErr, 0, int(latency))
			excludeChannelIDs = append(excludeChannelIDs, ch.ID)
			lastErr = chatErr
			h.logger.Warn("v1 chat: 上游请求失败，尝试下一渠道",
				zap.Uint("channel_id", ch.ID), zap.Int("attempt", attempt+1), zap.Error(chatErr))
			continue
		}

		// 请求成功
		h.channelRouter.RecordResult(ch.ID, true, int(latency), 200)
		h.recordLog(ch.ID, chatReq.Model, keyInfo, requestID,
			resp.Usage.PromptTokens, resp.Usage.CompletionTokens, int(latency), 200, "")
		// 异步更新模型调用次数
		go func(modelName string) {
			h.db.Model(&model.AIModel{}).
				Where("model_name = ?", modelName).
				UpdateColumn("call_count", gorm.Expr("call_count + 1"))
		}(req.Model)
		cost, costRMB := h.calculateAndDeductCost(c, req.Model, keyInfo, resp.Usage, requestID)
		if h.commissionCalc != nil && cost > 0 {
			h.commissionCalc.CalculateCommissionsAsyncByModelName(keyInfo.UserID, keyInfo.TenantID, cost, req.Model)
		}
		// 记录实际 TPM 消耗
		if h.tpmLimiter != nil && resp.Usage.TotalTokens > 0 {
			h.tpmLimiter.RecordTPM(c.Request.Context(), keyInfo.UserID, resp.Usage.TotalTokens)
		}

		// 记录全链路日志
		callLog.ChannelID = ch.ID
		callLog.ChannelName = ch.Name
		callLog.ActualModel = chatReq.Model
		callLog.StatusCode = 200
		callLog.PromptTokens = resp.Usage.PromptTokens
		callLog.CompletionTokens = resp.Usage.CompletionTokens
		callLog.TotalTokens = resp.Usage.TotalTokens
		callLog.CacheReadTokens = resp.Usage.CacheReadTokens
		callLog.CacheWriteTokens = resp.Usage.CacheWriteTokens
		callLog.TotalLatencyMs = int(latency)
		callLog.UpstreamLatencyMs = int(latency)
		callLog.RetryCount = attempt
		callLog.CostCredits = cost
		callLog.CostRMB = costRMB
		applyMatchedTierFromCtx(c, callLog)
		callLog.Status = "success"
		h.recordApiCallLog(callLog)

		// 在响应头和响应体中注入 request_id
		c.Header("X-Request-ID", requestID)
		c.Header("X-Actual-Model", chatReq.Model)
		c.Header("X-Channel-ID", fmt.Sprintf("%d", ch.ID))
		c.Header("X-Upstream-Latency-Ms", fmt.Sprintf("%d", latency))
		// 将 resp 转为 map 以注入 request_id
		respBytes, _ := json.Marshal(resp)
		var respMap map[string]interface{}
		json.Unmarshal(respBytes, &respMap)
		respMap["request_id"] = requestID
		c.JSON(http.StatusOK, respMap)
		return
	}

	// 所有重试均失败
	h.logger.Error("v1 chat: 所有重试均失败",
		zap.String("model", req.Model), zap.Int("attempts", maxRetries), zap.Error(lastErr))

	// 记录失败的全链路日志
	callLog.StatusCode = http.StatusBadGateway
	callLog.TotalLatencyMs = int(time.Since(start).Milliseconds())
	callLog.RetryCount = maxRetries
	callLog.ErrorMessage = lastErr.Error()
	callLog.ErrorType = "upstream_error"
	callLog.Status = "error"
	h.recordApiCallLog(callLog)

	c.Header("X-Request-ID", requestID)
	h.releaseBillingFreeze(c, freezeID)
	c.JSON(http.StatusBadGateway, gin.H{
		"error": gin.H{
			"message": "All upstream channels failed for model: " + req.Model,
			"type":    "server_error",
		},
		"request_id": requestID,
	})
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
		// 使用带错误分类的记录：timeout / canceled / 4xx 不计入熔断
		h.channelRouter.RecordResultWithError(ch.ID, err, 0, int(latency))
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
	cost, _ := h.calculateAndDeductCost(c, req.Model, keyInfo, resp.Usage, requestID)
	if h.commissionCalc != nil && cost > 0 {
		h.commissionCalc.CalculateCommissionsAsyncByModelName(keyInfo.UserID, keyInfo.TenantID, cost, req.Model)
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
		h.releaseBillingFreeze(c, h.billingFreezeID(c))
		h.recordLog(ch.ID, req.Model, keyInfo, requestID, 0, 0, int(latency), 500, err.Error())
		// 使用带错误分类的记录：timeout / canceled / 4xx 不计入熔断
		h.channelRouter.RecordResultWithError(ch.ID, err, 0, int(latency))
		c.JSON(http.StatusBadGateway, gin.H{
			"error": gin.H{
				"message": "Upstream provider error",
				"type":    "server_error",
			},
		})
		return
	}

	// 使用 SSE 写入器转发流式响应
	sseResult, err := provider.SSEWriter(c, reader, includeUsage)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		// 使用带错误分类的记录：客户端主动断开不应触发熔断
		h.channelRouter.RecordResultWithError(ch.ID, err, 0, int(latency))
		if sseResult == nil || sseResult.Usage == nil || sseResult.Usage.TotalTokens <= 0 {
			h.releaseBillingFreeze(c, h.billingFreezeID(c))
		}
		h.recordLog(ch.ID, req.Model, keyInfo, requestID, 0, 0, int(latency), 500, err.Error())
		return
	}

	h.channelRouter.RecordResult(ch.ID, true, int(latency), 200)
	warnMsg := ""
	if sseResult != nil && sseResult.ThinkingOnly {
		warnMsg = "warn:thinking_only"
		if sseResult.FinishReason != "" {
			warnMsg += ":finish=" + sseResult.FinishReason
		}
	}
	if sseResult != nil && sseResult.Usage != nil {
		usage := sseResult.Usage
		h.recordLog(ch.ID, req.Model, keyInfo, requestID,
			usage.PromptTokens, usage.CompletionTokens, int(latency), 200, warnMsg)
		cost, _ := h.calculateAndDeductCost(c, req.Model, keyInfo, *usage, requestID)
		if h.commissionCalc != nil && cost > 0 {
			h.commissionCalc.CalculateCommissionsAsyncByModelName(keyInfo.UserID, keyInfo.TenantID, cost, req.Model)
		}
	} else if warnMsg != "" {
		h.releaseBillingFreeze(c, h.billingFreezeID(c))
		h.recordLog(ch.ID, req.Model, keyInfo, requestID, 0, 0, int(latency), 200, warnMsg)
	} else {
		h.releaseBillingFreeze(c, h.billingFreezeID(c))
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
	if globalReqID, exists := c.Get("X-Request-ID"); exists {
		if rid, ok := globalReqID.(string); ok && rid != "" {
			requestID = rid
		}
	}
	start := time.Now()

	// 选择 Coding 渠道
	freezeID := ""
	if h.billingSvc != nil {
		freeze, fErr := h.billingSvc.FreezeUsage(c.Request.Context(), billingsvc.UsageRequest{
			RequestID: requestID,
			UserID:    keyInfo.UserID,
			TenantID:  keyInfo.TenantID,
			ModelName: req.Model,
			Usage:     estimateFIMRequestUsage(&req),
		})
		if fErr != nil {
			c.JSON(http.StatusPaymentRequired, gin.H{
				"error": gin.H{
					"message": "Insufficient balance",
					"type":    "insufficient_quota",
					"code":    "insufficient_quota",
				},
			})
			return
		}
		if freeze != nil && freeze.FreezeID != "" {
			freezeID = freeze.FreezeID
			c.Set("billing_freeze_id", freezeID)
			c.Set("estimated_cost_credits", freeze.EstimatedCostCredits)
			c.Set("estimated_cost_units", freeze.EstimatedCostUnits)
		}
	}

	ch, err := h.codingSvc.SelectCodingChannel(c.Request.Context(), req.Model)
	if err != nil {
		// 尝试通过通用渠道路由选择
		result, err2 := h.channelRouter.SelectChannel(c.Request.Context(), req.Model, keyInfo.CustomChannelID, keyInfo.UserID)
		if err2 != nil {
			h.releaseBillingFreeze(c, freezeID)
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
		h.releaseBillingFreeze(c, freezeID)
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
	cost, costRMB := h.calculateAndDeductCost(c, req.Model, keyInfo, resp.Usage, requestID)
	if h.commissionCalc != nil && cost > 0 {
		h.commissionCalc.CalculateCommissionsAsyncByModelName(keyInfo.UserID, keyInfo.TenantID, cost, req.Model)
	}
	if h.tpmLimiter != nil && resp.Usage.TotalTokens > 0 {
		h.tpmLimiter.RecordTPM(c.Request.Context(), keyInfo.UserID, resp.Usage.TotalTokens)
	}
	h.recordFIMApiCallLog(c, &req, ch, keyInfo, requestID, &resp.Usage, int(latency), 200, cost, costRMB, "")

	// v5.1: 记录积分消耗到反滥用 TPM 计数器
	if pkgredis.Client != nil && cost > 0 {
		minuteKey := time.Now().Unix() / 60
		abuseTpmKey := fmt.Sprintf("abuse:tpm:%d:%d", keyInfo.UserID, minuteKey)
		pkgredis.Client.IncrBy(c.Request.Context(), abuseTpmKey, cost)
		pkgredis.Client.Expire(c.Request.Context(), abuseTpmKey, 120*time.Second)
	}

	// 转换为 OpenAI completions 响应格式
	completionText := ""
	finishReason := "stop"
	if len(resp.Choices) > 0 {
		completionText = provider.TextContent(resp.Choices[0].Message.Content)
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
			h.releaseBillingFreeze(c, h.billingFreezeID(c))
			h.recordLog(ch.ID, req.Model, keyInfo, requestID, 0, 0, int(latency), 500, err.Error())
			c.JSON(http.StatusBadGateway, gin.H{
				"error": gin.H{
					"message": "Upstream provider error",
					"type":    "server_error",
				},
			})
			return
		}
		sseResult, err := provider.SSEWriter(c, reader, false)
		latency := time.Since(start).Milliseconds()
		if err != nil {
			if sseResult == nil || sseResult.Usage == nil || sseResult.Usage.TotalTokens <= 0 {
				h.releaseBillingFreeze(c, h.billingFreezeID(c))
			}
			h.recordLog(ch.ID, req.Model, keyInfo, requestID, 0, 0, int(latency), 500, err.Error())
			return
		}
		if sseResult != nil && sseResult.Usage != nil {
			usage := sseResult.Usage
			h.recordLog(ch.ID, req.Model, keyInfo, requestID,
				usage.PromptTokens, usage.CompletionTokens, int(latency), 200, "")
			cost, costRMB := h.calculateAndDeductCost(c, req.Model, keyInfo, *usage, requestID)
			if h.commissionCalc != nil && cost > 0 {
				h.commissionCalc.CalculateCommissionsAsyncByModelName(keyInfo.UserID, keyInfo.TenantID, cost, req.Model)
			}
			if h.tpmLimiter != nil && usage.TotalTokens > 0 {
				h.tpmLimiter.RecordTPM(c.Request.Context(), keyInfo.UserID, usage.TotalTokens)
			}
			h.recordFIMApiCallLog(c, req, ch, keyInfo, requestID, usage, int(latency), 200, cost, costRMB, "")
		} else {
			h.releaseBillingFreeze(c, h.billingFreezeID(c))
		}
		return
	}

	// 非流式 FIM
	fimResp, err := p.FIMCompletion(c.Request.Context(), fimReq)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		h.releaseBillingFreeze(c, h.billingFreezeID(c))
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
		cost, costRMB := h.calculateAndDeductCost(c, req.Model, keyInfo, *fimResp.Usage, requestID)
		if h.commissionCalc != nil && cost > 0 {
			h.commissionCalc.CalculateCommissionsAsyncByModelName(keyInfo.UserID, keyInfo.TenantID, cost, req.Model)
		}
		if h.tpmLimiter != nil && fimResp.Usage.TotalTokens > 0 {
			h.tpmLimiter.RecordTPM(c.Request.Context(), keyInfo.UserID, fimResp.Usage.TotalTokens)
		}
		h.recordFIMApiCallLog(c, req, ch, keyInfo, requestID, fimResp.Usage, int(latency), 200, cost, costRMB, "")
	} else {
		h.releaseBillingFreeze(c, h.billingFreezeID(c))
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

// recordLog 异步保存渠道调用日志（同时写入 channel_logs 和 api_call_logs）
// 注意：此函数不读 gin.Context，因此 MatchedPriceTier 信息不会写入 ChannelLog
// ApiCallLog 中的 MatchedPriceTier 通过 applyMatchedTierFromCtx 辅助函数单独写入
//
// ErrorCategory 自动分类规则：
//   - statusCode 2xx 且 errMsg 为空 → "success"
//   - statusCode 2xx 但 errMsg 以 "warn:" 开头 → "success"（业务预警仍算成功）
//   - 其他情况调用 ClassifyError(nil, statusCode) 基于 statusCode + errMsg 字符串匹配推断
func (h *CompletionsHandler) recordLog(
	channelID uint, modelName string, keyInfo *apikey.ApiKeyInfo,
	requestID string, promptTokens, completionTokens, latencyMs, statusCode int, errMsg string,
) {
	// 预先计算 ErrorCategory（避免在 goroutine 里重复推断）
	category := inferErrorCategory(statusCode, errMsg)
	go func() {
		log := &model.ChannelLog{
			ChannelID:           channelID,
			ModelName:           modelName,
			TenantID:            keyInfo.TenantID,
			UserID:              keyInfo.UserID,
			ApiKeyID:            keyInfo.KeyID,
			RequestTokens:       promptTokens,
			ResponseTokens:      completionTokens,
			LatencyMs:           latencyMs,
			StatusCode:          statusCode,
			ErrorMessage:        errMsg,
			ErrorCategory:       category,
			RequestID:           requestID,
			MatchedPriceTierIdx: -1,
		}
		if err := h.db.Create(log).Error; err != nil {
			h.logger.Error("v1: 记录渠道日志失败", zap.Error(err))
		}
	}()
}

// inferErrorCategory 基于 status_code 与 errMsg 字符串推断错误类别
// 调用 channelsvc.ClassifyError 使用其 HTTP status + 字符串回退分支
func inferErrorCategory(statusCode int, errMsg string) string {
	// warn: 前缀属于业务预警（如 thinking_only、stream_disconnect_deducted），视为成功
	if statusCode >= 200 && statusCode < 400 {
		return string(channelsvc.ErrCatSuccess)
	}
	// 用 errMsg 伪造一个 error 传入 ClassifyError 以触发其字符串 fallback 分支
	var err error
	if errMsg != "" {
		err = stringError(errMsg)
	}
	return string(channelsvc.ClassifyError(err, statusCode))
}

// stringError 辅助类型：把字符串包装成 error 供 ClassifyError 做 errors.Is / As / 字符串匹配
type stringError string

func (s stringError) Error() string { return string(s) }

// applyMatchedTierFromCtx 从 gin.Context 读取阶梯命中信息并写入 ApiCallLog
// 由 calculateAndDeductCostWithErr 写入上下文；失败或未计费时保持默认 (-1, "")
func applyMatchedTierFromCtx(c *gin.Context, callLog *model.ApiCallLog) {
	if callLog == nil {
		return
	}
	callLog.MatchedPriceTierIdx = -1
	if tier, ok := c.Get("matched_price_tier"); ok {
		if s, ok2 := tier.(string); ok2 {
			callLog.MatchedPriceTier = s
		}
	}
	if idx, ok := c.Get("matched_price_tier_idx"); ok {
		if i, ok2 := idx.(int); ok2 {
			callLog.MatchedPriceTierIdx = i
		}
	}
	// 思考模式标记：calculateAndDeductCostWithErr 在实际应用思考加价时设置 thinking_mode_applied
	// 即使未应用加价（模型未配置 thinking 价），仅 enable_thinking=true 也记录 thinking_mode 方便审计
	if v, ok := c.Get("thinking_mode"); ok {
		if b, ok2 := v.(bool); ok2 && b {
			callLog.ThinkingMode = true
		}
	}
	// v4.0 用户特殊折扣信息
	if v, ok := c.Get("user_discount_id"); ok {
		if id, ok2 := v.(uint); ok2 && id > 0 {
			callLog.UserDiscountID = &id
		}
	}
	if v, ok := c.Get("user_discount_rate"); ok {
		if r, ok2 := v.(float64); ok2 {
			callLog.UserDiscountRate = &r
		}
	}
	if v, ok := c.Get("user_discount_type"); ok {
		if s, ok2 := v.(string); ok2 {
			callLog.UserDiscountType = s
		}
	}
	if v, ok := c.Get("billing_status"); ok {
		if s, ok2 := v.(string); ok2 {
			callLog.BillingStatus = s
		}
	}
	if v, ok := c.Get("estimated_cost_credits"); ok {
		if n, ok2 := v.(int64); ok2 {
			callLog.EstimatedCostCredits = n
		}
	}
	if v, ok := c.Get("estimated_cost_units"); ok {
		if n, ok2 := v.(int64); ok2 {
			callLog.EstimatedCostUnits = n
		}
	}
	if v, ok := c.Get("actual_cost_credits"); ok {
		if n, ok2 := v.(int64); ok2 {
			callLog.ActualCostCredits = n
		}
	}
	if v, ok := c.Get("actual_cost_units"); ok {
		if n, ok2 := v.(int64); ok2 {
			callLog.ActualCostUnits = n
		}
	}
	if v, ok := c.Get("under_collected_credits"); ok {
		if n, ok2 := v.(int64); ok2 {
			callLog.UnderCollectedCredits = n
		}
	}
	if v, ok := c.Get("under_collected_units"); ok {
		if n, ok2 := v.(int64); ok2 {
			callLog.UnderCollectedUnits = n
		}
	}
	if v, ok := c.Get("cost_units"); ok {
		if n, ok2 := v.(int64); ok2 {
			callLog.CostUnits = n
		}
	}
	if v, ok := c.Get("platform_cost_units"); ok {
		if n, ok2 := v.(int64); ok2 {
			callLog.PlatformCostUnits = n
		}
	}
	if v, ok := c.Get("platform_cost_rmb"); ok {
		if n, ok2 := v.(float64); ok2 {
			callLog.PlatformCostRMB = n
		}
	}
	if v, ok := c.Get("usage_source"); ok {
		if s, ok2 := v.(string); ok2 {
			callLog.UsageSource = s
		}
	}
	if v, ok := c.Get("usage_estimated"); ok {
		if b, ok2 := v.(bool); ok2 {
			callLog.UsageEstimated = b
		}
	}
	if v, ok := c.Get("billing_snapshot_json"); ok {
		if raw, ok2 := v.(string); ok2 && raw != "" {
			callLog.BillingSnapshot = model.JSON(raw)
		}
	}
}

// ensureModelPriced 校验请求模型已在 model_pricings 表维护售价
// 未配置售价的模型一律拒绝（避免按成本价兜底扣费导致平台亏损）。
// 查不到 ai_models 记录时不拦截，让后续渠道路由自然返回 model_not_found。
func (h *CompletionsHandler) ensureModelPriced(ctx context.Context, modelName string) error {
	if h.pricingCalc == nil || modelName == "" {
		return nil
	}
	var m model.AIModel
	if err := h.db.WithContext(ctx).
		Select("id").
		Where("model_name = ? AND is_active = true", modelName).
		First(&m).Error; err != nil {
		return nil // 模型不存在交给渠道路由处理
	}
	_, err := h.pricingCalc.CalculatePrice(ctx, 0, m.ID, 0, 0)
	return err
}

// recordApiCallLog 异步保存 API 调用全链路日志
func (h *CompletionsHandler) recordApiCallLog(log *model.ApiCallLog) {
	go func() {
		if err := h.db.Create(log).Error; err != nil {
			h.logger.Error("v1: 记录API调用日志失败", zap.Error(err))
		}
	}()
}

// calculateAndDeductCost 计算用量费用并从余额中扣减，返回积分（int64）和人民币金额（float64）
// 失败时仍返回计算出的 cost，便于调用方记录 ApiCallLog.CostCredits（对账用）。
// 扣费失败会被记 error 日志；调用方可通过 deductFailed 进一步打标。
func (h *CompletionsHandler) calculateAndDeductCost(
	c *gin.Context,
	modelName string, keyInfo *apikey.ApiKeyInfo, usage provider.Usage, requestID string,
) (int64, float64) {
	cost, costRMB, _ := h.calculateAndDeductCostWithErr(c, modelName, keyInfo, usage, requestID)
	return cost, costRMB
}

// calculateAndDeductCostWithErr 带错误返回的扣费版本，供流式/重试场景显式判断扣费是否成功
func (h *CompletionsHandler) calculateAndDeductCostWithErr(
	c *gin.Context,
	modelName string, keyInfo *apikey.ApiKeyInfo, usage provider.Usage, requestID string,
) (int64, float64, error) {
	if h.billingSvc != nil {
		out, err := h.billingSvc.SettleUsage(c.Request.Context(), billingsvc.UsageRequest{
			RequestID:    requestID,
			UserID:       keyInfo.UserID,
			TenantID:     keyInfo.TenantID,
			ModelName:    modelName,
			Usage:        usage,
			ThinkingMode: c.GetBool("thinking_mode"),
			FreezeID:     h.billingFreezeID(c),
		})
		if out == nil {
			return 0, 0, err
		}
		h.applyBillingOutcomeToContext(c, out)
		if err != nil {
			h.logger.Error("鎵ｈ垂澶辫触锛岄渶瑕佷汉宸ュ璐?",
				zap.Error(err),
				zap.String("request_id", requestID),
				zap.Uint("user_id", keyInfo.UserID),
				zap.Uint("tenant_id", keyInfo.TenantID),
				zap.String("model", modelName),
				zap.Int64("cost_credits", out.CostCredits))
		}
		return out.CostCredits, out.CostRMB, err
	}
	if h.pricingCalc == nil {
		return 0, 0, nil
	}
	ctx := c.Request.Context()
	var aiModel model.AIModel
	if err := h.db.WithContext(ctx).Where("model_name = ? AND is_active = true", modelName).First(&aiModel).Error; err != nil {
		return 0, 0, nil
	}
	// 含缓存用量时走 CalculateCostWithCache，将 cache_read/cache_write 按缓存比率从用户售价中扣除
	// 无缓存命中时内部自动回退到普通 CalculateCost 路径
	var costResult *pricing.CostResult
	var err error
	if usage.CacheReadTokens > 0 || usage.CacheWriteTokens > 0 {
		costResult, err = h.pricingCalc.CalculateCostWithCache(ctx, keyInfo.UserID, &aiModel, keyInfo.TenantID, 0, pricing.CacheUsageInput{
			InputTokens:      usage.PromptTokens,
			OutputTokens:     usage.CompletionTokens,
			CacheReadTokens:  usage.CacheReadTokens,
			CacheWriteTokens: usage.CacheWriteTokens,
		})
	} else {
		costResult, err = h.pricingCalc.CalculateCost(ctx, keyInfo.UserID, aiModel.ID, keyInfo.TenantID, 0, usage.PromptTokens, usage.CompletionTokens)
	}
	if err != nil || costResult == nil {
		return 0, 0, err
	}
	c.Set("estimated_cost_credits", costResult.TotalCost)
	c.Set("platform_cost_rmb", float64(costResult.PlatformCost)/10000.0)
	c.Set("usage_source", "provider")
	c.Set("usage_estimated", false)
	// 思考模式加价：按优先级查找有效"思考输出售价"，与普通输出售价的差值做加价
	//   1. 阶梯命中 + tier.SellingOutputThinkingPrice  (平台阶梯售价覆盖)
	//   2. 阶梯命中 + tier.OutputPriceThinking (成本) × ratio  (保持售价链路)
	//   3. mp.OutputPriceThinkingRMB (模型级思考售价)
	//   4. aiModel.OutputCostThinkingRMB × ratio (顶层成本 × 当前 output 售价/成本 比例)
	// ratio = 当前已应用的 output 售价 / 模型 output 成本价（在有阶梯时自动等于 tier 售价/tier 成本）
	if tmVal, ok := c.Get("thinking_mode"); ok {
		if tm, isBool := tmVal.(bool); isBool && tm && usage.CompletionTokens > 0 {
			thinkingSellRMB := resolveThinkingOutputSellRMB(h.db, ctx, &aiModel, costResult)
			if thinkingSellRMB > 0 {
				normalSellRMB := costResult.PriceDetail.OutputPriceRMB
				diffRMB := thinkingSellRMB - normalSellRMB
				if diffRMB > 0 {
					surchargeRMB := diffRMB * float64(usage.CompletionTokens) / 1_000_000
					surchargeCredits := int64(surchargeRMB*10000 + 0.5) // 1 RMB = 10,000 积分
					costResult.TotalCost += surchargeCredits
					costResult.OutputCost += surchargeCredits
					costResult.TotalCostRMB += surchargeRMB
					c.Set("thinking_mode_applied", true)
					c.Set("thinking_output_price_rmb", thinkingSellRMB)
					h.logger.Info("thinking mode surcharge applied",
						zap.String("model", modelName),
						zap.Int("completion_tokens", usage.CompletionTokens),
						zap.Float64("normal_sell_rmb", normalSellRMB),
						zap.Float64("thinking_sell_rmb", thinkingSellRMB),
						zap.Float64("surcharge_rmb", surchargeRMB),
						zap.Int64("surcharge_credits", surchargeCredits))
				}
			}
		}
	}
	// 将命中阶梯信息写入 gin.Context，供 ApiCallLog 构造时提取（key: matched_price_tier / matched_price_tier_idx）
	c.Set("matched_price_tier", costResult.MatchedTier)
	c.Set("matched_price_tier_idx", costResult.MatchedTierIdx)
	// 将用户特殊折扣信息写入 gin.Context（v4.0），供 ApiCallLog 构造时提取
	if costResult.UserDiscountID != nil {
		c.Set("user_discount_id", *costResult.UserDiscountID)
	}
	if costResult.UserDiscountRate != nil {
		c.Set("user_discount_rate", *costResult.UserDiscountRate)
	}
	if costResult.UserDiscountType != "" {
		c.Set("user_discount_type", costResult.UserDiscountType)
	}
	c.Set("estimated_cost_credits", costResult.TotalCost)
	if h.balanceSvc != nil && costResult.TotalCost > 0 {
		if dErr := h.balanceSvc.DeductForRequest(ctx, keyInfo.UserID, keyInfo.TenantID, costResult.TotalCost, modelName, requestID); dErr != nil {
			h.logger.Error("扣费失败，需要人工对账",
				zap.Error(dErr),
				zap.String("request_id", requestID),
				zap.Uint("user_id", keyInfo.UserID),
				zap.Uint("tenant_id", keyInfo.TenantID),
				zap.String("model", modelName),
				zap.Int64("cost_credits", costResult.TotalCost))
			c.Set("billing_status", "deduct_failed")
			c.Set("actual_cost_credits", int64(0))
			c.Set("under_collected_credits", costResult.TotalCost)
			h.setBillingSnapshot(c, requestID, modelName, &aiModel, costResult, usage, "deduct_failed", 0, costResult.TotalCost)
			return costResult.TotalCost, costResult.TotalCostRMB, dErr
		}
	}
	if costResult.TotalCost > 0 {
		c.Set("billing_status", "settled")
		c.Set("actual_cost_credits", costResult.TotalCost)
		c.Set("under_collected_credits", int64(0))
		h.setBillingSnapshot(c, requestID, modelName, &aiModel, costResult, usage, "settled", costResult.TotalCost, 0)
	} else {
		c.Set("billing_status", "no_charge")
		c.Set("actual_cost_credits", int64(0))
		c.Set("under_collected_credits", int64(0))
		h.setBillingSnapshot(c, requestID, modelName, &aiModel, costResult, usage, "no_charge", 0, 0)
	}
	// 异步计算并记录缓存节省金额（不阻塞响应）
	if (usage.CacheReadTokens > 0 || usage.CacheWriteTokens > 0) && h.pricingCalc != nil {
		go func() {
			_, savingsRMB, sErr := h.pricingCalc.CalculateWithCache(
				context.Background(), keyInfo.UserID, &aiModel, keyInfo.TenantID, 0,
				pricing.CacheUsageInput{
					InputTokens:      usage.PromptTokens,
					OutputTokens:     usage.CompletionTokens,
					CacheReadTokens:  usage.CacheReadTokens,
					CacheWriteTokens: usage.CacheWriteTokens,
				},
			)
			if sErr != nil {
				h.logger.Debug("计算缓存节省金额失败", zap.Error(sErr))
				return
			}
			if savingsRMB > 0 {
				// 更新 api_call_log 的缓存节省字段（异步）
				h.db.Model(&model.ApiCallLog{}).
					Where("request_id = ?", requestID).
					Updates(map[string]interface{}{
						"cache_read_tokens":  usage.CacheReadTokens,
						"cache_write_tokens": usage.CacheWriteTokens,
						"cache_savings_rmb":  savingsRMB,
					})
			}
		}()
	}
	return costResult.TotalCost, costResult.TotalCostRMB, nil
}

// setBillingSnapshot 将本次扣费使用的价格、用量、折扣和账务状态固化到日志快照。
// 成本分析优先读取该快照，避免后续改价后污染历史账单。
func (h *CompletionsHandler) setBillingSnapshot(
	c *gin.Context,
	requestID, modelName string,
	aiModel *model.AIModel,
	costResult *pricing.CostResult,
	usage provider.Usage,
	billingStatus string,
	actualCostCredits, underCollectedCredits int64,
) {
	if c == nil || aiModel == nil || costResult == nil {
		return
	}
	snapshot := map[string]interface{}{
		"schema_version":          1,
		"generated_at":            time.Now().UTC().Format(time.RFC3339Nano),
		"request_id":              requestID,
		"model_id":                aiModel.ID,
		"model_name":              modelName,
		"pricing_unit":            aiModel.PricingUnit,
		"prompt_tokens":           usage.PromptTokens,
		"completion_tokens":       usage.CompletionTokens,
		"total_tokens":            usage.TotalTokens,
		"cache_read_tokens":       usage.CacheReadTokens,
		"cache_write_tokens":      usage.CacheWriteTokens,
		"sell_input_per_million":  costResult.PriceDetail.InputPricePerMillion,
		"sell_output_per_million": costResult.PriceDetail.OutputPricePerMillion,
		"sell_input_rmb":          costResult.PriceDetail.InputPriceRMB,
		"sell_output_rmb":         costResult.PriceDetail.OutputPriceRMB,
		"input_cost_credits":      costResult.InputCost,
		"output_cost_credits":     costResult.OutputCost,
		"total_cost_credits":      costResult.TotalCost,
		"total_cost_rmb":          costResult.TotalCostRMB,
		"platform_cost_credits":   costResult.PlatformCost,
		"platform_cost_rmb":       float64(costResult.PlatformCost) / 10000.0,
		"pricing_source":          costResult.PriceDetail.Source,
		"matched_price_tier":      costResult.MatchedTier,
		"matched_price_tier_idx":  costResult.MatchedTierIdx,
		"user_discount_type":      costResult.UserDiscountType,
		"billing_status":          billingStatus,
		"actual_cost_credits":     actualCostCredits,
		"under_collected_credits": underCollectedCredits,
		"cache_read_cost":         costResult.CacheReadCost,
		"cache_write_cost":        costResult.CacheWriteCost,
		"regular_input_cost":      costResult.RegularInputCost,
		"cache_saving_credits":    costResult.CacheSavingCredits,
		"thinking_mode":           c.GetBool("thinking_mode"),
		"thinking_mode_applied":   c.GetBool("thinking_mode_applied"),
	}
	if costResult.UserDiscountID != nil {
		snapshot["user_discount_id"] = *costResult.UserDiscountID
	}
	if costResult.UserDiscountRate != nil {
		snapshot["user_discount_rate"] = *costResult.UserDiscountRate
	}
	if v, ok := c.Get("thinking_output_price_rmb"); ok {
		snapshot["thinking_output_price_rmb"] = v
	}
	if b, err := json.Marshal(snapshot); err == nil {
		c.Set("billing_snapshot_json", string(b))
	}
}

// shouldInjectCacheControl 判断是否应为当前请求自动注入缓存控制标记
// 仅对 Anthropic explicit 机制模型生效（注意：Anthropic 是唯一需要显式参数才能触发缓存的供应商）
func (h *CompletionsHandler) shouldInjectCacheControl(req *chatCompletionRequest, aiModel *model.AIModel) bool {
	if aiModel == nil || !aiModel.SupportsCache {
		return false
	}
	if aiModel.CacheMechanism != "explicit" {
		return false // only=explicit; "both" mode 下 auto 已自动生效
	}
	// 用户已传递 cache_control 字段（优先使用用户设置）
	if hasCacheControlInRequest(req.Messages) {
		return false
	}
	// Token 门槛检查（简单估算：使用字符数 / 4 作为 Token 数近似值）
	if aiModel.CacheMinTokens > 0 {
		var systemLen int
		for _, m := range req.Messages {
			if m.Role == "system" {
				systemLen = len(provider.TextContent(m.Content))
				break
			}
		}
		if systemLen == 0 && len(req.Messages) > 0 {
			systemLen = len(provider.TextContent(req.Messages[0].Content))
		}
		estimatedTokens := systemLen / 4
		if estimatedTokens < aiModel.CacheMinTokens {
			return false
		}
	}
	return true
}

// hasCacheControlInRequest 检查请求消息中是否已包含 cache_control 相关字段
// 通过检查消息内容中是否含有 cache_control JSON 键来判断
func hasCacheControlInRequest(messages []provider.Message) bool {
	for _, m := range messages {
		// 对 string 类型按子串匹配；对数组类型按 JSON 编码后搜索关键字，覆盖 OpenAI 多模态块内
		// 出现 cache_control 的情形（如 Anthropic 风格的 ephemeral 断点透传）。
		switch v := m.Content.(type) {
		case string:
			if strings.Contains(v, "cache_control") {
				return true
			}
		default:
			if v == nil {
				continue
			}
			if b, err := json.Marshal(v); err == nil && strings.Contains(string(b), "cache_control") {
				return true
			}
		}
	}
	return false
}

// loadAIModelForCache 加载 AI 模型的缓存相关字段
// 返回 nil 表示未找到模型（不影响主流程）
func (h *CompletionsHandler) loadAIModelForCache(ctx context.Context, modelName string) *model.AIModel {
	var aiModel model.AIModel
	err := h.db.WithContext(ctx).
		Select("id, model_name, supports_cache, cache_mechanism, cache_min_tokens, "+
			"cache_input_price_rmb, cache_explicit_input_price_rmb, cache_write_price_rmb, "+
			"input_cost_rmb, output_cost_rmb, features").
		Where("model_name = ? AND is_active = true", modelName).
		First(&aiModel).Error
	if err != nil {
		return nil
	}
	return &aiModel
}

// mergeExtraParams 合并模型级和渠道级的自定义参数
// 优先级：渠道 CustomParams > 模型 ExtraParams（渠道参数覆盖模型参数）
//
// 防御性过滤（自 2026-04-20 起）：
// 对每个键-值对，若 key 命中 model.BogusFlagKeys 白名单 且 value 是 bool，则跳过。
// 这类 {key: bool} 是历史脏数据（能力标记被误写入 extra_params），直接合并进请求体
// 会触发上游 400 "cannot unmarshal bool into ... []string" 等错误。
// 与 RunExtraParamsFeatureFlagsCleanup 迁移使用同一份白名单。
func (h *CompletionsHandler) mergeExtraParams(modelName string, ch *model.Channel) map[string]interface{} {
	extra := make(map[string]interface{})

	// 1. 加载模型级自定义参数
	var aiModel model.AIModel
	if err := h.db.Where("model_name = ? AND is_active = ?", modelName, true).First(&aiModel).Error; err == nil {
		if len(aiModel.ExtraParams) > 0 {
			var modelParams map[string]interface{}
			if json.Unmarshal(aiModel.ExtraParams, &modelParams) == nil {
				for k, v := range modelParams {
					if skipBogusBoolFlag(modelName, "ai_models.extra_params", k, v) {
						continue
					}
					extra[k] = v
				}
			}
		}
	}

	// 2. 合并渠道级自定义参数（extra_body 部分）
	if ch != nil && len(ch.CustomParams) > 0 {
		var channelParams map[string]interface{}
		if json.Unmarshal(ch.CustomParams, &channelParams) == nil {
			// 提取 extra_body 中的参数，合并到 extra（覆盖模型参数）
			if body, ok := channelParams["extra_body"].(map[string]interface{}); ok {
				for k, v := range body {
					if skipBogusBoolFlag(modelName, "channels.custom_params.extra_body", k, v) {
						continue
					}
					extra[k] = v
				}
			}
			// 如果没有 extra_body 包装，直接合并非保留字段
			// 保留字段：headers, extra_body
			for k, v := range channelParams {
				if k != "headers" && k != "extra_body" {
					if skipBogusBoolFlag(modelName, "channels.custom_params", k, v) {
						continue
					}
					extra[k] = v
				}
			}
		}
	}

	if len(extra) == 0 {
		return nil
	}

	return extra
}

// skipBogusBoolFlag 判定某个 extra_params 键值对是否应被跳过。
// 返回 true 表示这是脏能力标记（{key: bool} 形式），应丢弃，并记录 warn 日志。
func skipBogusBoolFlag(modelName, source, key string, value interface{}) bool {
	if !model.IsBogusFlagKey(key) {
		return false
	}
	if _, isBool := value.(bool); !isBool {
		return false
	}
	logger.L.Warn("extra_params: skip bogus bool flag (likely dirty capability marker)",
		zap.String("model", modelName),
		zap.String("source", source),
		zap.String("key", key),
	)
	return true
}

// modelMeta 是模型前置校验所需的最小元信息
type modelMeta struct {
	ID         uint   `gorm:"column:id"`
	ModelName  string `gorm:"column:model_name"`
	ModelType  string `gorm:"column:model_type"`
	Status     string `gorm:"column:status"`
	IsActive   bool   `gorm:"column:is_active"`
	IsFreeTier bool   `gorm:"column:is_free_tier"`
	Features   model.JSON
}

// loadModelMeta 从 ai_models 表查找模型的可用状态与类型
// 返回 ErrRecordNotFound 视为"未知模型"，由后续 SelectChannel 统一处理
func (h *CompletionsHandler) loadModelMeta(modelName string) (*modelMeta, error) {
	var m modelMeta
	err := h.db.Table("ai_models").
		Select("id, model_name, model_type, status, is_active, is_free_tier, features").
		Where("model_name = ?", modelName).
		Take(&m).Error
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (m *modelMeta) MissingRequiredFeatures(required []string) []string {
	if len(required) == 0 {
		return nil
	}
	var feats map[string]interface{}
	if len(m.Features) > 0 {
		_ = json.Unmarshal(m.Features, &feats)
	}
	var missing []string
	for _, key := range required {
		if !featureBool(feats, key) {
			missing = append(missing, key)
		}
	}
	return missing
}

func featureBool(features map[string]interface{}, key string) bool {
	if features == nil {
		return false
	}
	switch v := features[key].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	case float64:
		return v != 0
	default:
		return false
	}
}

func requiredChatFeatureKeys(req *chatCompletionRequest, rawMap map[string]json.RawMessage, thinkingMode bool) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(key string) {
		if key != "" && !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}

	if requestHasVisionInput(req) {
		add("supports_vision")
	}
	if requestHasTools(rawMap) {
		add("supports_function_call")
	}
	if requestRequiresJSONMode(rawMap) {
		add("supports_json_mode")
	}
	if thinkingMode {
		add("supports_thinking")
	}
	return out
}

func requestHasVisionInput(req *chatCompletionRequest) bool {
	if req == nil {
		return false
	}
	for _, msg := range req.Messages {
		if contentHasMedia(msg.Content) {
			return true
		}
	}
	return false
}

func contentHasMedia(v interface{}) bool {
	switch x := v.(type) {
	case []interface{}:
		for _, item := range x {
			if contentHasMedia(item) {
				return true
			}
		}
	case map[string]interface{}:
		if typ, _ := x["type"].(string); isVisionContentType(typ) {
			return true
		}
		if _, ok := x["image_url"]; ok {
			return true
		}
		if _, ok := x["video_url"]; ok {
			return true
		}
		for _, val := range x {
			if contentHasMedia(val) {
				return true
			}
		}
	}
	return false
}

func isVisionContentType(typ string) bool {
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "image_url", "input_image", "image", "video_url", "input_video", "video":
		return true
	default:
		return false
	}
}

func requestHasTools(rawMap map[string]json.RawMessage) bool {
	raw, ok := rawMap["tools"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var tools []interface{}
	if err := json.Unmarshal(raw, &tools); err == nil {
		return len(tools) > 0
	}
	return true
}

func requestRequiresJSONMode(rawMap map[string]json.RawMessage) bool {
	raw, ok := rawMap["response_format"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var rf map[string]interface{}
	if err := json.Unmarshal(raw, &rf); err != nil {
		return false
	}
	typ, _ := rf["type"].(string)
	typ = strings.ToLower(strings.TrimSpace(typ))
	return typ != "" && typ != "text"
}

// isChatCompatibleModelType 判断 model_type 是否允许走 /v1/chat/completions
// 空字符串视为历史数据（默认兼容），仅拒绝明确声明的非 chat 类型
func isChatCompatibleModelType(t string) bool {
	tl := strings.ToLower(strings.TrimSpace(t))
	if tl == "" {
		return true
	}
	switch tl {
	case "llm", "chat", "vlm", "vision", "reasoning", "multimodal":
		return true
	}
	return false
}

// endpointHintForModelType 根据模型类型给出正确端点的提示信息
func endpointHintForModelType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "imagegeneration", "image":
		return "/v1/images/generations"
	case "videogeneration", "video":
		return "/v1/videos/generations"
	case "tts", "speech":
		return "/v1/audio/speech"
	case "asr", "stt":
		return "/v1/audio/transcriptions"
	case "embedding", "embeddings":
		return "/v1/embeddings"
	case "rerank", "reranker":
		return "/v1/rerank"
	case "translation":
		return "/v1/translations"
	case "moderation":
		return "/v1/moderations"
	}
	return "the matching dedicated endpoint"
}

// resolveThinkingOutputSellRMB 按优先级计算思考模式输出的实际"平台售价"（元/百万 token）
//  1. 命中阶梯 + tier.SellingOutputThinkingPrice（管理员显式覆盖，最高优先级）
//  2. 命中阶梯 + tier.OutputPriceThinking（阶梯成本价）× ratio（= 当前阶梯售价/阶梯成本）
//  3. ModelPricing.OutputPriceThinkingRMB（模型级平台售价）
//  4. aiModel.OutputCostThinkingRMB × ratio（顶层成本 × 当前 output 售价/成本 比例）
//
// 返回 0 表示该请求不应用思考模式加价（沿用普通 output 售价）。
func resolveThinkingOutputSellRMB(
	db *gorm.DB,
	ctx context.Context,
	aiModel *model.AIModel,
	costResult *pricing.CostResult,
) float64 {
	if aiModel == nil || costResult == nil {
		return 0
	}

	// 当前 output 售价与成本
	normalSellRMB := costResult.PriceDetail.OutputPriceRMB
	normalCostRMB := aiModel.OutputCostRMB

	// 加载 ModelPricing（可能没有）
	var mp model.ModelPricing
	hasMP := false
	if err := db.WithContext(ctx).Where("model_id = ?", aiModel.ID).First(&mp).Error; err == nil {
		hasMP = true
	}

	// 1 & 2. 命中阶梯
	if costResult.MatchedTierIdx >= 0 {
		// 2a. 平台阶梯数据
		if hasMP && len(mp.PriceTiers) > 0 {
			var mpData model.PriceTiersData
			if json.Unmarshal(mp.PriceTiers, &mpData) == nil &&
				costResult.MatchedTierIdx < len(mpData.Tiers) {
				t := mpData.Tiers[costResult.MatchedTierIdx]
				// 1. 阶梯售价覆盖
				if t.SellingOutputThinkingPrice != nil && *t.SellingOutputThinkingPrice > 0 {
					return *t.SellingOutputThinkingPrice
				}
			}
		}
		// 2b. AIModel 阶梯成本数据
		if len(aiModel.PriceTiers) > 0 {
			var amData model.PriceTiersData
			if json.Unmarshal(aiModel.PriceTiers, &amData) == nil &&
				costResult.MatchedTierIdx < len(amData.Tiers) {
				t := amData.Tiers[costResult.MatchedTierIdx]
				if t.OutputPriceThinking > 0 && t.OutputPrice > 0 && normalSellRMB > 0 {
					// 按当前阶梯 (售价/成本) 比例把成本缩放到售价
					tierRatio := normalSellRMB / t.OutputPrice
					return t.OutputPriceThinking * tierRatio
				}
			}
		}
	}

	// 3. 模型级 MP 思考售价
	if hasMP && mp.OutputPriceThinkingRMB > 0 {
		return mp.OutputPriceThinkingRMB
	}

	// 4. 顶层成本 × ratio
	if aiModel.OutputCostThinkingRMB > 0 && normalCostRMB > 0 && normalSellRMB > 0 {
		ratio := normalSellRMB / normalCostRMB
		return aiModel.OutputCostThinkingRMB * ratio
	}

	return 0
}

func (h *CompletionsHandler) applyBillingOutcomeToContext(c *gin.Context, out *billingsvc.UsageOutcome) {
	if c == nil || out == nil || out.CostResult == nil {
		return
	}
	c.Set("estimated_cost_credits", out.CostCredits)
	c.Set("estimated_cost_units", out.CostUnits)
	c.Set("cost_units", out.CostUnits)
	c.Set("platform_cost_rmb", out.PlatformCostRMB)
	c.Set("platform_cost_units", out.PlatformCostUnits)
	c.Set("usage_source", out.UsageSource)
	c.Set("usage_estimated", out.UsageEstimated)
	c.Set("matched_price_tier", out.CostResult.MatchedTier)
	c.Set("matched_price_tier_idx", out.CostResult.MatchedTierIdx)
	c.Set("billing_status", out.BillingStatus)
	c.Set("actual_cost_credits", out.ActualCostCredits)
	c.Set("actual_cost_units", out.ActualCostUnits)
	c.Set("under_collected_credits", out.UnderCollectedCredits)
	c.Set("under_collected_units", out.UnderCollectedUnits)
	if out.ThinkingModeApplied {
		c.Set("thinking_mode_applied", true)
	}
	if out.ThinkingOutputPriceRMB > 0 {
		c.Set("thinking_output_price_rmb", out.ThinkingOutputPriceRMB)
	}
	if out.CostResult.UserDiscountID != nil {
		c.Set("user_discount_id", *out.CostResult.UserDiscountID)
	}
	if out.CostResult.UserDiscountRate != nil {
		c.Set("user_discount_rate", *out.CostResult.UserDiscountRate)
	}
	if out.CostResult.UserDiscountType != "" {
		c.Set("user_discount_type", out.CostResult.UserDiscountType)
	}
	if out.SnapshotJSON != "" {
		c.Set("billing_snapshot_json", out.SnapshotJSON)
	}
}

func (h *CompletionsHandler) billingFreezeID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if v, ok := c.Get("billing_freeze_id"); ok {
		if s, ok2 := v.(string); ok2 {
			return s
		}
	}
	return ""
}

func (h *CompletionsHandler) releaseBillingFreeze(c *gin.Context, freezeID string) {
	if h == nil || h.billingSvc == nil || freezeID == "" {
		return
	}
	if err := h.billingSvc.ReleaseFrozen(c.Request.Context(), freezeID); err != nil {
		h.logger.Warn("release billing freeze failed", zap.String("freeze_id", freezeID), zap.Error(err))
	}
}

func estimateChatRequestUsage(req *chatCompletionRequest) provider.Usage {
	if req == nil {
		return provider.Usage{}
	}
	promptTokens := 0
	for _, msg := range req.Messages {
		promptTokens += estimateTextTokens(msg.Role)
		promptTokens += estimateTextTokens(provider.TextContent(msg.Content))
	}
	outputTokens := req.MaxTokens
	if outputTokens <= 0 {
		outputTokens = 1024
	}
	total := promptTokens + outputTokens
	return provider.Usage{
		PromptTokens:     promptTokens,
		CompletionTokens: outputTokens,
		TotalTokens:      total,
	}
}

func estimateFIMRequestUsage(req *fimCompletionRequest) provider.Usage {
	if req == nil {
		return provider.Usage{}
	}
	promptTokens := estimateTextTokens(req.Prompt) + estimateTextTokens(req.Suffix)
	outputTokens := req.MaxTokens
	if outputTokens <= 0 {
		outputTokens = 512
	}
	return provider.Usage{
		PromptTokens:     promptTokens,
		CompletionTokens: outputTokens,
		TotalTokens:      promptTokens + outputTokens,
	}
}

func estimateTextTokens(text string) int {
	runes := len([]rune(text))
	if runes == 0 {
		return 0
	}
	tokens := (runes + 3) / 4
	if tokens < 1 {
		return 1
	}
	return tokens
}
