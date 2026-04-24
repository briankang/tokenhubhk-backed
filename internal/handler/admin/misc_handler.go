package admin

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	auditmw "tokenhub-server/internal/middleware/audit"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/audit"
	reportSvc "tokenhub-server/internal/service/report"
)

// MiscHandler 杂项管理接口处理器（审计日志、每日统计、对账报告）
type MiscHandler struct {
	db       *gorm.DB
	auditSvc *audit.AuditService
}

// NewMiscHandler 创建杂项管理Handler实例
func NewMiscHandler(db *gorm.DB) *MiscHandler {
	if db == nil {
		panic("admin misc handler: db is nil")
	}
	return &MiscHandler{
		db:       db,
		auditSvc: audit.NewAuditService(db),
	}
}

// ListAuditLogs 获取审计日志列表 GET /api/v1/admin/audit-logs
// 支持过滤参数: action, operator_id, start_date, end_date
func (h *MiscHandler) ListAuditLogs(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	// 构建查询参数
	query := &model.AuditLogQuery{
		Page:     page,
		PageSize: pageSize,
	}

	// action 过滤
	if action := c.Query("action"); action != "" {
		query.Action = action
	}

	// 菜单 / 功能 / 资源 过滤（v3.3 新增）
	if menu := c.Query("menu"); menu != "" {
		query.Menu = menu
	}
	if feature := c.Query("feature"); feature != "" {
		query.Feature = feature
	}
	if resource := c.Query("resource"); resource != "" {
		query.Resource = resource
	}

	// operator_id 过滤
	if operatorID := c.Query("operator_id"); operatorID != "" {
		if oid, err := strconv.ParseUint(operatorID, 10, 64); err == nil && oid > 0 {
			query.OperatorID = uint(oid)
		}
	}

	// 日期范围过滤
	if startDate := c.Query("start_date"); startDate != "" {
		if t, err := time.Parse("2006-01-02", startDate); err == nil {
			query.StartDate = t
		}
	}
	if endDate := c.Query("end_date"); endDate != "" {
		if t, err := time.Parse("2006-01-02", endDate); err == nil {
			// 设置为当天结束时间
			query.EndDate = t.Add(24 * time.Hour).Add(-time.Second)
		}
	}

	logs, total, err := h.auditSvc.List(c.Request.Context(), query)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.PageResult(c, logs, total, page, pageSize)
}

// ListAuditMenus 返回审计日志可用的菜单列表 GET /api/v1/admin/audit-logs/menus
// 数据来自审计中间件 routeMap，前端筛选下拉框使用，避免前端硬编码
func (h *MiscHandler) ListAuditMenus(c *gin.Context) {
	menus := auditmw.AllMenus()
	sort.Strings(menus)
	response.Success(c, gin.H{"menus": menus})
}

// DailyStats 获取每日统计数据 GET /api/v1/admin/stats/daily
func (h *MiscHandler) DailyStats(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	query := h.db.WithContext(c.Request.Context()).Model(&model.DailyStats{})

	// Optional filters
	if date := c.Query("date"); date != "" {
		query = query.Where("date = ?", date)
	}
	if startDate := c.Query("start_date"); startDate != "" {
		query = query.Where("date >= ?", startDate)
	}
	if endDate := c.Query("end_date"); endDate != "" {
		query = query.Where("date <= ?", endDate)
	}
	if tenantID := c.Query("tenant_id"); tenantID != "" {
		if tid, err := strconv.ParseUint(tenantID, 10, 64); err == nil && tid > 0 {
			query = query.Where("tenant_id = ?", tid)
		}
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	var stats []model.DailyStats
	offset := (page - 1) * pageSize
	if err := query.Order("date DESC, id DESC").Offset(offset).Limit(pageSize).Find(&stats).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	response.PageResult(c, stats, total, page, pageSize)
}

// ReconciliationReport 获取对账报告 GET /api/v1/admin/reconciliation
// 数据来源：cron scheduler 的对账任务结果
func (h *MiscHandler) ReconciliationReport(c *gin.Context) {
	ctx := c.Request.Context()

	// 1. 统计过期冻结记录
	expireTime := time.Now().Add(-5 * time.Minute)
	var expiredFreezeCount int64
	var expiredFreezeTotal int64
	h.db.WithContext(ctx).Model(&model.FreezeRecord{}).
		Where("status = 'FROZEN' AND created_at < ?", expireTime).
		Count(&expiredFreezeCount)
	h.db.WithContext(ctx).Model(&model.FreezeRecord{}).
		Where("status = 'FROZEN' AND created_at < ?", expireTime).
		Select("COALESCE(SUM(frozen_amount), 0)").Scan(&expiredFreezeTotal)

	// 2. 统计余额异常用户（余额为负）
	var abnormalUserCount int64
	h.db.WithContext(ctx).Model(&model.UserBalance{}).
		Where("balance < 0 OR free_quota < 0 OR frozen_amount < 0").
		Count(&abnormalUserCount)

	// 3. 统计平台总收入（总充值）
	var totalRecharge int64
	h.db.WithContext(ctx).Model(&model.BalanceRecord{}).
		Where("type = 'RECHARGE'").
		Select("COALESCE(SUM(amount), 0)").Scan(&totalRecharge)

	// 4. 统计平台总消费
	var totalConsumed int64
	h.db.WithContext(ctx).Model(&model.BalanceRecord{}).
		Where("type = 'CONSUME'").
		Select("COALESCE(SUM(ABS(amount)), 0)").Scan(&totalConsumed)

	// 5. 当前平台总余额
	var totalBalance int64
	var totalFreeQuota int64
	var totalFrozen int64
	h.db.WithContext(ctx).Model(&model.UserBalance{}).
		Select("COALESCE(SUM(balance), 0)").Scan(&totalBalance)
	h.db.WithContext(ctx).Model(&model.UserBalance{}).
		Select("COALESCE(SUM(free_quota), 0)").Scan(&totalFreeQuota)
	h.db.WithContext(ctx).Model(&model.UserBalance{}).
		Select("COALESCE(SUM(frozen_amount), 0)").Scan(&totalFrozen)

	// 6. 冻结记录状态汇总
	type statusSummary struct {
		Status string `json:"status"`
		Count  int64  `json:"count"`
		Total  int64  `json:"total"`
	}
	var freezeSummary []statusSummary
	h.db.WithContext(ctx).Model(&model.FreezeRecord{}).
		Select("status, COUNT(*) as count, COALESCE(SUM(frozen_amount), 0) as total").
		Group("status").Scan(&freezeSummary)

	// 7. 今日统计
	today := time.Now().Format("2006-01-02")
	var todayRecharge int64
	var todayConsumed int64
	h.db.WithContext(ctx).Model(&model.BalanceRecord{}).
		Where("type = 'RECHARGE' AND DATE(created_at) = ?", today).
		Select("COALESCE(SUM(amount), 0)").Scan(&todayRecharge)
	h.db.WithContext(ctx).Model(&model.BalanceRecord{}).
		Where("type = 'CONSUME' AND DATE(created_at) = ?", today).
		Select("COALESCE(SUM(ABS(amount)), 0)").Scan(&todayConsumed)

	type billingSummary struct {
		TotalRequests          int64   `json:"total_requests"`
		SettledRequests        int64   `json:"settled_requests"`
		DeductFailedRequests   int64   `json:"deduct_failed_requests"`
		EstimatedRequests      int64   `json:"estimated_requests"`
		ActualRevenueCredits   int64   `json:"actual_revenue_credits"`
		ActualRevenueRMB       float64 `json:"actual_revenue_rmb"`
		EstimatedCostCredits   int64   `json:"estimated_cost_credits"`
		FrozenCredits          int64   `json:"frozen_credits"`
		UnderCollectedCredits  int64   `json:"under_collected_credits"`
		UnderCollectedRMB      float64 `json:"under_collected_rmb"`
		PlatformCostRMB        float64 `json:"platform_cost_rmb"`
		GrossProfitRMB         float64 `json:"gross_profit_rmb"`
		MissingUsageRequests   int64   `json:"missing_usage_requests"`
		MissingPlatformCostReq int64   `json:"missing_platform_cost_requests"`
	}
	var billing billingSummary
	h.db.WithContext(ctx).Table("api_call_logs").
		Select(
			"COUNT(*) AS total_requests," +
				"SUM(CASE WHEN COALESCE(billing_status,'settled') IN ('settled','no_charge') THEN 1 ELSE 0 END) AS settled_requests," +
				"SUM(CASE WHEN billing_status = 'deduct_failed' THEN 1 ELSE 0 END) AS deduct_failed_requests," +
				"SUM(CASE WHEN usage_estimated = TRUE OR usage_source = 'estimated' THEN 1 ELSE 0 END) AS estimated_requests," +
				"COALESCE(SUM(CASE WHEN COALESCE(actual_cost_credits,0) > 0 THEN actual_cost_credits WHEN COALESCE(billing_status,'settled') = 'settled' THEN cost_credits ELSE 0 END),0) AS actual_revenue_credits," +
				"COALESCE(SUM(CASE WHEN COALESCE(actual_cost_credits,0) > 0 THEN actual_cost_credits WHEN COALESCE(billing_status,'settled') = 'settled' THEN cost_credits ELSE 0 END),0)/10000.0 AS actual_revenue_rmb," +
				"COALESCE(SUM(estimated_cost_credits),0) AS estimated_cost_credits," +
				"COALESCE(SUM(frozen_credits),0) AS frozen_credits," +
				"COALESCE(SUM(under_collected_credits),0) AS under_collected_credits," +
				"COALESCE(SUM(under_collected_credits),0)/10000.0 AS under_collected_rmb," +
				"COALESCE(SUM(platform_cost_rmb),0) AS platform_cost_rmb," +
				"(COALESCE(SUM(CASE WHEN COALESCE(actual_cost_credits,0) > 0 THEN actual_cost_credits WHEN COALESCE(billing_status,'settled') = 'settled' THEN cost_credits ELSE 0 END),0)/10000.0 - COALESCE(SUM(platform_cost_rmb),0)) AS gross_profit_rmb," +
				"SUM(CASE WHEN COALESCE(usage_source,'') = '' OR usage_source = 'unknown' THEN 1 ELSE 0 END) AS missing_usage_requests," +
				"SUM(CASE WHEN COALESCE(platform_cost_rmb,0) = 0 AND COALESCE(total_tokens,0) > 0 THEN 1 ELSE 0 END) AS missing_platform_cost_requests",
		).Scan(&billing)

	response.Success(c, gin.H{
		"expiredFreezes": gin.H{
			"count": expiredFreezeCount,
			"total": expiredFreezeTotal,
		},
		"abnormalUsers": gin.H{
			"count": abnormalUserCount,
		},
		"platformStats": gin.H{
			"totalRecharge":  totalRecharge,
			"totalConsumed":  totalConsumed,
			"totalBalance":   totalBalance,
			"totalFreeQuota": totalFreeQuota,
			"totalFrozen":    totalFrozen,
		},
		"freezeSummary": freezeSummary,
		"todayStats": gin.H{
			"date":     today,
			"recharge": todayRecharge,
			"consumed": todayConsumed,
		},
		"billingReconciliation": billing,
		"generatedAt":           time.Now(),
	})
}

// BillingReconciliationSnapshots returns persisted daily billing reconciliation snapshots.
func (h *MiscHandler) BillingReconciliationSnapshots(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	svc := reportSvc.NewBillingReconciliationService(h.db)
	list, total, err := svc.List(c.Request.Context(), page, pageSize)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.PageResult(c, list, total, page, pageSize)
}

// CreateBillingReconciliationSnapshot builds and persists a snapshot for a date.
func (h *MiscHandler) CreateBillingReconciliationSnapshot(c *gin.Context) {
	date := c.DefaultQuery("date", time.Now().Format("2006-01-02"))
	svc := reportSvc.NewBillingReconciliationService(h.db)
	snap, err := svc.UpsertDate(c.Request.Context(), date)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, snap)
}
