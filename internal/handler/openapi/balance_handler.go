package openapi

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/response"
	svc "tokenhub-server/internal/service/openapi"
)

// BalanceHandler 处理余额/充值记录相关的 Open API 请求。
type BalanceHandler struct {
	service *svc.OpenAPIService
}

// NewBalanceHandler 创建余额 Handler 实例。
func NewBalanceHandler(service *svc.OpenAPIService) *BalanceHandler {
	return &BalanceHandler{service: service}
}

// Register 注册余额相关路由。
func (h *BalanceHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/balance", h.GetBalance)
	rg.GET("/balance/recharge-records", h.RechargeRecords)
}

// GetBalance 获取当前余额 + 免费额度 + 已消费信息。
// GET /api/v1/open/balance
func (h *BalanceHandler) GetBalance(c *gin.Context) {
	userID := c.GetUint("userId")

	info, err := h.service.GetBalance(c.Request.Context(), userID)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.Success(c, info)
}

// RechargeRecords 获取充值记录列表。
// GET /api/v1/open/balance/recharge-records?page=1&page_size=20
func (h *BalanceHandler) RechargeRecords(c *gin.Context) {
	userID := c.GetUint("userId")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	items, total, err := h.service.GetRechargeRecords(c.Request.Context(), userID, page, pageSize)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, err.Error())
		return
	}
	response.PageResult(c, items, total, page, pageSize)
}
