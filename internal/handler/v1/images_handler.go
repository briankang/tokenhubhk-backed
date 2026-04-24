// Package v1 图像生成端点处理器
//
// 提供 OpenAI 兼容的 POST /v1/images/generations 接口：
//   - 通过 API Key 认证（复用现有 OpenAPIAuth 中间件）
//   - 使用 ChannelRouter 做渠道选择（支持自动路由、别名映射）
//   - 使用类型断言 `p.(provider.ImageGenerator)` 检查能力
//   - 支持 Failover 重试（最多 3 次，排除已失败的 channel_id）
//   - 记录 channel_logs / api_call_logs 供监控
//
// 未来添加 /v1/videos/generations 可完全复用此模式（改用 VideoGenerator 接口）。
package v1

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	billingsvc "tokenhub-server/internal/service/billing"
	channelsvc "tokenhub-server/internal/service/channel"
	codingsvc "tokenhub-server/internal/service/coding"
	"tokenhub-server/internal/service/parammapping"
	"tokenhub-server/internal/service/pricing"
)

// ImagesHandler 图像生成处理器
type ImagesHandler struct {
	db            *gorm.DB
	codingSvc     *codingsvc.CodingService
	channelRouter *channelsvc.ChannelRouter
	apiKeySvc     *apikey.ApiKeyService
	balanceSvc    *balancesvc.BalanceService
	billingSvc    *billingsvc.Service
	paramSvc      *parammapping.ParamMappingService
	pricingCalc   *pricing.PricingCalculator
	logger        *zap.Logger
}

// NewImagesHandler 创建 ImagesHandler 实例
func NewImagesHandler(
	db *gorm.DB,
	codingSvc *codingsvc.CodingService,
	channelRouter *channelsvc.ChannelRouter,
	apiKeySvc *apikey.ApiKeyService,
	balSvc *balancesvc.BalanceService,
	paramSvc *parammapping.ParamMappingService,
	pricingCalc *pricing.PricingCalculator,
) *ImagesHandler {
	return &ImagesHandler{
		db:            db,
		codingSvc:     codingSvc,
		channelRouter: channelRouter,
		apiKeySvc:     apiKeySvc,
		balanceSvc:    balSvc,
		billingSvc:    billingsvc.NewService(db, pricingCalc, balSvc),
		paramSvc:      paramSvc,
		pricingCalc:   pricingCalc,
		logger:        logger.L,
	}
}

// Register 注册路由到 /v1/ 路由组
func (h *ImagesHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/images/generations", h.GenerateImages)
}

// imageGenerationRequest 图像生成请求（OpenAI 兼容）
type imageGenerationRequest struct {
	Model          string `json:"model" binding:"required"`
	Prompt         string `json:"prompt" binding:"required"`
	N              int    `json:"n,omitempty"`
	Size           string `json:"size,omitempty"`
	Quality        string `json:"quality,omitempty"`
	Style          string `json:"style,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"`
	NegativePrompt string `json:"negative_prompt,omitempty"`
	Seed           int64  `json:"seed,omitempty"`
}

// GenerateImages 处理 POST /v1/images/generations
func (h *ImagesHandler) GenerateImages(c *gin.Context) {
	// 1. 身份认证
	keyInfo, err := h.authenticateAPIKey(c)
	if err != nil || keyInfo == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{
				"message": "Invalid API key",
				"type":    "invalid_request_error",
				"code":    "invalid_api_key",
			},
		})
		return
	}

	// 2. 解析请求
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
	var req imageGenerationRequest
	if err := json.Unmarshal(rawBody, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"message": "Invalid request: " + err.Error(),
				"type":    "invalid_request_error",
			},
		})
		return
	}

	// 提取非标准字段作为 extra 参数透传
	var rawMap map[string]json.RawMessage
	_ = json.Unmarshal(rawBody, &rawMap)
	standardFields := map[string]bool{
		"model": true, "prompt": true, "n": true, "size": true,
		"quality": true, "style": true, "response_format": true,
		"negative_prompt": true, "seed": true, "user": true,
	}
	userExtraParams := make(map[string]interface{})
	for k, v := range rawMap {
		if !standardFields[k] {
			var val interface{}
			if json.Unmarshal(v, &val) == nil {
				userExtraParams[k] = val
			}
		}
	}

	requestID := "img-" + uuid.New().String()
	if globalReqID, exists := c.Get("X-Request-ID"); exists {
		if rid, ok := globalReqID.(string); ok && rid != "" {
			requestID = rid
		}
	}
	start := time.Now()
	estimatedImages := req.N
	if estimatedImages <= 0 {
		estimatedImages = 1
	}
	freezeID := ""
	if h.billingSvc != nil {
		freeze, fErr := h.billingSvc.FreezeUnitUsage(c.Request.Context(), billingsvc.UnitUsageRequest{
			RequestID: requestID,
			UserID:    keyInfo.UserID,
			TenantID:  keyInfo.TenantID,
			ModelName: req.Model,
			Usage:     pricing.UsageInput{ImageCount: estimatedImages},
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
		if freeze != nil {
			freezeID = freeze.FreezeID
			c.Set("estimated_cost_credits", freeze.EstimatedCostCredits)
			c.Set("estimated_cost_units", freeze.EstimatedCostUnits)
		}
	}

	// 3. 检查余额
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

	// 4. Failover 重试循环
	const maxRetries = 3
	customChannelID := keyInfo.CustomChannelID
	var excludeChannelIDs []uint
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		// 4.1 选择渠道
		result, err := h.channelRouter.SelectChannelForCapability(
			c.Request.Context(), req.Model, customChannelID, keyInfo.UserID, model.CapabilityImage, excludeChannelIDs)
		if err != nil {
			h.logger.Warn("v1 images: 渠道选择失败",
				zap.String("model", req.Model), zap.Int("attempt", attempt+1), zap.Error(err))
			lastErr = fmt.Errorf("no available channel for model %s: %w", req.Model, err)
			break
		}
		ch := result.Channel
		actualModel := result.ActualModel
		if actualModel == "" {
			actualModel = req.Model
		}

		// 4.2 创建 Provider
		p := h.codingSvc.CreateProviderForChannel(ch)

		// 4.3 能力断言
		ig, ok := p.(provider.ImageGenerator)
		if !ok {
			h.logger.Warn("v1 images: provider 不支持图像生成",
				zap.String("model", req.Model), zap.Uint("channel_id", ch.ID))
			// 能力不匹配不是渠道故障，无需重试其他渠道（同一模型映射到的 provider 相同）
			lastErr = fmt.Errorf("model %s does not support image generation on this provider", req.Model)
			break
		}

		// 4.4 合并自定义参数 + 参数映射
		extra := h.mergeExtraParams(req.Model, ch)
		for k, v := range userExtraParams {
			if _, exists := extra[k]; !exists {
				if extra == nil {
					extra = make(map[string]interface{})
				}
				extra[k] = v
			}
		}
		if h.paramSvc != nil && ch != nil && len(extra) > 0 {
			supplierCode := ch.Supplier.Code
			if supplierCode == "" {
				var supplier model.Supplier
				if h.db.Select("code").First(&supplier, ch.SupplierID).Error == nil {
					supplierCode = supplier.Code
				}
			}
			if supplierCode != "" {
				extra = h.paramSvc.TransformParamsWithContext(c.Request.Context(), supplierCode, extra)
			}
		}

		imgReq := &provider.ImageRequest{
			Model:          actualModel,
			Prompt:         req.Prompt,
			N:              req.N,
			Size:           req.Size,
			Quality:        req.Quality,
			Style:          req.Style,
			ResponseFormat: req.ResponseFormat,
			NegativePrompt: req.NegativePrompt,
			Seed:           req.Seed,
			Extra:          extra,
		}

		// 4.5 执行生成
		resp, genErr := ig.GenerateImage(c.Request.Context(), imgReq)
		latency := time.Since(start).Milliseconds()

		if genErr != nil {
			h.recordLog(ch.ID, actualModel, keyInfo, requestID, int(latency), 500, genErr.Error())
			h.channelRouter.RecordResult(ch.ID, false, int(latency), 500)
			excludeChannelIDs = append(excludeChannelIDs, ch.ID)
			lastErr = genErr
			h.logger.Warn("v1 images: 上游生成失败，尝试下一渠道",
				zap.Uint("channel_id", ch.ID), zap.Int("attempt", attempt+1), zap.Error(genErr))
			continue
		}

		// 4.6 成功 — 计费 + 扣费 + 全链路日志
		h.channelRouter.RecordResult(ch.ID, true, int(latency), 200)
		h.recordLog(ch.ID, actualModel, keyInfo, requestID, int(latency), 200, "")

		imageCount := len(resp.Data)
		costCredits, costRMB := h.calculateAndDeductCost(c, req.Model, keyInfo, pricing.UsageInput{ImageCount: imageCount}, requestID, freezeID)
		h.recordApiCallLog(c, keyInfo, ch, requestID, req.Model, actualModel, c.ClientIP(), int(latency), 200, costCredits, costRMB, rawBody, imageCount)

		h.logger.Info("v1 images: 图像生成成功",
			zap.String("model", req.Model),
			zap.Uint("channel_id", ch.ID),
			zap.Int("images", imageCount),
			zap.Int64("cost_credits", costCredits),
			zap.Int64("latency_ms", latency))

		c.Header("X-Request-ID", requestID)
		c.Header("X-Actual-Model", actualModel)
		c.Header("X-Channel-ID", fmt.Sprintf("%d", ch.ID))
		c.Header("X-Upstream-Latency-Ms", fmt.Sprintf("%d", latency))
		c.JSON(http.StatusOK, gin.H{
			"created":    resp.Created,
			"model":      req.Model,
			"data":       resp.Data,
			"request_id": requestID,
		})
		return
	}

	// 所有重试均失败
	h.logger.Error("v1 images: 所有重试均失败",
		zap.String("model", req.Model), zap.Int("attempts", maxRetries), zap.Error(lastErr))

	errMsg := "All upstream channels failed for image model: " + req.Model
	if lastErr != nil {
		errMsg = lastErr.Error()
	}
	c.Header("X-Request-ID", requestID)
	_ = releaseFrozenWithBillingService(c, h.billingSvc, freezeID)
	c.JSON(http.StatusBadGateway, gin.H{
		"error": gin.H{
			"message": errMsg,
			"type":    "server_error",
		},
		"request_id": requestID,
	})
}

// authenticateAPIKey 复用 completions_handler 的认证逻辑
func (h *ImagesHandler) authenticateAPIKey(c *gin.Context) (*apikey.ApiKeyInfo, error) {
	auth := c.GetHeader("Authorization")
	if auth == "" {
		return nil, fmt.Errorf("missing authorization header")
	}
	const bearerPrefix = "Bearer "
	if len(auth) < len(bearerPrefix) || auth[:len(bearerPrefix)] != bearerPrefix {
		return nil, fmt.Errorf("invalid authorization format")
	}
	key := auth[len(bearerPrefix):]
	if key == "" {
		return nil, fmt.Errorf("empty api key")
	}
	return h.apiKeySvc.Verify(c.Request.Context(), key)
}

// recordLog 异步记录渠道日志（图像请求无 token 统计，用 0 占位）
func (h *ImagesHandler) recordLog(
	channelID uint, modelName string, keyInfo *apikey.ApiKeyInfo,
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
			h.logger.Error("v1 images: 记录渠道日志失败", zap.Error(err))
		}
	}()
}

// calculateAndDeductCost 按计费单位计算费用并扣减余额（失败时记录 error 日志，返回已计算的 cost 供对账）
func (h *ImagesHandler) calculateAndDeductCost(
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
			h.logger.Error("v1 images 鎵ｈ垂澶辫触锛岄渶瑕佷汉宸ュ璐?",
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
	aiModel, billedModel := findActiveModelByRequestOrActual(ctx, h.db, modelName, modelName)
	if aiModel == nil {
		return 0, 0
	}
	costResult, err := h.pricingCalc.CalculateCostByUnit(ctx, keyInfo.UserID, aiModel.ID, keyInfo.TenantID, 0, usage)
	if err != nil || costResult == nil {
		return 0, 0
	}
	if h.balanceSvc != nil && costResult.TotalCost > 0 {
		if dErr := h.balanceSvc.DeductForRequest(ctx, keyInfo.UserID, keyInfo.TenantID, costResult.TotalCost, billedModel, requestID); dErr != nil {
			h.logger.Error("v1 images 扣费失败，需要人工对账",
				zap.Error(dErr),
				zap.String("request_id", requestID),
				zap.Uint("user_id", keyInfo.UserID),
				zap.String("model", billedModel),
				zap.Int64("cost_credits", costResult.TotalCost))
			setUnitBillingContext(c, requestID, billedModel, aiModel, costResult, usage, "deduct_failed", 0, costResult.TotalCost)
			return costResult.TotalCost, costResult.TotalCostRMB
		}
	}
	status := "no_charge"
	actualCredits := int64(0)
	if costResult.TotalCost > 0 {
		status = "settled"
		actualCredits = costResult.TotalCost
	}
	setUnitBillingContext(c, requestID, billedModel, aiModel, costResult, usage, status, actualCredits, 0)
	return costResult.TotalCost, costResult.TotalCostRMB
}

// recordApiCallLog 异步写入 /v1/images/generations 的全链路日志（供 /user/usage 聚合使用）
func (h *ImagesHandler) recordApiCallLog(
	c *gin.Context, keyInfo *apikey.ApiKeyInfo, ch *model.Channel, requestID, requestModel, actualModel, clientIP string,
	latencyMs, statusCode int, costCredits int64, costRMB float64, rawBody []byte, imageCount int,
) {
	callLog := &model.ApiCallLog{
		RequestID:         requestID,
		UserID:            keyInfo.UserID,
		TenantID:          keyInfo.TenantID,
		ApiKeyID:          keyInfo.KeyID,
		ClientIP:          clientIP,
		Endpoint:          "/v1/images/generations",
		RequestModel:      requestModel,
		ActualModel:       actualModel,
		ChannelID:         ch.ID,
		ChannelName:       ch.Name,
		SupplierName:      resolveChannelSupplierName(c.Request.Context(), h.db, ch),
		StatusCode:        statusCode,
		TotalLatencyMs:    latencyMs,
		UpstreamLatencyMs: latencyMs,
		CostCredits:       costCredits,
		CostRMB:           costRMB,
		ImageCount:        imageCount,
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
			h.logger.Error("v1 images: 记录API调用日志失败", zap.Error(err))
		}
	}()
}

// mergeExtraParams 合并模型级和渠道级的自定义参数（与 completions_handler 逻辑一致）
func (h *ImagesHandler) mergeExtraParams(modelName string, ch *model.Channel) map[string]interface{} {
	extra := make(map[string]interface{})

	// 1. 模型级 ExtraParams
	var aiModel model.AIModel
	if err := h.db.Where("model_name = ? AND is_active = ?", modelName, true).First(&aiModel).Error; err == nil {
		if len(aiModel.ExtraParams) > 0 {
			var modelParams map[string]interface{}
			if json.Unmarshal(aiModel.ExtraParams, &modelParams) == nil {
				for k, v := range modelParams {
					extra[k] = v
				}
			}
		}
	}

	// 2. 渠道级 CustomParams
	if ch != nil && len(ch.CustomParams) > 0 {
		var channelParams map[string]interface{}
		if json.Unmarshal(ch.CustomParams, &channelParams) == nil {
			if body, ok := channelParams["extra_body"].(map[string]interface{}); ok {
				for k, v := range body {
					extra[k] = v
				}
			}
			for k, v := range channelParams {
				if k != "headers" && k != "extra_body" {
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
