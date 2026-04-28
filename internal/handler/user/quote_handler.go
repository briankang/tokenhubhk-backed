package user

// 用户视角 BillingQuote 查询（A4 任务）
//
// 终端用户做完一次 chat 后，希望看到"本次扣了多少钱、按什么单价"。
// admin 端 GET /admin/api-call-logs/:requestId/cost-breakdown 透出完整数据
// （含 platform_cost / 毛利率 / consistency 等敏感字段），不适合直接暴露给用户。
//
// 本 handler 提供:
//
//	GET /api/v1/user/api-call-logs/:requestId/quote
//
// 仅返回用户视角的精简 quote：金额、用量、line items 拆分、命中规则提示，
// 严格过滤掉 platform_cost / margin / quote_consistency / replay_status。

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/ctxutil"
	"tokenhub-server/internal/pkg/dbctx"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
)

// QuoteHandler 用户级 BillingQuote 查询
type QuoteHandler struct {
	db *gorm.DB
}

// NewQuoteHandler 构造 handler
func NewQuoteHandler(db *gorm.DB) *QuoteHandler {
	return &QuoteHandler{db: db}
}

// Register 注册路由（authorized 路由组下，userGroup）
func (h *QuoteHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/api-call-logs/:requestId/quote", h.GetQuote)
}

// userQuoteLineItem 用户视角的 line item（剥离 platform_cost 字段）
type userQuoteLineItem struct {
	Component      string  `json:"component"`
	UsageKey       string  `json:"usage_key,omitempty"`
	Quantity       float64 `json:"quantity"`
	Unit           string  `json:"unit,omitempty"`
	Denominator    int64   `json:"denominator,omitempty"`
	UnitPriceRMB   float64 `json:"unit_price_rmb"`
	CostCredits    int64   `json:"cost_credits"`
	CostRMB        float64 `json:"cost_rmb"`
}

// userQuoteUsage 用户用量（保留所有计费维度供透明展示）
type userQuoteUsage struct {
	InputTokens        int64   `json:"input_tokens,omitempty"`
	OutputTokens       int64   `json:"output_tokens,omitempty"`
	ThinkingTokens     int64   `json:"thinking_tokens,omitempty"`
	CacheReadTokens    int64   `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens   int64   `json:"cache_write_tokens,omitempty"`
	CacheWrite1hTokens int64   `json:"cache_write_1h_tokens,omitempty"`
	ImageCount         int64   `json:"image_count,omitempty"`
	CharCount          int64   `json:"char_count,omitempty"`
	DurationSec        float64 `json:"duration_sec,omitempty"`
	CallCount          int64   `json:"call_count,omitempty"`
}

// userQuoteResponse 用户视角的精简 quote 响应
type userQuoteResponse struct {
	RequestID           string              `json:"request_id"`
	ModelName           string              `json:"model_name"`
	ModelType           string              `json:"model_type,omitempty"`
	PricingUnit         string              `json:"pricing_unit,omitempty"`
	Scenario            string              `json:"scenario,omitempty"`
	Usage               userQuoteUsage      `json:"usage"`
	LineItems           []userQuoteLineItem `json:"line_items"`
	TotalCredits        int64               `json:"total_credits"`
	TotalRMB            float64             `json:"total_rmb"`
	ActualCostCredits   int64               `json:"actual_cost_credits"`
	BillingStatus       string              `json:"billing_status,omitempty"`
	MatchedTierName     string              `json:"matched_tier_name,omitempty"`
	ThinkingModeApplied bool                `json:"thinking_mode_applied"`
	CreatedAt           string              `json:"created_at,omitempty"`
}

// GetQuote 用户查询自己的 BillingQuote
//
// 安全策略：
//  1. 强制按 user_id + request_id 双条件查询，无法越权查别人记录
//  2. 仅从 billing_snapshot["quote"] 透出指定字段；platform_cost / margin / consistency 完全不暴露
//  3. 旧日志无 snapshot 时降级返回 ApiCallLog 顶层字段（cost_credits 等），仅作为 best-effort
func (h *QuoteHandler) GetQuote(c *gin.Context) {
	uid, ok := ctxutil.MustUserID(c)
	if !ok {
		return
	}
	requestID := c.Param("requestId")
	if requestID == "" {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "request_id 不能为空")
		return
	}

	ctx, cancel := dbctx.Medium(c.Request.Context())
	defer cancel()

	var log model.ApiCallLog
	err := h.db.WithContext(ctx).
		Where("request_id = ? AND user_id = ?", requestID, uid).
		First(&log).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
			return
		}
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 模型名：优先实际命中的 ActualModel，回退请求时的 RequestModel
	modelName := log.ActualModel
	if modelName == "" {
		modelName = log.RequestModel
	}

	resp := userQuoteResponse{
		RequestID:         log.RequestID,
		ModelName:         modelName,
		TotalCredits:      log.CostCredits,
		TotalRMB:          log.CostRMB,
		ActualCostCredits: log.ActualCostCredits,
		BillingStatus:     log.BillingStatus,
		Usage: userQuoteUsage{
			InputTokens:      int64(log.PromptTokens),
			OutputTokens:     int64(log.CompletionTokens),
			CacheReadTokens:  int64(log.CacheReadTokens),
			CacheWriteTokens: int64(log.CacheWriteTokens),
			ImageCount:       int64(log.ImageCount),
			CharCount:        int64(log.CharCount),
			DurationSec:      log.DurationSec,
			CallCount:        int64(log.CallCount),
		},
		Scenario:            "charge",
		ThinkingModeApplied: log.ThinkingMode,
		MatchedTierName:     log.MatchedPriceTier,
		LineItems:           []userQuoteLineItem{},
	}
	if !log.CreatedAt.IsZero() {
		resp.CreatedAt = log.CreatedAt.UTC().Format("2006-01-02T15:04:05Z")
	}

	// 优先从 billing_snapshot["quote"] 提取详细 line items
	if len(log.BillingSnapshot) > 0 {
		var snap map[string]interface{}
		if jerr := json.Unmarshal(log.BillingSnapshot, &snap); jerr == nil {
			if q, qok := snap["quote"].(map[string]interface{}); qok {
				populateUserQuoteFromSnapshot(&resp, q)
			}
		}
	}

	response.Success(c, resp)
}

// populateUserQuoteFromSnapshot 从 BillingQuote snapshot 中提取用户视角字段
//
// 严格白名单：只读取已知安全字段，未知字段一律忽略。
func populateUserQuoteFromSnapshot(resp *userQuoteResponse, q map[string]interface{}) {
	if v, ok := q["model_type"].(string); ok {
		resp.ModelType = v
	}
	if v, ok := q["pricing_unit"].(string); ok {
		resp.PricingUnit = v
	}
	if v, ok := q["scenario"].(string); ok && v != "" {
		resp.Scenario = v
	}
	if v, ok := q["matched_tier_name"].(string); ok {
		resp.MatchedTierName = v
	}
	if v, ok := q["thinking_mode_applied"].(bool); ok {
		resp.ThinkingModeApplied = v
	}
	if v, ok := q["total_credits"].(float64); ok && v > 0 {
		resp.TotalCredits = int64(v)
	}
	if v, ok := q["total_rmb"].(float64); ok && v > 0 {
		resp.TotalRMB = v
	}

	if usage, uok := q["usage"].(map[string]interface{}); uok {
		resp.Usage = userQuoteUsage{
			InputTokens:        readInt64(usage, "input_tokens"),
			OutputTokens:       readInt64(usage, "output_tokens"),
			ThinkingTokens:     readInt64(usage, "thinking_tokens"),
			CacheReadTokens:    readInt64(usage, "cache_read_tokens"),
			CacheWriteTokens:   readInt64(usage, "cache_write_tokens"),
			CacheWrite1hTokens: readInt64(usage, "cache_write_1h_tokens"),
			ImageCount:         readInt64(usage, "image_count"),
			CharCount:          readInt64(usage, "char_count"),
			DurationSec:        readFloat64(usage, "duration_sec"),
			CallCount:          readInt64(usage, "call_count"),
		}
	}

	if items, iok := q["line_items"].([]interface{}); iok {
		out := make([]userQuoteLineItem, 0, len(items))
		for _, raw := range items {
			it, mok := raw.(map[string]interface{})
			if !mok {
				continue
			}
			out = append(out, userQuoteLineItem{
				Component:    readString(it, "component"),
				UsageKey:     readString(it, "usage_key"),
				Quantity:     readFloat64(it, "quantity"),
				Unit:         readString(it, "unit"),
				Denominator:  readInt64(it, "denominator"),
				UnitPriceRMB: readFloat64(it, "unit_price_rmb"),
				CostCredits:  readInt64(it, "cost_credits"),
				CostRMB:      readFloat64(it, "cost_rmb"),
			})
		}
		resp.LineItems = out
	}
}

func readString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func readInt64(m map[string]interface{}, key string) int64 {
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	}
	return 0
}

func readFloat64(m map[string]interface{}, key string) float64 {
	switch v := m[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return 0
}
