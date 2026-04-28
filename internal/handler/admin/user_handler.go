package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/database"
	"tokenhub-server/internal/middleware"
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
	search := c.Query("search")

	users, total, err := h.svc.List(c.Request.Context(), uint(tenantID), search, page, pageSize)
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

	// 读出目标用户(同时供守卫和审计使用)
	oldUser, gerr := h.svc.GetByID(c.Request.Context(), uint(id))
	if gerr == nil && oldUser != nil {
		// 受保护管理员账号守卫: 仅允许 admin 本人修改自己
		if middleware.ShouldBlockProtectedAdminWrite(c, database.DB, oldUser.Email) {
			response.ErrorMsg(c, http.StatusForbidden, 40301,
				"cannot modify protected admin account from this context; admin user can only be modified by themselves via /user/password or by direct DB write per CLAUDE.md")
			return
		}
		// 审计: 记录修改前的用户快照(中间件会写入 audit_logs.old_value)
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

// Delete handles DELETE /api/v1/admin/users/:id (软删除并释放原邮箱 tombstone 模式)
//
// 行为变化(2026-04-28):
//   - 旧版本只置 is_active=0(deactivate),原 email 仍占用唯一索引,无法重新注册
//   - 新版本调用 svc.Delete:tombstone email/referral_code + GORM 软删除,原 email 立即可复用
//
// 如需"暂停但不释放邮箱"语义,请改用 PUT /admin/users/:id 把 is_active 改 false
func (h *UserHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	// 受保护管理员账号守卫: 即使是 admin 本人也禁止删除自己
	if oldUser, gerr := h.svc.GetByID(c.Request.Context(), uint(id)); gerr == nil && oldUser != nil {
		if middleware.ShouldBlockProtectedAdminCritical(oldUser.Email) {
			response.ErrorMsg(c, http.StatusForbidden, 40301,
				"cannot delete protected admin account; this is a critical operation forbidden by CLAUDE.md covenant")
			return
		}
	}

	if err := h.svc.Delete(c.Request.Context(), uint(id)); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, gin.H{"message": "deleted"})
}
