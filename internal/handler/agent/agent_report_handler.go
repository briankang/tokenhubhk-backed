package agent

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/permission"
	"tokenhub-server/internal/service/report"
)

// ReportHandler 代理商报表接口处理器
type ReportHandler struct {
	reportSvc  *report.ReportService
	profitCalc *report.ProfitCalculator
	perm       *permission.PermissionService
}

// NewReportHandler 创建代理商报表Handler实例
func NewReportHandler(reportSvc *report.ReportService, profitCalc *report.ProfitCalculator, perm *permission.PermissionService) *ReportHandler {
	if reportSvc == nil {
		panic("agent report handler: report service is nil")
	}
	return &ReportHandler{reportSvc: reportSvc, profitCalc: profitCalc, perm: perm}
}

// Register 注册代理商报表路由到路由组
func (h *ReportHandler) Register(rg *gin.RouterGroup) {
	reports := rg.Group("/reports")
	{
		reports.GET("/overview", h.Overview)
		reports.GET("/profit", h.Profit)
		reports.GET("/profit/drilldown", h.ProfitDrillDown)
	}
}

// Overview 获取代理商报表总览 GET /api/v1/agent/reports/overview
func (h *ReportHandler) Overview(c *gin.Context) {
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

	// Filter sensitive data for agents
	if h.perm != nil {
		result.TotalCost = 0
		result.GrossProfit = 0
		result.ProfitMargin = 0
	}

	response.Success(c, result)
}

// Profit 获取代理商利润报表 GET /api/v1/agent/reports/profit
func (h *ReportHandler) Profit(c *gin.Context) {
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

	filter := report.ReportFilter{
		StartDate: c.Query("start_date"),
		EndDate:   c.Query("end_date"),
		GroupBy:   c.DefaultQuery("group_by", "day"),
		TenantID:  &tid,
	}
	filter.Page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	filter.PageSize, _ = strconv.Atoi(c.DefaultQuery("page_size", "20"))

	if filter.StartDate == "" || filter.EndDate == "" {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	result, err := h.reportSvc.GetProfitReport(c.Request.Context(), filter)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	// Agents see revenue but not upstream cost
	if result.Summary != nil {
		result.Summary.TotalCost = 0
		result.Summary.GrossProfit = 0
		result.Summary.ProfitMargin = 0
	}
	for i := range result.Trend {
		result.Trend[i].TotalCost = 0
		result.Trend[i].GrossProfit = 0
		result.Trend[i].ProfitMargin = 0
	}

	response.Success(c, result)
}

// ProfitDrillDown 获取利润钻取数据 GET /api/v1/agent/reports/profit/drilldown
func (h *ReportHandler) ProfitDrillDown(c *gin.Context) {
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

	result, err := h.profitCalc.GetAgentDrillDown(c.Request.Context(), tid, startDate, endDate)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	// Hide upstream cost for agent view
	for i := range result {
		result[i].TotalCost = 0
		result[i].GrossProfit = 0
		result[i].ProfitMargin = 0
	}

	response.Success(c, result)
}
