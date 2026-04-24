package auth

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tokenhub-server/internal/database"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/pkg/response"
	emailsvc "tokenhub-server/internal/service/email"
	"tokenhub-server/internal/service/guard"
)

// EmailCodeHandler 邮箱验证码发送
type EmailCodeHandler struct{}

// NewEmailCodeHandler 构造
func NewEmailCodeHandler() *EmailCodeHandler { return &EmailCodeHandler{} }

// Register 注册路由（挂在 publicWriteGroup 或 auth group 均可）
func (h *EmailCodeHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/send-register-code", h.SendRegisterCode)
	rg.POST("/send-password-reset-code", h.SendPasswordResetCode)
}

type sendCodeRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Language string `json:"language,omitempty"` // v4.3: 前端当前 i18n 语言（可选，空则按 Accept-Language / 默认 en）
}

// SendRegisterCode POST /api/v1/auth/send-register-code
// IP + email 双维度防刷：每个 IP 每 60s 1 次，每个 email 每 60s 1 次
func (h *EmailCodeHandler) SendRegisterCode(c *gin.Context) {
	h.sendCode(c, "REGISTER", "register_verify", "TokenHub Registration Verification Code")
}

// SendPasswordResetCode POST /api/v1/auth/send-password-reset-code
func (h *EmailCodeHandler) SendPasswordResetCode(c *gin.Context) {
	h.sendCode(c, "RESET_PASSWORD", "password_reset", "TokenHub Password Reset Code")
}

// sendCode 共用逻辑
func (h *EmailCodeHandler) sendCode(c *gin.Context, purpose, templateCode, subjectFallback string) {
	var req sendCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, "invalid email")
		return
	}

	// 语言解析优先级：
	//  1) 请求体 language 字段（前端 i18next 当前语言）
	//  2) Accept-Language 请求头
	//  3) ctx 中上游 i18n 中间件（geo-based 语言）识别的 lang
	//  4) 默认 "en"
	lang := strings.TrimSpace(req.Language)
	if lang == "" {
		lang = c.GetHeader("Accept-Language")
	}
	if lang == "" {
		if v, ok := c.Get("lang"); ok {
			if s, ok2 := v.(string); ok2 {
				lang = s
			}
		}
	}
	if lang != "" && strings.Contains(lang, ",") {
		// Accept-Language 可能是 "zh-CN,zh;q=0.9,en;q=0.8"，取第一段
		lang = strings.TrimSpace(strings.Split(lang, ",")[0])
	}

	// 防刷：IP + email，各 60s
	if redis.Client != nil {
		ctx := c.Request.Context()
		ipKey := "email_code:ip:" + purpose + ":" + c.ClientIP()
		emailKey := "email_code:email:" + purpose + ":" + email
		if ok, _ := redis.Client.SetNX(ctx, ipKey, "1", 60*time.Second).Result(); !ok {
			response.ErrorMsg(c, http.StatusTooManyRequests, 42901, "请求过于频繁，请 60 秒后再试")
			return
		}
		if ok, _ := redis.Client.SetNX(ctx, emailKey, "1", 60*time.Second).Result(); !ok {
			response.ErrorMsg(c, http.StatusTooManyRequests, 42901, "该邮箱刚刚已发送过验证码")
			return
		}
	}

	// v5.1: 接入 GuardService 获取动态配置并生成 OTP
	// 取代了之前硬编码的 6位、10分钟过期逻辑
	gSvc := guard.NewService(database.DB, redis.Client)
	code, token, err := gSvc.GenerateOTP(c.Request.Context(), email, purpose, c.ClientIP())
	if err != nil {
		if strings.Contains(err.Error(), "OTP_RATE_LIMIT") {
			response.ErrorMsg(c, http.StatusTooManyRequests, 42901, "请求过于频繁，请稍后再试")
		} else {
			response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		}
		return
	}

	expireMinutes := int(token.ExpiresAt.Sub(time.Now()).Minutes())
	if expireMinutes <= 0 {
		expireMinutes = 1
	}

	// 发送邮件（失败则删除刚写入的 token，让用户可再次请求）
	if emailsvc.Default == nil {
		logger.L.Warn("email service not initialized, cannot send otp", zap.String("email", email))
		response.ErrorMsg(c, http.StatusServiceUnavailable, 50301, "邮件服务暂未配置，请联系管理员")
		return
	}
	result, err := emailsvc.Default.SendByTemplate(c.Request.Context(), emailsvc.SendByTemplateRequest{
		TemplateCode: templateCode,
		Channel:      model.EmailChannelNotification,
		Language:     lang, // v4.3: 按用户语言偏好选模板 code_{lang}
		To:           []string{email},
		Variables: map[string]any{
			"Code":          code,
			"ExpireMinutes": expireMinutes,
		},
		TriggeredBy:      "user_" + strings.ToLower(purpose),
		OverrideSubject:  subjectFallback, // 当模板不存在/未启用时使用
		OverrideHTMLBody: fmt.Sprintf("<p>Your verification code is <strong>%s</strong>. Expires in %d minutes.</p>", code, expireMinutes),
		OverrideTextBody: fmt.Sprintf("Your verification code is %s. Expires in %d minutes.", code, expireMinutes),
	})
	if err != nil || result == nil || !result.Success {
		// 回滚 token 以便用户再次请求
		_ = database.DB.Delete(token).Error
		msg := "邮件发送失败"
		if err != nil {
			msg += ": " + err.Error()
		} else if result != nil {
			msg += ": " + result.Message
		}
		response.ErrorMsg(c, http.StatusBadGateway, 50302, msg)
		return
	}

	response.Success(c, gin.H{
		"sent":        true,
		"expires_in":  expireMinutes * 60,
		"email":       maskEmail(email),
	})
}

func generate6DigitCode() string {
	var n uint32
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%06d", time.Now().UnixNano()%1000000)
	}
	n = binary.BigEndian.Uint32(b)
	return fmt.Sprintf("%06d", n%1000000)
}

// maskEmail a***@example.com
func maskEmail(e string) string {
	at := strings.Index(e, "@")
	if at <= 1 {
		return e
	}
	return e[:1] + strings.Repeat("*", at-1) + e[at:]
}
