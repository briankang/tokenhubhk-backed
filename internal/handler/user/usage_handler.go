package user

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
)

// UsageHandler 用户用量与账单接口处理器
type UsageHandler struct {
	db *gorm.DB
}

// NewUsageHandler 创建用户用量Handler实例
func NewUsageHandler(db *gorm.DB) *UsageHandler {
	if db == nil {
		panic("user usage handler: db is nil")
	}
	return &UsageHandler{db: db}
}

// Register 注册用户用量/账单相关路由到路由组
func (h *UsageHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/usage", h.Usage)
	rg.GET("/billing", h.Billing)
	rg.POST("/billing/topup", h.Topup)
}

// Usage 获取用户用量记录 GET /api/v1/user/usage
func (h *UsageHandler) Usage(c *gin.Context) {
	userID, exists := c.Get("userId")
	if !exists {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}
	uid, ok := userID.(uint)
	if !ok || uid == 0 {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	query := h.db.WithContext(c.Request.Context()).Model(&model.ChannelLog{}).Where("user_id = ?", uid)

	// 可选的日期范围过滤
	if startDate := c.Query("start_date"); startDate != "" {
		query = query.Where("created_at >= ?", startDate)
	}
	if endDate := c.Query("end_date"); endDate != "" {
		query = query.Where("created_at <= ?", endDate+" 23:59:59")
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	var logs []model.ChannelLog
	offset := (page - 1) * pageSize
	if err := query.Order("created_at DESC").Offset(offset).Limit(pageSize).Find(&logs).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 计算汇总统计数据
	var summary struct {
		TotalRequests     int64 `json:"total_requests"`
		TotalInputTokens  int64 `json:"total_input_tokens"`
		TotalOutputTokens int64 `json:"total_output_tokens"`
	}
	h.db.WithContext(c.Request.Context()).Model(&model.ChannelLog{}).
		Select("COUNT(*) as total_requests, COALESCE(SUM(request_tokens),0) as total_input_tokens, COALESCE(SUM(response_tokens),0) as total_output_tokens").
		Where("user_id = ?", uid).
		Scan(&summary)

	response.Success(c, gin.H{
		"logs":     logs,
		"total":    total,
		"page":     page,
		"page_size": pageSize,
		"summary":  summary,
	})
}

// Billing 获取用户账单信息 GET /api/v1/user/billing
func (h *UsageHandler) Billing(c *gin.Context) {
	userID, exists := c.Get("userId")
	if !exists {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}
	uid, ok := userID.(uint)
	if !ok || uid == 0 {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return
	}

	// 查询余额记录
	var ub model.UserBalance
	if err := h.db.WithContext(c.Request.Context()).Where("user_id = ?", uid).First(&ub).Error; err != nil {
		// 余额记录不存在，返回默认值
		response.Success(c, gin.H{
			"userId":           uid,
			"balance":          0,
			"balanceRmb":       0,
			"freeQuota":        0,
			"freeQuotaRmb":     0,
			"totalConsumed":    0,
			"totalConsumedRmb": 0,
			"frozenAmount":     0,
			"currency":         "CREDIT",
		})
		return
	}

	// 返回双轨字段（积分 + RMB）
	response.Success(c, gin.H{
		"userId":           ub.UserID,
		"balance":          ub.Balance,
		"balanceRmb":       ub.BalanceRMB,
		"freeQuota":        ub.FreeQuota,
		"freeQuotaRmb":     ub.FreeQuota / 10000.0, // 转换为RMB展示
		"totalConsumed":    ub.TotalConsumed,
		"totalConsumedRmb": ub.TotalConsumedRMB,
		"frozenAmount":     ub.FrozenAmount,
		"currency":         ub.Currency,
	})
}

// Topup 处理用户充值请求 POST /api/v1/user/billing/topup
func (h *UsageHandler) Topup(c *gin.Context) {
	// 充值功能待UserBalance模型完善后实现，当前引导用户使用支付系统
	response.Success(c, gin.H{
		"message": "please use the payment system to top up your account",
	})
}
