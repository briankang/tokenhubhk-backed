package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/payment"
)

// ExchangeRateHandler 汇率管理接口处理器
type ExchangeRateHandler struct {
	svc *payment.ExchangeService
}

// NewExchangeRateHandler 创建汇率管理 Handler 实例
func NewExchangeRateHandler(svc *payment.ExchangeService) *ExchangeRateHandler {
	return &ExchangeRateHandler{svc: svc}
}

// Register 注册汇率管理路由到管理员路由组
func (h *ExchangeRateHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/exchange-rates", h.List)
	rg.POST("/exchange-rates", h.Create)
	rg.PUT("/exchange-rates/:id", h.Update)
}

// List 获取所有汇率配置 GET /api/v1/admin/exchange-rates
func (h *ExchangeRateHandler) List(c *gin.Context) {
	rates, err := h.svc.ListExchangeRates(c.Request.Context())
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, rates)
}

// Create 新增汇率配置 POST /api/v1/admin/exchange-rates
func (h *ExchangeRateHandler) Create(c *gin.Context) {
	var req struct {
		FromCurrency string  `json:"from_currency" binding:"required"`
		ToCurrency   string  `json:"to_currency" binding:"required"`
		Rate         float64 `json:"rate" binding:"required"`
		FeeRate      float64 `json:"fee_rate"`
		IsActive     bool    `json:"is_active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	rate := &model.ExchangeRate{
		FromCurrency: req.FromCurrency,
		ToCurrency:   req.ToCurrency,
		Rate:         req.Rate,
		FeeRate:      req.FeeRate,
		IsActive:     req.IsActive,
	}

	if err := h.svc.UpdateExchangeRate(c.Request.Context(), rate); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, rate)
}

// Update 更新汇率配置 PUT /api/v1/admin/exchange-rates/:id
func (h *ExchangeRateHandler) Update(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var req struct {
		Rate     float64 `json:"rate"`
		FeeRate  float64 `json:"fee_rate"`
		IsActive bool    `json:"is_active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	// 获取旧值用于审计日志
	oldRate, _ := h.svc.GetExchangeRateByID(c.Request.Context(), uint(id))

	// 构建更新对象
	updateRate := &model.ExchangeRate{
		FromCurrency: oldRate.FromCurrency,
		ToCurrency:   oldRate.ToCurrency,
		Rate:         req.Rate,
		FeeRate:      req.FeeRate,
		IsActive:     req.IsActive,
	}

	if err := h.svc.UpdateExchangeRateByID(c.Request.Context(), uint(id), updateRate); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// TODO: 记录审计日志
	_ = oldRate

	response.Success(c, updateRate)
}
