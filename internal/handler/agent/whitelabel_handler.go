package agent

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	whitelabel "tokenhub-server/internal/service/whitelabel"
)

// WhiteLabelHandler 白标配置接口处理器
type WhiteLabelHandler struct {
	svc      *whitelabel.WhiteLabelService
	resolver *whitelabel.DomainResolver
}

// NewWhiteLabelHandler 创建白标配置Handler实例
func NewWhiteLabelHandler(svc *whitelabel.WhiteLabelService, resolver *whitelabel.DomainResolver) *WhiteLabelHandler {
	return &WhiteLabelHandler{svc: svc, resolver: resolver}
}

// RegisterAgent 注册代理商范围的白标路由到路由组
// GET  /whitelabel   - get own config
// PUT  /whitelabel   - update own config
func (h *WhiteLabelHandler) RegisterAgent(rg *gin.RouterGroup) {
	rg.GET("/whitelabel", h.GetConfig)
	rg.PUT("/whitelabel", h.UpdateConfig)
}

// RegisterPublic 注册公开白标路由
// GET /public/whitelabel - get current domain's public config (for frontend init)
func (h *WhiteLabelHandler) RegisterPublic(rg *gin.RouterGroup) {
	rg.GET("/whitelabel", h.GetPublicConfig)
}

// updateWhiteLabelReq is the request body for updating whitelabel config.
type updateWhiteLabelReq struct {
	Domain       string `json:"domain"`
	BrandName    string `json:"brand_name"`
	LogoURL      string `json:"logo_url"`
	FaviconURL   string `json:"favicon_url"`
	PrimaryColor string `json:"primary_color"`
	FooterText   string `json:"footer_text"`
	CustomCSS    string `json:"custom_css"`
}

// GetConfig 获取白标配置 GET /api/v1/agent/whitelabel
func (h *WhiteLabelHandler) GetConfig(c *gin.Context) {
	tenantID, ok := c.Get("tenantId")
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}
	tid, _ := tenantID.(uint)
	if tid == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	cfg, err := h.svc.GetConfig(c.Request.Context(), tid)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, cfg)
}

// UpdateConfig 更新白标配置 PUT /api/v1/agent/whitelabel
func (h *WhiteLabelHandler) UpdateConfig(c *gin.Context) {
	tenantID, ok := c.Get("tenantId")
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}
	tid, _ := tenantID.(uint)
	if tid == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var req updateWhiteLabelReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	cfg := &whitelabel.WhiteLabelConfig{
		TenantID:     tid,
		Domain:       req.Domain,
		BrandName:    req.BrandName,
		LogoURL:      req.LogoURL,
		FaviconURL:   req.FaviconURL,
		PrimaryColor: req.PrimaryColor,
		FooterText:   req.FooterText,
		CustomCSS:    req.CustomCSS,
	}

	if err := h.svc.UpdateConfig(c.Request.Context(), tid, cfg); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, err.Error())
		return
	}

	response.Success(c, nil)
}

// GetPublicConfig 获取公开白标配置 GET /api/v1/public/whitelabel
// It uses the resolved tenant from the TenantResolveMiddleware.
func (h *WhiteLabelHandler) GetPublicConfig(c *gin.Context) {
	// Try resolved tenant from middleware first
	resolvedID, exists := c.Get("resolvedTenantID")
	if !exists || resolvedID == nil {
		// No tenant resolved from domain – return empty/default config
		response.Success(c, &whitelabel.PublicWhiteLabelConfig{
			BrandName: "TokenHub",
		})
		return
	}

	tid, ok := resolvedID.(uint)
	if !ok || tid == 0 {
		response.Success(c, &whitelabel.PublicWhiteLabelConfig{
			BrandName: "TokenHub",
		})
		return
	}

	cfg, err := h.svc.GetPublicConfig(c.Request.Context(), tid)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, cfg)
}
