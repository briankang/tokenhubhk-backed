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

// RateLimitHandler 限流限额管理接口处理器
type RateLimitHandler struct {
	balanceSvc   *balance.BalanceService
	quotaLimiter *balance.QuotaLimiter
}

// NewRateLimitHandler 创建限流限额管理Handler实例
func NewRateLimitHandler(balSvc *balance.BalanceService, ql *balance.QuotaLimiter) *RateLimitHandler {
	return &RateLimitHandler{balanceSvc: balSvc, quotaLimiter: ql}
}

// Register 注册限流限额管理路由到路由组
func (h *RateLimitHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/rate-limits", h.GetRateLimits)
	rg.PUT("/rate-limits", h.UpdateRateLimits)
	rg.GET("/users/:id/limits", h.GetUserLimits)
	rg.PUT("/users/:id/limits", h.UpdateUserLimits)
	rg.GET("/balance/reconciliation", h.GetReconciliation)
}

// GetRateLimits 获取全局限流配置 GET /api/v1/admin/rate-limits
func (h *RateLimitHandler) GetRateLimits(c *gin.Context) {
	cfg := middleware.LoadRateLimiterConfig()
	response.Success(c, cfg)
}

// UpdateRateLimits 更新全局限流配置 PUT /api/v1/admin/rate-limits
func (h *RateLimitHandler) UpdateRateLimits(c *gin.Context) {
	var req middleware.RateLimiterConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	// 参数校验：各限流值必须为正数
	if req.IPRPM <= 0 || req.UserRPM <= 0 || req.APIKeyRPM <= 0 || req.GlobalQPS <= 0 {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrValidation.Code, "all rate limit values must be positive")
		return
	}

	if err := middleware.SaveRateLimiterConfig(&req); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, req)
}

// GetUserLimits 获取指定用户的限额配置 GET /api/v1/admin/users/:id/limits
func (h *RateLimitHandler) GetUserLimits(c *gin.Context) {
	userID, err := parseUserID(c)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	cfg := h.quotaLimiter.GetUserQuotaConfig(c.Request.Context(), userID)

	// 同时获取用户消费统计
	daily, _ := h.balanceSvc.GetDailyConsumption(c.Request.Context(), userID)
	monthly, _ := h.balanceSvc.GetMonthlyConsumption(c.Request.Context(), userID)

	response.Success(c, gin.H{
		"quotaConfig":        cfg,
		"dailyConsumption":   daily,
		"monthlyConsumption": monthly,
	})
}

// UpdateUserLimits 设置指定用户的限额配置 PUT /api/v1/admin/users/:id/limits
func (h *RateLimitHandler) UpdateUserLimits(c *gin.Context) {
	userID, err := parseUserID(c)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var req struct {
		DailyLimit      int64 `json:"dailyLimit"`      // 日限额（积分 credits）
		MonthlyLimit    int64 `json:"monthlyLimit"`    // 月限额（积分 credits）
		MaxTokensPerReq int   `json:"maxTokensPerReq"` // 单次请求最大Token数
		MaxConcurrent   int   `json:"maxConcurrent"`   // 最大并发请求数
		CustomRPM       int   `json:"customRpm"`       // 自定义每分钟请求数
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	cfg := &model.UserQuotaConfig{
		UserID:          userID,
		DailyLimit:      req.DailyLimit,
		MonthlyLimit:    req.MonthlyLimit,
		MaxTokensPerReq: req.MaxTokensPerReq,
		MaxConcurrent:   req.MaxConcurrent,
		CustomRPM:       req.CustomRPM,
	}

	if err := h.quotaLimiter.UpdateUserQuotaConfig(c.Request.Context(), cfg); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.Success(c, cfg)
}

// GetReconciliation 获取余额对账报告 GET /api/v1/admin/balance/reconciliation
// 包含各状态冻结记录统计和超时未结算记录
func (h *RateLimitHandler) GetReconciliation(c *gin.Context) {
	report, err := h.balanceSvc.GetReconciliationReport(c.Request.Context())
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, report)
}

// parseUserID 从URL参数解析用户ID
func parseUserID(c *gin.Context) (uint, error) {
	idStr := c.Param("id")
	uid, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil || uid == 0 {
		return 0, err
	}
	return uint(uid), nil
}
