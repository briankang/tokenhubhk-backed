package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/pricing"
)

// PricingHandler 定价管理接口处理器
type PricingHandler struct {
	pricingSvc *pricing.PricingService
	calculator *pricing.PricingCalculator
}

// NewPricingHandler creates a new PricingHandler.
func NewPricingHandler(pricingSvc *pricing.PricingService, calculator *pricing.PricingCalculator) *PricingHandler {
	return &PricingHandler{
		pricingSvc: pricingSvc,
		calculator: calculator,
	}
}

// listModelPricingsReq is the query params for listing model pricings.
type listModelPricingsReq struct {
	Page     int `form:"page" binding:"omitempty,min=1"`
	PageSize int `form:"page_size" binding:"omitempty,min=1,max=100"`
}

// ListModelPricings handles GET /api/v1/admin/model-pricings
func (h *PricingHandler) ListModelPricings(c *gin.Context) {
	var req listModelPricingsReq
	if err := c.ShouldBindQuery(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 20001, "invalid query params: "+err.Error())
		return
	}
	if req.Page == 0 {
		req.Page = 1
	}
	if req.PageSize == 0 {
		req.PageSize = 20
	}

	list, total, err := h.pricingSvc.ListModelPricings(c.Request.Context(), req.Page, req.PageSize)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.PageResult(c, list, total, req.Page, req.PageSize)
}

// createModelPricingReq is the request body for creating/setting model pricing.
// 价格单位：每百万 token 的积分价格（int64）
type createModelPricingReq struct {
	ModelID             uint   `json:"model_id" binding:"required"`
	InputPricePerToken  int64  `json:"input_price_per_token"`  // 输入售价（积分/百万token）
	OutputPricePerToken int64  `json:"output_price_per_token"` // 输出售价（积分/百万token）
	Currency            string `json:"currency"`
}

// CreateModelPricing handles POST /api/v1/admin/model-pricings
func (h *PricingHandler) CreateModelPricing(c *gin.Context) {
	var req createModelPricingReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 20001, "invalid request body: "+err.Error())
		return
	}

	mp := &model.ModelPricing{
		ModelID:             req.ModelID,
		InputPricePerToken:  req.InputPricePerToken,
		OutputPricePerToken: req.OutputPricePerToken,
		Currency:            req.Currency,
	}
	if mp.Currency == "" {
		mp.Currency = "USD"
	}

	if err := h.pricingSvc.SetModelPricing(c.Request.Context(), mp); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, mp)
}

// updateModelPricingReq is the request body for updating model pricing.
// 价格单位：每百万 token 的积分价格（int64）
type updateModelPricingReq struct {
	InputPricePerToken  int64  `json:"input_price_per_token"`  // 输入售价（积分/百万token）
	OutputPricePerToken int64  `json:"output_price_per_token"` // 输出售价（积分/百万token）
	Currency            string `json:"currency"`
}

// UpdateModelPricing handles PUT /api/v1/admin/model-pricings/:id
func (h *PricingHandler) UpdateModelPricing(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 20001, "invalid id")
		return
	}

	var req updateModelPricingReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 20001, "invalid request body: "+err.Error())
		return
	}

	mp := &model.ModelPricing{
		InputPricePerToken:  req.InputPricePerToken,
		OutputPricePerToken: req.OutputPricePerToken,
		Currency:            req.Currency,
	}
	if mp.Currency == "" {
		mp.Currency = "USD"
	}

	if err := h.pricingSvc.UpdateModelPricing(c.Request.Context(), uint(id), mp); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, nil)
}

// DeleteModelPricing handles DELETE /api/v1/admin/model-pricings/:id
func (h *PricingHandler) DeleteModelPricing(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 20001, "invalid id")
		return
	}

	if err := h.pricingSvc.DeleteModelPricing(c.Request.Context(), uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, nil)
}

// priceMatrixReq is the query params for the price matrix.
type priceMatrixReq struct {
	TenantID   uint `form:"tenant_id"`
	AgentLevel int  `form:"agent_level"`
}

// GetPriceMatrix handles GET /api/v1/admin/price-matrix
func (h *PricingHandler) GetPriceMatrix(c *gin.Context) {
	var req priceMatrixReq
	if err := c.ShouldBindQuery(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 20001, "invalid query params: "+err.Error())
		return
	}

	matrix, err := h.calculator.GetPriceMatrix(c.Request.Context(), req.TenantID, req.AgentLevel)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, matrix)
}

// priceCalculateReq is the request body for simulating price calculation.
type priceCalculateReq struct {
	ModelID      uint `json:"model_id" binding:"required"`
	TenantID     uint `json:"tenant_id"`
	AgentLevel   int  `json:"agent_level"`
	InputTokens  int  `json:"input_tokens"`
	OutputTokens int  `json:"output_tokens"`
}

// CalculatePrice handles POST /api/v1/admin/price-calculate
func (h *PricingHandler) CalculatePrice(c *gin.Context) {
	var req priceCalculateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 20001, "invalid request body: "+err.Error())
		return
	}

	// If tokens are provided, calculate cost; otherwise just return price
	if req.InputTokens > 0 || req.OutputTokens > 0 {
		cost, err := h.calculator.CalculateCost(
			c.Request.Context(), req.ModelID, req.TenantID, req.AgentLevel,
			req.InputTokens, req.OutputTokens,
		)
		if err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
			return
		}
		response.Success(c, cost)
		return
	}

	price, err := h.calculator.CalculatePrice(c.Request.Context(), req.ModelID, req.TenantID, req.AgentLevel)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, price)
}
