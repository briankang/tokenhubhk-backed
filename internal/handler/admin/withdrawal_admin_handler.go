package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/withdrawal"
)

// WithdrawalAdminHandler v3.1 提现审核
type WithdrawalAdminHandler struct {
	svc *withdrawal.Service
}

// NewWithdrawalAdminHandler 创建 handler 实例
func NewWithdrawalAdminHandler(svc *withdrawal.Service) *WithdrawalAdminHandler {
	return &WithdrawalAdminHandler{svc: svc}
}

// Register 注册路由到管理员路由组
func (h *WithdrawalAdminHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/withdrawals", h.List)
	rg.POST("/withdrawals/:id/approve", h.Approve)
	rg.POST("/withdrawals/:id/reject", h.Reject)
	rg.POST("/withdrawals/:id/mark-paid", h.MarkPaid)
}

// List 分页查询所有提现请求(可按状态过滤)
// GET /api/v1/admin/withdrawals?status=PENDING&page=1&page_size=20
func (h *WithdrawalAdminHandler) List(c *gin.Context) {
	status := c.Query("status")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	list, total, err := h.svc.ListWithdrawals(c.Request.Context(), status, page, pageSize)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}
	response.PageResult(c, list, total, page, pageSize)
}

// Approve 审核通过
// POST /api/v1/admin/withdrawals/:id/approve { "remark": "..." }
func (h *WithdrawalAdminHandler) Approve(c *gin.Context) {
	id := h.parseID(c)
	if id == 0 {
		return
	}
	var req struct {
		Remark string `json:"remark"`
	}
	_ = c.ShouldBindJSON(&req)
	adminID := getAdminID(c)
	if err := h.svc.Approve(c.Request.Context(), id, adminID, req.Remark); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"id": id, "status": "APPROVED"})
}

// Reject 审核拒绝并回退余额
// POST /api/v1/admin/withdrawals/:id/reject { "reason": "..." }
func (h *WithdrawalAdminHandler) Reject(c *gin.Context) {
	id := h.parseID(c)
	if id == 0 {
		return
	}
	var req struct {
		Reason string `json:"reason" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	adminID := getAdminID(c)
	if err := h.svc.Reject(c.Request.Context(), id, adminID, req.Reason); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"id": id, "status": "REJECTED"})
}

// MarkPaid 标记已打款
// POST /api/v1/admin/withdrawals/:id/mark-paid { "bankTxnId": "..." }
func (h *WithdrawalAdminHandler) MarkPaid(c *gin.Context) {
	id := h.parseID(c)
	if id == 0 {
		return
	}
	var req struct {
		BankTxnID string `json:"bankTxnId"`
	}
	_ = c.ShouldBindJSON(&req)
	adminID := getAdminID(c)
	if err := h.svc.MarkPaid(c.Request.Context(), id, adminID, req.BankTxnID); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"id": id, "status": "COMPLETED"})
}

// getAdminID 从 Gin context 读取当前管理员 ID (由 Auth 中间件注入)
func getAdminID(c *gin.Context) uint {
	if v, ok := c.Get("userId"); ok {
		if u, ok := v.(uint); ok {
			return u
		}
	}
	return 0
}

// parseID 解析 URL param :id,失败时已写入 400 响应并返回 0
func (h *WithdrawalAdminHandler) parseID(c *gin.Context) uint {
	idStr := c.Param("id")
	n, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil || n == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return 0
	}
	return uint(n)
}
