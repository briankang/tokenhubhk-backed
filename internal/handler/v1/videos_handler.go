// Package v1 视频生成端点处理器
//
// POST /v1/videos/generations — 视频生成任务（内部异步提交 + 轮询至完成）
// 复用 ChannelRouter / 能力断言 / Failover 模式，与 images_handler 结构一致。
package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
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
	billingsvc "tokenhub-server/internal/service/billing"
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
	billingSvc    *billingsvc.Service
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
		billingSvc:    billingsvc.NewService(db, pricingCalc, balSvc),
		paramSvc:      paramSvc,
		pricingCalc:   pricingCalc,
		logger:        logger.L,
	}
}

// Register 注册路由
func (h *VideosHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/videos/generations", h.GenerateVideos)
	rg.POST("/videos/uploads", h.UploadVideo)
	rg.GET("/videos/tasks/:task_id", h.GetVideoTask)
}

// videoGenerationRequest 视频生成请求
type videoGenerationRequest struct {
	Model          string `json:"model" binding:"required"`
	Prompt         string `json:"prompt" binding:"required"`
	ImageURL       string `json:"image_url,omitempty"`    // 图生视频
	VideoURL       string `json:"video_url,omitempty"`    // 视频输入
	Duration       int    `json:"duration,omitempty"`     // 秒数
	Resolution     string `json:"resolution,omitempty"`   // 720P/1080P
	AspectRatio    string `json:"aspect_ratio,omitempty"` // 16:9
	FPS            int    `json:"fps,omitempty"`
	GenerateAudio  *bool  `json:"generate_audio,omitempty"` // Seedance 1.5 Pro: true=有声, false=无声
	ServiceTier    string `json:"service_tier,omitempty"`   // default=在线推理, flex=离线推理
	Draft          *bool  `json:"draft,omitempty"`          // Seedance 1.5 Pro 样片模式
	NegativePrompt string `json:"negative_prompt,omitempty"`
	Seed           int64  `json:"seed,omitempty"`
}

// GenerateVideos 处理 POST /v1/videos/generations
func (h *VideosHandler) GenerateVideos(c *gin.Context) {
	// 1. 认证
	keyInfo, err := h.authenticateAPIKey(c)
	if err != nil || keyInfo == nil {
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
		"model": true, "prompt": true, "image_url": true, "video_url": true, "duration": true,
		"resolution": true, "aspect_ratio": true, "fps": true,
		"generate_audio": true, "service_tier": true, "draft": true,
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
	estimatedVideoCount := 1
	estimatedDurationSec := float64(req.Duration)
	if estimatedDurationSec <= 0 {
		estimatedDurationSec = 1
	}
	estimatedVideoTokens := int(estimatedDurationSec*1280*720*24/1024 + 0.999)
	if req.VideoURL != "" && isSeedanceVideoModel(req.Model) {
		if minTokens := seedanceInputVideoMinTokens(req.Resolution); estimatedVideoTokens < minTokens {
			estimatedVideoTokens = minTokens
		}
	}
	// S3 + S4 (2026-04-28): 使用业务维度透传到计费层（不再走 token-scale 黑魔法）
	// PriceTier.DimValues 阶梯（resolution × input_has_video × inference_mode × audio_mode）
	// 由 selectPriceForTokens 直接命中正确单价档；token 数量保持基础公式不变
	estimateDims := buildVideoDimensions(req)
	freezeID := ""
	if h.billingSvc != nil {
		freeze, fErr := h.billingSvc.FreezeUnitUsage(c.Request.Context(), billingsvc.UnitUsageRequest{
			RequestID: requestID,
			UserID:    keyInfo.UserID,
			TenantID:  keyInfo.TenantID,
			ModelName: req.Model,
			Usage: pricing.UsageInput{
				ImageCount:   estimatedVideoCount,
				DurationSec:  estimatedDurationSec,
				OutputTokens: estimatedVideoTokens,
				Variant:      req.Resolution, // F3：wan2.7-t2v 等 per_second 路径仍用 Variant 兜底
				Dimensions:   estimateDims,
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
		result, err := h.channelRouter.SelectChannelForCapability(
			c.Request.Context(), req.Model, customChannelID, keyInfo.UserID, model.CapabilityVideo, excludeChannelIDs)
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
		extra = applyVideoRequestExtraParams(extra, &req)
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
			VideoURL:       req.VideoURL,
			Duration:       req.Duration,
			Resolution:     req.Resolution,
			AspectRatio:    req.AspectRatio,
			FPS:            req.FPS,
			GenerateAudio:  req.GenerateAudio,
			ServiceTier:    req.ServiceTier,
			Draft:          req.Draft,
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
		if totalDurationSec <= 0 && req.Duration > 0 {
			totalDurationSec = float64(req.Duration * videoCount)
		}
		actualVideoTokens := int(totalDurationSec*1280*720*24/1024 + 0.999)
		if req.VideoURL != "" && isSeedanceVideoModel(req.Model) {
			if minTokens := seedanceInputVideoMinTokens(req.Resolution); actualVideoTokens < minTokens {
				actualVideoTokens = minTokens
			}
		}
		// S3 + S4：实际扣费走 DimValues 阶梯，不再 scale tokens
		actualDims := buildVideoDimensions(req)
		costCredits, costRMB := h.calculateAndDeductCost(c, req.Model, keyInfo, pricing.UsageInput{
			ImageCount:   videoCount,
			DurationSec:  totalDurationSec,
			OutputTokens: actualVideoTokens,
			Variant:      req.Resolution,
			Dimensions:   actualDims,
		}, requestID, freezeID)
		h.recordApiCallLog(c, keyInfo, ch, requestID, req.Model, actualModel, c.ClientIP(), int(latency), 200, costCredits, costRMB, rawBody, videoCount, totalDurationSec)

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
			"status":     resp.Status,
			"request_id": requestID,
		})
		return
	}

	msg := "All upstream channels failed for video model: " + req.Model
	if lastErr != nil {
		msg = lastErr.Error()
	}
	c.Header("X-Request-ID", requestID)
	_ = releaseFrozenWithBillingService(c, h.billingSvc, freezeID)
	c.JSON(http.StatusBadGateway, gin.H{
		"error":      gin.H{"message": msg, "type": "server_error"},
		"request_id": requestID,
	})
}

func seedanceInputVideoMinTokens(resolution string) int {
	switch strings.ToLower(resolution) {
	case "480p", "480":
		return 55000
	case "1080p", "1080":
		return 265882
	default:
		return 118260
	}
}

func isSeedanceVideoModel(modelName string) bool {
	return strings.Contains(strings.ToLower(modelName), "seedance")
}

// seedanceBillingOptions, applySeedanceBillingTokenScale, seedanceBillingPriceScale (RIP)
//
// 这些函数已被 buildVideoDimensions（下方）替代。整个 token-scale 黑魔法在
// 2026-04-28 随 S4 数据迁移上线一起退役 —— PriceTier.DimValues 阶梯由
// pricing.selectPriceForTokens 直接命中正确单价档，无需缩放 token 数。
//
// 配套迁移：RunSeedanceDimValuesMigration 已把 DB 中所有 Seedance 模型的 PriceTiers
// 重写为 DimValues 形态。

// buildVideoDimensions 把 videoGenerationRequest 翻译成 PriceTier.DimValues 匹配用的 dim map
//
// 设计（S3 + S4，2026-04-28）：
//   - 取代 applySeedanceBillingTokenScale token-scale 黑魔法
//   - 维度键统一：resolution / input_has_video / inference_mode / audio_mode
//   - 不在此函数判断模型 family —— 计费层（selectPriceForTokens）按 PriceTier.DimValues
//     精确命中即可（多余维度不影响匹配）
//   - 所有维度都填，未来新模型若声明新维度（如 draft_mode/voice_kind）只需扩展此函数
func buildVideoDimensions(req videoGenerationRequest) map[string]string {
	dims := make(map[string]string, 4)

	// resolution 归一化（用户传 "1080P"/"1080p"/"1080" 都视为 "1080p"）
	res := strings.ToLower(strings.TrimSpace(req.Resolution))
	switch {
	case strings.Contains(res, "1080"):
		dims[model.DimKeyResolution] = "1080p"
	case strings.Contains(res, "720"):
		dims[model.DimKeyResolution] = "720p"
	case strings.Contains(res, "480"):
		dims[model.DimKeyResolution] = "480p"
	case res != "":
		dims[model.DimKeyResolution] = res
	}

	// input_has_video：boolean → "true"/"false"
	if req.VideoURL != "" {
		dims[model.DimKeyInputHasVideo] = "true"
	} else {
		dims[model.DimKeyInputHasVideo] = "false"
	}

	// inference_mode：service_tier=flex → offline，否则 online
	if strings.EqualFold(strings.TrimSpace(req.ServiceTier), "flex") {
		dims[model.DimKeyInferenceMode] = "offline"
	} else {
		dims[model.DimKeyInferenceMode] = "online"
	}

	// audio_mode：generate_audio=false → silent，true 或未指定 → audio
	if req.GenerateAudio != nil && !*req.GenerateAudio {
		dims[model.DimKeyAudioMode] = "false"
	} else {
		dims[model.DimKeyAudioMode] = "true"
	}

	// draft_mode：仅当用户显式传 true 时设为 true（兼容 1.5 Pro 480p Draft 路径）
	if req.Draft != nil && *req.Draft {
		dims[model.DimKeyDraftMode] = "true"
	} else {
		dims[model.DimKeyDraftMode] = "false"
	}

	return dims
}

func applyVideoRequestExtraParams(extra map[string]interface{}, req *videoGenerationRequest) map[string]interface{} {
	if req == nil {
		return extra
	}
	if extra == nil {
		extra = make(map[string]interface{})
	}
	if req.GenerateAudio != nil {
		extra["generate_audio"] = *req.GenerateAudio
	}
	if strings.TrimSpace(req.ServiceTier) != "" {
		extra["service_tier"] = strings.TrimSpace(req.ServiceTier)
	}
	if req.Draft != nil {
		extra["draft"] = *req.Draft
	}
	return extra
}


const maxVideoUploadSize = 100 * 1024 * 1024

var allowedVideoExt = map[string]bool{
	".mp4": true, ".mov": true, ".webm": true, ".m4v": true, ".mpeg": true, ".mpg": true, ".avi": true, ".flv": true,
}

var allowedVideoMIME = map[string]bool{
	"video/mp4": true, "video/quicktime": true, "video/webm": true, "video/mpeg": true, "video/x-msvideo": true, "video/x-flv": true, "application/octet-stream": true,
}

// UploadVideo 上传视频调试输入，返回可传给上游 video_url 的公开 URL。
func (h *VideosHandler) UploadVideo(c *gin.Context) {
	if keyInfo, err := h.authenticateAPIKey(c); err != nil || keyInfo == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"message": "Invalid API key", "type": "invalid_request_error", "code": "invalid_api_key"}})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxVideoUploadSize+1024)
	fileHeader, err := c.FormFile("video")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "missing video file or file exceeds 100MB", "type": "invalid_request_error"}})
		return
	}
	if fileHeader.Size <= 0 || fileHeader.Size > maxVideoUploadSize {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "video size must be between 1 byte and 100MB", "type": "invalid_request_error"}})
		return
	}
	ext := strings.ToLower(filepath.Ext(fileHeader.Filename))
	if !allowedVideoExt[ext] {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "only mp4/mov/webm/m4v/mpeg/avi/flv videos are supported", "type": "invalid_request_error"}})
		return
	}
	contentType := fileHeader.Header.Get("Content-Type")
	if contentType != "" && !allowedVideoMIME[contentType] {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "unsupported Content-Type: " + contentType, "type": "invalid_request_error"}})
		return
	}

	src, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": "open video failed", "type": "server_error"}})
		return
	}
	defer src.Close()
	data, err := io.ReadAll(src)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": "read video failed", "type": "server_error"}})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	if uploadURL, err := uploadVideoToCatbox(ctx, data, fileHeader.Filename); err == nil && uploadURL != "" {
		c.JSON(http.StatusOK, gin.H{"url": uploadURL, "provider": "catbox"})
		return
	}
	ctx2, cancel2 := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel2()
	if uploadURL, err := uploadVideoTo0x0(ctx2, data, fileHeader.Filename); err == nil && uploadURL != "" {
		c.JSON(http.StatusOK, gin.H{"url": uploadURL, "provider": "0x0.st"})
		return
	}
	c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": "video upload failed, please try again later", "type": "server_error"}})
}

func uploadVideoToCatbox(ctx context.Context, data []byte, filename string) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("reqtype", "fileupload"); err != nil {
		return "", err
	}
	fw, err := w.CreateFormFile("fileToUpload", filename)
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(data); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return postVideoUpload(ctx, "https://catbox.moe/user/api.php", w.FormDataContentType(), &buf)
}

func uploadVideoTo0x0(ctx context.Context, data []byte, filename string) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(data); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return postVideoUpload(ctx, "https://0x0.st", w.FormDataContentType(), &buf)
}

func postVideoUpload(ctx context.Context, endpoint, contentType string, body io.Reader) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "TokenHubHK-VideoUploader/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(raw))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("upload HTTP %d: %s", resp.StatusCode, text)
	}
	if !strings.HasPrefix(text, "https://") {
		return "", fmt.Errorf("upload returned non-URL response: %s", text)
	}
	if _, err := url.ParseRequestURI(text); err != nil {
		return "", err
	}
	return text, nil
}

// GetVideoTask handles GET /v1/videos/tasks/:task_id.
func (h *VideosHandler) GetVideoTask(c *gin.Context) {
	keyInfo, err := h.authenticateAPIKey(c)
	if err != nil || keyInfo == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{"message": "Invalid API key", "type": "invalid_request_error", "code": "invalid_api_key"},
		})
		return
	}

	taskID := c.Param("task_id")
	modelName := c.Query("model")
	if taskID == "" || modelName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "task_id and model are required", "type": "invalid_request_error"}})
		return
	}

	requestID := "vid-task-" + uuid.New().String()
	start := time.Now()
	result, err := h.channelRouter.SelectChannelForCapability(
		c.Request.Context(), modelName, keyInfo.CustomChannelID, keyInfo.UserID, model.CapabilityVideo, nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": "no channel for " + modelName + ": " + err.Error(), "type": "server_error"}})
		return
	}
	ch := result.Channel
	actualModel := result.ActualModel
	if actualModel == "" {
		actualModel = modelName
	}
	p := h.codingSvc.CreateProviderForChannel(ch)
	querier, ok := p.(provider.VideoTaskQuerier)
	if !ok {
		c.JSON(http.StatusNotImplemented, gin.H{"error": gin.H{"message": "provider does not support video task query", "type": "unsupported_operation"}})
		return
	}

	resp, queryErr := querier.QueryVideoTask(c.Request.Context(), taskID)
	latency := time.Since(start).Milliseconds()
	c.Header("X-Request-ID", requestID)
	c.Header("X-Actual-Model", actualModel)
	c.Header("X-Channel-ID", fmt.Sprintf("%d", ch.ID))
	c.Header("X-Upstream-Latency-Ms", fmt.Sprintf("%d", latency))
	if queryErr != nil {
		h.logger.Warn("v1 videos: query task not available yet", zap.String("task_id", taskID), zap.Error(queryErr))
		c.JSON(http.StatusAccepted, gin.H{
			"created":    time.Now().Unix(),
			"model":      modelName,
			"data":       []provider.VideoData{},
			"task_id":    taskID,
			"status":     "processing",
			"request_id": requestID,
			"message":    "task is still processing or the upstream query endpoint is not available yet",
		})
		return
	}
	if resp.Data == nil {
		resp.Data = []provider.VideoData{}
	}
	if resp.Status == "" {
		if len(resp.Data) > 0 {
			resp.Status = "succeeded"
		} else {
			resp.Status = "processing"
		}
	}
	if len(resp.Data) == 0 && resp.Status == "succeeded" {
		resp.Status = "processing"
	}
	if resp.TaskID == "" {
		resp.TaskID = taskID
	}
	if resp.Model == "" {
		resp.Model = modelName
	}
	c.JSON(http.StatusOK, gin.H{
		"created":    resp.Created,
		"model":      resp.Model,
		"data":       resp.Data,
		"task_id":    resp.TaskID,
		"status":     resp.Status,
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
			h.logger.Error("v1 videos 鎵ｈ垂澶辫触锛岄渶瑕佷汉宸ュ璐?",
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
			h.logger.Error("v1 videos 扣费失败，需要人工对账",
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

// recordApiCallLog 异步写入 /v1/videos/generations 的全链路日志
func (h *VideosHandler) recordApiCallLog(
	c *gin.Context, keyInfo *apikey.ApiKeyInfo, ch *model.Channel, requestID, requestModel, actualModel, clientIP string,
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
		SupplierName:      resolveChannelSupplierName(c.Request.Context(), h.db, ch),
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
	applyMatchedTierFromCtx(c, callLog)
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
