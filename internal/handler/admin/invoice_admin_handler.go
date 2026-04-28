package admin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tokenhub-server/internal/middleware/audit"
	"tokenhub-server/internal/pkg/ctxutil"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/pkg/safego"
	"tokenhub-server/internal/pkg/uploader"
	emailsvc "tokenhub-server/internal/service/email"
	invoicesvc "tokenhub-server/internal/service/invoice"
)

// InvoiceAdminHandler 管理员发票审批 Handler
type InvoiceAdminHandler struct {
	svc      *invoicesvc.Service
	uploader *uploader.Client
}

// NewInvoiceAdminHandler 构造
func NewInvoiceAdminHandler(svc *invoicesvc.Service) *InvoiceAdminHandler {
	return &InvoiceAdminHandler{svc: svc, uploader: uploader.New()}
}

// contextBG 返回带超时的背景 ctx（供异步任务使用）
func contextBG(seconds int) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), time.Duration(seconds)*time.Second)
}

// 单 PDF 最大 10MB
const maxPDFSize = 10 * 1024 * 1024

// Register 注册路由到 /admin 组
func (h *InvoiceAdminHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/invoices", h.List)
	rg.GET("/invoices/:id", h.Get)
	rg.POST("/invoices/:id/approve", h.Approve)
	rg.POST("/invoices/:id/reject", h.Reject)
	rg.POST("/invoices/:id/upload-pdf", h.UploadPDF)
	rg.DELETE("/invoices/:id", h.Delete)
}

// Delete 硬删除发票申请(仅 SUPER_ADMIN 场景,用于维护/清理脏数据)
func (h *InvoiceAdminHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	if old, err := h.svc.GetByID(c.Request.Context(), uint(id)); err == nil {
		audit.SetOldValue(c, old)
	}
	if err := h.svc.HardDelete(c.Request.Context(), uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"ok": true})
}

// List 列表查询
func (h *InvoiceAdminHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 20
	}

	list, total, err := h.svc.ListAdmin(c.Request.Context(), invoicesvc.ListAdminFilter{
		Status:      c.Query("status"),
		Region:      c.Query("region"),
		InvoiceType: c.Query("invoice_type"),
		Keyword:     c.Query("keyword"),
		Page:        page,
		PageSize:    pageSize,
	})
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.PageResult(c, list, total, page, pageSize)
}

// Get 详情
func (h *InvoiceAdminHandler) Get(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	r, err := h.svc.GetByID(c.Request.Context(), uint(id))
	if err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrNotFound.Code, "invoice not found")
		return
	}
	response.Success(c, r)
}

type approveReq struct {
	Remark string `json:"remark" binding:"max=500"`
}

// Approve 通过申请（标记为 approved，等待上传 PDF）
func (h *InvoiceAdminHandler) Approve(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	var req approveReq
	_ = c.ShouldBindJSON(&req)

	uid, _ := ctxutil.UserID(c)

	// 记 old value for audit
	if old, err := h.svc.GetByID(c.Request.Context(), uint(id)); err == nil {
		audit.SetOldValue(c, old)
	}

	if err := h.svc.Approve(c.Request.Context(), uint(id), uid, req.Remark); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"ok": true})
}

type rejectReq struct {
	Reason string `json:"reason" binding:"required,min=2,max=500"`
}

// Reject 拒绝申请
func (h *InvoiceAdminHandler) Reject(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	var req rejectReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	uid, _ := ctxutil.UserID(c)

	if old, err := h.svc.GetByID(c.Request.Context(), uint(id)); err == nil {
		audit.SetOldValue(c, old)
	}

	if err := h.svc.Reject(c.Request.Context(), uint(id), uid, req.Reason); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"ok": true})
}

// UploadPDF 支持两种方式:
//  1. JSON: { pdf_url: "<already-hosted url>" } —— 管理员自行上传到 OSS 后粘贴 URL
//  2. multipart/form-data: field=pdf —— 后端代理到 catbox.moe
//
// 任一方式成功均将状态推进到 issued。
func (h *InvoiceAdminHandler) UploadPDF(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	if old, err := h.svc.GetByID(c.Request.Context(), uint(id)); err == nil {
		audit.SetOldValue(c, old)
	}

	var pdfURL string
	ct := c.ContentType()

	if strings.HasPrefix(ct, "multipart/") {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxPDFSize+1024)
		fh, err := c.FormFile("pdf")
		if err != nil {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "missing 'pdf' field or file too large")
			return
		}
		if fh.Size > maxPDFSize {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "pdf exceeds 10MB")
			return
		}
		ext := strings.ToLower(filepath.Ext(fh.Filename))
		if ext != ".pdf" {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "only .pdf files allowed")
			return
		}
		f, err := fh.Open()
		if err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
			return
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, maxPDFSize+1))
		if err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
			return
		}
		if len(data) > maxPDFSize {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "pdf exceeds 10MB")
			return
		}
		u, _, err := h.uploader.UploadWithFallback(c.Request.Context(), data, fh.Filename)
		if err != nil {
			response.ErrorMsg(c, http.StatusBadGateway, 50201, "failed to upload pdf: "+err.Error())
			return
		}
		pdfURL = u
	} else {
		var body struct {
			PDFURL string `json:"pdf_url" binding:"required,url,max=500"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
			return
		}
		pdfURL = body.PDFURL
	}

	if err := h.svc.UploadPDF(c.Request.Context(), uint(id), pdfURL); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	// 异步发送发票通知邮件（失败仅记录，不阻塞响应）
	safego.Go("invoice-email-notify", func() {
		h.sendInvoiceIssuedEmail(uint(id), pdfURL)
	})

	response.Success(c, gin.H{"ok": true, "pdf_url": pdfURL})
}

// sendInvoiceIssuedEmail 异步发送发票开具通知
func (h *InvoiceAdminHandler) sendInvoiceIssuedEmail(invoiceID uint, pdfURL string) {
	if emailsvc.Default == nil {
		return
	}
	ctx, cancel := contextBG(30)
	defer cancel()
	inv, err := h.svc.GetByID(ctx, invoiceID)
	if err != nil || inv == nil || inv.Email == "" {
		return
	}
	invoiceNo := fmt.Sprintf("INV%06d", inv.ID)
	vars := map[string]any{
		"Name":        inv.Title,
		"TitleName":   inv.Title,
		"Amount":      fmt.Sprintf("%.2f", inv.AmountRMB),
		"InvoiceNo":   invoiceNo,
		"DownloadURL": pdfURL,
	}
	_, err = emailsvc.Default.SendByTemplate(ctx, emailsvc.SendByTemplateRequest{
		TemplateCode: "invoice_issued",
		To:           []string{inv.Email},
		Variables:    vars,
		TriggeredBy:  "invoice",
	})
	if err != nil {
		logger.L.Warn("send invoice issued email failed",
			zap.Uint("invoice_id", invoiceID),
			zap.String("to", inv.Email),
			zap.Error(err),
		)
	}
}
