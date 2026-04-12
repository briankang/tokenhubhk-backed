package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/report"
)

// ReportHandler 报表管理接口处理器
type ReportHandler struct {
	reportSvc  *report.ReportService
	profitCalc *report.ProfitCalculator
}

// NewReportHandler 创建报表管理Handler实例
func NewReportHandler(reportSvc *report.ReportService, profitCalc *report.ProfitCalculator) *ReportHandler {
	if reportSvc == nil {
		panic("admin report handler: report service is nil")
	}
	return &ReportHandler{reportSvc: reportSvc, profitCalc: profitCalc}
}

// Register 注册报表路由到管理员路由组
func (h *ReportHandler) Register(rg *gin.RouterGroup) {
	reports := rg.Group("/reports")
	{
		reports.GET("/overview", h.Overview)
		reports.GET("/usage", h.Usage)
		reports.GET("/profit", h.Profit)
		reports.GET("/profit/trend", h.ProfitTrend)
		reports.GET("/profit/top-agents", h.TopAgents)
		reports.GET("/profit/drilldown/:tenantId", h.ProfitDrillDown)
	}
}

// Overview 获取报表总览 GET /api/v1/admin/reports/overview
func (h *ReportHandler) Overview(c *gin.Context) {
	period := c.DefaultQuery("period", "month")

	result, err := h.reportSvc.GetOverviewReport(c.Request.Context(), 0, period)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, result)
}

// Usage 获取用量统计报表 GET /api/v1/admin/reports/usage
func (h *ReportHandler) Usage(c *gin.Context) {
	filter := parseReportFilter(c)

	result, err := h.reportSvc.GetUsageReport(c.Request.Context(), filter)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, result)
}

// Profit 获取利润报表 GET /api/v1/admin/reports/profit
func (h *ReportHandler) Profit(c *gin.Context) {
	filter := parseReportFilter(c)

	result, err := h.reportSvc.GetProfitReport(c.Request.Context(), filter)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, result)
}

// ProfitTrend 获取利润趋势图数据 GET /api/v1/admin/reports/profit/trend
func (h *ReportHandler) ProfitTrend(c *gin.Context) {
	filter := parseProfitFilter(c)

	result, err := h.profitCalc.GetProfitTrend(c.Request.Context(), filter)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, result)
}

// TopAgents 获取利润排行榜前几名代理商 GET /api/v1/admin/reports/profit/top-agents
func (h *ReportHandler) TopAgents(c *gin.Context) {
	startDate := c.Query("start_date")
	endDate := c.Query("end_date")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))

	if startDate == "" || endDate == "" {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	result, err := h.profitCalc.GetTopAgents(c.Request.Context(), startDate, endDate, limit)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, result)
}

// ProfitDrillDown handles GET /api/v1/admin/reports/profit/drilldown/:tenantId
func (h *ReportHandler) ProfitDrillDown(c *gin.Context) {
	tenantID, err := strconv.ParseUint(c.Param("tenantId"), 10, 64)
	if err != nil || tenantID == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	startDate := c.Query("start_date")
	endDate := c.Query("end_date")
	if startDate == "" || endDate == "" {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	result, err := h.profitCalc.GetAgentDrillDown(c.Request.Context(), uint(tenantID), startDate, endDate)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, result)
}

// parseReportFilter extracts common report filter params from query string.
func parseReportFilter(c *gin.Context) report.ReportFilter {
	filter := report.ReportFilter{
		StartDate: c.Query("start_date"),
		EndDate:   c.Query("end_date"),
		GroupBy:   c.DefaultQuery("group_by", "day"),
	}
	filter.Page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	filter.PageSize, _ = strconv.Atoi(c.DefaultQuery("page_size", "20"))

	if tid := c.Query("tenant_id"); tid != "" {
		if v, err := strconv.ParseUint(tid, 10, 64); err == nil {
			u := uint(v)
			filter.TenantID = &u
		}
	}
	if mid := c.Query("model_id"); mid != "" {
		if v, err := strconv.ParseUint(mid, 10, 64); err == nil {
			u := uint(v)
			filter.ModelID = &u
		}
	}
	if cid := c.Query("channel_id"); cid != "" {
		if v, err := strconv.ParseUint(cid, 10, 64); err == nil {
			u := uint(v)
			filter.ChannelID = &u
		}
	}
	return filter
}

// parseProfitFilter extracts profit-specific filter params from query string.
func parseProfitFilter(c *gin.Context) report.ProfitFilter {
	filter := report.ProfitFilter{
		StartDate: c.Query("start_date"),
		EndDate:   c.Query("end_date"),
		GroupBy:   c.DefaultQuery("group_by", "day"),
	}
	if tid := c.Query("tenant_id"); tid != "" {
		if v, err := strconv.ParseUint(tid, 10, 64); err == nil {
			u := uint(v)
			filter.TenantID = &u
		}
	}
	if mid := c.Query("model_id"); mid != "" {
		if v, err := strconv.ParseUint(mid, 10, 64); err == nil {
			u := uint(v)
			filter.ModelID = &u
		}
	}
	if cid := c.Query("channel_id"); cid != "" {
		if v, err := strconv.ParseUint(cid, 10, 64); err == nil {
			u := uint(v)
			filter.ChannelID = &u
		}
	}
	if al := c.Query("agent_level"); al != "" {
		if v, err := strconv.Atoi(al); err == nil {
			filter.AgentLevel = &v
		}
	}
	return filter
}
