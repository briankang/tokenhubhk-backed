package user

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	usersvc "tokenhub-server/internal/service/user"
)

// ProfileHandler 用户个人资料接口处理器
type ProfileHandler struct {
	svc *usersvc.UserService
}

// NewProfileHandler 创建用户资料Handler实例
func NewProfileHandler(svc *usersvc.UserService) *ProfileHandler {
	if svc == nil {
		panic("profile handler: service is nil")
	}
	return &ProfileHandler{svc: svc}
}

// GetProfile 获取用户个人资料 GET /api/v1/user/profile
func (h *ProfileHandler) GetProfile(c *gin.Context) {
	userID, ok := c.Get("userId")
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	uid, ok := userID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	user, err := h.svc.GetByID(c.Request.Context(), uid)
	if err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrUserNotFound.Code, err.Error())
		return
	}

	response.Success(c, user)
}

// UpdateProfile 更新用户个人资料 PUT /api/v1/user/profile
func (h *ProfileHandler) UpdateProfile(c *gin.Context) {
	userID, ok := c.Get("userId")
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	uid, ok := userID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	var req struct {
		Name     string `json:"name"`
		Language string `json:"language"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	if err := h.svc.UpdateProfile(c.Request.Context(), uid, req.Name, req.Language); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, gin.H{"message": "profile updated"})
}

// ChangePassword 修改用户密码 PUT /api/v1/user/password
func (h *ProfileHandler) ChangePassword(c *gin.Context) {
	userID, ok := c.Get("userId")
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	uid, ok := userID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	var req struct {
		OldPassword string `json:"old_password" binding:"required"`
		NewPassword string `json:"new_password" binding:"required,min=8"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	if err := h.svc.ChangePassword(c.Request.Context(), uid, req.OldPassword, req.NewPassword); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, gin.H{"message": "password changed"})
}
