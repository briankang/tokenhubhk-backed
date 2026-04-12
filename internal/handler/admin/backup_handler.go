package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	channelsvc "tokenhub-server/internal/service/channel"
)

// BackupHandler 备份规则管理接口处理器
type BackupHandler struct {
	svc *channelsvc.BackupService
}

// NewBackupHandler 创建备份管理Handler实例
func NewBackupHandler(svc *channelsvc.BackupService) *BackupHandler {
	return &BackupHandler{svc: svc}
}

// Register 注册备份管理路由到路由组
func (h *BackupHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/backup-rules", h.List)
	rg.POST("/backup-rules", h.Create)
	rg.GET("/backup-rules/:id", h.GetByID)
	rg.PUT("/backup-rules/:id", h.Update)
	rg.DELETE("/backup-rules/:id", h.Delete)
	rg.GET("/backup-rules/:id/status", h.GetStatus)
	rg.POST("/backup-rules/:id/switch", h.ManualSwitch)
	rg.POST("/backup-rules/:id/recover", h.ManualRecover)
	rg.GET("/backup-rules/:id/events", h.GetEvents)
}

// createBackupRuleReq is the request body for creating a backup rule.
type createBackupRuleReq struct {
	Name           string `json:"name" binding:"required"`
	ModelPattern   string `json:"model_pattern" binding:"required"`
	PrimaryGroupID uint   `json:"primary_group_id" binding:"required"`
	BackupGroupIDs []byte `json:"backup_group_ids,omitempty"`
	SwitchRules    []byte `json:"switch_rules,omitempty"`
	IsActive       *bool  `json:"is_active,omitempty"`
}

// Create 新建备份规则 POST /backup-rules
func (h *BackupHandler) Create(c *gin.Context) {
	var req createBackupRuleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	rule := &model.BackupRule{
		Name:           req.Name,
		ModelPattern:   req.ModelPattern,
		PrimaryGroupID: req.PrimaryGroupID,
		BackupGroupIDs: req.BackupGroupIDs,
		SwitchRules:    req.SwitchRules,
	}
	if req.IsActive != nil {
		rule.IsActive = *req.IsActive
	} else {
		rule.IsActive = true
	}

	if err := h.svc.Create(c.Request.Context(), rule); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, rule)
}

// List 获取备份规则列表 GET /backup-rules
func (h *BackupHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	rules, total, err := h.svc.List(c.Request.Context(), page, pageSize)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.PageResult(c, rules, total, page, pageSize)
}

// GetByID handles GET /backup-rules/:id
func (h *BackupHandler) GetByID(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	rule, err := h.svc.GetByID(c.Request.Context(), uint(id))
	if err != nil {
		response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
		return
	}

	response.Success(c, rule)
}

// Update handles PUT /backup-rules/:id
func (h *BackupHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var updates map[string]interface{}
	if err := c.ShouldBindJSON(&updates); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	if err := h.svc.Update(c.Request.Context(), uint(id), updates); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, nil)
}

// Delete handles DELETE /backup-rules/:id
func (h *BackupHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	if err := h.svc.Delete(c.Request.Context(), uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, nil)
}

// GetStatus handles GET /backup-rules/:id/status
func (h *BackupHandler) GetStatus(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	status, err := h.svc.GetStatus(c.Request.Context(), uint(id))
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, status)
}

// ManualSwitch handles POST /backup-rules/:id/switch
func (h *BackupHandler) ManualSwitch(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	if err := h.svc.ManualSwitch(c.Request.Context(), uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, nil)
}

// ManualRecover handles POST /backup-rules/:id/recover
func (h *BackupHandler) ManualRecover(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	if err := h.svc.ManualRecover(c.Request.Context(), uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, nil)
}

// GetEvents handles GET /backup-rules/:id/events
func (h *BackupHandler) GetEvents(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	events, total, err := h.svc.GetEvents(c.Request.Context(), uint(id), page, pageSize)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.PageResult(c, events, total, page, pageSize)
}
