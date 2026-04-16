package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/guard"
)

// GuardConfigHandler 注册风控配置管理
// v3.1 新增:RegistrationGuard CRUD + 一次性邮箱域名黑名单管理
type GuardConfigHandler struct {
	svc *guard.Service
}

// NewGuardConfigHandler 创建 handler 实例
func NewGuardConfigHandler(svc *guard.Service) *GuardConfigHandler {
	return &GuardConfigHandler{svc: svc}
}

// Register 注册路由
func (h *GuardConfigHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/guard-config", h.GetConfig)
	rg.PUT("/guard-config", h.UpdateConfig)
	rg.GET("/disposable-emails", h.ListDisposable)
	rg.POST("/disposable-emails", h.AddDisposable)
	rg.DELETE("/disposable-emails/:id", h.RemoveDisposable)
}

// GetConfig 获取当前风控配置
// GET /api/v1/admin/guard-config
func (h *GuardConfigHandler) GetConfig(c *gin.Context) {
	cfg := h.svc.GetConfig(c.Request.Context())
	// 不返回明文 secret (仅前端感知是否已设置)
	safe := *cfg
	if safe.CaptchaSecretEnc != "" {
		safe.CaptchaSecretEnc = "***SET***"
	}
	response.Success(c, safe)
}

// UpdateConfig 更新风控配置
// PUT /api/v1/admin/guard-config
// 字段边界见 v3.1 plan 的软校验表
func (h *GuardConfigHandler) UpdateConfig(c *gin.Context) {
	var req struct {
		CaptchaEnabled         *bool   `json:"captchaEnabled"`
		CaptchaProvider        *string `json:"captchaProvider"`
		CaptchaSiteKey         *string `json:"captchaSiteKey"`
		CaptchaSecretEnc       *string `json:"captchaSecretEnc"`
		EmailOTPEnabled        *bool   `json:"emailOtpEnabled"`
		EmailOTPLength         *int    `json:"emailOtpLength"`
		EmailOTPTTLSeconds     *int    `json:"emailOtpTtlSeconds"`
		IPRegLimitPerHour      *int    `json:"ipRegLimitPerHour"`
		IPRegLimitPerDay       *int    `json:"ipRegLimitPerDay"`
		EmailDomainDailyMax    *int    `json:"emailDomainDailyMax"`
		FingerprintEnabled     *bool   `json:"fingerprintEnabled"`
		FingerprintDailyMax    *int    `json:"fingerprintDailyMax"`
		MinFormDwellSeconds    *int    `json:"minFormDwellSeconds"`
		IPReputationEnabled    *bool   `json:"ipReputationEnabled"`
		BlockVPN               *bool   `json:"blockVpn"`
		BlockTor               *bool   `json:"blockTor"`
		DisposableEmailBlocked *bool   `json:"disposableEmailBlocked"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	cfg := h.svc.GetConfig(c.Request.Context())

	if req.CaptchaEnabled != nil {
		cfg.CaptchaEnabled = *req.CaptchaEnabled
	}
	if req.CaptchaProvider != nil {
		v := *req.CaptchaProvider
		if v != "turnstile" && v != "hcaptcha" && v != "recaptcha" {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "captchaProvider must be turnstile|hcaptcha|recaptcha")
			return
		}
		cfg.CaptchaProvider = v
	}
	if req.CaptchaSiteKey != nil {
		cfg.CaptchaSiteKey = *req.CaptchaSiteKey
	}
	if req.CaptchaSecretEnc != nil && *req.CaptchaSecretEnc != "" && *req.CaptchaSecretEnc != "***SET***" {
		cfg.CaptchaSecretEnc = *req.CaptchaSecretEnc
	}
	if req.EmailOTPEnabled != nil {
		cfg.EmailOTPEnabled = *req.EmailOTPEnabled
	}
	if req.EmailOTPLength != nil {
		v := *req.EmailOTPLength
		if v < 4 || v > 8 {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "emailOtpLength must be in [4, 8]")
			return
		}
		cfg.EmailOTPLength = v
	}
	if req.EmailOTPTTLSeconds != nil {
		v := *req.EmailOTPTTLSeconds
		if v < 60 || v > 1800 {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "emailOtpTtlSeconds must be in [60, 1800]")
			return
		}
		cfg.EmailOTPTTLSeconds = v
	}
	if req.IPRegLimitPerHour != nil {
		v := *req.IPRegLimitPerHour
		if v < 0 || v > 1000 {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "ipRegLimitPerHour must be in [0, 1000]")
			return
		}
		cfg.IPRegLimitPerHour = v
	}
	if req.IPRegLimitPerDay != nil {
		v := *req.IPRegLimitPerDay
		if v < 0 || v > 10000 {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "ipRegLimitPerDay must be in [0, 10000]")
			return
		}
		cfg.IPRegLimitPerDay = v
	}
	if req.EmailDomainDailyMax != nil {
		v := *req.EmailDomainDailyMax
		if v < 0 || v > 10000 {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "emailDomainDailyMax must be in [0, 10000]")
			return
		}
		cfg.EmailDomainDailyMax = v
	}
	if req.FingerprintEnabled != nil {
		cfg.FingerprintEnabled = *req.FingerprintEnabled
	}
	if req.FingerprintDailyMax != nil {
		v := *req.FingerprintDailyMax
		if v < 0 || v > 100 {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "fingerprintDailyMax must be in [0, 100]")
			return
		}
		cfg.FingerprintDailyMax = v
	}
	if req.MinFormDwellSeconds != nil {
		v := *req.MinFormDwellSeconds
		if v < 0 || v > 300 {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "minFormDwellSeconds must be in [0, 300]")
			return
		}
		cfg.MinFormDwellSeconds = v
	}
	if req.IPReputationEnabled != nil {
		cfg.IPReputationEnabled = *req.IPReputationEnabled
	}
	if req.BlockVPN != nil {
		cfg.BlockVPN = *req.BlockVPN
	}
	if req.BlockTor != nil {
		cfg.BlockTor = *req.BlockTor
	}
	if req.DisposableEmailBlocked != nil {
		cfg.DisposableEmailBlocked = *req.DisposableEmailBlocked
	}

	if err := h.svc.UpdateConfig(c.Request.Context(), cfg); err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}
	// 回显时屏蔽 secret
	safe := *cfg
	if safe.CaptchaSecretEnc != "" {
		safe.CaptchaSecretEnc = "***SET***"
	}
	response.Success(c, safe)
}

// ListDisposable 分页查询一次性邮箱域名
// GET /api/v1/admin/disposable-emails?page=1&page_size=50
func (h *GuardConfigHandler) ListDisposable(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	list, total, err := h.svc.ListDisposable(c.Request.Context(), page, pageSize)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}
	response.PageResult(c, list, total, page, pageSize)
}

// AddDisposable 新增一次性邮箱域名
// POST /api/v1/admin/disposable-emails { domain, note, source }
func (h *GuardConfigHandler) AddDisposable(c *gin.Context) {
	var req struct {
		Domain string `json:"domain" binding:"required"`
		Note   string `json:"note"`
		Source string `json:"source"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	row, err := h.svc.AddDisposable(c.Request.Context(), req.Domain, req.Note, req.Source)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, row)
}

// RemoveDisposable 软删除一次性邮箱域名
// DELETE /api/v1/admin/disposable-emails/:id
func (h *GuardConfigHandler) RemoveDisposable(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil || id == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	if err := h.svc.RemoveDisposable(c.Request.Context(), uint(id)); err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}
	response.Success(c, gin.H{"id": id})
}
