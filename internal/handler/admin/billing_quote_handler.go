package admin

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	billingsvc "tokenhub-server/internal/service/billing"
	"tokenhub-server/internal/service/pricing"
)

// BillingQuoteHandler 暴露统一计价真相源给管理员前端。
//
// 该 handler 提供 `POST /api/v1/admin/billing/quote-preview`,
// 直接返回 `BillingQuote`,与真实扣费、成本分析共用同一份计价口径。
type BillingQuoteHandler struct {
	quoteSvc *billingsvc.QuoteService
}

// NewBillingQuoteHandler 构造 handler。db 与 pricingCalc 必须非 nil。
func NewBillingQuoteHandler(db *gorm.DB, pricingCalc *pricing.PricingCalculator) *BillingQuoteHandler {
	return &BillingQuoteHandler{
		quoteSvc: billingsvc.NewQuoteService(db, pricingCalc),
	}
}

// quotePreviewRequest 是 quote-preview 端点的请求体。
//
// 与 Spec §1 对齐:scenario 默认 "preview",model_id 必填,
// usage 与 params 可选;不写库、不动余额。
//
// dim_values:多维矩阵命中输入(如 Seedance 的 resolution / input_has_video / inference_mode)。
// 与真实扣费、对账重放共用 PriceMatrix.MatchCellByModelID,确保命中口径一致。
type quotePreviewRequest struct {
	ModelID      uint                   `json:"model_id" binding:"required"`
	ModelName    string                 `json:"model_name"`
	Scenario     string                 `json:"scenario"`
	UserID       uint                   `json:"user_id"`
	TenantID     uint                   `json:"tenant_id"`
	AgentLevel   int                    `json:"agent_level"`
	Usage        billingsvc.QuoteUsage  `json:"usage"`
	Params       map[string]interface{} `json:"params"`
	ThinkingMode bool                   `json:"thinking_mode"`
	DimValues    map[string]interface{} `json:"dim_values"`
}

// QuotePreview 试算端点。
//
// 用法:
//
//	POST /api/v1/admin/billing/quote-preview
//	{ "model_id": 1, "scenario": "preview", "usage": { "input_tokens": 1000, "output_tokens": 500 } }
//
// 返回 `{ "quote": { ... BillingQuote ... } }`,与真实扣费 snapshot.quote 同源。
func (h *BillingQuoteHandler) QuotePreview(c *gin.Context) {
	var req quotePreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, err.Error())
		return
	}
	if req.ModelID == 0 && req.ModelName == "" {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, "model_id 或 model_name 必须至少有一个")
		return
	}
	if req.ThinkingMode {
		// thinking_mode 旗位与 params.enable_thinking 等价
	} else if req.Params != nil {
		if v, ok := req.Params["enable_thinking"].(bool); ok && v {
			req.ThinkingMode = true
		}
	}

	scenario := req.Scenario
	if scenario == "" {
		scenario = billingsvc.QuoteScenarioPreview
	}

	quote, err := h.quoteSvc.Calculate(c.Request.Context(), billingsvc.QuoteRequest{
		Scenario:     scenario,
		ModelID:      req.ModelID,
		ModelName:    req.ModelName,
		UserID:       req.UserID,
		TenantID:     req.TenantID,
		AgentLevel:   req.AgentLevel,
		Usage:        req.Usage,
		ThinkingMode: req.ThinkingMode,
		DimValues:    req.DimValues,
	})
	if err != nil {
		switch {
		case errors.Is(err, billingsvc.ErrQuoteModelNotFound):
			response.ErrorMsg(c, http.StatusNotFound, errcode.ErrNotFound.Code, "模型不存在")
		case errors.Is(err, billingsvc.ErrQuotePricingMissing):
			response.ErrorMsg(c, http.StatusUnprocessableEntity, errcode.ErrValidation.Code, "模型未配置售价(model_pricings 缺失)")
		default:
			response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		}
		return
	}
	response.Success(c, gin.H{"quote": quote})
}
