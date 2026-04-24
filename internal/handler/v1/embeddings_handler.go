// Package v1 提供 OpenAI 兼容的 /v1/ 路由处理器
//
// embeddings_handler 处理 POST /v1/embeddings：
//   - 通过 API Key 认证（与 completions/images/audio 一致）
//   - 使用 ChannelRouter 按模型选择渠道
//   - 透传到上游供应商的 /embeddings（OpenAI/Qwen/Doubao/DeepSeek/Zhipu 均兼容）
//   - 按 PricingUnit 计费（默认 per_million_tokens，使用上游返回的 usage.prompt_tokens；
//     若上游未返回 token 数，则按输入字符串 rune 长度估算并写入 CharCount 字段）
//   - 支持 Failover 重试（最多 3 次，排除已失败 channel）
package v1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/service/apikey"
	balancesvc "tokenhub-server/internal/service/balance"
	billingsvc "tokenhub-server/internal/service/billing"
	channelsvc "tokenhub-server/internal/service/channel"
	"tokenhub-server/internal/service/pricing"
)

// EmbeddingsHandler OpenAI 兼容的向量嵌入接口处理器
type EmbeddingsHandler struct {
	db            *gorm.DB
	channelRouter *channelsvc.ChannelRouter
	apiKeySvc     *apikey.ApiKeyService
	balanceSvc    *balancesvc.BalanceService
	billingSvc    *billingsvc.Service
	pricingCalc   *pricing.PricingCalculator
	httpClient    *http.Client
	logger        *zap.Logger
}

// NewEmbeddingsHandler 创建 EmbeddingsHandler 实例
func NewEmbeddingsHandler(
	db *gorm.DB,
	channelRouter *channelsvc.ChannelRouter,
	apiKeySvc *apikey.ApiKeyService,
	balSvc *balancesvc.BalanceService,
	pricingCalc *pricing.PricingCalculator,
) *EmbeddingsHandler {
	return &EmbeddingsHandler{
		db:            db,
		channelRouter: channelRouter,
		apiKeySvc:     apiKeySvc,
		balanceSvc:    balSvc,
		billingSvc:    billingsvc.NewService(db, pricingCalc, balSvc),
		pricingCalc:   pricingCalc,
		httpClient:    &http.Client{Timeout: 60 * time.Second},
		logger:        logger.L,
	}
}

// Register 注册路由到 /v1/ 路由组
func (h *EmbeddingsHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/embeddings", h.CreateEmbedding)
}

// embeddingRequest OpenAI 兼容的向量嵌入请求
type embeddingRequest struct {
	Model          string      `json:"model" binding:"required"`
	Input          interface{} `json:"input" binding:"required"` // string 或 []string
	EncodingFormat string      `json:"encoding_format,omitempty"`
	Dimensions     int         `json:"dimensions,omitempty"`
	User           string      `json:"user,omitempty"`
}

// embeddingUsage OpenAI 兼容的 usage 字段
type embeddingUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// embeddingResponse 仅用于解析 usage；其余字段原样透传给客户端
type embeddingResponse struct {
	Object string          `json:"object"`
	Data   json.RawMessage `json:"data"`
	Model  string          `json:"model"`
	Usage  embeddingUsage  `json:"usage"`
}

// CreateEmbedding 处理 POST /v1/embeddings
func (h *EmbeddingsHandler) CreateEmbedding(c *gin.Context) {
	// 1. 认证
	keyInfo, err := h.authenticateAPIKey(c)
	if err != nil || keyInfo == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"message": "Invalid API key", "type": "invalid_request_error", "code": "invalid_api_key"},
		})
		return
	}

	// 2. 解析请求体
	rawBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "read body failed", "type": "invalid_request_error"}})
		return
	}
	var req embeddingRequest
	if err := json.Unmarshal(rawBody, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "invalid request: " + err.Error(), "type": "invalid_request_error"}})
		return
	}
	if req.Model == "" || req.Input == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "model and input are required", "type": "invalid_request_error"}})
		return
	}

	requestID := "emb-" + uuid.New().String()
	if gid, ok := c.Get("X-Request-ID"); ok {
		if rid, _ := gid.(string); rid != "" {
			requestID = rid
		}
	}
	start := time.Now()

	// 3. 余额
	if h.balanceSvc != nil {
		if err := h.balanceSvc.CheckQuota(c.Request.Context(), keyInfo.UserID); err != nil {
			c.JSON(http.StatusPaymentRequired, gin.H{"error": gin.H{"message": "Insufficient balance", "type": "insufficient_quota"}})
			return
		}
	}

	// 统计输入字符数，用于 per_million_characters 计费或 token 数兜底
	charCount := countInputChars(req.Input)
	freezeID := ""
	if h.billingSvc != nil {
		freeze, fErr := h.billingSvc.FreezeUnitUsage(c.Request.Context(), billingsvc.UnitUsageRequest{
			RequestID: requestID,
			UserID:    keyInfo.UserID,
			TenantID:  keyInfo.TenantID,
			ModelName: req.Model,
			Usage: pricing.UsageInput{
				InputTokens: charCount,
				CharCount:   charCount,
			},
		})
		if fErr != nil {
			c.JSON(http.StatusPaymentRequired, gin.H{"error": gin.H{"message": "Insufficient balance", "type": "insufficient_quota"}})
			return
		}
		if freeze != nil {
			freezeID = freeze.FreezeID
			c.Set("estimated_cost_credits", freeze.EstimatedCostCredits)
			c.Set("estimated_cost_units", freeze.EstimatedCostUnits)
		}
	}

	// 4. Failover
	const maxRetries = 3
	customChannelID := keyInfo.CustomChannelID
	var excludeChannelIDs []uint
	var lastErr error
	var lastStatus int

	for attempt := 0; attempt < maxRetries; attempt++ {
		result, err := h.channelRouter.SelectChannelForCapability(
			c.Request.Context(), req.Model, customChannelID, keyInfo.UserID, model.CapabilityEmbedding, excludeChannelIDs)
		if err != nil {
			lastErr = fmt.Errorf("no channel for %s: %w", req.Model, err)
			break
		}
		ch := result.Channel
		actualModel := result.ActualModel
		if actualModel == "" {
			actualModel = req.Model
		}

		// 构造上游请求体（替换 model 为 actualModel）
		upstreamBody, marshalErr := rewriteModel(rawBody, actualModel)
		if marshalErr != nil {
			lastErr = marshalErr
			break
		}

		// 构造上游 URL
		upstreamURL := buildEmbeddingsURL(ch.Endpoint)
		if upstreamURL == "" {
			lastErr = fmt.Errorf("渠道 %s 未配置 endpoint", ch.Name)
			break
		}

		httpReq, reqErr := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, upstreamURL, bytes.NewReader(upstreamBody))
		if reqErr != nil {
			lastErr = reqErr
			break
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+ch.APIKey)

		resp, doErr := h.httpClient.Do(httpReq)
		latency := time.Since(start).Milliseconds()

		if doErr != nil {
			h.recordLog(ch.ID, actualModel, keyInfo, requestID, int(latency), 500, doErr.Error())
			h.channelRouter.RecordResult(ch.ID, false, int(latency), 500)
			excludeChannelIDs = append(excludeChannelIDs, ch.ID)
			lastErr = doErr
			h.logger.Warn("v1 embeddings: 上游调用失败",
				zap.Uint("channel_id", ch.ID), zap.Int("attempt", attempt+1), zap.Error(doErr))
			continue
		}

		respBody, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			excludeChannelIDs = append(excludeChannelIDs, ch.ID)
			continue
		}

		if resp.StatusCode >= 400 {
			h.recordLog(ch.ID, actualModel, keyInfo, requestID, int(latency), resp.StatusCode, string(respBody))
			h.channelRouter.RecordResult(ch.ID, false, int(latency), resp.StatusCode)
			excludeChannelIDs = append(excludeChannelIDs, ch.ID)
			lastErr = fmt.Errorf("upstream status %d: %s", resp.StatusCode, truncateStr(string(respBody), 200))
			lastStatus = resp.StatusCode
			h.logger.Warn("v1 embeddings: 上游返回错误",
				zap.Uint("channel_id", ch.ID), zap.Int("status", resp.StatusCode))
			continue
		}

		// 4.x 成功 — 解析 usage 并计费
		h.channelRouter.RecordResult(ch.ID, true, int(latency), 200)
		h.recordLog(ch.ID, actualModel, keyInfo, requestID, int(latency), 200, "")

		var parsed embeddingResponse
		_ = json.Unmarshal(respBody, &parsed)
		promptTokens := parsed.Usage.PromptTokens
		if promptTokens == 0 {
			// 上游未返回 token 数，按 rune 长度粗估（1 rune ≈ 1 token 的简单估算）
			promptTokens = charCount
		}

		usage := pricing.UsageInput{
			InputTokens: promptTokens,
			CharCount:   charCount,
		}
		costCredits, costRMB := h.calculateAndDeductCost(c, req.Model, keyInfo, usage, requestID, freezeID)
		h.recordApiCallLog(c, keyInfo, ch, requestID, req.Model, actualModel, c.ClientIP(),
			int(latency), 200, costCredits, costRMB, rawBody, promptTokens, charCount)

		h.logger.Info("v1 embeddings: 成功",
			zap.String("model", req.Model),
			zap.Uint("channel_id", ch.ID),
			zap.Int("prompt_tokens", promptTokens),
			zap.Int("char_count", charCount),
			zap.Int64("cost_credits", costCredits),
			zap.Int64("latency_ms", latency))

		c.Header("X-Request-ID", requestID)
		c.Header("X-Actual-Model", actualModel)
		c.Header("X-Channel-ID", fmt.Sprintf("%d", ch.ID))
		c.Header("X-Upstream-Latency-Ms", fmt.Sprintf("%d", latency))
		c.Data(http.StatusOK, "application/json", respBody)
		return
	}

	// 所有重试失败
	h.logger.Error("v1 embeddings: 所有重试均失败",
		zap.String("model", req.Model), zap.Int("attempts", maxRetries), zap.Error(lastErr))

	statusCode := http.StatusBadGateway
	if lastStatus >= 400 && lastStatus < 600 {
		statusCode = lastStatus
	}
	msg := "All upstream channels failed for embedding model: " + req.Model
	if lastErr != nil {
		msg = lastErr.Error()
	}
	c.Header("X-Request-ID", requestID)
	_ = releaseFrozenWithBillingService(c, h.billingSvc, freezeID)
	c.JSON(statusCode, gin.H{
		"error":      gin.H{"message": msg, "type": "server_error"},
		"request_id": requestID,
	})
}

// authenticateAPIKey 复用 API Key 认证逻辑
func (h *EmbeddingsHandler) authenticateAPIKey(c *gin.Context) (*apikey.ApiKeyInfo, error) {
	auth := c.GetHeader("Authorization")
	const bearer = "Bearer "
	if len(auth) < len(bearer) || auth[:len(bearer)] != bearer {
		return nil, fmt.Errorf("invalid authorization")
	}
	return h.apiKeySvc.Verify(c.Request.Context(), auth[len(bearer):])
}

// recordLog 异步写入渠道日志
func (h *EmbeddingsHandler) recordLog(channelID uint, modelName string, keyInfo *apikey.ApiKeyInfo,
	requestID string, latencyMs, statusCode int, errMsg string,
) {
	go func() {
		log := &model.ChannelLog{
			ChannelID:    channelID,
			ModelName:    modelName,
			TenantID:     keyInfo.TenantID,
			UserID:       keyInfo.UserID,
			ApiKeyID:     keyInfo.KeyID,
			LatencyMs:    latencyMs,
			StatusCode:   statusCode,
			ErrorMessage: errMsg,
			RequestID:    requestID,
		}
		if err := h.db.Create(log).Error; err != nil {
			h.logger.Error("v1 embeddings: 记录日志失败", zap.Error(err))
		}
	}()
}

// calculateAndDeductCost 计费并扣费（支持 per_million_tokens 与 per_million_characters）
func (h *EmbeddingsHandler) calculateAndDeductCost(
	c *gin.Context, modelName string, keyInfo *apikey.ApiKeyInfo, usage pricing.UsageInput, requestID string, freezeID string,
) (int64, float64) {
	if h.billingSvc != nil {
		out, err := h.billingSvc.SettleUnitUsage(c.Request.Context(), billingsvc.UnitUsageRequest{
			RequestID: requestID,
			UserID:    keyInfo.UserID,
			TenantID:  keyInfo.TenantID,
			ModelName: modelName,
			Usage:     usage,
			FreezeID:  freezeID,
		})
		if out == nil {
			return 0, 0
		}
		applyUnitBillingOutcomeToContext(c, out)
		if err != nil {
			h.logger.Error("v1 embeddings 鎵ｈ垂澶辫触锛岄渶瑕佷汉宸ュ璐?",
				zap.Error(err),
				zap.String("request_id", requestID),
				zap.Uint("user_id", keyInfo.UserID),
				zap.String("model", modelName),
				zap.Int64("cost_credits", out.CostCredits))
		}
		return out.CostCredits, out.CostRMB
	}
	if h.pricingCalc == nil {
		return 0, 0
	}
	ctx := c.Request.Context()
	var aiModel model.AIModel
	if err := h.db.WithContext(ctx).Where("model_name = ? AND is_active = true", modelName).First(&aiModel).Error; err != nil {
		return 0, 0
	}
	costResult, err := h.pricingCalc.CalculateCostByUnit(ctx, keyInfo.UserID, aiModel.ID, keyInfo.TenantID, 0, usage)
	if err != nil || costResult == nil {
		return 0, 0
	}
	if h.balanceSvc != nil && costResult.TotalCost > 0 {
		if dErr := h.balanceSvc.DeductForRequest(ctx, keyInfo.UserID, keyInfo.TenantID, costResult.TotalCost, modelName, requestID); dErr != nil {
			h.logger.Error("v1 embeddings 扣费失败，需要人工对账",
				zap.Error(dErr),
				zap.String("request_id", requestID),
				zap.Uint("user_id", keyInfo.UserID),
				zap.String("model", modelName),
				zap.Int64("cost_credits", costResult.TotalCost))
		}
	}
	return costResult.TotalCost, costResult.TotalCostRMB
}

// recordApiCallLog 异步写入 /v1/embeddings 的全链路日志
func (h *EmbeddingsHandler) recordApiCallLog(
	c *gin.Context, keyInfo *apikey.ApiKeyInfo, ch *model.Channel, requestID, requestModel, actualModel, clientIP string,
	latencyMs, statusCode int, costCredits int64, costRMB float64, rawBody []byte, promptTokens, charCount int,
) {
	callLog := &model.ApiCallLog{
		RequestID:         requestID,
		UserID:            keyInfo.UserID,
		TenantID:          keyInfo.TenantID,
		ApiKeyID:          keyInfo.KeyID,
		ClientIP:          clientIP,
		Endpoint:          "/v1/embeddings",
		RequestModel:      requestModel,
		ActualModel:       actualModel,
		ChannelID:         ch.ID,
		ChannelName:       ch.Name,
		SupplierName:      ch.Supplier.Name,
		StatusCode:        statusCode,
		TotalLatencyMs:    latencyMs,
		UpstreamLatencyMs: latencyMs,
		CostCredits:       costCredits,
		CostRMB:           costRMB,
		PromptTokens:      promptTokens,
		CharCount:         charCount,
		Status:            "success",
	}
	if statusCode >= 400 {
		callLog.Status = "error"
	}
	if rawBody != nil && len(rawBody) > 0 {
		callLog.RequestBody = string(rawBody)
	}
	applyMatchedTierFromCtx(c, callLog)
	go func() {
		if err := h.db.Create(callLog).Error; err != nil {
			h.logger.Error("v1 embeddings: 记录API调用日志失败", zap.Error(err))
		}
	}()
}

// countInputChars 统计 embedding 请求 input 字段的总 rune 长度
// Input 可以是 string 也可以是 []string，兼容 OpenAI 接口
func countInputChars(input interface{}) int {
	switch v := input.(type) {
	case string:
		return len([]rune(v))
	case []interface{}:
		total := 0
		for _, item := range v {
			if s, ok := item.(string); ok {
				total += len([]rune(s))
			}
		}
		return total
	case []string:
		total := 0
		for _, s := range v {
			total += len([]rune(s))
		}
		return total
	}
	return 0
}

// rewriteModel 替换请求体中的 model 字段为 actualModel（别名路由用）
func rewriteModel(rawBody []byte, actualModel string) ([]byte, error) {
	var m map[string]interface{}
	if err := json.Unmarshal(rawBody, &m); err != nil {
		return nil, err
	}
	m["model"] = actualModel
	return json.Marshal(m)
}

// buildEmbeddingsURL 根据渠道 endpoint 构造 /embeddings URL
// 兼容两种 endpoint 配置：
//   - 完整 base URL（如 https://api.openai.com/v1）→ 追加 /embeddings
//   - 已含 /embeddings 的完整 URL → 原样返回
func buildEmbeddingsURL(endpoint string) string {
	if endpoint == "" {
		return ""
	}
	endpoint = strings.TrimRight(endpoint, "/")
	if strings.HasSuffix(endpoint, "/embeddings") {
		return endpoint
	}
	return endpoint + "/embeddings"
}

// truncateStr 截断字符串，避免错误消息过长
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
