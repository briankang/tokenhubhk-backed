package user

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/ctxutil"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/privacy"
)

type PrivacyHandler struct {
	svc *privacy.Service
}

func NewPrivacyHandler(svc *privacy.Service) *PrivacyHandler {
	if svc == nil {
		panic("user privacy handler: service is nil")
	}
	return &PrivacyHandler{svc: svc}
}

func (h *PrivacyHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/privacy/requests", h.List)
	rg.GET("/privacy/requests/:id", h.Get)
	rg.POST("/privacy/requests", h.Create)
	rg.POST("/privacy/export", h.ExportData)
	rg.POST("/privacy/delete-account", h.DeleteAccount)
	rg.POST("/privacy/delete-api-logs", h.DeleteAPILogs)
	rg.POST("/privacy/marketing-consent", h.MarketingConsent)
}

type privacyCreateReq struct {
	Type     string         `json:"type" binding:"required"`
	Reason   string         `json:"reason" binding:"max=1000"`
	Scope    string         `json:"scope" binding:"max=64"`
	Region   string         `json:"region" binding:"max=16"`
	Language string         `json:"language" binding:"max=16"`
	Metadata map[string]any `json:"metadata"`
}

func (h *PrivacyHandler) List(c *gin.Context) {
	uid, ok := ctxutil.MustUserID(c)
	if !ok {
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	list, total, err := h.svc.ListUser(c.Request.Context(), privacy.ListUserFilter{
		UserID:   uid,
		Type:     c.Query("type"),
		Status:   c.Query("status"),
		Page:     page,
		PageSize: pageSize,
	})
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.PageResult(c, list, total, page, pageSize)
}

func (h *PrivacyHandler) Get(c *gin.Context) {
	uid, ok := ctxutil.MustUserID(c)
	if !ok {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	req, err := h.svc.GetUserRequest(c.Request.Context(), uint(id), uid)
	if err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrNotFound.Code, "privacy request not found")
		return
	}
	response.Success(c, req)
}

func (h *PrivacyHandler) Create(c *gin.Context) {
	var req privacyCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	h.createTyped(c, req.Type, req.Reason, req.Scope, req.Region, req.Language, req.Metadata)
}

func (h *PrivacyHandler) ExportData(c *gin.Context) {
	h.createTyped(c, "export_data", "User requested personal data export.", "all", "", "", nil)
}

func (h *PrivacyHandler) DeleteAccount(c *gin.Context) {
	var req struct {
		Reason string `json:"reason" binding:"max=1000"`
	}
	_ = c.ShouldBindJSON(&req)
	h.createTyped(c, "delete_account", req.Reason, "account", "", "", nil)
}

func (h *PrivacyHandler) DeleteAPILogs(c *gin.Context) {
	var req struct {
		Scope string `json:"scope" binding:"max=64"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.Scope == "" {
		req.Scope = "payloads_and_details"
	}
	h.createTyped(c, "delete_api_logs", "User requested API log deletion.", req.Scope, "", "", nil)
}

func (h *PrivacyHandler) MarketingConsent(c *gin.Context) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.Enabled {
		response.Success(c, gin.H{"ok": true, "message": "marketing consent enabled"})
		return
	}
	h.createTyped(c, "marketing_opt_out", "User withdrew marketing consent.", "marketing", "", "", nil)
}

func (h *PrivacyHandler) createTyped(c *gin.Context, reqType, reason, scope, region, language string, metadata map[string]any) {
	uid, ok := ctxutil.MustUserID(c)
	if !ok {
		return
	}
	email := ""
	if raw, exists := c.Get("email"); exists {
		if v, ok := raw.(string); ok {
			email = v
		}
	}

	out, err := h.svc.Create(c.Request.Context(), privacy.CreateInput{
		UserID:   uid,
		Email:    email,
		Type:     reqType,
		Region:   region,
		Language: language,
		Reason:   reason,
		Scope:    scope,
		Metadata: metadata,
	})
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, out)
}
