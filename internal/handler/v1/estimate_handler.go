package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/pricing"
)

type EstimateHandler struct {
	db         *gorm.DB
	calculator *pricing.PricingCalculator
}

type estimateCostRequest struct {
	Endpoint            string          `json:"endpoint,omitempty"`
	Request             json.RawMessage `json:"request,omitempty"`
	Model               string          `json:"model"`
	InputTokens         int             `json:"input_tokens,omitempty"`
	OutputTokens        int             `json:"output_tokens,omitempty"`
	MaxTokens           int             `json:"max_tokens,omitempty"`
	MaxCompletionTokens int             `json:"max_completion_tokens,omitempty"`
	CacheReadTokens     int             `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens    int             `json:"cache_write_tokens,omitempty"`
	CacheWrite1hTokens  int             `json:"cache_write_1h_tokens,omitempty"`
	ImageCount          int             `json:"image_count,omitempty"`
	CharCount           int             `json:"char_count,omitempty"`
	DurationSec         float64         `json:"duration_sec,omitempty"`
	InputVideoSeconds   float64         `json:"input_video_seconds,omitempty"`
	OutputSeconds       float64         `json:"output_seconds,omitempty"`
	CallCount           int             `json:"call_count,omitempty"`
	Resolution          string          `json:"resolution,omitempty"`
	GenerateAudio       *bool           `json:"generate_audio,omitempty"`
	ServiceTier         string          `json:"service_tier,omitempty"`
	InputContainsVideo  bool            `json:"input_contains_video,omitempty"`
}

func NewEstimateHandler(db *gorm.DB, calculator *pricing.PricingCalculator) *EstimateHandler {
	return &EstimateHandler{db: db, calculator: calculator}
}

func (h *EstimateHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/estimate/cost", h.EstimateCost)
}

func (h *EstimateHandler) EstimateCost(c *gin.Context) {
	var req estimateCostRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, err.Error())
		return
	}

	assumptions := make([]string, 0, 4)
	usageSource := "provided"
	tokensEstimated := false
	if len(bytes.TrimSpace(req.Request)) > 0 && string(bytes.TrimSpace(req.Request)) != "null" {
		rawAssumptions, estimated, err := req.applyOpenAIRequestBody()
		if err != nil {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, err.Error())
			return
		}
		assumptions = append(assumptions, rawAssumptions...)
		usageSource = "request_body"
		tokensEstimated = estimated
	}
	if strings.TrimSpace(req.Model) == "" {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, "model is required")
		return
	}

	outputFromCap := false
	if req.MaxTokens == 0 && req.MaxCompletionTokens > 0 {
		req.MaxTokens = req.MaxCompletionTokens
	}
	if req.OutputTokens == 0 && req.MaxTokens > 0 {
		req.OutputTokens = req.MaxTokens
		outputFromCap = true
		assumptions = append(assumptions, "output_tokens uses max_tokens/max_completion_tokens as an upper bound")
	}
	if req.DurationSec == 0 && req.OutputSeconds > 0 {
		req.DurationSec = req.OutputSeconds
	}

	var aiModel model.AIModel
	err := h.db.WithContext(c.Request.Context()).
		Where("model_name = ? OR display_name = ?", req.Model, req.Model).
		First(&aiModel).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.ErrorMsg(c, http.StatusNotFound, errcode.ErrNotFound.Code, "model not found")
			return
		}
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	if req.CallCount == 0 && aiModel.PricingUnit == model.UnitPerCall {
		req.CallCount = 1
		assumptions = append(assumptions, "per_call models default to one billable call")
	}

	userID, _ := c.Get("userId")
	tenantID, _ := c.Get("tenantId")
	uid, _ := userID.(uint)
	tid, _ := tenantID.(uint)

	// S3 + S4 (2026-04-28): 维度透传到计费层（不再 scale tokens）
	estimateDims := buildEstimateDimensions(req)
	usage := pricing.UsageInput{
		InputTokens:  req.InputTokens,
		OutputTokens: req.OutputTokens,
		ImageCount:   req.ImageCount,
		CharCount:    req.CharCount,
		DurationSec:  req.DurationSec + req.InputVideoSeconds,
		CallCount:    req.CallCount,
		Variant:      req.Resolution,
		Dimensions:   estimateDims,
	}
	result, err := h.calculateEstimate(c.Request.Context(), uid, tid, &aiModel, usage, req)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	expectedUsage := usage
	if outputFromCap && expectedUsage.OutputTokens > 1 {
		expectedUsage.OutputTokens = int(math.Ceil(float64(expectedUsage.OutputTokens) / 2.0))
	}
	expectedResult, _ := h.calculateEstimate(c.Request.Context(), uid, tid, &aiModel, expectedUsage, req)
	if expectedResult == nil {
		expectedResult = result
	}
	minUsage := usage
	if aiModel.PricingUnit == "" || aiModel.PricingUnit == model.UnitPerMillionTokens {
		minUsage.OutputTokens = 0
	}
	minResult, _ := h.calculateEstimate(c.Request.Context(), uid, tid, &aiModel, minUsage, req)
	if minResult == nil {
		minResult = result
	}
	confidence := estimateConfidence(usageSource, tokensEstimated, outputFromCap, req.MaxTokens)
	estimateType := "provided_usage"
	if outputFromCap {
		estimateType = "upper_bound"
	} else if tokensEstimated {
		estimateType = "best_effort"
	}
	if len(assumptions) == 0 {
		assumptions = append(assumptions, "estimate uses current TokenHub sale pricing and does not execute upstream providers")
	}

	response.Success(c, gin.H{
		"model":               aiModel.ModelName,
		"model_id":            aiModel.ID,
		"model_type":          aiModel.ModelType,
		"pricing_unit":        aiModel.PricingUnit,
		"input_tokens":        req.InputTokens,
		"output_tokens":       req.OutputTokens,
		"image_count":         req.ImageCount,
		"char_count":          req.CharCount,
		"duration_sec":        req.DurationSec,
		"input_video_seconds": req.InputVideoSeconds,
		"output_seconds":      req.OutputSeconds,
		"call_count":          req.CallCount,
		"estimated_credits":   result.TotalCost,
		"estimated_rmb":       result.TotalCostRMB,
		"platform_cost":       result.PlatformCost,
		"price_detail":        result.PriceDetail,
		"matched_tier":        result.MatchedTier,
		"matched_tier_idx":    result.MatchedTierIdx,
		"estimate_type":       estimateType,
		"confidence":          confidence,
		"assumptions":         assumptions,
		"usage_estimate": gin.H{
			"source":           usageSource,
			"tokens_estimated": tokensEstimated,
			"endpoint":         req.Endpoint,
		},
		"estimate_range": gin.H{
			"min_credits":      minResult.TotalCost,
			"expected_credits": expectedResult.TotalCost,
			"max_credits":      result.TotalCost,
			"min_rmb":          minResult.TotalCostRMB,
			"expected_rmb":     expectedResult.TotalCostRMB,
			"max_rmb":          result.TotalCostRMB,
		},
		"price_version": gin.H{
			"source":           result.PriceDetail.Source,
			"matched_tier":     result.MatchedTier,
			"matched_tier_idx": result.MatchedTierIdx,
		},
	})
}

func (h *EstimateHandler) calculateEstimate(ctx context.Context, userID uint, tenantID uint, aiModel *model.AIModel, usage pricing.UsageInput, req estimateCostRequest) (*pricing.CostResult, error) {
	if aiModel != nil &&
		(aiModel.PricingUnit == "" || aiModel.PricingUnit == model.UnitPerMillionTokens) &&
		(req.CacheReadTokens > 0 || req.CacheWriteTokens > 0) {
		return h.calculator.CalculateCostWithCache(ctx, userID, aiModel, tenantID, 0, pricing.CacheUsageInput{
			InputTokens:        usage.InputTokens,
			OutputTokens:       usage.OutputTokens,
			CacheReadTokens:    req.CacheReadTokens,
			CacheWriteTokens:   req.CacheWriteTokens,
			CacheWrite1hTokens: req.CacheWrite1hTokens,
		})
	}
	return h.calculator.CalculateCostByUnit(ctx, userID, aiModel.ID, tenantID, 0, usage)
}

func (r *estimateCostRequest) applyOpenAIRequestBody() ([]string, bool, error) {
	var raw map[string]interface{}
	decoder := json.NewDecoder(bytes.NewReader(r.Request))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return nil, false, fmt.Errorf("request must be a JSON object: %w", err)
	}
	if len(raw) == 0 {
		return nil, false, nil
	}

	if r.Model == "" {
		r.Model = stringField(raw, "model")
	}
	if r.MaxTokens == 0 {
		r.MaxTokens = intField(raw, "max_tokens")
	}
	if r.MaxCompletionTokens == 0 {
		r.MaxCompletionTokens = intField(raw, "max_completion_tokens")
	}
	if r.OutputTokens == 0 {
		r.OutputTokens = intField(raw, "output_tokens")
	}
	if r.ImageCount == 0 {
		r.ImageCount = countImageInputs(raw)
	}
	if r.CharCount == 0 {
		r.CharCount = countTextRunes(raw)
	}

	assumptions := []string{"request body was parsed without contacting upstream providers"}
	tokensEstimated := false
	if r.InputTokens == 0 {
		r.InputTokens = estimateInputTokens(raw)
		if r.InputTokens > 0 {
			tokensEstimated = true
			assumptions = append(assumptions, "input_tokens estimated from request text, tools, and multimodal parts")
		}
	}
	if r.OutputTokens == 0 && r.MaxTokens == 0 && r.MaxCompletionTokens == 0 {
		assumptions = append(assumptions, "no output cap supplied; output cost is not included unless output_tokens is provided")
	}
	if r.ImageCount > 0 {
		assumptions = append(assumptions, "image inputs are counted as image units when the model pricing unit requires it")
	}
	return assumptions, tokensEstimated, nil
}

func estimateConfidence(source string, tokensEstimated bool, outputFromCap bool, maxTokens int) string {
	if source == "provided" && !tokensEstimated && !outputFromCap {
		return "high"
	}
	if source == "request_body" && maxTokens > 0 {
		return "medium"
	}
	return "low"
}

func estimateInputTokens(raw map[string]interface{}) int {
	tokens := 0
	if messages, ok := raw["messages"].([]interface{}); ok {
		for _, msg := range messages {
			tokens += 4
			tokens += estimateTokensFromRunes(countTextRunes(msg))
		}
	}
	if prompt, ok := raw["prompt"]; ok {
		tokens += estimateTokensFromRunes(countTextRunes(prompt))
	}
	if input, ok := raw["input"]; ok {
		tokens += estimateTokensFromRunes(countTextRunes(input))
	}
	for _, key := range []string{"tools", "response_format", "functions"} {
		if v, ok := raw[key]; ok {
			tokens += estimateJSONTokens(v)
		}
	}
	imageCount := countImageInputs(raw)
	if imageCount > 0 {
		tokens += imageCount * 85
	}
	if tokens < 0 {
		return 0
	}
	return tokens
}

func estimateTokensFromRunes(runes int) int {
	if runes <= 0 {
		return 0
	}
	return int(math.Ceil(float64(runes) / 4.0))
}

func estimateJSONTokens(v interface{}) int {
	b, err := json.Marshal(v)
	if err != nil || len(b) == 0 {
		return 0
	}
	return int(math.Ceil(float64(utf8.RuneCount(b)) / 4.0))
}

func stringField(raw map[string]interface{}, key string) string {
	if v, ok := raw[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func intField(raw map[string]interface{}, key string) int {
	v, ok := raw[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	case float64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}

func countTextRunes(v interface{}) int {
	switch x := v.(type) {
	case string:
		return utf8.RuneCountInString(x)
	case []interface{}:
		total := 0
		for _, item := range x {
			total += countTextRunes(item)
		}
		return total
	case map[string]interface{}:
		total := 0
		for key, value := range x {
			lower := strings.ToLower(key)
			if lower == "image_url" || lower == "url" || lower == "b64_json" {
				continue
			}
			if lower == "text" || lower == "content" || lower == "prompt" || lower == "input" || lower == "name" {
				total += countTextRunes(value)
				continue
			}
			if lower == "messages" || lower == "tools" || lower == "response_format" || lower == "function" || lower == "parameters" {
				total += countTextRunes(value)
			}
		}
		return total
	default:
		return 0
	}
}

func countImageInputs(v interface{}) int {
	switch x := v.(type) {
	case []interface{}:
		total := 0
		for _, item := range x {
			total += countImageInputs(item)
		}
		return total
	case map[string]interface{}:
		total := 0
		if typ, _ := x["type"].(string); typ == "image_url" || typ == "input_image" {
			return 1
		}
		if _, ok := x["image_url"]; ok {
			total++
		}
		for key, value := range x {
			if strings.EqualFold(key, "image_url") {
				continue
			}
			total += countImageInputs(value)
		}
		return total
	default:
		return 0
	}
}

// buildEstimateDimensions 把 estimateCostRequest 的视频维度参数翻译为 PriceTier.DimValues 匹配键
//
// S3 + S4 (2026-04-28)：取代 estimate_handler 中的 applySeedanceBillingTokenScale 调用。
// 与 videos_handler.buildVideoDimensions 等价的逻辑，保持估算/实扣价格一致。
func buildEstimateDimensions(req estimateCostRequest) map[string]string {
	dims := make(map[string]string, 5)

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

	if req.InputContainsVideo {
		dims[model.DimKeyInputHasVideo] = "true"
	} else {
		dims[model.DimKeyInputHasVideo] = "false"
	}

	if strings.EqualFold(strings.TrimSpace(req.ServiceTier), "flex") {
		dims[model.DimKeyInferenceMode] = "offline"
	} else {
		dims[model.DimKeyInferenceMode] = "online"
	}

	if req.GenerateAudio != nil && !*req.GenerateAudio {
		dims[model.DimKeyAudioMode] = "false"
	} else {
		dims[model.DimKeyAudioMode] = "true"
	}

	return dims
}
