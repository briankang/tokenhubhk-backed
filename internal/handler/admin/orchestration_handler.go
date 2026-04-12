package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	orchsvc "tokenhub-server/internal/service/orchestration"
)

// OrchestrationHandler 编排工作流管理接口处理器
type OrchestrationHandler struct {
	svc *orchsvc.OrchestrationService
}

// NewOrchestrationHandler 创建编排管理Handler实例
func NewOrchestrationHandler(svc *orchsvc.OrchestrationService) *OrchestrationHandler {
	return &OrchestrationHandler{svc: svc}
}

// Register 注册编排管理路由到路由组
func (h *OrchestrationHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/orchestrations", h.List)
	rg.POST("/orchestrations", h.Create)
	rg.GET("/orchestrations/:id", h.GetByID)
	rg.PUT("/orchestrations/:id", h.Update)
	rg.DELETE("/orchestrations/:id", h.Delete)
}

// createOrchReq is the request body for creating an orchestration.
type createOrchReq struct {
	Name        string          `json:"name" binding:"required"`
	Code        string          `json:"code" binding:"required"`
	Description string          `json:"description"`
	Mode        string          `json:"mode" binding:"required"`
	Steps       json.RawMessage `json:"steps" binding:"required"`
	IsActive    *bool           `json:"is_active"`
	IsPublic    *bool           `json:"is_public"`
}

// Create 新建编排工作流 POST /orchestrations
func (h *OrchestrationHandler) Create(c *gin.Context) {
	var req createOrchReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	orch := &model.Orchestration{
		Name:        req.Name,
		Code:        req.Code,
		Description: req.Description,
		Mode:        req.Mode,
		Steps:       req.Steps,
		IsActive:    true,
		IsPublic:    false,
	}
	if req.IsActive != nil {
		orch.IsActive = *req.IsActive
	}
	if req.IsPublic != nil {
		orch.IsPublic = *req.IsPublic
	}

	if err := h.svc.Create(c.Request.Context(), orch); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, orch)
}

// List 分页获取编排工作流列表 GET /orchestrations
func (h *OrchestrationHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	filters := make(map[string]interface{})
	if v := c.Query("mode"); v != "" {
		filters["mode"] = v
	}
	if v := c.Query("is_active"); v != "" {
		filters["is_active"] = v == "true"
	}
	if v := c.Query("name"); v != "" {
		filters["name"] = v
	}

	list, total, err := h.svc.List(c.Request.Context(), page, pageSize, filters)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.PageResult(c, list, total, page, pageSize)
}

// GetByID 根据ID获取编排详情 GET /orchestrations/:id
func (h *OrchestrationHandler) GetByID(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	orch, err := h.svc.GetByID(c.Request.Context(), uint(id))
	if err != nil {
		response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
		return
	}

	response.Success(c, orch)
}

// Update 更新编排工作流 PUT /orchestrations/:id
func (h *OrchestrationHandler) Update(c *gin.Context) {
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

// Delete 删除编排工作流 DELETE /orchestrations/:id
func (h *OrchestrationHandler) Delete(c *gin.Context) {
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
