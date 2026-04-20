package admin

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/middleware"
	auditmw "tokenhub-server/internal/middleware/audit"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/referral"
)

// ReferralConfigHandler 推荐配置管理接口处理器
type ReferralConfigHandler struct {
	svc *referral.ReferralService
}

// NewReferralConfigHandler 创建推荐配置管理Handler实例
func NewReferralConfigHandler(svc *referral.ReferralService) *ReferralConfigHandler {
	return &ReferralConfigHandler{svc: svc}
}

// Register 注册推荐配置路由到管理员路由组
func (h *ReferralConfigHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/referral-config", h.GetConfig)
	rg.PUT("/referral-config", h.UpdateConfig)
	rg.GET("/commissions", h.ListCommissions)
}

// GetConfig 获取推荐配置 GET /api/v1/admin/referral-config
func (h *ReferralConfigHandler) GetConfig(c *gin.Context) {
	cfg, err := h.svc.GetConfig(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}
	response.Success(c, cfg)
}

// UpdateConfig 更新推荐配置 PUT /api/v1/admin/referral-config
func (h *ReferralConfigHandler) UpdateConfig(c *gin.Context) {
	cfg, err := h.svc.GetConfig(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}

	// 审计日志：记录修改前的旧值（中间件会写入 audit_logs.old_value）
	auditmw.SetOldValue(c, cfg)

	var req struct {
		// v3.1 核心字段
		CommissionRate       *float64 `json:"commissionRate"`
		AttributionDays      *int     `json:"attributionDays"`
		LifetimeCapCredits   *int64   `json:"lifetimeCapCredits"`
		MinPaidCreditsUnlock *int64   `json:"minPaidCreditsUnlock"`
		MinWithdrawAmount    *int64   `json:"minWithdrawAmount"`
		SettleDays           *int     `json:"settleDays"`
		IsActive             *bool    `json:"isActive"`
		// 兼容字段(v3.x 弃用,保留以避免破坏性改动)
		PersonalCashbackRate *float64 `json:"personalCashbackRate"`
		L1CommissionRate     *float64 `json:"l1CommissionRate"`
		L2CommissionRate     *float64 `json:"l2CommissionRate"`
		L3CommissionRate     *float64 `json:"l3CommissionRate"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	// v3.1 字段校验:commission_rate [0, 0.80], attribution_days [7, 3650], settle_days [1, 90]
	if req.CommissionRate != nil {
		v := *req.CommissionRate
		if v < 0 || v > 0.80 {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "commissionRate must be in [0, 0.80]")
			return
		}
		cfg.CommissionRate = v
	}
	if req.AttributionDays != nil {
		v := *req.AttributionDays
		if v < 7 || v > 3650 {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "attributionDays must be in [7, 3650]")
			return
		}
		cfg.AttributionDays = v
	}
	if req.LifetimeCapCredits != nil {
		v := *req.LifetimeCapCredits
		if v < 0 {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "lifetimeCapCredits must be >= 0")
			return
		}
		cfg.LifetimeCapCredits = v
	}
	if req.MinPaidCreditsUnlock != nil {
		v := *req.MinPaidCreditsUnlock
		if v < 0 {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "minPaidCreditsUnlock must be >= 0")
			return
		}
		cfg.MinPaidCreditsUnlock = v
	}
	if req.MinWithdrawAmount != nil {
		v := *req.MinWithdrawAmount
		if v < 0 {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "minWithdrawAmount must be >= 0")
			return
		}
		cfg.MinWithdrawAmount = v
	}
	if req.SettleDays != nil {
		v := *req.SettleDays
		if v < 1 || v > 90 {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "settleDays must be in [1, 90]")
			return
		}
		cfg.SettleDays = v
	}
	if req.IsActive != nil {
		cfg.IsActive = *req.IsActive
	}

	// 兼容字段仍允许更新
	if req.PersonalCashbackRate != nil {
		cfg.PersonalCashbackRate = *req.PersonalCashbackRate
	}
	if req.L1CommissionRate != nil {
		cfg.L1CommissionRate = *req.L1CommissionRate
	}
	if req.L2CommissionRate != nil {
		cfg.L2CommissionRate = *req.L2CommissionRate
	}
	if req.L3CommissionRate != nil {
		cfg.L3CommissionRate = *req.L3CommissionRate
	}

	if err := h.svc.UpdateConfig(c.Request.Context(), cfg); err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}

	// 清除公开配置缓存，使管理员变更立即对 /partners 等公开页可见
	middleware.CacheInvalidate("cache:/api/v1/public/referral-config*")

	response.Success(c, cfg)
}

// ListCommissions 获取佣金记录列表 GET /api/v1/admin/commissions
func (h *ReferralConfigHandler) ListCommissions(c *gin.Context) {
	page := 1
	pageSize := 20
	if p := c.Query("page"); p != "" {
		fmt.Sscanf(p, "%d", &page)
	}
	if ps := c.Query("page_size"); ps != "" {
		fmt.Sscanf(ps, "%d", &pageSize)
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	records, total, err := h.svc.GetAllCommissions(c.Request.Context(), page, pageSize)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}
	response.PageResult(c, records, total, page, pageSize)
}
