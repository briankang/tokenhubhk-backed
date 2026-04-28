package v1

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/response"
)

// GenerationHandler exposes OpenRouter-style request metadata for completed generations.
type GenerationHandler struct {
	db *gorm.DB
}

func NewGenerationHandler(db *gorm.DB) *GenerationHandler {
	return &GenerationHandler{db: db}
}

func (h *GenerationHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/generation", h.GetGeneration)
}

func (h *GenerationHandler) GetGeneration(c *gin.Context) {
	id := c.Query("id")
	if id == "" {
		response.SendOpenAIErrorWithParam(c, http.StatusBadRequest, "id is required", "invalid_request_error", "id", "missing_required_parameter")
		return
	}

	userID, _ := c.Get("userId")
	uid, _ := userID.(uint)

	var log model.ApiCallLog
	query := h.db.WithContext(c.Request.Context()).Where("request_id = ?", id)
	if uid > 0 {
		query = query.Where("user_id = ?", uid)
	}
	err := query.First(&log).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.SendOpenAIError(c, http.StatusNotFound, "generation not found", "not_found_error", "generation_not_found")
			return
		}
		response.SendOpenAIError(c, http.StatusInternalServerError, "failed to query generation", "server_error", "internal_error")
		return
	}

	modelName := log.ActualModel
	if modelName == "" {
		modelName = log.RequestModel
	}
	providerName := log.SupplierName
	if providerName == "" {
		providerName = log.ChannelName
	}

	c.JSON(http.StatusOK, gin.H{
		"data": gin.H{
			"id":                       log.RequestID,
			"request_id":               log.RequestID,
			"created_at":               log.CreatedAt.UTC().Format(time.RFC3339Nano),
			"api_type":                 apiTypeFromEndpoint(log.Endpoint),
			"model":                    modelName,
			"requested_model":          log.RequestModel,
			"provider_name":            providerName,
			"channel_name":             log.ChannelName,
			"streamed":                 log.IsStream,
			"status":                   log.Status,
			"status_code":              log.StatusCode,
			"finish_reason":            "",
			"tokens_prompt":            log.PromptTokens,
			"tokens_completion":        log.CompletionTokens,
			"tokens_total":             log.TotalTokens,
			"native_tokens_prompt":     log.PromptTokens,
			"native_tokens_completion": log.CompletionTokens,
			"native_tokens_cached":     log.CacheReadTokens,
			"native_tokens_reasoning":  reasoningTokensFromSnapshot(log.BillingSnapshot),
			"total_cost":               log.CostRMB,
			"usage":                    log.CostRMB,
			"estimated_cost_credits":   log.EstimatedCostCredits,
			"estimated_cost_units":     log.EstimatedCostUnits,
			"actual_cost_credits":      log.ActualCostCredits,
			"actual_cost_units":        log.ActualCostUnits,
			"platform_cost_rmb":        log.PlatformCostRMB,
			"platform_cost_units":      log.PlatformCostUnits,
			"billing_status":           log.BillingStatus,
			"usage_source":             log.UsageSource,
			"usage_estimated":          log.UsageEstimated,
			"under_collected_credits":  log.UnderCollectedCredits,
			"latency":                  log.FirstTokenMs,
			"generation_time":          log.TotalLatencyMs,
			"upstream_latency":         log.UpstreamLatencyMs,
			"upstream_status":          log.UpstreamStatus,
			"cache_read_tokens":        log.CacheReadTokens,
			"cache_write_tokens":       log.CacheWriteTokens,
			"matched_price_tier":       log.MatchedPriceTier,
			"matched_price_tier_idx":   log.MatchedPriceTierIdx,
		},
	})
}

func apiTypeFromEndpoint(endpoint string) string {
	switch endpoint {
	case "/v1/completions":
		return "completions"
	case "/v1/embeddings":
		return "embeddings"
	case "/v1/images/generations":
		return "images"
	case "/v1/audio/speech", "/v1/audio/transcriptions":
		return "audio"
	default:
		return "chat"
	}
}

func reasoningTokensFromSnapshot(snapshot model.JSON) int {
	if len(snapshot) == 0 {
		return 0
	}
	var data map[string]interface{}
	if err := json.Unmarshal(snapshot, &data); err != nil {
		return 0
	}
	switch v := data["reasoning_tokens"].(type) {
	case float64:
		return int(v)
	case json.Number:
		i, _ := strconv.Atoi(v.String())
		return i
	default:
		return 0
	}
}
