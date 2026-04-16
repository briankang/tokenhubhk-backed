package user

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/withdrawal"
)

// WithdrawalHandler v3.1 用户提现申请 + 查询
// 路径:/api/v1/user/withdrawals
type WithdrawalHandler struct {
	svc *withdrawal.Service
}

// NewWithdrawalHandler 创建 handler 实例
func NewWithdrawalHandler(svc *withdrawal.Service) *WithdrawalHandler {
	return &WithdrawalHandler{svc: svc}
}

// Register 注册路由到 /api/v1/user 组
func (h *WithdrawalHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/withdrawals", h.Create)
	rg.GET("/withdrawals", h.List)
}

// Create 创建提现申请(用户冻结余额 → PENDING)
// POST /api/v1/user/withdrawals { amountCredits: int64, bankInfo: string }
func (h *WithdrawalHandler) Create(c *gin.Context) {
	uid, ok := getUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}
	var req struct {
		AmountCredits int64  `json:"amountCredits" binding:"required"`
		BankInfo      string `json:"bankInfo" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	row, err := h.svc.CreateWithdrawal(c.Request.Context(), uid, req.AmountCredits, req.BankInfo)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, row)
}

// List 分页查询本人的提现记录
// GET /api/v1/user/withdrawals?page=1&page_size=20
func (h *WithdrawalHandler) List(c *gin.Context) {
	uid, ok := getUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	list, total, err := h.svc.ListUserWithdrawals(c.Request.Context(), uid, page, pageSize)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}
	response.PageResult(c, list, total, page, pageSize)
}

// getUserID 从 Gin context 读取当前用户 ID
func getUserID(c *gin.Context) (uint, bool) {
	v, exists := c.Get("userId")
	if !exists {
		return 0, false
	}
	uid, ok := v.(uint)
	if !ok || uid == 0 {
		return 0, false
	}
	return uid, true
}
