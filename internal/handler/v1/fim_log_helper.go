package v1

import (
	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/provider"
	"tokenhub-server/internal/service/apikey"
)

func (h *CompletionsHandler) recordFIMApiCallLog(c *gin.Context, req *fimCompletionRequest, ch *model.Channel, keyInfo *apikey.ApiKeyInfo, requestID string, usage *provider.Usage, latencyMs, statusCode int, cost int64, costRMB float64, errMsg string) {
	if req == nil || keyInfo == nil {
		return
	}
	callLog := &model.ApiCallLog{
		RequestID:      requestID,
		UserID:         keyInfo.UserID,
		TenantID:       keyInfo.TenantID,
		ApiKeyID:       keyInfo.KeyID,
		ClientIP:       c.ClientIP(),
		Endpoint:       "/v1/completions",
		RequestModel:   req.Model,
		ActualModel:    req.Model,
		IsStream:       req.Stream,
		MessageCount:   1,
		MaxTokens:      req.MaxTokens,
		StatusCode:     statusCode,
		TotalLatencyMs: latencyMs,
		CostCredits:    cost,
		CostRMB:        costRMB,
		ErrorMessage:   errMsg,
		Status:         "success",
		BillingStatus:  "no_charge",
		UsageSource:    "provider",
	}
	if ch != nil {
		callLog.ChannelID = ch.ID
		callLog.ChannelName = ch.Name
	}
	if usage != nil {
		callLog.PromptTokens = usage.PromptTokens
		callLog.CompletionTokens = usage.CompletionTokens
		callLog.TotalTokens = usage.TotalTokens
		callLog.CacheReadTokens = usage.CacheReadTokens
		callLog.CacheWriteTokens = usage.CacheWriteTokens
	}
	if statusCode >= 400 || errMsg != "" {
		callLog.Status = "error"
		callLog.ErrorType = "upstream_error"
	}
	applyMatchedTierFromCtx(c, callLog)
	h.recordApiCallLog(callLog)
}
