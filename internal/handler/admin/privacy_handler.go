package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	auditmw "tokenhub-server/internal/middleware/audit"
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
		panic("admin privacy handler: service is nil")
	}
	return &PrivacyHandler{svc: svc}
}

func (h *PrivacyHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/privacy/requests", h.List)
	rg.GET("/privacy/requests/:id", h.Get)
	rg.PATCH("/privacy/requests/:id", h.Update)
	rg.POST("/privacy/requests/:id/complete", h.Complete)
	rg.POST("/privacy/requests/:id/reject", h.Reject)
}

func (h *PrivacyHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	userID, _ := strconv.ParseUint(c.Query("user_id"), 10, 64)

	list, total, err := h.svc.ListAdmin(c.Request.Context(), privacy.ListAdminFilter{
		UserID:   uint(userID),
		Type:     c.Query("type"),
		Status:   c.Query("status"),
		Region:   c.Query("region"),
		Keyword:  c.Query("keyword"),
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
	id, ok := parsePrivacyID(c)
	if !ok {
		return
	}
	req, err := h.svc.GetByID(c.Request.Context(), id)
	if err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrNotFound.Code, "privacy request not found")
		return
	}
	response.Success(c, req)
}

type privacyUpdateReq struct {
	Status         string `json:"status" binding:"omitempty,oneof=received identity_verification_required in_review processing completed rejected cancelled"`
	AssignedTo     uint   `json:"assigned_to"`
	ResolutionNote string `json:"resolution_note" binding:"max=2000"`
	RejectReason   string `json:"reject_reason" binding:"max=2000"`
	Verified       bool   `json:"verified"`
}

func (h *PrivacyHandler) Update(c *gin.Context) {
	id, ok := parsePrivacyID(c)
	if !ok {
		return
	}
	var req privacyUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	if old, err := h.svc.GetByID(c.Request.Context(), id); err == nil {
		auditmw.SetOldValue(c, old)
	}
	out, err := h.svc.Update(c.Request.Context(), id, privacy.UpdateInput{
		Status:         req.Status,
		AssignedTo:     req.AssignedTo,
		ResolutionNote: req.ResolutionNote,
		RejectReason:   req.RejectReason,
		Verified:       req.Verified,
	})
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, out)
}

func (h *PrivacyHandler) Complete(c *gin.Context) {
	id, ok := parsePrivacyID(c)
	if !ok {
		return
	}
	var req struct {
		ResolutionNote string `json:"resolution_note" binding:"max=2000"`
	}
	_ = c.ShouldBindJSON(&req)
	adminID, _ := ctxutil.UserID(c)
	if old, err := h.svc.GetByID(c.Request.Context(), id); err == nil {
		auditmw.SetOldValue(c, old)
	}
	out, err := h.svc.Update(c.Request.Context(), id, privacy.UpdateInput{
		Status:         "completed",
		AssignedTo:     adminID,
		ResolutionNote: req.ResolutionNote,
	})
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, out)
}

func (h *PrivacyHandler) Reject(c *gin.Context) {
	id, ok := parsePrivacyID(c)
	if !ok {
		return
	}
	var req struct {
		RejectReason string `json:"reject_reason" binding:"required,min=2,max=2000"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	adminID, _ := ctxutil.UserID(c)
	if old, err := h.svc.GetByID(c.Request.Context(), id); err == nil {
		auditmw.SetOldValue(c, old)
	}
	out, err := h.svc.Update(c.Request.Context(), id, privacy.UpdateInput{
		Status:       "rejected",
		AssignedTo:   adminID,
		RejectReason: req.RejectReason,
	})
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, out)
}

func parsePrivacyID(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return 0, false
	}
	return uint(id), true
}
