package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	paymentsvc "tokenhub-server/internal/service/payment"
)

// PaymentConfigHandler 支付配置管理 Handler
type PaymentConfigHandler struct {
	svc *paymentsvc.PaymentConfigService
}

// NewPaymentConfigHandler 创建支付配置管理 Handler 实例
func NewPaymentConfigHandler(svc *paymentsvc.PaymentConfigService) *PaymentConfigHandler {
	if svc == nil {
		panic("payment config handler: service is nil")
	}
	return &PaymentConfigHandler{svc: svc}
}

// Register 注册支付配置管理路由到 admin 路由组
func (h *PaymentConfigHandler) Register(rg *gin.RouterGroup) {
	pg := rg.Group("/payment")
	{
		// 支付渠道配置
		pg.GET("/providers", h.GetAllProviders)
		pg.PUT("/providers/:type", h.UpdateProvider)
		pg.PATCH("/providers/:type/toggle", h.ToggleProvider)
		pg.POST("/providers/:type/test", h.TestProvider)

		// 银行账号管理
		pg.GET("/bank-accounts", h.GetAllBankAccounts)
		pg.POST("/bank-accounts", h.CreateBankAccount)
		pg.PUT("/bank-accounts/:id", h.UpdateBankAccount)
		pg.DELETE("/bank-accounts/:id", h.DeleteBankAccount)

		// 付款方式管理
		pg.GET("/methods", h.GetAllPaymentMethods)
		pg.PUT("/methods/:type", h.UpdatePaymentMethod)
		pg.PATCH("/methods/:type/toggle", h.TogglePaymentMethod)
	}
}

// ==================== 支付渠道配置 ====================

// GetAllProviders 获取所有支付渠道配置
func (h *PaymentConfigHandler) GetAllProviders(c *gin.Context) {
	providers, err := h.svc.GetAllProviders(c.Request.Context())
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, providers)
}

// UpdateProvider 更新支付渠道配置
func (h *PaymentConfigHandler) UpdateProvider(c *gin.Context) {
	providerType := c.Param("type")
	if providerType == "" {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	var updates map[string]interface{}
	if err := c.ShouldBindJSON(&updates); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	if err := h.svc.UpdateProvider(c.Request.Context(), providerType, updates); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"message": "updated"})
}

// ToggleProvider 启用/停用支付渠道
func (h *PaymentConfigHandler) ToggleProvider(c *gin.Context) {
	providerType := c.Param("type")
	if providerType == "" {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	provider, err := h.svc.ToggleProvider(c.Request.Context(), providerType)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, provider)
}

// ==================== 银行账号管理 ====================

// GetAllBankAccounts 获取所有银行账号
func (h *PaymentConfigHandler) GetAllBankAccounts(c *gin.Context) {
	accounts, err := h.svc.GetAllBankAccounts(c.Request.Context())
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, accounts)
}

// CreateBankAccount 创建银行账号
func (h *PaymentConfigHandler) CreateBankAccount(c *gin.Context) {
	var account model.BankAccount
	if err := c.ShouldBindJSON(&account); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}
	if account.AccountName == "" || account.BankName == "" || account.AccountNumber == "" {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, "account_name, bank_name, account_number are required")
		return
	}

	if err := h.svc.CreateBankAccount(c.Request.Context(), &account); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, account)
}

// UpdateBankAccount 更新银行账号
func (h *PaymentConfigHandler) UpdateBankAccount(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	var updates map[string]interface{}
	if err := c.ShouldBindJSON(&updates); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	if err := h.svc.UpdateBankAccount(c.Request.Context(), uint(id), updates); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"message": "updated"})
}

// DeleteBankAccount 删除银行账号
func (h *PaymentConfigHandler) DeleteBankAccount(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	if err := h.svc.DeleteBankAccount(c.Request.Context(), uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"message": "deleted"})
}

// ==================== 付款方式管理 ====================

// GetAllPaymentMethods 获取所有付款方式列表
func (h *PaymentConfigHandler) GetAllPaymentMethods(c *gin.Context) {
	methods, err := h.svc.GetAllPaymentMethods(c.Request.Context())
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, methods)
}

// UpdatePaymentMethod 更新付款方式
func (h *PaymentConfigHandler) UpdatePaymentMethod(c *gin.Context) {
	methodType := c.Param("type")
	if methodType == "" {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	var updates map[string]interface{}
	if err := c.ShouldBindJSON(&updates); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	if err := h.svc.UpdatePaymentMethod(c.Request.Context(), methodType, updates); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"message": "updated"})
}

// TogglePaymentMethod 启用/停用付款方式
func (h *PaymentConfigHandler) TogglePaymentMethod(c *gin.Context) {
	methodType := c.Param("type")
	if methodType == "" {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	method, err := h.svc.TogglePaymentMethod(c.Request.Context(), methodType)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, method)
}

// ==================== 公开接口 ====================

// GetActivePaymentMethods 获取可用付款方式（公开接口，无需登录）
func (h *PaymentConfigHandler) GetActivePaymentMethods(c *gin.Context) {
	methods, err := h.svc.GetActivePaymentMethods(c.Request.Context())
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, methods)
}
