// Package user 用户用量与账单接口
//
// 提供以下端点：
//   - GET /user/usage              — 历史分页列表（旧接口，保留兼容）
//   - GET /user/billing            — 当前余额（旧接口，保留兼容）
//   - POST /user/billing/topup     — 引导到支付系统（旧接口）
//   - GET /user/usage/overview     — 控制台首屏概览：余额 + 今日/本月汇总
//   - GET /user/usage/report       — 按天趋势（近 N 天折线图）
//   - GET /user/usage/by-model     — 按模型分组消费排行
//   - GET /user/usage/recent       — 最近调用明细
//
// 数据源：聚合类查询统一使用 api_call_logs（含 cost_credits，与扣费强一致）；
// /usage 旧列表仍使用 channel_logs 以保持与历史前端兼容。
package user

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	balancesvc "tokenhub-server/internal/service/balance"
)

// UsageHandler 用户用量与账单接口处理器
type UsageHandler struct {
	db         *gorm.DB
	balanceSvc *balancesvc.BalanceService
}

// NewUsageHandler 创建用户用量Handler实例（兼容旧签名）
func NewUsageHandler(db *gorm.DB) *UsageHandler {
	if db == nil {
		panic("user usage handler: db is nil")
	}
	return &UsageHandler{db: db}
}

// NewUsageHandlerWithBalance 创建用户用量Handler并注入余额服务（控制台首屏所需）
func NewUsageHandlerWithBalance(db *gorm.DB, balSvc *balancesvc.BalanceService) *UsageHandler {
	if db == nil {
		panic("user usage handler: db is nil")
	}
	return &UsageHandler{db: db, balanceSvc: balSvc}
}

// Register 注册用户用量/账单相关路由到路由组
func (h *UsageHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/usage", h.Usage)
	rg.GET("/billing", h.Billing)
	rg.POST("/billing/topup", h.Topup)

	// --- 控制台专用聚合端点 ---
	rg.GET("/usage/overview", h.Overview)
	rg.GET("/usage/report", h.Report)
	rg.GET("/usage/by-model", h.ByModel)
	rg.GET("/usage/recent", h.Recent)
}

// requireUserID 提取当前登录 user_id
func (h *UsageHandler) requireUserID(c *gin.Context) (uint, bool) {
	v, exists := c.Get("userId")
	if !exists {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return 0, false
	}
	uid, ok := v.(uint)
	if !ok || uid == 0 {
		response.Error(c, http.StatusUnauthorized, errcode.ErrUnauthorized)
		return 0, false
	}
	return uid, true
}

// Usage 获取用户用量记录 GET /api/v1/user/usage
func (h *UsageHandler) Usage(c *gin.Context) {
	uid, ok := h.requireUserID(c)
	if !ok {
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
		"logs":      logs,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"summary":   summary,
	})
}

// Billing 获取用户账单信息 GET /api/v1/user/billing
func (h *UsageHandler) Billing(c *gin.Context) {
	uid, ok := h.requireUserID(c)
	if !ok {
		return
	}

	var ub model.UserBalance
	if err := h.db.WithContext(c.Request.Context()).Where("user_id = ?", uid).First(&ub).Error; err != nil {
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

	response.Success(c, gin.H{
		"userId":           ub.UserID,
		"balance":          ub.Balance,
		"balanceRmb":       ub.BalanceRMB,
		"freeQuota":        ub.FreeQuota,
		"freeQuotaRmb":     credits.CreditsToRMB(ub.FreeQuota),
		"totalConsumed":    ub.TotalConsumed,
		"totalConsumedRmb": ub.TotalConsumedRMB,
		"frozenAmount":     ub.FrozenAmount,
		"currency":         ub.Currency,
	})
}

// Topup 处理用户充值请求 POST /api/v1/user/billing/topup
func (h *UsageHandler) Topup(c *gin.Context) {
	response.Success(c, gin.H{
		"message": "please use the payment system to top up your account",
	})
}

// ========== 新增：控制台专用聚合端点 ==========

// overviewResponse 首屏概览返回体
type overviewResponse struct {
	BalanceCredits       int64   `json:"balance_credits"`
	BalanceRMB           float64 `json:"balance_rmb"`
	FreeQuotaCredits     int64   `json:"free_quota_credits"`
	FreeQuotaRMB         float64 `json:"free_quota_rmb"`
	TotalConsumedCredits int64   `json:"total_consumed_credits"`
	TotalConsumedRMB     float64 `json:"total_consumed_rmb"`

	TodayRequests    int64   `json:"today_requests"`
	TodayTokens      int64   `json:"today_tokens"`
	TodayCostCredits int64   `json:"today_cost_credits"`
	TodayCostRMB     float64 `json:"today_cost_rmb"`

	MonthRequests    int64   `json:"month_requests"`
	MonthTokens      int64   `json:"month_tokens"`
	MonthCostCredits int64   `json:"month_cost_credits"`
	MonthCostRMB     float64 `json:"month_cost_rmb"`

	SuccessRate7d float64 `json:"success_rate_7d"`
}

// Overview GET /api/v1/user/usage/overview
func (h *UsageHandler) Overview(c *gin.Context) {
	uid, ok := h.requireUserID(c)
	if !ok {
		return
	}
	tenantID, _ := c.Get("tenantId")
	tid, _ := tenantID.(uint)

	ctx := c.Request.Context()
	resp := overviewResponse{}

	// 1) 余额：优先走缓存服务，降级查表
	if h.balanceSvc != nil {
		if ub, err := h.balanceSvc.GetBalanceCached(ctx, uid, tid); err == nil && ub != nil {
			resp.BalanceCredits = ub.Balance
			resp.BalanceRMB = credits.CreditsToRMB(ub.Balance)
			resp.FreeQuotaCredits = ub.FreeQuota
			resp.FreeQuotaRMB = credits.CreditsToRMB(ub.FreeQuota)
			resp.TotalConsumedCredits = ub.TotalConsumed
			resp.TotalConsumedRMB = credits.CreditsToRMB(ub.TotalConsumed)
		}
	} else {
		var ub model.UserBalance
		if err := h.db.WithContext(ctx).Where("user_id = ?", uid).First(&ub).Error; err == nil {
			resp.BalanceCredits = ub.Balance
			resp.BalanceRMB = credits.CreditsToRMB(ub.Balance)
			resp.FreeQuotaCredits = ub.FreeQuota
			resp.FreeQuotaRMB = credits.CreditsToRMB(ub.FreeQuota)
			resp.TotalConsumedCredits = ub.TotalConsumed
			resp.TotalConsumedRMB = credits.CreditsToRMB(ub.TotalConsumed)
		}
	}

	// 2) 今日汇总
	today := time.Now().Format("2006-01-02")
	var todayAgg struct {
		TotalRequests int64 `gorm:"column:total_requests"`
		TotalTokens   int64 `gorm:"column:total_tokens"`
		CostSum       int64 `gorm:"column:cost_sum"`
	}
	h.db.WithContext(ctx).Table("api_call_logs").
		Select("COUNT(*) as total_requests, COALESCE(SUM(total_tokens),0) as total_tokens, COALESCE(SUM(cost_credits),0) as cost_sum").
		Where("user_id = ? AND created_at >= ?", uid, today+" 00:00:00").
		Scan(&todayAgg)
	resp.TodayRequests = todayAgg.TotalRequests
	resp.TodayTokens = todayAgg.TotalTokens
	resp.TodayCostCredits = todayAgg.CostSum
	resp.TodayCostRMB = credits.CreditsToRMB(todayAgg.CostSum)

	// 3) 本月汇总
	monthStart := time.Now().Format("2006-01") + "-01"
	var monthAgg struct {
		TotalRequests int64 `gorm:"column:total_requests"`
		TotalTokens   int64 `gorm:"column:total_tokens"`
		CostSum       int64 `gorm:"column:cost_sum"`
	}
	h.db.WithContext(ctx).Table("api_call_logs").
		Select("COUNT(*) as total_requests, COALESCE(SUM(total_tokens),0) as total_tokens, COALESCE(SUM(cost_credits),0) as cost_sum").
		Where("user_id = ? AND created_at >= ?", uid, monthStart+" 00:00:00").
		Scan(&monthAgg)
	resp.MonthRequests = monthAgg.TotalRequests
	resp.MonthTokens = monthAgg.TotalTokens
	resp.MonthCostCredits = monthAgg.CostSum
	resp.MonthCostRMB = credits.CreditsToRMB(monthAgg.CostSum)

	// 4) 近 7 天成功率
	since := time.Now().AddDate(0, 0, -7).Format("2006-01-02")
	var rateAgg struct {
		Total   int64 `gorm:"column:total"`
		Success int64 `gorm:"column:success"`
	}
	h.db.WithContext(ctx).Table("api_call_logs").
		Select("COUNT(*) as total, SUM(CASE WHEN status_code=200 THEN 1 ELSE 0 END) as success").
		Where("user_id = ? AND created_at >= ?", uid, since+" 00:00:00").
		Scan(&rateAgg)
	if rateAgg.Total > 0 {
		resp.SuccessRate7d = float64(rateAgg.Success) / float64(rateAgg.Total)
	}

	response.Success(c, resp)
}

// reportItem 按天趋势数据点
type reportItem struct {
	Date         string  `json:"date"`
	Requests     int64   `json:"requests"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalTokens  int64   `json:"total_tokens"`
	CostCredits  int64   `json:"cost_credits"`
	CostRMB      float64 `json:"cost_rmb"`
}

// Report GET /api/v1/user/usage/report?days=30
func (h *UsageHandler) Report(c *gin.Context) {
	uid, ok := h.requireUserID(c)
	if !ok {
		return
	}
	days, _ := strconv.Atoi(c.DefaultQuery("days", "30"))
	if days < 1 || days > 365 {
		days = 30
	}
	since := time.Now().AddDate(0, 0, -(days - 1)).Format("2006-01-02")

	// 直接使用 database/sql 绕过 GORM 查询构建器
	sqlDB, sqlErr := h.db.DB()
	if sqlErr != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, sqlErr.Error())
		return
	}
	rawSQL := `SELECT DATE(created_at) AS report_date,
		COUNT(*) AS requests,
		COALESCE(SUM(prompt_tokens),0) AS input_tokens,
		COALESCE(SUM(completion_tokens),0) AS output_tokens,
		COALESCE(SUM(total_tokens),0) AS total_tokens,
		COALESCE(SUM(cost_credits),0) AS cost_credits
	FROM api_call_logs
	WHERE user_id = ? AND created_at >= ?
	GROUP BY DATE(created_at)
	ORDER BY report_date ASC`
	dbRows, queryErr := sqlDB.QueryContext(context.Background(), rawSQL, uint64(uid), since+" 00:00:00")
	if queryErr != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, queryErr.Error())
		return
	}
	defer dbRows.Close()

	type row struct {
		ReportDate  time.Time // MySQL DATE() 扫描为 time.Time，再手动格式化为 "YYYY-MM-DD"
		Requests    int64
		InputTokens int64
		OutputTokens int64
		TotalTokens int64
		CostCredits int64
	}
	var rows []row
	for dbRows.Next() {
		var r row
		if scanErr := dbRows.Scan(&r.ReportDate, &r.Requests, &r.InputTokens, &r.OutputTokens, &r.TotalTokens, &r.CostCredits); scanErr != nil {
			continue
		}
		rows = append(rows, r)
	}

	// 按天索引（用 YYYY-MM-DD 格式）；缺失的日期补 0 以避免折线图断点
	byDate := make(map[string]row, len(rows))
	for _, r := range rows {
		byDate[r.ReportDate.Format("2006-01-02")] = r
	}
	items := make([]reportItem, 0, days)
	for i := 0; i < days; i++ {
		d := time.Now().AddDate(0, 0, -(days - 1 - i)).Format("2006-01-02")
		r, hit := byDate[d]
		item := reportItem{Date: d}
		if hit {
			item.Requests = r.Requests
			item.InputTokens = r.InputTokens
			item.OutputTokens = r.OutputTokens
			item.TotalTokens = r.TotalTokens
			item.CostCredits = r.CostCredits
			item.CostRMB = credits.CreditsToRMB(r.CostCredits)
		}
		items = append(items, item)
	}
	response.Success(c, items)
}

// byModelItem 按模型分组消费
type byModelItem struct {
	ModelName    string  `json:"model_name"`
	Requests     int64   `json:"requests"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalTokens  int64   `json:"total_tokens"`
	CostCredits  int64   `json:"cost_credits"`
	CostRMB      float64 `json:"cost_rmb"`
}

// ByModel GET /api/v1/user/usage/by-model?days=30
func (h *UsageHandler) ByModel(c *gin.Context) {
	uid, ok := h.requireUserID(c)
	if !ok {
		return
	}
	days, _ := strconv.Atoi(c.DefaultQuery("days", "30"))
	if days < 1 || days > 365 {
		days = 30
	}
	since := time.Now().AddDate(0, 0, -(days - 1)).Format("2006-01-02")

	ctx := c.Request.Context()
	type row struct {
		ModelName    string `gorm:"column:model_name"`
		Requests     int64  `gorm:"column:requests"`
		InputTokens  int64  `gorm:"column:input_tokens"`
		OutputTokens int64  `gorm:"column:output_tokens"`
		TotalTokens  int64  `gorm:"column:total_tokens"`
		CostCredits  int64  `gorm:"column:cost_credits"`
	}
	var rows []row
	err := h.db.WithContext(ctx).Table("api_call_logs").
		Select("request_model as model_name, COUNT(*) as requests, "+
			"COALESCE(SUM(prompt_tokens),0) as input_tokens, "+
			"COALESCE(SUM(completion_tokens),0) as output_tokens, "+
			"COALESCE(SUM(total_tokens),0) as total_tokens, "+
			"COALESCE(SUM(cost_credits),0) as cost_credits").
		Where("user_id = ? AND created_at >= ? AND request_model <> ''", uid, since+" 00:00:00").
		Group("request_model").
		Order("cost_credits DESC").
		Limit(50).
		Scan(&rows).Error
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	items := make([]byModelItem, len(rows))
	for i, r := range rows {
		items[i] = byModelItem{
			ModelName:    r.ModelName,
			Requests:     r.Requests,
			InputTokens:  r.InputTokens,
			OutputTokens: r.OutputTokens,
			TotalTokens:  r.TotalTokens,
			CostCredits:  r.CostCredits,
			CostRMB:      credits.CreditsToRMB(r.CostCredits),
		}
	}
	response.Success(c, items)
}

// recentItem 最近调用明细
type recentItem struct {
	ID           uint      `json:"id"`
	RequestID    string    `json:"request_id"`
	Endpoint     string    `json:"endpoint"`
	Model        string    `json:"model"`
	StatusCode   int       `json:"status_code"`
	PromptTokens int       `json:"prompt_tokens"`
	OutputTokens int       `json:"output_tokens"`
	TotalTokens  int       `json:"total_tokens"`
	CostCredits  int64     `json:"cost_credits"`
	CostRMB      float64   `json:"cost_rmb"`
	LatencyMs    int       `json:"latency_ms"`
	CreatedAt    time.Time `json:"created_at"`
	// 多计费单位支持（v3.2）—— 非 Token 型调用的数量维度
	ImageCount  int     `json:"image_count,omitempty"`
	CharCount   int     `json:"char_count,omitempty"`
	DurationSec float64 `json:"duration_sec,omitempty"`
	CallCount   int     `json:"call_count,omitempty"`
}

// Recent GET /api/v1/user/usage/recent?limit=20
func (h *UsageHandler) Recent(c *gin.Context) {
	uid, ok := h.requireUserID(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if limit < 1 || limit > 100 {
		limit = 20
	}

	ctx := c.Request.Context()
	var logs []model.ApiCallLog
	if err := h.db.WithContext(ctx).
		Where("user_id = ?", uid).
		Order("created_at DESC").
		Limit(limit).
		Find(&logs).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	items := make([]recentItem, len(logs))
	for i, l := range logs {
		items[i] = recentItem{
			ID:           l.ID,
			RequestID:    l.RequestID,
			Endpoint:     l.Endpoint,
			Model:        l.RequestModel,
			StatusCode:   l.StatusCode,
			PromptTokens: l.PromptTokens,
			OutputTokens: l.CompletionTokens,
			TotalTokens:  l.TotalTokens,
			CostCredits:  l.CostCredits,
			CostRMB:      credits.CreditsToRMB(l.CostCredits),
			LatencyMs:    l.TotalLatencyMs,
			CreatedAt:    l.CreatedAt,
			ImageCount:   l.ImageCount,
			CharCount:    l.CharCount,
			DurationSec:  l.DurationSec,
			CallCount:    l.CallCount,
		}
	}
	response.Success(c, items)
}
