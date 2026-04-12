package agent

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/pricing"
	"tokenhub-server/internal/service/report"
)

// DashboardHandler 代理商仪表盘、定价、用量和统计接口处理器
type DashboardHandler struct {
	reportSvc  *report.ReportService
	pricingSvc *pricing.PricingService
}

// NewDashboardHandler 创建代理商仪表盘Handler实例
func NewDashboardHandler(reportSvc *report.ReportService, pricingSvc *pricing.PricingService) *DashboardHandler {
	return &DashboardHandler{reportSvc: reportSvc, pricingSvc: pricingSvc}
}

// Register 注册代理商仪表盘路由到路由组
func (h *DashboardHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/dashboard", h.Dashboard)
	rg.GET("/pricing", h.GetPricing)
	rg.PUT("/pricing", h.UpdatePricing)
	rg.GET("/usage", h.Usage)
	rg.GET("/stats", h.Stats)
}

// Dashboard 获取代理商仪表盘数据 GET /api/v1/agent/dashboard
func (h *DashboardHandler) Dashboard(c *gin.Context) {
	tenantID, exists := c.Get("tenantId")
	if !exists {
		response.Error(c, http.StatusForbidden, errcode.ErrPermissionDenied)
		return
	}
	tid, ok := tenantID.(uint)
	if !ok || tid == 0 {
		response.Error(c, http.StatusForbidden, errcode.ErrPermissionDenied)
		return
	}

	period := c.DefaultQuery("period", "month")
	result, err := h.reportSvc.GetOverviewReport(c.Request.Context(), tid, period)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// Hide cost data for agents
	result.TotalCost = 0
	result.GrossProfit = 0
	result.ProfitMargin = 0

	response.Success(c, result)
}

// GetPricing 获取代理商定价配置 GET /api/v1/agent/pricing
func (h *DashboardHandler) GetPricing(c *gin.Context) {
	tenantID, exists := c.Get("tenantId")
	if !exists {
		response.Error(c, http.StatusForbidden, errcode.ErrPermissionDenied)
		return
	}
	tid, ok := tenantID.(uint)
	if !ok || tid == 0 {
		response.Error(c, http.StatusForbidden, errcode.ErrPermissionDenied)
		return
	}

	pricings, err := h.pricingSvc.ListAgentPricings(c.Request.Context(), tid)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, pricings)
}

// UpdatePricing 更新代理商定价 PUT /api/v1/agent/pricing
func (h *DashboardHandler) UpdatePricing(c *gin.Context) {
	tenantID, exists := c.Get("tenantId")
	if !exists {
		response.Error(c, http.StatusForbidden, errcode.ErrPermissionDenied)
		return
	}
	tid, ok := tenantID.(uint)
	if !ok || tid == 0 {
		response.Error(c, http.StatusForbidden, errcode.ErrPermissionDenied)
		return
	}

	var req struct {
		ModelID      uint    `json:"model_id" binding:"required"`
		PricingType  string  `json:"pricing_type" binding:"required"`
		InputPrice   float64 `json:"input_price"`
		OutputPrice  float64 `json:"output_price"`
		MarkupRate   float64 `json:"markup_rate"`
		DiscountRate float64 `json:"discount_rate"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	ap := &model.AgentPricing{
		TenantID:     tid,
		ModelID:      req.ModelID,
		PricingType:  req.PricingType,
		InputPrice:   &req.InputPrice,
		OutputPrice:  &req.OutputPrice,
		MarkupRate:   &req.MarkupRate,
		DiscountRate: &req.DiscountRate,
	}

	if err := h.pricingSvc.SetAgentPricing(c.Request.Context(), ap); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, gin.H{"message": "pricing updated"})
}

// Usage 获取代理商用量数据 GET /api/v1/agent/usage
func (h *DashboardHandler) Usage(c *gin.Context) {
	tenantID, exists := c.Get("tenantId")
	if !exists {
		response.Error(c, http.StatusForbidden, errcode.ErrPermissionDenied)
		return
	}
	tid, ok := tenantID.(uint)
	if !ok || tid == 0 {
		response.Error(c, http.StatusForbidden, errcode.ErrPermissionDenied)
		return
	}

	startDate := c.Query("start_date")
	endDate := c.Query("end_date")
	if startDate == "" || endDate == "" {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	filter := report.ReportFilter{
		TenantID:  &tid,
		StartDate: startDate,
		EndDate:   endDate,
		GroupBy:   c.DefaultQuery("group_by", "day"),
		Page:      page,
		PageSize:  pageSize,
	}

	result, err := h.reportSvc.GetUsageReport(c.Request.Context(), filter)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, result)
}

// Stats 获取代理商统计数据 GET /api/v1/agent/stats
func (h *DashboardHandler) Stats(c *gin.Context) {
	tenantID, exists := c.Get("tenantId")
	if !exists {
		response.Error(c, http.StatusForbidden, errcode.ErrPermissionDenied)
		return
	}
	tid, ok := tenantID.(uint)
	if !ok || tid == 0 {
		response.Error(c, http.StatusForbidden, errcode.ErrPermissionDenied)
		return
	}

	period := c.DefaultQuery("period", "month")
	result, err := h.reportSvc.GetOverviewReport(c.Request.Context(), tid, period)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// Hide cost data for agents
	result.TotalCost = 0
	result.GrossProfit = 0
	result.ProfitMargin = 0

	response.Success(c, gin.H{
		"total_requests":      result.TotalRequests,
		"total_input_tokens":  result.TotalInputTokens,
		"total_output_tokens": result.TotalOutputTokens,
		"total_revenue":       result.TotalRevenue,
		"error_count":         result.ErrorCount,
		"error_rate":          result.ErrorRate,
	})
}
