package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

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
func (h *QuotaHandler) UpdateQuotaConfig(c *gin.Context) {
	var req struct {
		DefaultFreeQuota  int64  `json:"defaultFreeQuota"`  // 积分(credits)
		RegistrationBonus int64  `json:"registrationBonus"` // 积分(credits)
		Description       string `json:"description"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	cfg := &model.QuotaConfig{
		DefaultFreeQuota:  req.DefaultFreeQuota,
		RegistrationBonus: req.RegistrationBonus,
		Description:       req.Description,
	}

	if err := h.balanceSvc.UpdateQuotaConfig(c.Request.Context(), cfg); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, cfg)
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
