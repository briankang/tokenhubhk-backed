// Package admin - 角色与权限管理 HTTP handler
package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	permissionsvc "tokenhub-server/internal/service/permission"
)

// RoleAdminHandler 角色管理 + 用户授权端点
type RoleAdminHandler struct {
	svc *permissionsvc.RoleService
}

// NewRoleAdminHandler 创建 handler 实例
func NewRoleAdminHandler(svc *permissionsvc.RoleService) *RoleAdminHandler {
	if svc == nil {
		panic("role admin handler: svc is nil")
	}
	return &RoleAdminHandler{svc: svc}
}

// Register 挂载路由到给定 RouterGroup（通常是 /api/v1/admin）
func (h *RoleAdminHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/permissions", h.ListPermissions)

	rg.GET("/roles", h.ListRoles)
	rg.GET("/roles/:id", h.GetRole)
	rg.POST("/roles", h.CreateRole)
	rg.PUT("/roles/:id", h.UpdateRole)
	rg.DELETE("/roles/:id", h.DeleteRole)
	rg.POST("/roles/:id/clone", h.CloneRole)
	rg.GET("/roles/:id/users", h.ListRoleUsers)

	rg.GET("/users/:id/roles", h.ListUserRoles)
	rg.POST("/users/:id/roles", h.AssignUserRole)
	rg.DELETE("/users/:id/roles/:rid", h.RevokeUserRole)
}

// ListPermissions GET /admin/permissions
func (h *RoleAdminHandler) ListPermissions(c *gin.Context) {
	perms, err := h.svc.ListPermissions(c.Request.Context())
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"list": perms, "total": len(perms)})
}

// ListRoles GET /admin/roles
func (h *RoleAdminHandler) ListRoles(c *gin.Context) {
	roles, err := h.svc.List(c.Request.Context())
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"list": roles, "total": len(roles)})
}

// GetRole GET /admin/roles/:id
func (h *RoleAdminHandler) GetRole(c *gin.Context) {
	id, err := parseUintParam(c, "id")
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	dto, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrNotFound.Code, err.Error())
		return
	}
	response.Success(c, dto)
}

// CreateRole POST /admin/roles
func (h *RoleAdminHandler) CreateRole(c *gin.Context) {
	var req permissionsvc.CreateRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, err.Error())
		return
	}
	uid, _ := c.Get("userId")
	grantedBy, _ := uid.(uint)

	dto, err := h.svc.Create(c.Request.Context(), req, grantedBy)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, dto)
}

// UpdateRole PUT /admin/roles/:id
func (h *RoleAdminHandler) UpdateRole(c *gin.Context) {
	id, err := parseUintParam(c, "id")
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	var req permissionsvc.UpdateRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, err.Error())
		return
	}
	dto, err := h.svc.Update(c.Request.Context(), id, req)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, dto)
}

// DeleteRole DELETE /admin/roles/:id
func (h *RoleAdminHandler) DeleteRole(c *gin.Context) {
	id, err := parseUintParam(c, "id")
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"message": "deleted"})
}

// CloneRole POST /admin/roles/:id/clone
func (h *RoleAdminHandler) CloneRole(c *gin.Context) {
	id, err := parseUintParam(c, "id")
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	var req struct {
		Code string `json:"code" binding:"required"`
		Name string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, err.Error())
		return
	}
	uid, _ := c.Get("userId")
	grantedBy, _ := uid.(uint)

	dto, err := h.svc.Clone(c.Request.Context(), id, req.Code, req.Name, grantedBy)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, dto)
}

// ListRoleUsers GET /admin/roles/:id/users
func (h *RoleAdminHandler) ListRoleUsers(c *gin.Context) {
	id, err := parseUintParam(c, "id")
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	users, err := h.svc.ListRoleUsers(c.Request.Context(), id)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"list": users, "total": len(users)})
}

// ListUserRoles GET /admin/users/:id/roles
func (h *RoleAdminHandler) ListUserRoles(c *gin.Context) {
	uid, err := parseUintParam(c, "id")
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	roles, err := h.svc.ListUserRoles(c.Request.Context(), uid)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"list": roles, "total": len(roles)})
}

// AssignUserRole POST /admin/users/:id/roles
func (h *RoleAdminHandler) AssignUserRole(c *gin.Context) {
	uid, err := parseUintParam(c, "id")
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	var req struct {
		RoleID uint `json:"role_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, err.Error())
		return
	}
	opID, _ := c.Get("userId")
	grantedBy, _ := opID.(uint)

	if err := h.svc.AssignUserRole(c.Request.Context(), uid, req.RoleID, grantedBy); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"message": "assigned"})
}

// RevokeUserRole DELETE /admin/users/:id/roles/:rid
func (h *RoleAdminHandler) RevokeUserRole(c *gin.Context) {
	uid, err := parseUintParam(c, "id")
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	rid, err := parseUintParam(c, "rid")
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	if err := h.svc.RevokeUserRole(c.Request.Context(), uid, rid); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"message": "revoked"})
}

func parseUintParam(c *gin.Context, key string) (uint, error) {
	v := c.Param(key)
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil || n == 0 {
		return 0, err
	}
	return uint(n), nil
}
