package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/middleware"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/balance"
)

// QuotaHandler 配额和用户余额管理接口处理器
type QuotaHandler struct {
	balanceSvc *balance.BalanceService
}

// NewQuotaHandler 创建配额管理Handler实例
func NewQuotaHandler(svc *balance.BalanceService) *QuotaHandler {
	return &QuotaHandler{balanceSvc: svc}
}

// Register 注册配额管理路由到路由组
func (h *QuotaHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/quota-config", h.GetQuotaConfig)
	rg.PUT("/quota-config", h.UpdateQuotaConfig)
	rg.POST("/users/:id/recharge", h.RechargeUser)
	rg.GET("/users/:id/balance", h.GetUserBalance)

	// 积分充值 API（新）- 使用 :id 参数名与其他路由保持一致
	rg.POST("/users/:id/recharge-credits", h.RechargeCredits)
	rg.PUT("/users/:id/set-credits", h.SetCredits)
}

// GetQuotaConfig 获取配额配置 GET /api/v1/admin/quota-config
func (h *QuotaHandler) GetQuotaConfig(c *gin.Context) {
	cfg := h.balanceSvc.GetQuotaConfig(c.Request.Context())
	response.Success(c, cfg)
}

// UpdateQuotaConfig 更新配额配置 PUT /api/v1/admin/quota-config
// v3.1: 支持邀请双向奖励字段(InviteeBonus/InviteeUnlockCredits/InviterBonus/InviterUnlockPaidRMB/InviterMonthlyCap)
// 所有字段采用指针类型,不传则保持原值
func (h *QuotaHandler) UpdateQuotaConfig(c *gin.Context) {
	// 先读取现有配置作为基础值
	current := h.balanceSvc.GetQuotaConfig(c.Request.Context())
	if current == nil {
		current = &model.QuotaConfig{}
	}

	var req struct {
		DefaultFreeQuota     *int64  `json:"defaultFreeQuota"`
		RegistrationBonus    *int64  `json:"registrationBonus"`
		InviteeBonus         *int64  `json:"inviteeBonus"`
		InviteeUnlockCredits *int64  `json:"inviteeUnlockCredits"`
		InviterBonus         *int64  `json:"inviterBonus"`
		InviterUnlockPaidRMB *int64  `json:"inviterUnlockPaidRmb"`
		InviterMonthlyCap    *int    `json:"inviterMonthlyCap"`
		FreeQuotaExpiryDays  *int    `json:"freeQuotaExpiryDays"`
		PaidThresholdCredits *int64  `json:"paidThresholdCredits"`
		Description          *string `json:"description"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	// v3.1 软边界校验(参考 plan 文档):
	// default_free_quota       [0, 100000]
	// registration_bonus       [0, 100000]
	// invitee_bonus            [0, 100000]
	// invitee_unlock_credits   [0, 1000000]
	// inviter_bonus            [0, 500000]
	// inviter_unlock_paid_rmb  [0, 10000000]
	// inviter_monthly_cap      [0, 1000]
	if req.DefaultFreeQuota != nil {
		v := *req.DefaultFreeQuota
		if v < 0 || v > 100000 {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "defaultFreeQuota must be in [0, 100000]")
			return
		}
		current.DefaultFreeQuota = v
	}
	if req.RegistrationBonus != nil {
		v := *req.RegistrationBonus
		if v < 0 || v > 100000 {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "registrationBonus must be in [0, 100000]")
			return
		}
		current.RegistrationBonus = v
	}
	if req.InviteeBonus != nil {
		v := *req.InviteeBonus
		if v < 0 || v > 100000 {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "inviteeBonus must be in [0, 100000]")
			return
		}
		current.InviteeBonus = v
	}
	if req.InviteeUnlockCredits != nil {
		v := *req.InviteeUnlockCredits
		if v < 0 || v > 1000000 {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "inviteeUnlockCredits must be in [0, 1000000]")
			return
		}
		current.InviteeUnlockCredits = v
	}
	if req.InviterBonus != nil {
		v := *req.InviterBonus
		if v < 0 || v > 500000 {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "inviterBonus must be in [0, 500000]")
			return
		}
		current.InviterBonus = v
	}
	if req.InviterUnlockPaidRMB != nil {
		v := *req.InviterUnlockPaidRMB
		if v < 0 || v > 10000000 {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "inviterUnlockPaidRmb must be in [0, 10000000]")
			return
		}
		current.InviterUnlockPaidRMB = v
	}
	if req.InviterMonthlyCap != nil {
		v := *req.InviterMonthlyCap
		if v < 0 || v > 1000 {
			response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "inviterMonthlyCap must be in [0, 1000]")
			return
		}
		current.InviterMonthlyCap = v
	}
	if req.FreeQuotaExpiryDays != nil {
		current.FreeQuotaExpiryDays = *req.FreeQuotaExpiryDays
	}
	if req.PaidThresholdCredits != nil {
		current.PaidThresholdCredits = *req.PaidThresholdCredits
	}
	if req.Description != nil {
		current.Description = *req.Description
	}

	if err := h.balanceSvc.UpdateQuotaConfig(c.Request.Context(), current); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 清除公开配置缓存，使管理员变更立即对 /partners 等公开页可见
	middleware.CacheInvalidate("cache:/api/v1/public/quota-config*")

	response.Success(c, current)
}

// RechargeUser 用户充值 POST /api/v1/admin/users/:id/recharge
func (h *QuotaHandler) RechargeUser(c *gin.Context) {
	userIDStr := c.Param("id")
	uid, err := strconv.ParseUint(userIDStr, 10, 64)
	if err != nil || uid == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var req struct {
		Credits   int64  `json:"credits" binding:"required"` // 积分(credits)
		Remark    string `json:"remark"`
		RelatedID string `json:"relatedId"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	if req.Credits <= 0 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "credits must be positive")
		return
	}

	// Get tenant ID from context
	tenantID, _ := c.Get("tenantId")
	tid, _ := tenantID.(uint)

	ub, err := h.balanceSvc.Recharge(c.Request.Context(), uint(uid), tid, req.Credits, req.Remark, req.RelatedID)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, ub)
}

// GetUserBalance 获取用户余额 GET /api/v1/admin/users/:id/balance
func (h *QuotaHandler) GetUserBalance(c *gin.Context) {
	userIDStr := c.Param("id")
	uid, err := strconv.ParseUint(userIDStr, 10, 64)
	if err != nil || uid == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	tenantID, _ := c.Get("tenantId")
	tid, _ := tenantID.(uint)

	ub, err := h.balanceSvc.GetBalance(c.Request.Context(), uint(uid), tid)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, ub)
}

// RechargeCredits 管理员为用户充值积分 POST /api/v1/admin/users/:id/recharge-credits
func (h *QuotaHandler) RechargeCredits(c *gin.Context) {
	userIDStr := c.Param("id")
	uid, err := strconv.ParseUint(userIDStr, 10, 64)
	if err != nil || uid == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var req struct {
		Credits int64  `json:"credits" binding:"required"`
		Remark  string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	if req.Credits <= 0 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "credits must be positive")
		return
	}

	// 获取租户ID
	tenantID, _ := c.Get("tenantId")
	tid, _ := tenantID.(uint)

	// 获取操作人ID
	operatorID, _ := c.Get("userId")
	opID, _ := operatorID.(uint)

	// 调用充值服务
	ub, err := h.balanceSvc.Recharge(c.Request.Context(), uint(uid), tid, req.Credits, req.Remark, "")
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// TODO: 记录审计日志
	_ = opID

	response.Success(c, ub)
}

// SetCredits 管理员直接设置用户积分余额 PUT /api/v1/admin/users/:id/set-credits
func (h *QuotaHandler) SetCredits(c *gin.Context) {
	userIDStr := c.Param("id")
	uid, err := strconv.ParseUint(userIDStr, 10, 64)
	if err != nil || uid == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var req struct {
		Credits int64  `json:"credits" binding:"required"`
		Remark  string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	if req.Credits < 0 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "credits cannot be negative")
		return
	}

	// 获取租户ID
	tenantID, _ := c.Get("tenantId")
	tid, _ := tenantID.(uint)

	// 获取操作人ID
	operatorID, _ := c.Get("userId")
	opID, _ := operatorID.(uint)

	// 获取当前余额
	oldBalance, err := h.balanceSvc.GetBalance(c.Request.Context(), uint(uid), tid)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 计算差值
	diff := req.Credits - oldBalance.Balance

	// 调用调整服务
	ub, err := h.balanceSvc.AdminAdjust(c.Request.Context(), uint(uid), tid, diff, req.Remark)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// TODO: 记录审计日志
	_ = oldBalance
	_ = opID

	response.Success(c, ub)
}
