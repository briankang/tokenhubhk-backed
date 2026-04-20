package user

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	paymentsvc "tokenhub-server/internal/service/payment"
)

// RefundHandler 用户退款申请 Handler
type RefundHandler struct {
	svc *paymentsvc.RefundService
}

// NewRefundHandler 构造函数
func NewRefundHandler(svc *paymentsvc.RefundService) *RefundHandler {
	return &RefundHandler{svc: svc}
}

// Register 注册路由到用户组（JWT 认证）
// Register 注册到 /api/v1/user 组（因此路径不含 /user 前缀）
func (h *RefundHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/refund-requests", h.Submit)
	rg.GET("/refund-requests", h.List)
	rg.GET("/refund-requests/:id", h.Get)
}

type submitRefundReq struct {
	PaymentID   uint64   `json:"payment_id" binding:"required"`
	AmountRMB   float64  `json:"amount_rmb" binding:"required,gt=0"`
	Reason      string   `json:"reason" binding:"required,min=10,max=500"`
	Attachments []string `json:"attachments"`
}

// Submit 提交退款申请
func (h *RefundHandler) Submit(c *gin.Context) {
	var req submitRefundReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	userID, _ := c.Get("userId")
	tenantID, _ := c.Get("tenantId")
	uid := uint64(userID.(uint))
	tid := uint64(0)
	if tenantID != nil {
		tid = uint64(tenantID.(uint))
	}

	r, err := h.svc.SubmitUserRequest(c.Request.Context(), paymentsvc.SubmitUserRequestInput{
		UserID:      uid,
		TenantID:    tid,
		PaymentID:   req.PaymentID,
		AmountRMB:   req.AmountRMB,
		Reason:      req.Reason,
		Attachments: req.Attachments,
		IP:          c.ClientIP(),
		UserAgent:   c.Request.UserAgent(),
	})
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, r)
}

// List 列出当前用户的退款申请
func (h *RefundHandler) List(c *gin.Context) {
	userID, _ := c.Get("userId")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	list, total, err := h.svc.ListUserRequests(c.Request.Context(), uint64(userID.(uint)), page, pageSize)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.PageResult(c, list, total, page, pageSize)
}

// Get 单条退款申请详情
func (h *RefundHandler) Get(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	r, err := h.svc.GetByID(c.Request.Context(), id)
	if err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrNotFound.Code, "refund not found")
		return
	}
	userID, _ := c.Get("userId")
	if r.UserID != uint64(userID.(uint)) {
		response.ErrorMsg(c, http.StatusForbidden, 20010, "forbidden")
		return
	}
	response.Success(c, r)
}
