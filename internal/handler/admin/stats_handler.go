package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/pkg/errcode"
	pkgredis "tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/pkg/response"
)

// ========================================================================
// 共享缓存辅助（admin:stats:*）
// 所有 /admin/stats/* 端点复用此逻辑：
//   - cacheHit: true 表示命中并已写响应；调用方应 return
//   - cacheWrite: 计算完成后在返回 response.Success 前写缓存
// Redis 不可用时 fail-open（cacheHit 永远 false，cacheWrite 静默跳过）
// ========================================================================

const (
	statsCacheTTLShort = 30 * time.Minute
	statsCacheTTLLong  = 1 * time.Hour
)

func statsCacheGet(ctx context.Context, key string, dst any) bool {
	if pkgredis.Client == nil {
		return false
	}
	raw, err := pkgredis.Client.Get(ctx, key).Result()
	if err != nil {
		return false
	}
	return json.Unmarshal([]byte(raw), dst) == nil
}

func statsCacheSet(ctx context.Context, key string, value any, ttl time.Duration) {
	if pkgredis.Client == nil {
		return
	}
	if data, err := json.Marshal(value); err == nil {
		_ = pkgredis.Client.Set(ctx, key, string(data), ttl).Err()
	}
}

func statsCacheKey(kind, startDate, endDate string, extra ...string) string {
	base := fmt.Sprintf("admin:stats:%s:%s:%s", kind, startDate, endDate)
	for _, e := range extra {
		base += ":" + e
	}
	return base
}

// StatsHandler 运营统计接口处理器
type StatsHandler struct {
	db *gorm.DB
}

// NewStatsHandler 创建统计接口 Handler
func NewStatsHandler(db *gorm.DB) *StatsHandler {
	return &StatsHandler{db: db}
}

// Register 注册路由
func (h *StatsHandler) Register(rg *gin.RouterGroup) {
	stats := rg.Group("/stats")
	{
		stats.GET("/registrations",    h.GetRegistrationStats)
		stats.GET("/referrals",        h.GetReferralStats)
		stats.GET("/pnl",              h.GetPnLStatement)
		stats.GET("/payment-analysis", h.GetPaymentAnalysis)
	}
}

// ────────────────────────────────────────────────────────────────
// 辅助类型
// ────────────────────────────────────────────────────────────────

// parseDateRange 解析通用日期范围参数，缺省近 30 天
func parseDateRange(c *gin.Context) (startDate, endDate string) {
	startDate = c.Query("start_date")
	endDate = c.Query("end_date")
	if startDate == "" {
		startDate = time.Now().AddDate(0, 0, -30).Format("2006-01-02")
	}
	if endDate == "" {
		endDate = time.Now().Format("2006-01-02")
	}
	return
}

// ────────────────────────────────────────────────────────────────
// 1. 用户注册统计
// ────────────────────────────────────────────────────────────────

// RegistrationDayItem 每日注册数据点
type RegistrationDayItem struct {
	Date         string  `json:"date"`
	NewUsers     int64   `json:"new_users"`
	InvitedUsers int64   `json:"invited_users"`
	GiftCredits  int64   `json:"gift_credits"`  // 赠送积分(估算)
	GiftRmb      float64 `json:"gift_rmb"`      // 赠送金额(估算)
}

// RegistrationStatsResponse 注册统计响应
type RegistrationStatsResponse struct {
	Items        []RegistrationDayItem `json:"items"`
	TotalNewUsers int64               `json:"total_new_users"`
	TotalInvited  int64               `json:"total_invited"`
	TotalGiftRmb  float64             `json:"total_gift_rmb"`
}

// GetRegistrationStats GET /api/v1/admin/stats/registrations
func (h *StatsHandler) GetRegistrationStats(c *gin.Context) {
	startDate, endDate := parseDateRange(c)

	// 尝试缓存命中（1h TTL）
	cacheKey := statsCacheKey("registrations", startDate, endDate)
	var cached RegistrationStatsResponse
	if statsCacheGet(c.Request.Context(), cacheKey, &cached) {
		response.Success(c, cached)
		return
	}

	// 1. 查询每日注册数（含邀请注册子集）
	type dayRow struct {
		Date         string `gorm:"column:date"`
		NewUsers     int64  `gorm:"column:new_users"`
		InvitedUsers int64  `gorm:"column:invited_users"`
	}
	var rows []dayRow

	err := h.db.WithContext(c.Request.Context()).Raw(`
		SELECT
			DATE_FORMAT(u.created_at, '%Y-%m-%d') AS date,
			COUNT(*)           AS new_users,
			COALESCE(SUM(CASE WHEN ra.id IS NOT NULL THEN 1 ELSE 0 END), 0) AS invited_users
		FROM users u
		LEFT JOIN referral_attributions ra ON ra.user_id = u.id AND ra.is_valid = true
		WHERE DATE(u.created_at) BETWEEN ? AND ?
		GROUP BY DATE_FORMAT(u.created_at, '%Y-%m-%d')
		ORDER BY date ASC
	`, startDate, endDate).Scan(&rows).Error

	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 2. 查询默认注册赠送积分
	var defaultFreeQuota int64
	h.db.WithContext(c.Request.Context()).Raw(
		`SELECT COALESCE(default_free_quota, 0) FROM quota_configs LIMIT 1`,
	).Scan(&defaultFreeQuota)

	// 3. 组装响应
	var totalNew, totalInvited int64
	var totalGiftRmb float64

	items := make([]RegistrationDayItem, 0, len(rows))
	for _, r := range rows {
		giftCredits := r.NewUsers * defaultFreeQuota
		giftRmb := float64(giftCredits) / 10000.0
		items = append(items, RegistrationDayItem{
			Date:         r.Date,
			NewUsers:     r.NewUsers,
			InvitedUsers: r.InvitedUsers,
			GiftCredits:  giftCredits,
			GiftRmb:      giftRmb,
		})
		totalNew += r.NewUsers
		totalInvited += r.InvitedUsers
		totalGiftRmb += giftRmb
	}

	resp := RegistrationStatsResponse{
		Items:         items,
		TotalNewUsers: totalNew,
		TotalInvited:  totalInvited,
		TotalGiftRmb:  totalGiftRmb,
	}
	statsCacheSet(c.Request.Context(), cacheKey, resp, statsCacheTTLLong)
	response.Success(c, resp)
}

// ────────────────────────────────────────────────────────────────
// 2. 邀请数据明细
// ────────────────────────────────────────────────────────────────

// ReferralDetailItem 邀请明细行
type ReferralDetailItem struct {
	AttributionID      uint    `json:"attribution_id"`
	InviterID          uint    `json:"inviter_id"`
	InviterEmail       string  `json:"inviter_email"`
	InviterName        string  `json:"inviter_name"`
	InviteeEmail       string  `json:"invitee_email"`
	AttributedAt       string  `json:"attributed_at"`
	UnlockedAt         *string `json:"unlocked_at"`
	ExpiresAt          string  `json:"expires_at"`
	IsValid            bool    `json:"is_valid"`
	TotalCommissionRmb float64 `json:"total_commission_rmb"`
	PendingRmb         float64 `json:"pending_rmb"`
	SettledRmb         float64 `json:"settled_rmb"`
	InviteeBonusGranted bool   `json:"invitee_bonus_granted"`
	InviterBonusGranted bool   `json:"inviter_bonus_granted"`
}

// ReferralStatsSummary 邀请汇总
type ReferralStatsSummary struct {
	TotalInvitations   int64   `json:"total_invitations"`
	Unlocked           int64   `json:"unlocked"`
	TotalCommissionRmb float64 `json:"total_commission_rmb"`
	PendingRmb         float64 `json:"pending_rmb"`
}

// ReferralStatsResponse 邀请数据响应
type ReferralStatsResponse struct {
	List    []ReferralDetailItem `json:"list"`
	Total   int64                `json:"total"`
	Summary ReferralStatsSummary `json:"summary"`
}

// GetReferralStats GET /api/v1/admin/stats/referrals
func (h *StatsHandler) GetReferralStats(c *gin.Context) {
	startDate, endDate := parseDateRange(c)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	statusFilter := c.Query("status") // pending / settled / all
	keyword := c.Query("keyword")

	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	// 缓存键含分页 + 筛选条件（仅无 keyword 时命中，keyword 搜索基数大不缓存）
	cacheable := keyword == ""
	cacheKey := statsCacheKey("referrals", startDate, endDate,
		fmt.Sprintf("p%d", page),
		fmt.Sprintf("s%d", pageSize),
		"f"+statusFilter,
	)
	if cacheable {
		var cached ReferralStatsResponse
		if statsCacheGet(c.Request.Context(), cacheKey, &cached) {
			response.Success(c, cached)
			return
		}
	}

	// 构建 WHERE 子句
	statusWhere := ""
	args := []interface{}{startDate, endDate}

	switch statusFilter {
	case "pending":
		statusWhere = "AND EXISTS (SELECT 1 FROM commission_records cr2 WHERE cr2.attribution_id = ra.id AND cr2.status = 'PENDING')"
	case "settled":
		statusWhere = "AND EXISTS (SELECT 1 FROM commission_records cr2 WHERE cr2.attribution_id = ra.id AND cr2.status IN ('SETTLED','WITHDRAWN'))"
	}

	keywordWhere := ""
	if keyword != "" {
		keywordWhere = "AND (u_inv.email LIKE ? OR u_itee.email LIKE ?)"
		args = append(args, "%"+keyword+"%", "%"+keyword+"%")
	}

	// 计数查询
	var total int64
	countSQL := `
		SELECT COUNT(DISTINCT ra.id)
		FROM referral_attributions ra
		JOIN users u_inv  ON ra.inviter_id = u_inv.id
		JOIN users u_itee ON ra.user_id    = u_itee.id
		WHERE DATE(ra.attributed_at) BETWEEN ? AND ?
		` + statusWhere + keywordWhere
	if err := h.db.WithContext(c.Request.Context()).Raw(countSQL, args...).Scan(&total).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 明细查询
	type rawRow struct {
		ID                 uint    `gorm:"column:id"`
		InviterID          uint    `gorm:"column:inviter_id"`
		InviterEmail       string  `gorm:"column:inviter_email"`
		InviterName        string  `gorm:"column:inviter_name"`
		InviteeEmail       string  `gorm:"column:invitee_email"`
		AttributedAt       string  `gorm:"column:attributed_at"`
		UnlockedAt         *string `gorm:"column:unlocked_at"`
		ExpiresAt          string  `gorm:"column:expires_at"`
		IsValid            bool    `gorm:"column:is_valid"`
		InviteeBonusGranted bool   `gorm:"column:invitee_bonus_granted"`
		InviterBonusGranted bool   `gorm:"column:inviter_bonus_granted"`
		TotalCommission    float64 `gorm:"column:total_commission_rmb"`
		PendingRmb         float64 `gorm:"column:pending_rmb"`
		SettledRmb         float64 `gorm:"column:settled_rmb"`
	}
	var rawRows []rawRow

	listArgs := append(args, pageSize, offset)
	listSQL := `
		SELECT
			ra.id,
			ra.inviter_id,
			u_inv.email  AS inviter_email,
			u_inv.name   AS inviter_name,
			u_itee.email AS invitee_email,
			DATE_FORMAT(ra.attributed_at, '%Y-%m-%dT%H:%i:%sZ') AS attributed_at,
			DATE_FORMAT(ra.unlocked_at,   '%Y-%m-%dT%H:%i:%sZ') AS unlocked_at,
			DATE_FORMAT(ra.expires_at,    '%Y-%m-%dT%H:%i:%sZ') AS expires_at,
			ra.is_valid,
			ra.invitee_bonus_granted,
			ra.inviter_bonus_granted,
			COALESCE(SUM(cr.commission_amount_rmb), 0)                                                                          AS total_commission_rmb,
			COALESCE(SUM(CASE WHEN cr.status = 'PENDING'                  THEN cr.commission_amount_rmb ELSE 0 END), 0)         AS pending_rmb,
			COALESCE(SUM(CASE WHEN cr.status IN ('SETTLED','WITHDRAWN')   THEN cr.commission_amount_rmb ELSE 0 END), 0)         AS settled_rmb
		FROM referral_attributions ra
		JOIN users u_inv  ON ra.inviter_id = u_inv.id
		JOIN users u_itee ON ra.user_id    = u_itee.id
		LEFT JOIN commission_records cr ON cr.attribution_id = ra.id
		WHERE DATE(ra.attributed_at) BETWEEN ? AND ?
		` + statusWhere + keywordWhere + `
		GROUP BY ra.id, ra.inviter_id, u_inv.email, u_inv.name, u_itee.email,
		         ra.attributed_at, ra.unlocked_at, ra.expires_at,
		         ra.is_valid, ra.invitee_bonus_granted, ra.inviter_bonus_granted
		ORDER BY ra.attributed_at DESC
		LIMIT ? OFFSET ?`

	if err := h.db.WithContext(c.Request.Context()).Raw(listSQL, listArgs...).Scan(&rawRows).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 全局 summary（不受分页影响）
	type summaryRow struct {
		TotalInvitations int64   `gorm:"column:total_invitations"`
		Unlocked         int64   `gorm:"column:unlocked"`
		TotalCommission  float64 `gorm:"column:total_commission_rmb"`
		Pending          float64 `gorm:"column:pending_rmb"`
	}
	var sum summaryRow
	summaryArgs := []interface{}{startDate, endDate}
	if keyword != "" {
		summaryArgs = append(summaryArgs, "%"+keyword+"%", "%"+keyword+"%")
	}
	summarySQL := `
		SELECT
			COUNT(DISTINCT ra.id)                                                                                          AS total_invitations,
			COUNT(DISTINCT CASE WHEN ra.unlocked_at IS NOT NULL THEN ra.id END)                                            AS unlocked,
			COALESCE(SUM(cr.commission_amount_rmb), 0)                                                                     AS total_commission_rmb,
			COALESCE(SUM(CASE WHEN cr.status = 'PENDING' THEN cr.commission_amount_rmb ELSE 0 END), 0)                     AS pending_rmb
		FROM referral_attributions ra
		JOIN users u_inv  ON ra.inviter_id = u_inv.id
		JOIN users u_itee ON ra.user_id    = u_itee.id
		LEFT JOIN commission_records cr ON cr.attribution_id = ra.id
		WHERE DATE(ra.attributed_at) BETWEEN ? AND ?
		` + keywordWhere
	h.db.WithContext(c.Request.Context()).Raw(summarySQL, summaryArgs...).Scan(&sum)

	// 组装
	items := make([]ReferralDetailItem, 0, len(rawRows))
	for _, r := range rawRows {
		items = append(items, ReferralDetailItem{
			AttributionID:       r.ID,
			InviterID:           r.InviterID,
			InviterEmail:        r.InviterEmail,
			InviterName:         r.InviterName,
			InviteeEmail:        r.InviteeEmail,
			AttributedAt:        r.AttributedAt,
			UnlockedAt:          r.UnlockedAt,
			ExpiresAt:           r.ExpiresAt,
			IsValid:             r.IsValid,
			TotalCommissionRmb:  r.TotalCommission,
			PendingRmb:          r.PendingRmb,
			SettledRmb:          r.SettledRmb,
			InviteeBonusGranted: r.InviteeBonusGranted,
			InviterBonusGranted: r.InviterBonusGranted,
		})
	}

	resp := ReferralStatsResponse{
		List:  items,
		Total: total,
		Summary: ReferralStatsSummary{
			TotalInvitations:   sum.TotalInvitations,
			Unlocked:           sum.Unlocked,
			TotalCommissionRmb: sum.TotalCommission,
			PendingRmb:         sum.Pending,
		},
	}
	if cacheable {
		statsCacheSet(c.Request.Context(), cacheKey, resp, statsCacheTTLShort)
	}
	response.Success(c, resp)
}

// ────────────────────────────────────────────────────────────────
// 3. 利润表（P&L Statement）
// ────────────────────────────────────────────────────────────────

// PnLDayItem 每日利润表行
type PnLDayItem struct {
	Date               string  `json:"date"`
	RechargeRmb        float64 `json:"recharge_rmb"`         // 充值进账
	ApiRevenueRmb      float64 `json:"api_revenue_rmb"`      // API 产生收入（按售价）
	ApiCostRmb         float64 `json:"api_cost_rmb"`         // API 供应商成本
	CommissionPaidRmb  float64 `json:"commission_paid_rmb"`  // 佣金结算支出
	RefundRmb          float64 `json:"refund_rmb"`           // 退款支出
	GiftCostRmb        float64 `json:"gift_cost_rmb"`        // 赠送成本（估算）
	NetProfitRmb       float64 `json:"net_profit_rmb"`       // 净利润
	ProfitMargin       float64 `json:"profit_margin"`        // 利润率(小数)
	RequestCount       int64   `json:"request_count"`        // API 调用次数
}

// PnLSummary 利润表汇总
type PnLSummary struct {
	TotalRechargeRmb       float64 `json:"total_recharge_rmb"`
	TotalApiRevenueRmb     float64 `json:"total_api_revenue_rmb"`
	TotalApiCostRmb        float64 `json:"total_api_cost_rmb"`
	TotalCommissionPaidRmb float64 `json:"total_commission_paid_rmb"`
	TotalRefundRmb         float64 `json:"total_refund_rmb"`
	TotalGiftCostRmb       float64 `json:"total_gift_cost_rmb"`
	NetProfitRmb           float64 `json:"net_profit_rmb"`
	AvgProfitMargin        float64 `json:"avg_profit_margin"`
	// 按网关分拆充值
	WechatRmb  float64 `json:"wechat_rmb"`
	AlipayRmb  float64 `json:"alipay_rmb"`
	StripeRmb  float64 `json:"stripe_rmb"`
	PaypalRmb  float64 `json:"paypal_rmb"`
}

// PnLResponse 利润表响应
type PnLResponse struct {
	Items   []PnLDayItem `json:"items"`
	Summary PnLSummary   `json:"summary"`
}

// GetPnLStatement GET /api/v1/admin/stats/pnl
func (h *StatsHandler) GetPnLStatement(c *gin.Context) {
	startDate, endDate := parseDateRange(c)

	ctx := c.Request.Context()

	// 尝试缓存（1h TTL，P&L 数据稳定）
	cacheKey := statsCacheKey("pnl", startDate, endDate)
	var cached PnLResponse
	if statsCacheGet(ctx, cacheKey, &cached) {
		response.Success(c, cached)
		return
	}

	// ---------- 1. 每日充值进账（payments, status=completed）----------
	type rechargeRow struct {
		Date    string  `gorm:"column:date"`
		Gateway string  `gorm:"column:gateway"`
		Amount  float64 `gorm:"column:amount"`
	}
	var rechargeRows []rechargeRow
	h.db.WithContext(ctx).Raw(`
		SELECT DATE_FORMAT(created_at, '%Y-%m-%d') AS date, gateway, SUM(rmb_amount) AS amount
		FROM payments
		WHERE status = 'completed' AND DATE(created_at) BETWEEN ? AND ?
		GROUP BY DATE_FORMAT(created_at, '%Y-%m-%d'), gateway
		ORDER BY date ASC
	`, startDate, endDate).Scan(&rechargeRows)

	// 聚合充值到 map[date]充值总额
	rechargeByDate := map[string]float64{}
	gatewayByDate := map[string]map[string]float64{}
	for _, r := range rechargeRows {
		rechargeByDate[r.Date] += r.Amount
		if gatewayByDate[r.Date] == nil {
			gatewayByDate[r.Date] = map[string]float64{}
		}
		gatewayByDate[r.Date][r.Gateway] += r.Amount
	}

	// ---------- 2. 每日 API 收入 + 成本（daily_stats）----------
	type statsRow struct {
		Date         string  `gorm:"column:date"`
		TotalRevenue float64 `gorm:"column:total_revenue"`
		TotalCost    float64 `gorm:"column:total_cost"`
		TotalReqs    int64   `gorm:"column:total_reqs"`
	}
	var statsRows []statsRow
	h.db.WithContext(ctx).Raw(`
		SELECT DATE_FORMAT(date, '%Y-%m-%d') AS date,
		       SUM(total_revenue) AS total_revenue,
		       SUM(total_cost)    AS total_cost,
		       SUM(total_requests) AS total_reqs
		FROM daily_stats
		WHERE date BETWEEN ? AND ?
		GROUP BY DATE(date)
		ORDER BY date ASC
	`, startDate, endDate).Scan(&statsRows)

	statsByDate := map[string]statsRow{}
	for _, s := range statsRows {
		statsByDate[s.Date] = s
	}

	// ---------- 3. 每日佣金结算支出（commission_records）----------
	type commRow struct {
		Date   string  `gorm:"column:date"`
		Amount float64 `gorm:"column:amount"`
	}
	var commRows []commRow
	h.db.WithContext(ctx).Raw(`
		SELECT DATE_FORMAT(settle_at, '%Y-%m-%d') AS date, SUM(commission_amount_rmb) AS amount
		FROM commission_records
		WHERE status IN ('SETTLED','WITHDRAWN')
		  AND DATE(settle_at) BETWEEN ? AND ?
		GROUP BY DATE_FORMAT(settle_at, '%Y-%m-%d')
		ORDER BY date ASC
	`, startDate, endDate).Scan(&commRows)

	commByDate := map[string]float64{}
	for _, r := range commRows {
		commByDate[r.Date] = r.Amount
	}

	// ---------- 4. 每日退款（payments, refunded_amount > 0）----------
	type refundRow struct {
		Date   string  `gorm:"column:date"`
		Amount float64 `gorm:"column:amount"`
	}
	var refundRows []refundRow
	h.db.WithContext(ctx).Raw(`
		SELECT DATE_FORMAT(updated_at, '%Y-%m-%d') AS date, SUM(refunded_amount) AS amount
		FROM payments
		WHERE status IN ('refunded','partial_refunded')
		  AND DATE(updated_at) BETWEEN ? AND ?
		GROUP BY DATE_FORMAT(updated_at, '%Y-%m-%d')
		ORDER BY date ASC
	`, startDate, endDate).Scan(&refundRows)

	refundByDate := map[string]float64{}
	for _, r := range refundRows {
		refundByDate[r.Date] = r.Amount
	}

	// ---------- 5. 每日新增用户 + 赠送成本（估算）----------
	type regRow struct {
		Date     string `gorm:"column:date"`
		NewUsers int64  `gorm:"column:new_users"`
	}
	var regRows []regRow
	h.db.WithContext(ctx).Raw(`
		SELECT DATE_FORMAT(created_at, '%Y-%m-%d') AS date, COUNT(*) AS new_users
		FROM users
		WHERE DATE(created_at) BETWEEN ? AND ?
		GROUP BY DATE_FORMAT(created_at, '%Y-%m-%d')
	`, startDate, endDate).Scan(&regRows)

	var defaultFreeQuota int64
	h.db.WithContext(ctx).Raw(`SELECT COALESCE(default_free_quota, 0) FROM quota_configs LIMIT 1`).Scan(&defaultFreeQuota)

	regByDate := map[string]int64{}
	for _, r := range regRows {
		regByDate[r.Date] = r.NewUsers
	}

	// ---------- 6. 生成每日 P&L 日历（按 daily_stats 日期驱动）----------
	// 合并所有日期
	dateSet := map[string]struct{}{}
	for d := range rechargeByDate {
		dateSet[d] = struct{}{}
	}
	for d := range statsByDate {
		dateSet[d] = struct{}{}
	}
	for d := range commByDate {
		dateSet[d] = struct{}{}
	}
	for d := range refundByDate {
		dateSet[d] = struct{}{}
	}
	for d := range regByDate {
		dateSet[d] = struct{}{}
	}

	// 按日期序列填充
	items := make([]PnLDayItem, 0, len(dateSet))
	sum := PnLSummary{}

	// 收集日期排序
	dates := make([]string, 0, len(dateSet))
	for d := range dateSet {
		dates = append(dates, d)
	}
	// 简单升序（日期格式 YYYY-MM-DD 可字符串比较）
	sortStrings(dates)

	for _, d := range dates {
		recharge := rechargeByDate[d]
		s := statsByDate[d]
		commission := commByDate[d]
		refund := refundByDate[d]
		giftCost := float64(regByDate[d]*defaultFreeQuota) / 10000.0

		net := s.TotalRevenue - s.TotalCost - commission - refund - giftCost
		margin := 0.0
		if s.TotalRevenue > 0 {
			margin = net / s.TotalRevenue
		}

		items = append(items, PnLDayItem{
			Date:              d,
			RechargeRmb:       recharge,
			ApiRevenueRmb:     s.TotalRevenue,
			ApiCostRmb:        s.TotalCost,
			CommissionPaidRmb: commission,
			RefundRmb:         refund,
			GiftCostRmb:       giftCost,
			NetProfitRmb:      net,
			ProfitMargin:      margin,
			RequestCount:      s.TotalReqs,
		})

		// 网关汇总
		if gw := gatewayByDate[d]; gw != nil {
			sum.WechatRmb += gw["wechat"]
			sum.AlipayRmb += gw["alipay"]
			sum.StripeRmb += gw["stripe"]
			sum.PaypalRmb += gw["paypal"]
		}

		sum.TotalRechargeRmb += recharge
		sum.TotalApiRevenueRmb += s.TotalRevenue
		sum.TotalApiCostRmb += s.TotalCost
		sum.TotalCommissionPaidRmb += commission
		sum.TotalRefundRmb += refund
		sum.TotalGiftCostRmb += giftCost
	}

	sum.NetProfitRmb = sum.TotalApiRevenueRmb - sum.TotalApiCostRmb - sum.TotalCommissionPaidRmb - sum.TotalRefundRmb - sum.TotalGiftCostRmb
	if sum.TotalApiRevenueRmb > 0 {
		sum.AvgProfitMargin = sum.NetProfitRmb / sum.TotalApiRevenueRmb
	}

	resp := PnLResponse{Items: items, Summary: sum}
	statsCacheSet(ctx, cacheKey, resp, statsCacheTTLLong)
	response.Success(c, resp)
}

// ────────────────────────────────────────────────────────────────
// 4. 支付分析（经营分析用）
// ────────────────────────────────────────────────────────────────

// PaymentDayItem 每日支付汇总
type PaymentDayItem struct {
	Date           string  `json:"date"`
	TotalRechargeRmb float64 `json:"total_recharge_rmb"`
	TotalRefundRmb   float64 `json:"total_refund_rmb"`
	NetRmb           float64 `json:"net_rmb"`
	OrderCount       int64   `json:"order_count"`
	RefundCount      int64   `json:"refund_count"`
	WechatRmb        float64 `json:"wechat_rmb"`
	AlipayRmb        float64 `json:"alipay_rmb"`
	StripeRmb        float64 `json:"stripe_rmb"`
	PaypalRmb        float64 `json:"paypal_rmb"`
}

// GatewaySummaryItem 网关汇总
type GatewaySummaryItem struct {
	Gateway      string  `json:"gateway"`
	TotalRmb     float64 `json:"total_rmb"`
	OrderCount   int64   `json:"order_count"`
	SuccessRate  float64 `json:"success_rate"`
}

// PaymentSummary 支付总体汇总
type PaymentSummary struct {
	TotalRechargeRmb float64 `json:"total_recharge_rmb"`
	TotalRefundRmb   float64 `json:"total_refund_rmb"`
	TotalOrderCount  int64   `json:"total_order_count"`
}

// PaymentAnalysisResponse 支付分析响应
type PaymentAnalysisResponse struct {
	Daily          []PaymentDayItem    `json:"daily"`
	GatewaySummary []GatewaySummaryItem `json:"gateway_summary"`
	Summary        PaymentSummary      `json:"summary"`
}

// GetPaymentAnalysis GET /api/v1/admin/stats/payment-analysis
func (h *StatsHandler) GetPaymentAnalysis(c *gin.Context) {
	startDate, endDate := parseDateRange(c)
	ctx := c.Request.Context()

	// 尝试缓存（1h TTL）
	cacheKey := statsCacheKey("payment-analysis", startDate, endDate)
	var cached PaymentAnalysisResponse
	if statsCacheGet(ctx, cacheKey, &cached) {
		response.Success(c, cached)
		return
	}

	// 按日+网关聚合
	type rawRow struct {
		Date         string  `gorm:"column:date"`
		Gateway      string  `gorm:"column:gateway"`
		Status       string  `gorm:"column:status"`
		OrderCount   int64   `gorm:"column:order_count"`
		TotalRmb     float64 `gorm:"column:total_rmb"`
		RefundedRmb  float64 `gorm:"column:refunded_rmb"`
	}
	var rawRows []rawRow

	h.db.WithContext(ctx).Raw(`
		SELECT
			DATE_FORMAT(created_at, '%Y-%m-%d') AS date,
			gateway,
			status,
			COUNT(*)             AS order_count,
			SUM(rmb_amount)      AS total_rmb,
			SUM(refunded_amount) AS refunded_rmb
		FROM payments
		WHERE DATE(created_at) BETWEEN ? AND ?
		GROUP BY DATE_FORMAT(created_at, '%Y-%m-%d'), gateway, status
		ORDER BY date ASC
	`, startDate, endDate).Scan(&rawRows)

	// 整理成 daily map
	type dayAgg struct {
		WechatRmb    float64
		AlipayRmb    float64
		StripeRmb    float64
		PaypalRmb    float64
		RechargeRmb  float64
		RefundRmb    float64
		OrderCount   int64
		RefundCount  int64
	}
	dayMap := map[string]*dayAgg{}

	// 网关 success/total 计数
	type gwAgg struct {
		TotalRmb    float64
		OrderCount  int64
		SuccessCount int64
	}
	gwMap := map[string]*gwAgg{}

	for _, r := range rawRows {
		if dayMap[r.Date] == nil {
			dayMap[r.Date] = &dayAgg{}
		}
		agg := dayMap[r.Date]

		if gwMap[r.Gateway] == nil {
			gwMap[r.Gateway] = &gwAgg{}
		}
		gw := gwMap[r.Gateway]

		gw.OrderCount += r.OrderCount

		if r.Status == "completed" {
			agg.RechargeRmb += r.TotalRmb
			agg.OrderCount += r.OrderCount
			gw.TotalRmb += r.TotalRmb
			gw.SuccessCount += r.OrderCount

			switch r.Gateway {
			case "wechat":
				agg.WechatRmb += r.TotalRmb
			case "alipay":
				agg.AlipayRmb += r.TotalRmb
			case "stripe":
				agg.StripeRmb += r.TotalRmb
			case "paypal":
				agg.PaypalRmb += r.TotalRmb
			}
		}

		if r.Status == "refunded" || r.Status == "partial_refunded" {
			agg.RefundRmb += r.RefundedRmb
			agg.RefundCount += r.OrderCount
		}
	}

	// 组装 daily 列表
	dates := make([]string, 0, len(dayMap))
	for d := range dayMap {
		dates = append(dates, d)
	}
	sortStrings(dates)

	daily := make([]PaymentDayItem, 0, len(dates))
	sumRecharge, sumRefund := 0.0, 0.0
	var sumOrders int64

	for _, d := range dates {
		a := dayMap[d]
		daily = append(daily, PaymentDayItem{
			Date:             d,
			TotalRechargeRmb: a.RechargeRmb,
			TotalRefundRmb:   a.RefundRmb,
			NetRmb:           a.RechargeRmb - a.RefundRmb,
			OrderCount:       a.OrderCount,
			RefundCount:      a.RefundCount,
			WechatRmb:        a.WechatRmb,
			AlipayRmb:        a.AlipayRmb,
			StripeRmb:        a.StripeRmb,
			PaypalRmb:        a.PaypalRmb,
		})
		sumRecharge += a.RechargeRmb
		sumRefund += a.RefundRmb
		sumOrders += a.OrderCount
	}

	// 网关汇总
	gateways := []string{"wechat", "alipay", "stripe", "paypal"}
	gwSummary := make([]GatewaySummaryItem, 0, len(gateways))
	for _, g := range gateways {
		agg := gwMap[g]
		if agg == nil {
			continue
		}
		rate := 0.0
		if agg.OrderCount > 0 {
			rate = float64(agg.SuccessCount) / float64(agg.OrderCount)
		}
		gwSummary = append(gwSummary, GatewaySummaryItem{
			Gateway:     g,
			TotalRmb:    agg.TotalRmb,
			OrderCount:  agg.SuccessCount,
			SuccessRate: rate,
		})
	}

	resp := PaymentAnalysisResponse{
		Daily:          daily,
		GatewaySummary: gwSummary,
		Summary: PaymentSummary{
			TotalRechargeRmb: sumRecharge,
			TotalRefundRmb:   sumRefund,
			TotalOrderCount:  sumOrders,
		},
	}
	statsCacheSet(ctx, cacheKey, resp, statsCacheTTLLong)
	response.Success(c, resp)
}

// ────────────────────────────────────────────────────────────────
// 工具函数
// ────────────────────────────────────────────────────────────────

// sortStrings 简单插入排序（日期字符串，数量通常 < 100）
func sortStrings(ss []string) {
	for i := 1; i < len(ss); i++ {
		key := ss[i]
		j := i - 1
		for j >= 0 && ss[j] > key {
			ss[j+1] = ss[j]
			j--
		}
		ss[j+1] = key
	}
}
