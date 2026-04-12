package payment

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	paymentsvc "tokenhub-server/internal/service/payment"
)

// PaymentHandler 支付接口处理器，负责创建支付、查询、列表和退款
type PaymentHandler struct {
	svc *paymentsvc.PaymentService
}

// NewPaymentHandler 创建支付Handler实例
func NewPaymentHandler(svc *paymentsvc.PaymentService) *PaymentHandler {
	return &PaymentHandler{svc: svc}
}

// Register 注册支付相关路由到路由组（需要JWT认证）
func (h *PaymentHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/payment/create", h.Create)
	rg.GET("/payment/query/:orderNo", h.Query)
	rg.GET("/payment/list", h.List)
	rg.POST("/payment/refund/:orderNo", h.Refund)
}

// createPaymentReq 创建支付请求结构体
type createPaymentReq struct {
	Gateway   string  `json:"gateway" binding:"required"`   // wechat / alipay / stripe / paypal
	Amount    float64 `json:"amount" binding:"required,gt=0"`
	Currency  string  `json:"currency" binding:"required"`  // CNY / USD
	Subject   string  `json:"subject" binding:"required"`
	ReturnURL string  `json:"return_url"`
}

// Create 创建支付订单 POST /api/v1/payment/create
func (h *PaymentHandler) Create(c *gin.Context) {
	var req createPaymentReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	// 验证支付网关是否合法
	validGateways := map[string]bool{"wechat": true, "alipay": true, "stripe": true, "paypal": true}
	if !validGateways[req.Gateway] {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "invalid gateway")
		return
	}

	// 验证货币与网关是否匹配（微信/支付宝仅支持CNY，Stripe/PayPal不支持CNY）
	if (req.Gateway == "wechat" || req.Gateway == "alipay") && req.Currency != "CNY" {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "wechat/alipay only supports CNY")
		return
	}
	if (req.Gateway == "stripe" || req.Gateway == "paypal") && req.Currency == "CNY" {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "stripe/paypal does not support CNY")
		return
	}

	userID, _ := c.Get("userId")
	tenantID, _ := c.Get("tenantId")
	clientIP := c.ClientIP()

	result, err := h.svc.CreatePayment(
		c.Request.Context(),
		userID.(uint),
		tenantID.(uint),
		req.Gateway,
		req.Amount,
		req.Currency,
		req.Subject,
		req.ReturnURL,
		clientIP,
	)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrPaymentFailed.Code, err.Error())
		return
	}

	response.Success(c, result)
}

// Query 查询支付订单 GET /api/v1/payment/query/:orderNo
func (h *PaymentHandler) Query(c *gin.Context) {
	orderNo := c.Param("orderNo")
	if orderNo == "" {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	payment, err := h.svc.QueryPayment(c.Request.Context(), orderNo)
	if err != nil {
		response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
		return
	}

	// 验证订单是否属于当前请求用户
	userID, _ := c.Get("userId")
	if payment.UserID != userID.(uint) {
		response.Error(c, http.StatusForbidden, errcode.ErrPermissionDenied)
		return
	}

	response.Success(c, payment)
}

// List 获取用户支付订单列表 GET /api/v1/payment/list
func (h *PaymentHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	userID, _ := c.Get("userId")
	payments, total, err := h.svc.ListPayments(c.Request.Context(), userID.(uint), page, pageSize)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.PageResult(c, payments, total, page, pageSize)
}

// refundReq 退款请求结构体
type refundReq struct {
	Amount float64 `json:"amount" binding:"required,gt=0"`
	Reason string  `json:"reason" binding:"required"`
}

// Refund 处理退款请求 POST /api/v1/payment/refund/:orderNo（需要管理员权限）
func (h *PaymentHandler) Refund(c *gin.Context) {
	orderNo := c.Param("orderNo")
	if orderNo == "" {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var req refundReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	userID, _ := c.Get("userId")
	clientIP := c.ClientIP()

	if err := h.svc.RefundPayment(c.Request.Context(), orderNo, req.Amount, req.Reason, clientIP, userID.(uint)); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrRefundFailed.Code, err.Error())
		return
	}

	response.Success(c, nil)
}
