// Package v1 语音合成（TTS）与语音识别（ASR）端点处理器
//
// 两个端点：
//   - POST /v1/audio/speech          — 文本转语音，返回二进制音频
//   - POST /v1/audio/transcriptions  — 语音转文本，multipart/form-data 上传音频
//
// 与 images_handler / videos_handler 相同的 ChannelRouter + 能力断言 + Failover 模式。
package v1

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
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

// AudioHandler 语音端点处理器（TTS + ASR）
type AudioHandler struct {
	db            *gorm.DB
	codingSvc     *codingsvc.CodingService
	channelRouter *channelsvc.ChannelRouter
	apiKeySvc     *apikey.ApiKeyService
	balanceSvc    *balancesvc.BalanceService
	paramSvc      *parammapping.ParamMappingService
	pricingCalc   *pricing.PricingCalculator
	logger        *zap.Logger
}

// NewAudioHandler 创建 AudioHandler
func NewAudioHandler(
	db *gorm.DB,
	codingSvc *codingsvc.CodingService,
	channelRouter *channelsvc.ChannelRouter,
	apiKeySvc *apikey.ApiKeyService,
	balSvc *balancesvc.BalanceService,
	paramSvc *parammapping.ParamMappingService,
	pricingCalc *pricing.PricingCalculator,
) *AudioHandler {
	return &AudioHandler{
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
func (h *AudioHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/audio/speech", h.SynthesizeSpeech)
	rg.POST("/audio/transcriptions", h.TranscribeSpeech)
}

// ttsRequest TTS 请求体
type ttsRequest struct {
	Model          string  `json:"model" binding:"required"`
	Input          string  `json:"input" binding:"required"`
	Voice          string  `json:"voice,omitempty"`
	ResponseFormat string  `json:"response_format,omitempty"`
	Speed          float64 `json:"speed,omitempty"`
}

// SynthesizeSpeech 处理 POST /v1/audio/speech
func (h *AudioHandler) SynthesizeSpeech(c *gin.Context) {
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
	var req ttsRequest
	if err := json.Unmarshal(rawBody, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "invalid request: " + err.Error(), "type": "invalid_request_error"}})
		return
	}

	// 非标准字段透传为 extra
	var rawMap map[string]json.RawMessage
	_ = json.Unmarshal(rawBody, &rawMap)
	standardFields := map[string]bool{
		"model": true, "input": true, "voice": true,
		"response_format": true, "speed": true,
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

	requestID := "tts-" + uuid.New().String()
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

	// 4. Failover
	const maxRetries = 2
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

		if !ch.HasCapability(model.CapabilityTTS) {
			lastErr = fmt.Errorf("渠道 %s 未声明支持 tts 能力，请在管理后台配置", ch.Name)
			break
		}

		p := h.codingSvc.CreateProviderForChannel(ch)
		ss, ok := p.(provider.SpeechSynthesizer)
		if !ok {
			lastErr = fmt.Errorf("model %s does not support tts on this provider", req.Model)
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

		ttsReq := &provider.TTSRequest{
			Model:          actualModel,
			Input:          req.Input,
			Voice:          req.Voice,
			ResponseFormat: req.ResponseFormat,
			Speed:          req.Speed,
			Extra:          extra,
		}

		resp, genErr := ss.SynthesizeSpeech(c.Request.Context(), ttsReq)
		latency := time.Since(start).Milliseconds()

		if genErr != nil {
			h.recordLog(ch.ID, actualModel, keyInfo, requestID, int(latency), 500, genErr.Error())
			h.channelRouter.RecordResult(ch.ID, false, int(latency), 500)
			excludeChannelIDs = append(excludeChannelIDs, ch.ID)
			lastErr = genErr
			h.logger.Warn("v1 tts: 上游失败", zap.Uint("channel_id", ch.ID), zap.Error(genErr))
			continue
		}

		h.channelRouter.RecordResult(ch.ID, true, int(latency), 200)
		h.recordLog(ch.ID, actualModel, keyInfo, requestID, int(latency), 200, "")

		// 计费：TTS 按字符数计费，使用 rune 长度避免中文字符计数偏差
		charCount := len([]rune(req.Input))
		costCredits, costRMB := h.calculateAndDeductCost(c, req.Model, keyInfo, pricing.UsageInput{CharCount: charCount}, requestID)
		h.recordApiCallLog(keyInfo, ch, requestID, req.Model, actualModel, "/v1/audio/speech", c.ClientIP(), int(latency), 200, costCredits, costRMB, rawBody, charCount, 0)

		h.logger.Info("v1 tts: 合成成功",
			zap.String("model", req.Model),
			zap.Uint("channel_id", ch.ID),
			zap.Int("char_count", charCount),
			zap.Int64("cost_credits", costCredits),
			zap.Int64("latency_ms", latency))

		contentType := resp.ContentType
		if contentType == "" {
			contentType = "audio/mpeg"
		}
		c.Header("X-Request-ID", requestID)
		c.Header("X-Actual-Model", actualModel)
		c.Header("X-Channel-ID", fmt.Sprintf("%d", ch.ID))
		c.Header("X-Upstream-Latency-Ms", fmt.Sprintf("%d", latency))
		c.Header("Content-Type", contentType)
		c.Header("Content-Length", strconv.Itoa(len(resp.Audio)))
		c.Status(http.StatusOK)
		_, _ = c.Writer.Write(resp.Audio)
		return
	}

	msg := "All upstream channels failed for tts model: " + req.Model
	if lastErr != nil {
		msg = lastErr.Error()
	}
	c.Header("X-Request-ID", requestID)
	c.JSON(http.StatusBadGateway, gin.H{
		"error":      gin.H{"message": msg, "type": "server_error"},
		"request_id": requestID,
	})
}

// TranscribeSpeech 处理 POST /v1/audio/transcriptions (multipart/form-data)
func (h *AudioHandler) TranscribeSpeech(c *gin.Context) {
	// 1. 认证
	keyInfo, err := h.authenticateAPIKey(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"message": "Invalid API key", "type": "invalid_request_error", "code": "invalid_api_key"},
		})
		return
	}

	// 2. 解析 multipart
	if err := c.Request.ParseMultipartForm(64 << 20); err != nil { // 64 MB
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "parse multipart failed: " + err.Error(), "type": "invalid_request_error"}})
		return
	}
	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "file field required", "type": "invalid_request_error"}})
		return
	}
	file, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "open file failed", "type": "invalid_request_error"}})
		return
	}
	defer file.Close()
	audioBytes, err := io.ReadAll(file)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "read file failed", "type": "invalid_request_error"}})
		return
	}

	modelName := c.PostForm("model")
	if modelName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "model required", "type": "invalid_request_error"}})
		return
	}
	language := c.PostForm("language")
	prompt := c.PostForm("prompt")
	responseFormat := c.PostForm("response_format")
	temperature := 0.0
	if v := c.PostForm("temperature"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			temperature = f
		}
	}

	requestID := "asr-" + uuid.New().String()
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

	// 4. Failover
	const maxRetries = 2
	customChannelID := keyInfo.CustomChannelID
	var excludeChannelIDs []uint
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		result, err := h.channelRouter.SelectChannelWithExcludes(
			c.Request.Context(), modelName, customChannelID, keyInfo.UserID, excludeChannelIDs)
		if err != nil {
			lastErr = fmt.Errorf("no channel for %s: %w", modelName, err)
			break
		}
		ch := result.Channel
		actualModel := result.ActualModel
		if actualModel == "" {
			actualModel = modelName
		}

		if !ch.HasCapability(model.CapabilityASR) {
			lastErr = fmt.Errorf("渠道 %s 未声明支持 asr 能力，请在管理后台配置", ch.Name)
			break
		}

		p := h.codingSvc.CreateProviderForChannel(ch)
		st, ok := p.(provider.SpeechTranscriber)
		if !ok {
			lastErr = fmt.Errorf("model %s does not support asr on this provider", modelName)
			break
		}

		extra := h.mergeExtraParams(modelName, ch)
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

		asrReq := &provider.ASRRequest{
			Model:          actualModel,
			Audio:          audioBytes,
			Filename:       fileHeader.Filename,
			Language:       language,
			Prompt:         prompt,
			ResponseFormat: responseFormat,
			Temperature:    temperature,
			Extra:          extra,
		}

		resp, genErr := st.TranscribeSpeech(c.Request.Context(), asrReq)
		latency := time.Since(start).Milliseconds()

		if genErr != nil {
			h.recordLog(ch.ID, actualModel, keyInfo, requestID, int(latency), 500, genErr.Error())
			h.channelRouter.RecordResult(ch.ID, false, int(latency), 500)
			excludeChannelIDs = append(excludeChannelIDs, ch.ID)
			lastErr = genErr
			h.logger.Warn("v1 asr: 上游失败", zap.Uint("channel_id", ch.ID), zap.Error(genErr))
			continue
		}

		h.channelRouter.RecordResult(ch.ID, true, int(latency), 200)
		h.recordLog(ch.ID, actualModel, keyInfo, requestID, int(latency), 200, "")

		// 计费：ASR 按音频时长（秒）计费；若上游未返回 duration，按文件大小（128kbps 粗估）兜底避免漏扣
		durationSec := resp.Duration
		if durationSec <= 0 {
			// 128 kbps = 16 KB/s；估算时长 = bytes / 16000，保守偏少不会超扣
			durationSec = float64(len(audioBytes)) / 16000.0
			h.logger.Warn("v1 asr: 上游未返回 duration，按文件大小估算",
				zap.String("request_id", requestID),
				zap.Int("bytes", len(audioBytes)),
				zap.Float64("estimated_sec", durationSec))
		}
		costCredits, costRMB := h.calculateAndDeductCost(c, modelName, keyInfo, pricing.UsageInput{DurationSec: durationSec}, requestID)
		asrSummary := []byte(fmt.Sprintf(`{"model":"%s","bytes":%d,"language":"%s"}`, modelName, len(audioBytes), language))
		h.recordApiCallLog(keyInfo, ch, requestID, modelName, actualModel, "/v1/audio/transcriptions", c.ClientIP(), int(latency), 200, costCredits, costRMB, asrSummary, 0, durationSec)

		h.logger.Info("v1 asr: 识别成功",
			zap.String("model", modelName),
			zap.Uint("channel_id", ch.ID),
			zap.Float64("duration_sec", durationSec),
			zap.Int64("cost_credits", costCredits),
			zap.Int64("latency_ms", latency))

		c.Header("X-Request-ID", requestID)
		c.Header("X-Actual-Model", actualModel)
		c.Header("X-Channel-ID", fmt.Sprintf("%d", ch.ID))
		c.Header("X-Upstream-Latency-Ms", fmt.Sprintf("%d", latency))
		c.JSON(http.StatusOK, gin.H{
			"text":       resp.Text,
			"language":   resp.Language,
			"duration":   resp.Duration,
			"segments":   resp.Segments,
			"request_id": requestID,
		})
		return
	}

	msg := "All upstream channels failed for asr model: " + modelName
	if lastErr != nil {
		msg = lastErr.Error()
	}
	c.Header("X-Request-ID", requestID)
	c.JSON(http.StatusBadGateway, gin.H{
		"error":      gin.H{"message": msg, "type": "server_error"},
		"request_id": requestID,
	})
}

func (h *AudioHandler) authenticateAPIKey(c *gin.Context) (*apikey.ApiKeyInfo, error) {
	auth := c.GetHeader("Authorization")
	const bearer = "Bearer "
	if len(auth) < len(bearer) || auth[:len(bearer)] != bearer {
		return nil, fmt.Errorf("invalid authorization")
	}
	return h.apiKeySvc.Verify(c.Request.Context(), auth[len(bearer):])
}

func (h *AudioHandler) recordLog(channelID uint, modelName string, keyInfo *apikey.ApiKeyInfo,
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
			h.logger.Error("v1 audio: 记录日志失败", zap.Error(err))
		}
	}()
}

// calculateAndDeductCost 按计费单位计算费用并扣减余额
func (h *AudioHandler) calculateAndDeductCost(
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
			h.logger.Error("v1 audio 扣费失败，需要人工对账",
				zap.Error(dErr),
				zap.String("request_id", requestID),
				zap.Uint("user_id", keyInfo.UserID),
				zap.String("model", modelName),
				zap.Int64("cost_credits", costResult.TotalCost))
		}
	}
	return costResult.TotalCost, costResult.TotalCostRMB
}

// recordApiCallLog 异步写入 /v1/audio/* 的全链路日志
func (h *AudioHandler) recordApiCallLog(
	keyInfo *apikey.ApiKeyInfo, ch *model.Channel, requestID, requestModel, actualModel, endpoint, clientIP string,
	latencyMs, statusCode int, costCredits int64, costRMB float64, rawBody []byte, charCount int, durationSec float64,
) {
	callLog := &model.ApiCallLog{
		RequestID:         requestID,
		UserID:            keyInfo.UserID,
		TenantID:          keyInfo.TenantID,
		ApiKeyID:          keyInfo.KeyID,
		ClientIP:          clientIP,
		Endpoint:          endpoint,
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
		CharCount:         charCount,
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
			h.logger.Error("v1 audio: 记录API调用日志失败", zap.Error(err))
		}
	}()
}

func (h *AudioHandler) mergeExtraParams(modelName string, ch *model.Channel) map[string]interface{} {
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
