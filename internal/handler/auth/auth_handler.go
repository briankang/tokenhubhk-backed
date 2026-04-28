package auth

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	authsvc "tokenhub-server/internal/service/auth"
	"tokenhub-server/internal/service/authlog"
	geosvc "tokenhub-server/internal/service/geo"
)

// AuthHandler 认证接口处理器
type AuthHandler struct {
	svc      *authsvc.AuthService
	oauthSvc *authsvc.OAuthService
	geoSvc   *geosvc.GeoService // 可选；注册时用于 IP 国家自动检测
}

// NewAuthHandler 创建认证Handler实例
// geoSvc 可选，传入后注册时若前端未传 country_code 则自动从 IP 检测
func NewAuthHandler(svc *authsvc.AuthService, geoSvc ...*geosvc.GeoService) *AuthHandler {
	if svc == nil {
		panic("auth handler: service is nil")
	}
	h := &AuthHandler{svc: svc}
	if len(geoSvc) > 0 {
		h.geoSvc = geoSvc[0]
	}
	return h
}

// WithOAuthService 注入第三方 OAuth 登录服务。
func (h *AuthHandler) WithOAuthService(svc *authsvc.OAuthService) *AuthHandler {
	h.oauthSvc = svc
	return h
}

// emitAuthEvent 构造并异步入队用户认证日志
// 不阻塞响应路径；Recorder 未初始化时静默丢弃
func emitAuthEvent(c *gin.Context, userID uint, email, eventType, failReason string) {
	if authlog.Default == nil {
		return
	}
	ev := &model.UserAuthLog{
		UserID:     userID,
		Email:      email,
		EventType:  eventType,
		IP:         c.ClientIP(),
		UserAgent:  c.Request.UserAgent(),
		RequestID:  c.GetString("requestId"),
		FailReason: failReason,
	}
	authlog.Default.Enqueue(ev)
}

// Register 用户注册 POST /api/v1/auth/register
func (h *AuthHandler) Register(c *gin.Context) {
	var req authsvc.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		emitAuthEvent(c, 0, "", model.AuthEventRegister, model.AuthFailReasonInvalidRequest)
		response.Error(c, http.StatusBadRequest, errcode.ErrValidation)
		return
	}

	req.ClientIP = c.ClientIP()
	req.UserAgent = c.Request.UserAgent()

	// v5.0: 前端未传 country_code 时，通过 IP 自动检测兜底
	if req.CountryCode == "" && h.geoSvc != nil {
		ip := geosvc.GetClientIP(c.Request)
		result := h.geoSvc.DetectLocale(c.Request.Context(), ip)
		req.CountryCode = result.CountryCode // 仍为空则 service 层 fallback "CN"
	}

	user, err := h.svc.Register(c.Request.Context(), &req)
	if err != nil {
		// 将业务错误映射为枚举原因，禁止记录原始 error（可能含敏感信息）
		reason := classifyRegisterError(err.Error())
		emitAuthEvent(c, 0, req.Email, model.AuthEventRegister, reason)
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	// 注册成功后生成令牌对
	tokenPair, err := h.svc.Login(c.Request.Context(), &authsvc.LoginRequest{
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		emitAuthEvent(c, user.ID, user.Email, model.AuthEventRegister, model.AuthFailReasonInvalidRequest)
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "Failed to generate token")
		return
	}

	emitAuthEvent(c, user.ID, user.Email, model.AuthEventRegister, "")

	response.Success(c, gin.H{
		"access_token":  tokenPair.AccessToken,
		"refresh_token": tokenPair.RefreshToken,
		"expires_in":    tokenPair.ExpiresIn,
		"user": gin.H{
			"id":    user.ID,
			"email": user.Email,
			"name":  user.Name,
			"role":  "USER", // 注册默认返回 USER；真实角色通过 /user/profile 的 role_codes 获取
		},
	})
}

// classifyRegisterError 将 Register 业务错误映射为 FailReason 枚举
func classifyRegisterError(msg string) string {
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "email already registered"):
		return model.AuthFailReasonEmailExists
	case strings.Contains(m, "invite code"):
		return model.AuthFailReasonInviteRequired
	case strings.Contains(m, "username already exists"):
		return model.AuthFailReasonInvalidRequest
	case strings.Contains(m, "username must be at least"):
		return model.AuthFailReasonInvalidRequest
	case strings.Contains(m, "password must"):
		return model.AuthFailReasonInvalidRequest
	default:
		return model.AuthFailReasonInvalidRequest
	}
}

// Login 用户登录 POST /api/v1/auth/login
func (h *AuthHandler) Login(c *gin.Context) {
	var req authsvc.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		emitAuthEvent(c, 0, "", model.AuthEventLoginFailed, model.AuthFailReasonInvalidRequest)
		// 返回更详细的验证错误信息，便于调试
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, "请求格式错误: "+err.Error())
		return
	}

	tokenPair, err := h.svc.Login(c.Request.Context(), &req)
	if err != nil {
		// 按 service 返回的 error 字符串区分失败类型（安全枚举，不记原文）
		reason := classifyLoginError(err.Error())
		emitAuthEvent(c, 0, req.Email, model.AuthEventLoginFailed, reason)
		response.Error(c, http.StatusUnauthorized, errcode.ErrLoginFailed)
		return
	}

	// 登录成功记录 —— user_id 通过 email 回查
	var userID uint
	userID = h.lookupUserIDByIdentifier(c, req.Email)
	emitAuthEvent(c, userID, req.Email, model.AuthEventLoginSuccess, "")

	response.Success(c, tokenPair)
}

// OAuthProviders GET /api/v1/auth/oauth/providers
func (h *AuthHandler) OAuthProviders(c *gin.Context) {
	if h.oauthSvc == nil {
		response.Success(c, []authsvc.OAuthProviderDTO{})
		return
	}
	providers, err := h.oauthSvc.ListPublicProviders(c.Request.Context())
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, providers)
}

// OAuthStart GET /api/v1/auth/oauth/:provider/start
func (h *AuthHandler) OAuthStart(c *gin.Context) {
	if h.oauthSvc == nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "oauth service not initialized")
		return
	}
	authURL, err := h.oauthSvc.BuildAuthURL(
		c.Request.Context(),
		c.Param("provider"),
		c.DefaultQuery("redirect", "/dashboard"),
		c.Query("invite_code"),
		c.Query("referral_code"),
	)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	c.Redirect(http.StatusFound, authURL)
}

// OAuthCallback GET /api/v1/auth/oauth/:provider/callback
func (h *AuthHandler) OAuthCallback(c *gin.Context) {
	if h.oauthSvc == nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "oauth service not initialized")
		return
	}
	pair, redirectPath, frontendRedirectURL, err := h.oauthSvc.HandleCallback(
		c.Request.Context(),
		c.Param("provider"),
		c.Query("code"),
		c.Query("state"),
	)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(strings.ToLower(err.Error()), "disabled") {
			status = http.StatusForbidden
		}
		response.ErrorMsg(c, status, errcode.ErrBadRequest.Code, err.Error())
		return
	}

	base := frontendRedirectURL
	if base == "" {
		base = "/oauth/callback"
	}
	fragment := url.Values{}
	fragment.Set("access_token", pair.AccessToken)
	fragment.Set("refresh_token", pair.RefreshToken)
	fragment.Set("expires_in", fmt.Sprintf("%d", pair.ExpiresIn))
	fragment.Set("redirect", redirectPath)
	c.Redirect(http.StatusFound, base+"#"+fragment.Encode())
}

// lookupUserIDByIdentifier 从 DB 查 user_id 供日志使用（best-effort，失败返 0）
func (h *AuthHandler) lookupUserIDByIdentifier(c *gin.Context, identifier string) uint {
	identifier = strings.ToLower(strings.TrimSpace(identifier))
	if identifier == "" {
		return 0
	}
	type idOnly struct{ ID uint }
	var row idOnly
	if err := h.svc.DB().WithContext(c.Request.Context()).
		Table("users").Select("id").Where("email = ? OR username = ?", identifier, identifier).Scan(&row).Error; err != nil {
		return 0
	}
	return row.ID
}

// classifyLoginError 将 Login 错误映射为 FailReason 枚举
func classifyLoginError(msg string) string {
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "account is deactivated"):
		return model.AuthFailReasonAccountDisabled
	case strings.Contains(m, "invalid credentials"):
		return model.AuthFailReasonWrongPassword
	case strings.Contains(m, "user not found"):
		return model.AuthFailReasonUserNotFound
	default:
		return model.AuthFailReasonInvalidRequest
	}
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
		emitAuthEvent(c, 0, "", model.AuthEventRefresh, model.AuthFailReasonTokenInvalid)
		response.Error(c, http.StatusUnauthorized, errcode.ErrTokenInvalid)
		return
	}

	emitAuthEvent(c, 0, "", model.AuthEventRefresh, "")
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

	emitAuthEvent(c, uid, "", model.AuthEventLogout, "")
	response.Success(c, gin.H{"message": "logged out"})
}
