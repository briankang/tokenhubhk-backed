package user

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/balance"
)

// BalanceHandler 用户余额查询接口处理器
type BalanceHandler struct {
	balanceSvc *balance.BalanceService
}

// NewBalanceHandler 创建用户余额Handler实例
func NewBalanceHandler(svc *balance.BalanceService) *BalanceHandler {
	return &BalanceHandler{balanceSvc: svc}
}

// Register 注册用户余额相关路由到路由组
func (h *BalanceHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/balance", h.GetBalance)
	rg.GET("/balance/records", h.ListRecords)
}

// GetBalance 获取用户余额 GET /api/v1/user/balance
func (h *BalanceHandler) GetBalance(c *gin.Context) {
	userID, exists := c.Get("userId")
	if !exists {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}
	uid, ok := userID.(uint)
	if !ok || uid == 0 {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	tenantID, _ := c.Get("tenantId")
	tid, _ := tenantID.(uint)

	ub, err := h.balanceSvc.GetBalanceCached(c.Request.Context(), uid, tid)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, ub)
}

// ListRecords 获取用户余额变动记录 GET /api/v1/user/balance/records
func (h *BalanceHandler) ListRecords(c *gin.Context) {
	userID, exists := c.Get("userId")
	if !exists {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}
	uid, ok := userID.(uint)
	if !ok || uid == 0 {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	records, total, err := h.balanceSvc.ListRecords(c.Request.Context(), uid, page, pageSize)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.PageResult(c, records, total, page, pageSize)
}
