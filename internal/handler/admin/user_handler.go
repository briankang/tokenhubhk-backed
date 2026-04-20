package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	auditmw "tokenhub-server/internal/middleware/audit"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	usersvc "tokenhub-server/internal/service/user"
)

// UserHandler 用户管理接口处理器
type UserHandler struct {
	svc *usersvc.UserService
}

// NewUserHandler 创建用户管理Handler实例
func NewUserHandler(svc *usersvc.UserService) *UserHandler {
	if svc == nil {
		panic("admin user handler: service is nil")
	}
	return &UserHandler{svc: svc}
}

// List handles GET /api/v1/admin/users
func (h *UserHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	tenantID, _ := strconv.ParseUint(c.Query("tenant_id"), 10, 64)

	users, total, err := h.svc.List(c.Request.Context(), uint(tenantID), page, pageSize)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.PageResult(c, users, total, page, pageSize)
}

// GetByID handles GET /api/v1/admin/users/:id
func (h *UserHandler) GetByID(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	user, err := h.svc.GetByID(c.Request.Context(), uint(id))
	if err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrUserNotFound.Code, err.Error())
		return
	}

	response.Success(c, user)
}

// Update handles PUT /api/v1/admin/users/:id
func (h *UserHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	// 审计：记录修改前的用户快照（中间件会写入 audit_logs.old_value）
	if oldUser, gerr := h.svc.GetByID(c.Request.Context(), uint(id)); gerr == nil && oldUser != nil {
		auditmw.SetOldValue(c, oldUser)
	}

	var updates map[string]interface{}
	if err := c.ShouldBindJSON(&updates); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	if err := h.svc.Update(c.Request.Context(), uint(id), updates); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, gin.H{"message": "updated"})
}

// Delete handles DELETE /api/v1/admin/users/:id (deactivate)
func (h *UserHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	if err := h.svc.Deactivate(c.Request.Context(), uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, gin.H{"message": "deactivated"})
}
