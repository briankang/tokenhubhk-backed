// Package v1 视频生成端点处理器
//
// POST /v1/videos/generations — 视频生成任务（内部异步提交 + 轮询至完成）
// 复用 ChannelRouter / 能力断言 / Failover 模式，与 images_handler 结构一致。
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
	channelsvc "tokenhub-server/internal/service/channel"
	codingsvc "tokenhub-server/internal/service/coding"
	"tokenhub-server/internal/service/parammapping"
	"tokenhub-server/internal/service/pricing"
)

// VideosHandler 视频生成处理器
type VideosHandler struct {
	db            *gorm.DB
	codingSvc     *codingsvc.CodingService
	channelRouter *channelsvc.ChannelRouter
	apiKeySvc     *apikey.ApiKeyService
	balanceSvc    *balancesvc.BalanceService
	paramSvc      *parammapping.ParamMappingService
	pricingCalc   *pricing.PricingCalculator
	logger        *zap.Logger
}

// NewVideosHandler 创建视频生成处理器
func NewVideosHandler(
	db *gorm.DB,
	codingSvc *codingsvc.CodingService,
	channelRouter *channelsvc.ChannelRouter,
	apiKeySvc *apikey.ApiKeyService,
	balSvc *balancesvc.BalanceService,
	paramSvc *parammapping.ParamMappingService,
	pricingCalc *pricing.PricingCalculator,
) *VideosHandler {
	return &VideosHandler{
		db:            db,
		codingSvc:     codingSvc,
		channelRouter: channelRouter,
		apiKeySvc:     apiKeySvc,
		balanceSvc:    balSvc,
		paramSvc:      paramSvc,
		pricingCalc:   pricingCalc,
		logger:        logger.L,
	}
}

// Register 注册路由
func (h *VideosHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/videos/generations", h.GenerateVideos)
}

// videoGenerationRequest 视频生成请求
type videoGenerationRequest struct {
	Model          string `json:"model" binding:"required"`
	Prompt         string `json:"prompt" binding:"required"`
	ImageURL       string `json:"image_url,omitempty"`    // 图生视频
	Duration       int    `json:"duration,omitempty"`     // 秒数
	Resolution     string `json:"resolution,omitempty"`   // 720P/1080P
	AspectRatio    string `json:"aspect_ratio,omitempty"` // 16:9
	FPS            int    `json:"fps,omitempty"`
	NegativePrompt string `json:"negative_prompt,omitempty"`
	Seed           int64  `json:"seed,omitempty"`
}

// GenerateVideos 处理 POST /v1/videos/generations
func (h *VideosHandler) GenerateVideos(c *gin.Context) {
	// 1. 认证
	keyInfo, err := h.authenticateAPIKey(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"message": "Invalid API key", "type": "invalid_request_error", "code": "invalid_api_key"},
		})
		return
	}

	// 2. 解析请求
	rawBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "read body failed", "type": "invalid_request_error"}})
		return
	}
	var req videoGenerationRequest
	if err := json.Unmarshal(rawBody, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "invalid request: " + err.Error(), "type": "invalid_request_error"}})
		return
	}

	// 非标准字段透传为 extra
	var rawMap map[string]json.RawMessage
	_ = json.Unmarshal(rawBody, &rawMap)
	standardFields := map[string]bool{
		"model": true, "prompt": true, "image_url": true, "duration": true,
		"resolution": true, "aspect_ratio": true, "fps": true,
		"negative_prompt": true, "seed": true,
	}
	userExtra := make(map[string]interface{})
	for k, v := range rawMap {
		if !standardFields[k] {
			var val interface{}
			if json.Unmarshal(v, &val) == nil {
				userExtra[k] = val
			}
		}
	}

	requestID := "vid-" + uuid.New().String()
	if gid, ok := c.Get("X-Request-ID"); ok {
		if rid, _ := gid.(string); rid != "" {
			requestID = rid
		}
	}
	start := time.Now()

	// 3. 余额检查
	if h.balanceSvc != nil {
		if err := h.balanceSvc.CheckQuota(c.Request.Context(), keyInfo.UserID); err != nil {
			c.JSON(http.StatusPaymentRequired, gin.H{"error": gin.H{"message": "Insufficient balance", "type": "insufficient_quota"}})
			return
		}
	}

	// 4. Failover
	const maxRetries = 2 // 视频生成耗时长，重试次数减半
	customChannelID := keyInfo.CustomChannelID
	var excludeChannelIDs []uint
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		result, err := h.channelRouter.SelectChannelWithExcludes(
			c.Request.Context(), req.Model, customChannelID, keyInfo.UserID, excludeChannelIDs)
		if err != nil {
			lastErr = fmt.Errorf("no channel for %s: %w", req.Model, err)
			break
		}
		ch := result.Channel
		actualModel := result.ActualModel
		if actualModel == "" {
			actualModel = req.Model
		}

		// 能力标记校验（提前失败提示管理员配置）
		if !ch.HasCapability(model.CapabilityVideo) {
			lastErr = fmt.Errorf("渠道 %s 未声明支持 video 能力，请在管理后台配置", ch.Name)
			break
		}

		p := h.codingSvc.CreateProviderForChannel(ch)
		vg, ok := p.(provider.VideoGenerator)
		if !ok {
			lastErr = fmt.Errorf("model %s does not support video generation on this provider", req.Model)
			break
		}

		extra := h.mergeExtraParams(req.Model, ch)
		for k, v := range userExtra {
			if extra == nil {
				extra = make(map[string]interface{})
			}
			if _, exists := extra[k]; !exists {
				extra[k] = v
			}
		}
		if h.paramSvc != nil && ch != nil && len(extra) > 0 {
			code := ch.Supplier.Code
			if code == "" {
				var supplier model.Supplier
				if h.db.Select("code").First(&supplier, ch.SupplierID).Error == nil {
					code = supplier.Code
				}
			}
			if code != "" {
				extra = h.paramSvc.TransformParamsWithContext(c.Request.Context(), code, extra)
			}
		}

		vidReq := &provider.VideoRequest{
			Model:          actualModel,
			Prompt:         req.Prompt,
			ImageURL:       req.ImageURL,
			Duration:       req.Duration,
			Resolution:     req.Resolution,
			AspectRatio:    req.AspectRatio,
			FPS:            req.FPS,
			NegativePrompt: req.NegativePrompt,
			Seed:           req.Seed,
			Extra:          extra,
		}

		resp, genErr := vg.GenerateVideo(c.Request.Context(), vidReq)
		latency := time.Since(start).Milliseconds()

		if genErr != nil {
			h.recordLog(ch.ID, actualModel, keyInfo, requestID, int(latency), 500, genErr.Error())
			h.channelRouter.RecordResult(ch.ID, false, int(latency), 500)
			excludeChannelIDs = append(excludeChannelIDs, ch.ID)
			lastErr = genErr
			h.logger.Warn("v1 videos: 上游生成失败", zap.Uint("channel_id", ch.ID), zap.Error(genErr))
			continue
		}

		h.channelRouter.RecordResult(ch.ID, true, int(latency), 200)
		h.recordLog(ch.ID, actualModel, keyInfo, requestID, int(latency), 200, "")

		// 计费：视频计费单位多样（per_image=按视频条数 / per_hour=按总时长 / per_million_tokens=按 token）
		// 同时传入多种用量，由 pricing_calculator 按模型 PricingUnit 字段选择
		videoCount := len(resp.Data)
		var totalDurationSec float64
		for _, v := range resp.Data {
			totalDurationSec += float64(v.DurationSec)
		}
		costCredits, costRMB := h.calculateAndDeductCost(c, req.Model, keyInfo, pricing.UsageInput{
			ImageCount:  videoCount,
			DurationSec: totalDurationSec,
		}, requestID)
		h.recordApiCallLog(keyInfo, ch, requestID, req.Model, actualModel, c.ClientIP(), int(latency), 200, costCredits, costRMB, rawBody, videoCount, totalDurationSec)

		h.logger.Info("v1 videos: 生成成功",
			zap.String("model", req.Model),
			zap.Uint("channel_id", ch.ID),
			zap.Int("videos", videoCount),
			zap.Float64("total_sec", totalDurationSec),
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
			"task_id":    resp.TaskID,
			"request_id": requestID,
		})
		return
	}

	msg := "All upstream channels failed for video model: " + req.Model
	if lastErr != nil {
		msg = lastErr.Error()
	}
	c.Header("X-Request-ID", requestID)
	c.JSON(http.StatusBadGateway, gin.H{
		"error":      gin.H{"message": msg, "type": "server_error"},
		"request_id": requestID,
	})
}

func (h *VideosHandler) authenticateAPIKey(c *gin.Context) (*apikey.ApiKeyInfo, error) {
	auth := c.GetHeader("Authorization")
	const bearer = "Bearer "
	if len(auth) < len(bearer) || auth[:len(bearer)] != bearer {
		return nil, fmt.Errorf("invalid authorization")
	}
	return h.apiKeySvc.Verify(c.Request.Context(), auth[len(bearer):])
}

func (h *VideosHandler) recordLog(channelID uint, modelName string, keyInfo *apikey.ApiKeyInfo,
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
			h.logger.Error("v1 videos: 记录日志失败", zap.Error(err))
		}
	}()
}

// calculateAndDeductCost 按计费单位计算费用并扣减余额
func (h *VideosHandler) calculateAndDeductCost(
	c *gin.Context, modelName string, keyInfo *apikey.ApiKeyInfo, usage pricing.UsageInput, requestID string,
) (int64, float64) {
	if h.pricingCalc == nil {
		return 0, 0
	}
	ctx := c.Request.Context()
	var aiModel model.AIModel
	if err := h.db.WithContext(ctx).Where("model_name = ? AND is_active = true", modelName).First(&aiModel).Error; err != nil {
		return 0, 0
	}
	costResult, err := h.pricingCalc.CalculateCostByUnit(ctx, aiModel.ID, keyInfo.TenantID, 0, usage)
	if err != nil || costResult == nil {
		return 0, 0
	}
	if h.balanceSvc != nil && costResult.TotalCost > 0 {
		if dErr := h.balanceSvc.DeductForRequest(ctx, keyInfo.UserID, keyInfo.TenantID, costResult.TotalCost, modelName, requestID); dErr != nil {
			h.logger.Error("v1 videos 扣费失败，需要人工对账",
				zap.Error(dErr),
				zap.String("request_id", requestID),
				zap.Uint("user_id", keyInfo.UserID),
				zap.String("model", modelName),
				zap.Int64("cost_credits", costResult.TotalCost))
		}
	}
	return costResult.TotalCost, costResult.TotalCostRMB
}

// recordApiCallLog 异步写入 /v1/videos/generations 的全链路日志
func (h *VideosHandler) recordApiCallLog(
	keyInfo *apikey.ApiKeyInfo, ch *model.Channel, requestID, requestModel, actualModel, clientIP string,
	latencyMs, statusCode int, costCredits int64, costRMB float64, rawBody []byte, videoCount int, durationSec float64,
) {
	callLog := &model.ApiCallLog{
		RequestID:         requestID,
		UserID:            keyInfo.UserID,
		TenantID:          keyInfo.TenantID,
		ApiKeyID:          keyInfo.KeyID,
		ClientIP:          clientIP,
		Endpoint:          "/v1/videos/generations",
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
		ImageCount:        videoCount,
		DurationSec:       durationSec,
		Status:            "success",
	}
	if statusCode >= 400 {
		callLog.Status = "error"
	}
	if rawBody != nil && len(rawBody) > 0 {
		callLog.RequestBody = string(rawBody)
	}
	go func() {
		if err := h.db.Create(callLog).Error; err != nil {
			h.logger.Error("v1 videos: 记录API调用日志失败", zap.Error(err))
		}
	}()
}

func (h *VideosHandler) mergeExtraParams(modelName string, ch *model.Channel) map[string]interface{} {
	extra := make(map[string]interface{})
	var aiModel model.AIModel
	if err := h.db.Where("model_name = ? AND is_active = ?", modelName, true).First(&aiModel).Error; err == nil {
		if len(aiModel.ExtraParams) > 0 {
			var mp map[string]interface{}
			if json.Unmarshal(aiModel.ExtraParams, &mp) == nil {
				for k, v := range mp {
					extra[k] = v
				}
			}
		}
	}
	if ch != nil && len(ch.CustomParams) > 0 {
		var cp map[string]interface{}
		if json.Unmarshal(ch.CustomParams, &cp) == nil {
			if body, ok := cp["extra_body"].(map[string]interface{}); ok {
				for k, v := range body {
					extra[k] = v
				}
			}
			for k, v := range cp {
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
