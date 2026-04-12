package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/pricing"
)

// DiscountHandler 折扣配置管理接口处理器
type DiscountHandler struct {
	pricingSvc *pricing.PricingService
}

// NewDiscountHandler creates a new DiscountHandler.
func NewDiscountHandler(pricingSvc *pricing.PricingService) *DiscountHandler {
	return &DiscountHandler{pricingSvc: pricingSvc}
}

// ---- Level Discounts ----

// listLevelDiscountsReq is the query params for listing level discounts.
type listLevelDiscountsReq struct {
	Level int `form:"level" binding:"omitempty,min=1,max=3"`
}

// ListLevelDiscounts handles GET /api/v1/admin/level-discounts
func (h *DiscountHandler) ListLevelDiscounts(c *gin.Context) {
	var req listLevelDiscountsReq
	if err := c.ShouldBindQuery(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 20001, "invalid query params: "+err.Error())
		return
	}

	if req.Level > 0 {
		list, err := h.pricingSvc.GetLevelDiscounts(c.Request.Context(), req.Level)
		if err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
			return
		}
		response.Success(c, list)
		return
	}

	list, err := h.pricingSvc.ListAllDiscounts(c.Request.Context())
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, list)
}

// createLevelDiscountReq is the request body for creating a level discount.
type createLevelDiscountReq struct {
	Level          int      `json:"level" binding:"required,min=1,max=3"`
	ModelID        *uint    `json:"model_id"`
	InputDiscount  float64  `json:"input_discount" binding:"required,gt=0,lte=1"`
	OutputDiscount float64  `json:"output_discount" binding:"required,gt=0,lte=1"`
}

// CreateLevelDiscount handles POST /api/v1/admin/level-discounts
func (h *DiscountHandler) CreateLevelDiscount(c *gin.Context) {
	var req createLevelDiscountReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 20001, "invalid request body: "+err.Error())
		return
	}

	discount := &model.AgentLevelDiscount{
		Level:          req.Level,
		ModelID:        req.ModelID,
		InputDiscount:  req.InputDiscount,
		OutputDiscount: req.OutputDiscount,
	}

	if err := h.pricingSvc.SetLevelDiscount(c.Request.Context(), discount); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, discount)
}

// updateLevelDiscountReq is the request body for updating a level discount.
type updateLevelDiscountReq struct {
	Level          int      `json:"level" binding:"omitempty,min=1,max=3"`
	ModelID        *uint    `json:"model_id"`
	InputDiscount  float64  `json:"input_discount" binding:"omitempty,gt=0,lte=1"`
	OutputDiscount float64  `json:"output_discount" binding:"omitempty,gt=0,lte=1"`
}

// UpdateLevelDiscount handles PUT /api/v1/admin/level-discounts/:id
func (h *DiscountHandler) UpdateLevelDiscount(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 20001, "invalid id")
		return
	}

	var req updateLevelDiscountReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 20001, "invalid request body: "+err.Error())
		return
	}

	discount := &model.AgentLevelDiscount{
		Level:          req.Level,
		ModelID:        req.ModelID,
		InputDiscount:  req.InputDiscount,
		OutputDiscount: req.OutputDiscount,
	}

	if err := h.pricingSvc.UpdateLevelDiscount(c.Request.Context(), uint(id), discount); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, nil)
}

// DeleteLevelDiscount handles DELETE /api/v1/admin/level-discounts/:id
func (h *DiscountHandler) DeleteLevelDiscount(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 20001, "invalid id")
		return
	}

	if err := h.pricingSvc.DeleteLevelDiscount(c.Request.Context(), uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, nil)
}

// ---- Agent Pricings ----

// listAgentPricingsReq is the query params for listing agent pricings.
type listAgentPricingsReq struct {
	TenantID uint `form:"tenant_id" binding:"required"`
}

// ListAgentPricings handles GET /api/v1/admin/agent-pricings
func (h *DiscountHandler) ListAgentPricings(c *gin.Context) {
	var req listAgentPricingsReq
	if err := c.ShouldBindQuery(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 20001, "invalid query params: "+err.Error())
		return
	}

	list, err := h.pricingSvc.ListAgentPricings(c.Request.Context(), req.TenantID)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, list)
}

// createAgentPricingReq is the request body for creating agent pricing.
type createAgentPricingReq struct {
	TenantID     uint     `json:"tenant_id" binding:"required"`
	ModelID      uint     `json:"model_id" binding:"required"`
	PricingType  string   `json:"pricing_type" binding:"required,oneof=FIXED MARKUP DISCOUNT INHERIT"`
	InputPrice   *float64 `json:"input_price"`
	OutputPrice  *float64 `json:"output_price"`
	MarkupRate   *float64 `json:"markup_rate"`
	DiscountRate *float64 `json:"discount_rate"`
}

// CreateAgentPricing handles POST /api/v1/admin/agent-pricings
func (h *DiscountHandler) CreateAgentPricing(c *gin.Context) {
	var req createAgentPricingReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 20001, "invalid request body: "+err.Error())
		return
	}

	ap := &model.AgentPricing{
		TenantID:     req.TenantID,
		ModelID:      req.ModelID,
		PricingType:  req.PricingType,
		InputPrice:   req.InputPrice,
		OutputPrice:  req.OutputPrice,
		MarkupRate:   req.MarkupRate,
		DiscountRate: req.DiscountRate,
	}

	if err := h.pricingSvc.SetAgentPricing(c.Request.Context(), ap); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, ap)
}

// updateAgentPricingReq is the request body for updating agent pricing.
type updateAgentPricingReq struct {
	PricingType  string   `json:"pricing_type" binding:"omitempty,oneof=FIXED MARKUP DISCOUNT INHERIT"`
	InputPrice   *float64 `json:"input_price"`
	OutputPrice  *float64 `json:"output_price"`
	MarkupRate   *float64 `json:"markup_rate"`
	DiscountRate *float64 `json:"discount_rate"`
}

// UpdateAgentPricing handles PUT /api/v1/admin/agent-pricings/:id
func (h *DiscountHandler) UpdateAgentPricing(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 20001, "invalid id")
		return
	}

	var req updateAgentPricingReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 20001, "invalid request body: "+err.Error())
		return
	}

	ap := &model.AgentPricing{
		PricingType:  req.PricingType,
		InputPrice:   req.InputPrice,
		OutputPrice:  req.OutputPrice,
		MarkupRate:   req.MarkupRate,
		DiscountRate: req.DiscountRate,
	}

	if err := h.pricingSvc.UpdateAgentPricing(c.Request.Context(), uint(id), ap); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, nil)
}

// DeleteAgentPricing handles DELETE /api/v1/admin/agent-pricings/:id
func (h *DiscountHandler) DeleteAgentPricing(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 20001, "invalid id")
		return
	}

	if err := h.pricingSvc.DeleteAgentPricing(c.Request.Context(), uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, nil)
}
