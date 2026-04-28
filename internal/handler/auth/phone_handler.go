package auth

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	authsvc "tokenhub-server/internal/service/auth"
	smsauth "tokenhub-server/internal/service/sms"
)

// PhoneHandler 处理中国大陆手机号验证码登录注册。
type PhoneHandler struct {
	authSvc *authsvc.AuthService
	smsSvc  *smsauth.Service
}

func NewPhoneHandler(authSvc *authsvc.AuthService, smsSvc *smsauth.Service) *PhoneHandler {
	return &PhoneHandler{authSvc: authSvc, smsSvc: smsSvc}
}

func (h *PhoneHandler) Register(rg *gin.RouterGroup) {
	g := rg.Group("/phone")
	g.GET("/config", h.Config)
	g.POST("/precheck", h.Precheck)
	g.POST("/send-code", h.SendCode)
	g.POST("/login", h.Login)
	g.POST("/register", h.RegisterPhone)
}

func (h *PhoneHandler) Config(c *gin.Context) {
	response.Success(c, h.smsSvc.PublicPhoneConfig(c.Request.Context()))
}

type phonePrecheckRequest struct {
	Phone       string `json:"phone" binding:"required"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Purpose     string `json:"purpose,omitempty"`
}

func (h *PhoneHandler) Precheck(c *gin.Context) {
	var req phonePrecheckRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	out, err := h.smsSvc.Precheck(c.Request.Context(), smsauth.PrecheckRequest{
		Phone: req.Phone, Fingerprint: req.Fingerprint, IP: c.ClientIP(), Purpose: req.Purpose,
	})
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, err.Error())
		return
	}
	response.Success(c, out)
}

type phoneSendCodeRequest struct {
	Phone              string `json:"phone" binding:"required"`
	Fingerprint        string `json:"fingerprint,omitempty"`
	Purpose            string `json:"purpose,omitempty"`
	CaptchaVerifyParam string `json:"captcha_verify_param,omitempty"`
}

func (h *PhoneHandler) SendCode(c *gin.Context) {
	var req phoneSendCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	out, err := h.smsSvc.SendCode(c.Request.Context(), smsauth.SendCodeRequest{
		Phone: req.Phone, Fingerprint: req.Fingerprint, IP: c.ClientIP(),
		Purpose: req.Purpose, CaptchaVerifyParam: req.CaptchaVerifyParam,
	})
	if err != nil {
		var rl *smsauth.RateLimitError
		if errors.As(err, &rl) {
			c.JSON(http.StatusTooManyRequests, response.R{
				Code:    42901,
				Message: rl.Error(),
				Data:    gin.H{"retry_after": rl.RetryAfter, "limit_type": rl.LimitType},
			})
			return
		}
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, out)
}

func (h *PhoneHandler) Login(c *gin.Context) {
	var req authsvc.PhoneLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	user, pair, err := h.authSvc.PhoneLogin(c.Request.Context(), &req)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "phone not registered") {
			response.ErrorMsg(c, http.StatusNotFound, errcode.ErrUserNotFound.Code, "phone not registered")
			return
		}
		response.ErrorMsg(c, http.StatusUnauthorized, errcode.ErrLoginFailed.Code, err.Error())
		return
	}
	emitAuthEvent(c, user.ID, user.Email, model.AuthEventLoginSuccess, "")
	response.Success(c, phoneAuthResponse(user, pair))
}

func (h *PhoneHandler) RegisterPhone(c *gin.Context) {
	var req authsvc.PhoneRegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	user, pair, err := h.authSvc.PhoneRegister(c.Request.Context(), &req)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	emitAuthEvent(c, user.ID, user.Email, model.AuthEventRegister, "")
	response.Success(c, phoneAuthResponse(user, pair))
}

func phoneAuthResponse(user *model.User, pair *authsvc.TokenPair) gin.H {
	username := ""
	if user.Username != nil {
		username = *user.Username
	}
	phone := ""
	if user.PhoneE164 != nil {
		phone = smsauth.MaskPhone(*user.PhoneE164)
	}
	return gin.H{
		"access_token":  pair.AccessToken,
		"refresh_token": pair.RefreshToken,
		"expires_in":    pair.ExpiresIn,
		"user": gin.H{
			"id":        user.ID,
			"email":     user.Email,
			"name":      user.Name,
			"username":  username,
			"phone":     phone,
			"role":      "USER",
			"tenant_id": user.TenantID,
		},
	}
}
