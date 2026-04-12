package openapi

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/response"
	svc "tokenhub-server/internal/service/openapi"
)

// ConsumptionHandler 处理消费汇总/明细/导出相关的 Open API 请求。
type ConsumptionHandler struct {
	service *svc.OpenAPIService
}

// NewConsumptionHandler 创建消费 Handler 实例。
func NewConsumptionHandler(service *svc.OpenAPIService) *ConsumptionHandler {
	return &ConsumptionHandler{service: service}
}

// Register 注册消费相关路由。
func (h *ConsumptionHandler) Register(rg *gin.RouterGroup) {
	g := rg.Group("/consumption")
	g.GET("/summary", h.Summary)
	g.GET("/details", h.Details)
	g.GET("/export", h.Export)
}

// Summary 消费汇总接口，按日/周/月汇总，支持 date_from/date_to 查询参数。
// GET /api/v1/open/consumption/summary?date_from=2024-01-01&date_to=2024-12-31&group_by=day
func (h *ConsumptionHandler) Summary(c *gin.Context) {
	userID := c.GetUint("userId")
	dateFrom := c.Query("date_from")
	dateTo := c.Query("date_to")
	groupBy := c.DefaultQuery("group_by", "day")

	items, err := h.service.GetConsumptionSummary(c.Request.Context(), userID, dateFrom, dateTo, groupBy)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, items)
}

// Details 消费明细接口，分页，支持 model/date 过滤。
// GET /api/v1/open/consumption/details?model=gpt-4&date_from=2024-01-01&page=1&page_size=20
func (h *ConsumptionHandler) Details(c *gin.Context) {
	userID := c.GetUint("userId")
	modelName := c.Query("model")
	dateFrom := c.Query("date_from")
	dateTo := c.Query("date_to")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	items, total, err := h.service.GetConsumptionDetails(c.Request.Context(), userID, modelName, dateFrom, dateTo, page, pageSize)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.PageResult(c, items, total, page, pageSize)
}

// Export 消费导出接口，返回 CSV 格式。
// GET /api/v1/open/consumption/export?date_from=2024-01-01&date_to=2024-12-31
func (h *ConsumptionHandler) Export(c *gin.Context) {
	userID := c.GetUint("userId")
	dateFrom := c.Query("date_from")
	dateTo := c.Query("date_to")

	rows, err := h.service.ExportConsumptionCSV(c.Request.Context(), userID, dateFrom, dateTo)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}

	filename := fmt.Sprintf("consumption_%d.csv", userID)
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))

	writer := csv.NewWriter(c.Writer)
	for _, row := range rows {
		_ = writer.Write(row)
	}
	writer.Flush()
}
