package admin

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	auditctx "tokenhub-server/internal/middleware/audit"
	"tokenhub-server/internal/pkg/ctxutil"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/referral"
)

// CommissionRuleHandler 特殊返佣规则管理（用户×模型维度）
type CommissionRuleHandler struct {
	svc *referral.CommissionRuleService
}

// NewCommissionRuleHandler 构造
func NewCommissionRuleHandler(svc *referral.CommissionRuleService) *CommissionRuleHandler {
	return &CommissionRuleHandler{svc: svc}
}

// Register 注册路由
func (h *CommissionRuleHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/commission-rules", h.List)
	rg.GET("/commission-rules/:id", h.Get)
	rg.POST("/commission-rules", h.Create)
	rg.PUT("/commission-rules/:id", h.Update)
	rg.DELETE("/commission-rules/:id", h.Delete)
	rg.POST("/commission-rules/:id/toggle", h.Toggle)
}

type createRuleRequest struct {
	Name           string     `json:"name" binding:"required"`
	CommissionRate float64    `json:"commission_rate"`
	Priority       int        `json:"priority"`
	EffectiveFrom  *time.Time `json:"effective_from"`
	EffectiveTo    *time.Time `json:"effective_to"`
	Note           string     `json:"note"`
	UserIDs        []uint     `json:"user_ids" binding:"required"`
	ModelIDs       []uint     `json:"model_ids" binding:"required"`
}

// Create POST /admin/commission-rules
func (h *CommissionRuleHandler) Create(c *gin.Context) {
	var req createRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	operatorID, _ := ctxutil.UserID(c)
	in := referral.CreateInput{
		Name:           req.Name,
		CommissionRate: req.CommissionRate,
		Priority:       req.Priority,
		Note:           req.Note,
		UserIDs:        req.UserIDs,
		ModelIDs:       req.ModelIDs,
		CreatedBy:      operatorID,
	}
	if req.EffectiveFrom != nil {
		in.EffectiveFrom = *req.EffectiveFrom
	}
	in.EffectiveTo = req.EffectiveTo

	detail, err := h.svc.Create(c.Request.Context(), in)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	auditctx.SetResourceID(c, detail.ID)
	response.Success(c, detail)
}

type updateRuleRequest struct {
	Name           *string    `json:"name"`
	CommissionRate *float64   `json:"commission_rate"`
	Priority       *int       `json:"priority"`
	IsActive       *bool      `json:"is_active"`
	EffectiveFrom  *time.Time `json:"effective_from"`
	EffectiveTo    *time.Time `json:"effective_to"`
	Note           *string    `json:"note"`
	UserIDs        *[]uint    `json:"user_ids"`
	ModelIDs       *[]uint    `json:"model_ids"`
}

// Update PUT /admin/commission-rules/:id
func (h *CommissionRuleHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	var req updateRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	// 记录旧值（供审计 diff）
	if old, gErr := h.svc.Get(c.Request.Context(), uint(id)); gErr == nil {
		auditctx.SetOldValue(c, old)
	}

	in := referral.UpdateInput{
		Name:           req.Name,
		CommissionRate: req.CommissionRate,
		Priority:       req.Priority,
		IsActive:       req.IsActive,
		EffectiveFrom:  req.EffectiveFrom,
		EffectiveTo:    req.EffectiveTo,
		Note:           req.Note,
		UserIDs:        req.UserIDs,
		ModelIDs:       req.ModelIDs,
	}
	detail, err := h.svc.Update(c.Request.Context(), uint(id), in)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	auditctx.SetResourceID(c, uint(id))
	response.Success(c, detail)
}

// Delete DELETE /admin/commission-rules/:id
func (h *CommissionRuleHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	if old, gErr := h.svc.Get(c.Request.Context(), uint(id)); gErr == nil {
		auditctx.SetOldValue(c, old)
	}
	if err := h.svc.Delete(c.Request.Context(), uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	auditctx.SetResourceID(c, uint(id))
	response.Success(c, gin.H{"id": id})
}

// Toggle POST /admin/commission-rules/:id/toggle
func (h *CommissionRuleHandler) Toggle(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	detail, err := h.svc.Toggle(c.Request.Context(), uint(id))
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	auditctx.SetResourceID(c, uint(id))
	response.Success(c, detail)
}

// Get GET /admin/commission-rules/:id
func (h *CommissionRuleHandler) Get(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	detail, err := h.svc.Get(c.Request.Context(), uint(id))
	if err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrNotFound.Code, "rule not found")
		return
	}
	response.Success(c, detail)
}

// List GET /admin/commission-rules
func (h *CommissionRuleHandler) List(c *gin.Context) {
	in := referral.ListInput{
		Keyword: c.Query("keyword"),
	}
	if s := c.Query("user_id"); s != "" {
		if v, err := strconv.ParseUint(s, 10, 64); err == nil {
			in.UserID = uint(v)
		}
	}
	if s := c.Query("model_id"); s != "" {
		if v, err := strconv.ParseUint(s, 10, 64); err == nil {
			in.ModelID = uint(v)
		}
	}
	if s := c.Query("is_active"); s != "" {
		v := s == "true" || s == "1"
		in.IsActive = &v
	}
	if s := c.Query("page"); s != "" {
		in.Page, _ = strconv.Atoi(s)
	}
	if s := c.Query("page_size"); s != "" {
		in.PageSize, _ = strconv.Atoi(s)
	}
	out, err := h.svc.List(c.Request.Context(), in)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.PageResult(c, out.List, out.Total, max(in.Page, 1), max(in.PageSize, 20))
}

