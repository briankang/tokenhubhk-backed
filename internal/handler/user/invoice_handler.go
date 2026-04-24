package user

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/ctxutil"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	invoicesvc "tokenhub-server/internal/service/invoice"
)

// InvoiceHandler 用户发票 Handler
type InvoiceHandler struct {
	svc *invoicesvc.Service
}

// NewInvoiceHandler 构造
func NewInvoiceHandler(svc *invoicesvc.Service) *InvoiceHandler {
	return &InvoiceHandler{svc: svc}
}

// submitInvoiceReq 提交发票申请请求体
type submitInvoiceReq struct {
	PaymentID   uint   `json:"payment_id" binding:"required"`
	Region      string `json:"region" binding:"required,oneof=CN OVERSEAS"`
	InvoiceType string `json:"invoice_type" binding:"required,oneof=personal company vat_invoice"`
	Title       string `json:"title" binding:"required,min=1,max=200"`
	TaxID       string `json:"tax_id" binding:"max=64"`
	BankName    string `json:"bank_name" binding:"max=200"`
	BankAccount string `json:"bank_account" binding:"max=64"`
	Address     string `json:"address" binding:"max=500"`
	Phone       string `json:"phone" binding:"max=50"`
	Country     string `json:"country" binding:"max=64"`
	Email       string `json:"email" binding:"required,email,max=200"`
	Remark      string `json:"remark" binding:"max=500"`
}

// Submit 用户提交发票申请
func (h *InvoiceHandler) Submit(c *gin.Context) {
	var req submitInvoiceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	uid, ok := ctxutil.MustUserID(c)
	if !ok {
		return
	}
	tid := ctxutil.TenantID(c)

	r, err := h.svc.Submit(c.Request.Context(), invoicesvc.SubmitInput{
		UserID:      uid,
		TenantID:    tid,
		PaymentID:   req.PaymentID,
		Region:      req.Region,
		InvoiceType: req.InvoiceType,
		Title:       req.Title,
		TaxID:       req.TaxID,
		BankName:    req.BankName,
		BankAccount: req.BankAccount,
		Address:     req.Address,
		Phone:       req.Phone,
		Country:     req.Country,
		Email:       req.Email,
		Remark:      req.Remark,
	})
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, r)
}

// List 用户查询自己的发票记录
func (h *InvoiceHandler) List(c *gin.Context) {
	uid, ok := ctxutil.MustUserID(c)
	if !ok {
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	list, total, err := h.svc.ListUserRequests(c.Request.Context(), uid, page, pageSize)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.PageResult(c, list, total, page, pageSize)
}

// ── 发票抬头管理(快捷选择) ───────────────────────────────────────────────

type titleReq struct {
	Region      string `json:"region" binding:"required,oneof=CN OVERSEAS"`
	InvoiceType string `json:"invoice_type" binding:"required,oneof=personal company vat_invoice"`
	Title       string `json:"title" binding:"required,min=1,max=200"`
	TaxID       string `json:"tax_id" binding:"max=64"`
	BankName    string `json:"bank_name" binding:"max=200"`
	BankAccount string `json:"bank_account" binding:"max=64"`
	Address     string `json:"address" binding:"max=500"`
	Phone       string `json:"phone" binding:"max=50"`
	Country     string `json:"country" binding:"max=64"`
	Email       string `json:"email" binding:"omitempty,email,max=200"`
	Alias       string `json:"alias" binding:"max=100"`
	IsDefault   bool   `json:"is_default"`
}

// ListTitles 列出保存的抬头
func (h *InvoiceHandler) ListTitles(c *gin.Context) {
	uid, ok := ctxutil.MustUserID(c)
	if !ok {
		return
	}
	list, err := h.svc.ListTitles(c.Request.Context(), uid)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, list)
}

// CreateTitle 保存新抬头
func (h *InvoiceHandler) CreateTitle(c *gin.Context) {
	var req titleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	uid, ok := ctxutil.MustUserID(c)
	if !ok {
		return
	}
	tid := ctxutil.TenantID(c)
	t, err := h.svc.CreateTitle(c.Request.Context(), invoicesvc.TitleInput{
		UserID:      uid,
		TenantID:    tid,
		Region:      req.Region,
		InvoiceType: req.InvoiceType,
		Title:       req.Title,
		TaxID:       req.TaxID,
		BankName:    req.BankName,
		BankAccount: req.BankAccount,
		Address:     req.Address,
		Phone:       req.Phone,
		Country:     req.Country,
		Email:       req.Email,
		Alias:       req.Alias,
		IsDefault:   req.IsDefault,
	})
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, t)
}

// UpdateTitle 更新抬头
func (h *InvoiceHandler) UpdateTitle(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	var req titleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	uid, ok := ctxutil.MustUserID(c)
	if !ok {
		return
	}
	if err := h.svc.UpdateTitle(c.Request.Context(), uint(id), uid, invoicesvc.TitleInput{
		UserID:      uid,
		Region:      req.Region,
		InvoiceType: req.InvoiceType,
		Title:       req.Title,
		TaxID:       req.TaxID,
		BankName:    req.BankName,
		BankAccount: req.BankAccount,
		Address:     req.Address,
		Phone:       req.Phone,
		Country:     req.Country,
		Email:       req.Email,
		Alias:       req.Alias,
		IsDefault:   req.IsDefault,
	}); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"ok": true})
}

// DeleteTitle 删除抬头
func (h *InvoiceHandler) DeleteTitle(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	uid, ok := ctxutil.MustUserID(c)
	if !ok {
		return
	}
	if err := h.svc.DeleteTitle(c.Request.Context(), uint(id), uid); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"ok": true})
}

// Get 单条详情
func (h *InvoiceHandler) Get(c *gin.Context) {
	uid, ok := ctxutil.MustUserID(c)
	if !ok {
		return
	}
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	r, err := h.svc.GetByID(c.Request.Context(), uint(id))
	if err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrNotFound.Code, "invoice not found")
		return
	}
	if r.UserID != uid {
		response.ErrorMsg(c, http.StatusForbidden, 20010, "forbidden")
		return
	}
	response.Success(c, r)
}
