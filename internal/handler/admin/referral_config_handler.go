package admin

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

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

	var req struct {
		PersonalCashbackRate *float64 `json:"personalCashbackRate"`
		L1CommissionRate     *float64 `json:"l1CommissionRate"`
		L2CommissionRate     *float64 `json:"l2CommissionRate"`
		L3CommissionRate     *float64 `json:"l3CommissionRate"`
		MinWithdrawAmount    *float64 `json:"minWithdrawAmount"`
		IsActive             *bool    `json:"isActive"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

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
	if req.MinWithdrawAmount != nil {
		cfg.MinWithdrawAmount = int64(*req.MinWithdrawAmount)
	}
	if req.IsActive != nil {
		cfg.IsActive = *req.IsActive
	}

	if err := h.svc.UpdateConfig(c.Request.Context(), cfg); err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}
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
