package public

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/pkg/response"
	referralsvc "tokenhub-server/internal/service/referral"
)

// ReferralClickHandler 邀请链接点击追踪处理器（公开，无需认证）
type ReferralClickHandler struct {
	svc *referralsvc.ReferralService
}

// NewReferralClickHandler 创建邀请点击追踪处理器
func NewReferralClickHandler(db *gorm.DB) *ReferralClickHandler {
	return &ReferralClickHandler{svc: referralsvc.NewReferralService(db)}
}

// Register 注册路由
func (h *ReferralClickHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/referral/click", h.Track)
}

// Track 记录邀请链接点击 POST /api/v1/public/referral/click
// Body: {"code": "XXXXX"}
func (h *ReferralClickHandler) Track(c *gin.Context) {
	var req struct {
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Code) == "" {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, "code is required")
		return
	}

	// 忽略错误（code 不存在时静默失败，不暴露用户信息）
	_ = h.svc.IncrementClickCount(c.Request.Context(), strings.TrimSpace(req.Code))
	response.Success(c, nil)
}
