package payment

import (
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/response"
	paymentsvc "tokenhub-server/internal/service/payment"
)

// CallbackHandler 支付网关回调/Webhook通知处理器
type CallbackHandler struct {
	svc *paymentsvc.PaymentService
}

// NewCallbackHandler 创建支付回调Handler实例
func NewCallbackHandler(svc *paymentsvc.PaymentService) *CallbackHandler {
	return &CallbackHandler{svc: svc}
}

// RegisterCallbacks 注册支付回调路由（无需JWT认证，通过签名验证安全性）
func (h *CallbackHandler) RegisterCallbacks(rg *gin.RouterGroup) {
	rg.POST("/callback/wechat", h.WechatCallback)
	rg.POST("/callback/alipay", h.AlipayCallback)
	rg.POST("/callback/stripe", h.StripeCallback)
	rg.POST("/callback/paypal", h.PayPalCallback)
}

// WechatCallback 处理微信支付回调通知 POST /api/v1/callback/wechat
func (h *CallbackHandler) WechatCallback(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": "FAIL", "message": "read body failed"})
		return
	}

	headers := map[string]string{
		"Wechatpay-Timestamp": c.GetHeader("Wechatpay-Timestamp"),
		"Wechatpay-Nonce":     c.GetHeader("Wechatpay-Nonce"),
		"Wechatpay-Signature": c.GetHeader("Wechatpay-Signature"),
		"Wechatpay-Serial":    c.GetHeader("Wechatpay-Serial"),
	}

	if err := h.svc.HandleCallback(c.Request.Context(), "wechat", body, headers); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": "FAIL", "message": err.Error()})
		return
	}

	// 微信支付要求返回此特定格式
	c.JSON(http.StatusOK, gin.H{"code": "SUCCESS", "message": "ok"})
}

// AlipayCallback 处理支付宝回调通知 POST /api/v1/callback/alipay
func (h *CallbackHandler) AlipayCallback(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.String(http.StatusBadRequest, "fail")
		return
	}

	headers := make(map[string]string)
	if err := h.svc.HandleCallback(c.Request.Context(), "alipay", body, headers); err != nil {
		c.String(http.StatusBadRequest, "fail")
		return
	}

	// 支付宝要求返回纯文本"success"
	c.String(http.StatusOK, "success")
}

// StripeCallback 处理Stripe Webhook回调 POST /api/v1/callback/stripe
func (h *CallbackHandler) StripeCallback(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, "read body failed")
		return
	}

	headers := map[string]string{
		"Stripe-Signature": c.GetHeader("Stripe-Signature"),
	}

	if err := h.svc.HandleCallback(c.Request.Context(), "stripe", body, headers); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, err.Error())
		return
	}

	c.JSON(http.StatusOK, gin.H{"received": true})
}

// PayPalCallback 处理PayPal Webhook回调 POST /api/v1/callback/paypal
func (h *CallbackHandler) PayPalCallback(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, "read body failed")
		return
	}

	headers := map[string]string{
		"Paypal-Transmission-Id":   c.GetHeader("Paypal-Transmission-Id"),
		"Paypal-Transmission-Time": c.GetHeader("Paypal-Transmission-Time"),
		"Paypal-Transmission-Sig":  c.GetHeader("Paypal-Transmission-Sig"),
		"Paypal-Cert-Url":          c.GetHeader("Paypal-Cert-Url"),
		"Paypal-Auth-Algo":         c.GetHeader("Paypal-Auth-Algo"),
	}

	if err := h.svc.HandleCallback(c.Request.Context(), "paypal", body, headers); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, err.Error())
		return
	}

	c.JSON(http.StatusOK, gin.H{"received": true})
}
