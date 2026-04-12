package openapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/response"
	svc "tokenhub-server/internal/service/openapi"
)

// UsageHandler 处理用量统计/趋势相关的 Open API 请求。
type UsageHandler struct {
	service *svc.OpenAPIService
}

// NewUsageHandler 创建用量 Handler 实例。
func NewUsageHandler(service *svc.OpenAPIService) *UsageHandler {
	return &UsageHandler{service: service}
}

// Register 注册用量相关路由。
func (h *UsageHandler) Register(rg *gin.RouterGroup) {
	g := rg.Group("/usage")
	g.GET("/statistics", h.Statistics)
	g.GET("/trend", h.Trend)
}

// Statistics 用量统计接口，Token 数/请求数按模型分组。
// GET /api/v1/open/usage/statistics?date_from=2024-01-01&date_to=2024-12-31
func (h *UsageHandler) Statistics(c *gin.Context) {
	userID := c.GetUint("userId")
	dateFrom := c.Query("date_from")
	dateTo := c.Query("date_to")

	items, err := h.service.GetUsageStatistics(c.Request.Context(), userID, dateFrom, dateTo)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, items)
}

// Trend 用量趋势接口，每日 Token 消费趋势图数据。
// GET /api/v1/open/usage/trend?date_from=2024-01-01&date_to=2024-12-31
func (h *UsageHandler) Trend(c *gin.Context) {
	userID := c.GetUint("userId")
	dateFrom := c.Query("date_from")
	dateTo := c.Query("date_to")

	items, err := h.service.GetUsageTrend(c.Request.Context(), userID, dateFrom, dateTo)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, items)
}
