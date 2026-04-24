package public

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/balance"
	"tokenhub-server/internal/service/referral"
)

// ConfigHandler 公开配置接口处理器
// 用于前端动态展示邀请返佣和注册赠送规则
type ConfigHandler struct {
	referralSvc *referral.ReferralService
	balanceSvc  *balance.BalanceService
}

// NewConfigHandler 创建公开配置Handler实例
func NewConfigHandler(referralSvc *referral.ReferralService, balanceSvc *balance.BalanceService) *ConfigHandler {
	return &ConfigHandler{
		referralSvc: referralSvc,
		balanceSvc:  balanceSvc,
	}
}

// Register 注册公开配置路由到路由组
func (h *ConfigHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/referral-config", h.GetReferralConfig)
	rg.GET("/quota-config", h.GetQuotaConfig)
}

// ReferralConfigPublicResponse 邀请配置公开响应（仅返回前端需要的字段）
type ReferralConfigPublicResponse struct {
	CommissionRate       float64 `json:"commissionRate"`       // 佣金比例（如 0.10 表示 10%）
	AttributionDays      int     `json:"attributionDays"`      // 归因窗口天数
	LifetimeCapCredits   int64   `json:"lifetimeCapCredits"`   // 终身上限（积分）
	MinPaidCreditsUnlock int64   `json:"minPaidCreditsUnlock"` // 解锁门槛（积分）
	MinWithdrawAmount    int64   `json:"minWithdrawAmount"`    // 最低提现（积分）
	SettleDays           int     `json:"settleDays"`           // 结算天数
	RequireInviteCode    bool    `json:"requireInviteCode"`    // 注册是否强制要求邀请码
}

// QuotaConfigPublicResponse 注册赠送配置公开响应
type QuotaConfigPublicResponse struct {
	DefaultFreeQuota     int64 `json:"defaultFreeQuota"`     // 注册基础赠送（积分）
	RegistrationBonus    int64 `json:"registrationBonus"`    // 注册额外奖励（积分）
	InviteeBonus         int64 `json:"inviteeBonus"`         // 被邀者附加赠送（积分）
	InviteeUnlockCredits int64 `json:"inviteeUnlockCredits"` // 被邀者解锁门槛（积分）
	InviterBonus         int64 `json:"inviterBonus"`         // 邀请人奖励（积分）
	InviterUnlockPaidRMB int64 `json:"inviterUnlockPaidRmb"` // 邀请人解锁门槛（积分）
	InviterMonthlyCap    int   `json:"inviterMonthlyCap"`    // 邀请人月度上限（人次）
}

// GetReferralConfig 获取邀请配置 GET /api/v1/public/referral-config
func (h *ConfigHandler) GetReferralConfig(c *gin.Context) {
	cfg, err := h.referralSvc.GetConfig(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}

	resp := ReferralConfigPublicResponse{
		CommissionRate:       cfg.CommissionRate,
		AttributionDays:      cfg.AttributionDays,
		LifetimeCapCredits:   cfg.LifetimeCapCredits,
		MinPaidCreditsUnlock: cfg.MinPaidCreditsUnlock,
		MinWithdrawAmount:    cfg.MinWithdrawAmount,
		SettleDays:           cfg.SettleDays,
		RequireInviteCode:    cfg.RequireInviteCode,
	}
	response.Success(c, resp)
}

// GetQuotaConfig 获取注册赠送配置 GET /api/v1/public/quota-config
func (h *ConfigHandler) GetQuotaConfig(c *gin.Context) {
	cfg := h.balanceSvc.GetQuotaConfig(c.Request.Context())
	if cfg == nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}

	resp := QuotaConfigPublicResponse{
		DefaultFreeQuota:     cfg.DefaultFreeQuota,
		RegistrationBonus:    cfg.RegistrationBonus,
		InviteeBonus:         cfg.InviteeBonus,
		InviteeUnlockCredits: cfg.InviteeUnlockCredits,
		InviterBonus:         cfg.InviterBonus,
		InviterUnlockPaidRMB: cfg.InviterUnlockPaidRMB,
		InviterMonthlyCap:    cfg.InviterMonthlyCap,
	}
	response.Success(c, resp)
}

