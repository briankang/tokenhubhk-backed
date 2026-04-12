package agent

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/report"
)

// ConsumptionHandler 代理商消费数据接口处理器
type ConsumptionHandler struct {
	reportSvc *report.ReportService
}

// NewConsumptionHandler 创建代理商消费数据Handler实例
func NewConsumptionHandler(reportSvc *report.ReportService) *ConsumptionHandler {
	if reportSvc == nil {
		panic("agent consumption handler: report service is nil")
	}
	return &ConsumptionHandler{reportSvc: reportSvc}
}

// Register 注册代理商消费路由到路由组
func (h *ConsumptionHandler) Register(rg *gin.RouterGroup) {
	consumption := rg.Group("/consumption")
	{
		consumption.GET("", h.Summary)
		consumption.GET("/detail", h.Detail)
	}
}

// Summary 获取代理商消费汇总 GET /api/v1/agent/consumption
func (h *ConsumptionHandler) Summary(c *gin.Context) {
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

	// 代理商消费汇总：隐藏成本数据，仅展示营收相关指标
	response.Success(c, gin.H{
		"total_requests":      result.TotalRequests,
		"total_input_tokens":  result.TotalInputTokens,
		"total_output_tokens": result.TotalOutputTokens,
		"total_revenue":       result.TotalRevenue,
		"error_count":         result.ErrorCount,
		"error_rate":          result.ErrorRate,
		"active_keys":         result.ActiveKeys,
	})
}

// Detail 获取代理商消费明细 GET /api/v1/agent/consumption/detail
func (h *ConsumptionHandler) Detail(c *gin.Context) {
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

	filter := report.ConsumptionFilter{
		TenantID:  &tid,
		StartDate: startDate,
		EndDate:   endDate,
		Page:      page,
		PageSize:  pageSize,
	}

	items, total, err := h.reportSvc.GetConsumptionDetail(c.Request.Context(), filter)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.PageResult(c, items, total, page, pageSize)
}
