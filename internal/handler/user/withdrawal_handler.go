package user

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/withdrawal"
)

// WithdrawalHandler v3.1 用户提现申请 + 查询
// 路径:/api/v1/user/withdrawals
type WithdrawalHandler struct {
	svc *withdrawal.Service
	db  *gorm.DB // v3.2：用于 stats/config 查询
}

// NewWithdrawalHandler 创建 handler 实例
func NewWithdrawalHandler(svc *withdrawal.Service) *WithdrawalHandler {
	return &WithdrawalHandler{svc: svc}
}

// NewWithdrawalHandlerWithDB 带 DB 的构造函数（支持 stats/config）
func NewWithdrawalHandlerWithDB(svc *withdrawal.Service, db *gorm.DB) *WithdrawalHandler {
	return &WithdrawalHandler{svc: svc, db: db}
}

// Register 注册路由到 /api/v1/user 组
func (h *WithdrawalHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/withdrawals", h.Create)
	rg.GET("/withdrawals", h.List)
	rg.GET("/withdrawals/stats", h.Stats)
	rg.GET("/withdrawals/config", h.Config)
	rg.GET("/withdrawals/:id", h.Get)
	rg.DELETE("/withdrawals/:id", h.Cancel)
}

// ==================== v3.2 扩展 ====================

// Get 单条详情
func (h *WithdrawalHandler) Get(c *gin.Context) {
	uid, ok := getUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}
	idStr := c.Param("id")
	id64, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	r, err := h.svc.GetByID(c.Request.Context(), uint(id64))
	if err != nil {
		response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
		return
	}
	if r.UserID != uid {
		response.ErrorMsg(c, http.StatusForbidden, 20010, "forbidden")
		return
	}
	response.Success(c, r)
}

// Cancel 用户取消 pending 的提现
func (h *WithdrawalHandler) Cancel(c *gin.Context) {
	uid, ok := getUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}
	idStr := c.Param("id")
	id64, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	if err := h.svc.CancelByUser(c.Request.Context(), uint(id64), uid); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"cancelled": true})
}

// withdrawConfigResp 提现规则
type withdrawConfigResp struct {
	MinWithdrawRMB   float64 `json:"min_withdraw_rmb"`
	AvailableBalance float64 `json:"available_balance_rmb"`
	PendingBalance   float64 `json:"pending_balance_rmb"`
	DailyLimit       int     `json:"daily_limit"`
}

// Config 提现配置
func (h *WithdrawalHandler) Config(c *gin.Context) {
	if h.db == nil {
		response.Success(c, withdrawConfigResp{MinWithdrawRMB: 100, DailyLimit: 5})
		return
	}
	var refCfg model.ReferralConfig
	_ = h.db.WithContext(c.Request.Context()).Where("is_active = ?", true).First(&refCfg).Error

	uid, _ := getUserID(c)
	var ub model.UserBalance
	_ = h.db.WithContext(c.Request.Context()).Where("user_id = ?", uid).First(&ub).Error

	response.Success(c, withdrawConfigResp{
		MinWithdrawRMB:   credits.CreditsToRMB(refCfg.MinWithdrawAmount),
		AvailableBalance: credits.CreditsToRMB(ub.Balance),
		PendingBalance:   credits.CreditsToRMB(ub.FrozenAmount),
		DailyLimit:       5,
	})
}

// userWithdrawStatsResp 用户提现统计
type userWithdrawStatsResp struct {
	PendingCount     int64   `json:"pending_count"`
	CompletedCount   int64   `json:"completed_count"`
	RejectedCount    int64   `json:"rejected_count"`
	TotalWithdrawRMB float64 `json:"total_withdraw_rmb"`
}

// Stats 用户提现统计
func (h *WithdrawalHandler) Stats(c *gin.Context) {
	if h.db == nil {
		response.Success(c, userWithdrawStatsResp{})
		return
	}
	uid, _ := getUserID(c)
	resp := userWithdrawStatsResp{}
	h.db.WithContext(c.Request.Context()).Model(&model.WithdrawalRequest{}).
		Where("user_id = ? AND status = ?", uid, "PENDING").Count(&resp.PendingCount)
	h.db.WithContext(c.Request.Context()).Model(&model.WithdrawalRequest{}).
		Where("user_id = ? AND status = ?", uid, "COMPLETED").Count(&resp.CompletedCount)
	h.db.WithContext(c.Request.Context()).Model(&model.WithdrawalRequest{}).
		Where("user_id = ? AND status = ?", uid, "REJECTED").Count(&resp.RejectedCount)
	var total float64
	h.db.WithContext(c.Request.Context()).Model(&model.WithdrawalRequest{}).
		Where("user_id = ? AND status = ?", uid, "COMPLETED").
		Select("COALESCE(SUM(amount), 0)").Scan(&total)
	resp.TotalWithdrawRMB = total
	response.Success(c, resp)
}

// Create 创建提现申请(用户冻结余额 → PENDING)
// POST /api/v1/user/withdrawals { amountCredits: int64, bankInfo: string }
func (h *WithdrawalHandler) Create(c *gin.Context) {
	uid, ok := getUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}
	var req struct {
		AmountCredits int64  `json:"amountCredits" binding:"required"`
		BankInfo      string `json:"bankInfo" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	row, err := h.svc.CreateWithdrawal(c.Request.Context(), uid, req.AmountCredits, req.BankInfo)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, row)
}

// List 分页查询本人的提现记录
// GET /api/v1/user/withdrawals?page=1&page_size=20
func (h *WithdrawalHandler) List(c *gin.Context) {
	uid, ok := getUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	list, total, err := h.svc.ListUserWithdrawals(c.Request.Context(), uid, page, pageSize)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, errcode.ErrInternal)
		return
	}
	response.PageResult(c, list, total, page, pageSize)
}

// getUserID 从 Gin context 读取当前用户 ID
func getUserID(c *gin.Context) (uint, bool) {
	v, exists := c.Get("userId")
	if !exists {
		return 0, false
	}
	uid, ok := v.(uint)
	if !ok || uid == 0 {
		return 0, false
	}
	return uid, true
}
