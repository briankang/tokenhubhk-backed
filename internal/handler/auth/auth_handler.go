package auth

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	authsvc "tokenhub-server/internal/service/auth"
)

// AuthHandler 认证接口处理器
type AuthHandler struct {
	svc *authsvc.AuthService
}

// NewAuthHandler 创建认证Handler实例
func NewAuthHandler(svc *authsvc.AuthService) *AuthHandler {
	if svc == nil {
		panic("auth handler: service is nil")
	}
	return &AuthHandler{svc: svc}
}

// Register 用户注册 POST /api/v1/auth/register
func (h *AuthHandler) Register(c *gin.Context) {
	var req authsvc.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	user, err := h.svc.Register(c.Request.Context(), &req)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	response.Success(c, gin.H{
		"id":    user.ID,
		"email": user.Email,
		"name":  user.Name,
		"role":  user.Role,
	})
}

// Login 用户登录 POST /api/v1/auth/login
func (h *AuthHandler) Login(c *gin.Context) {
	var req authsvc.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// 返回更详细的验证错误信息，便于调试
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, "请求格式错误: "+err.Error())
		return
	}

	tokenPair, err := h.svc.Login(c.Request.Context(), &req)
	if err != nil {
		response.Error(c, http.StatusUnauthorized, errcode.ErrLoginFailed)
		return
	}

	response.Success(c, tokenPair)
}

// Refresh 刷新JWT令牌 POST /api/v1/auth/refresh
func (h *AuthHandler) Refresh(c *gin.Context) {
	var req struct {
		RefreshToken string `json:"refresh_token" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	tokenPair, err := h.svc.RefreshToken(c.Request.Context(), req.RefreshToken)
	if err != nil {
		response.Error(c, http.StatusUnauthorized, errcode.ErrTokenInvalid)
		return
	}

	response.Success(c, tokenPair)
}

// Logout 用户登出 POST /api/v1/auth/logout（需要JWT）
func (h *AuthHandler) Logout(c *gin.Context) {
	userID, exists := c.Get("userId")
	if !exists {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	uid, ok := userID.(uint)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	if err := h.svc.Logout(c.Request.Context(), uid); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, gin.H{"message": "logged out"})
}
