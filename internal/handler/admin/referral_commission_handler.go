package admin

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
)

// ========================================================================
// ReferralCommissionHandler — 运营报表：邀请返佣明细
// 数据源: commission_records + users (inviter/invitee) + referral_attributions
// ========================================================================

// ReferralCommissionHandler 邀请返佣报表处理器
type ReferralCommissionHandler struct {
	db *gorm.DB
}

// NewReferralCommissionHandler 创建处理器实例
func NewReferralCommissionHandler(db *gorm.DB) *ReferralCommissionHandler {
	if db == nil {
		panic("referral_commission handler: db is nil")
	}
	return &ReferralCommissionHandler{db: db}
}

// Register 注册路由
func (h *ReferralCommissionHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/referral-commissions", h.List)
	rg.GET("/referral-commissions/daily", h.Daily)
	rg.GET("/referral-commissions/stats", h.Stats)
	rg.POST("/referral-commissions/export", h.ExportCSV)
}

// commissionRow 明细行结构
type commissionRow struct {
	ID                  uint       `json:"id"`
	CreatedAt           time.Time  `json:"created_at"`
	InviterID           uint       `json:"inviter_id"`
	InviterEmail        string     `json:"inviter_email"`
	InviterName         string     `json:"inviter_name"`
	InviteeID           uint       `json:"invitee_id"`
	InviteeEmail        string     `json:"invitee_email"`
	InviteeName         string     `json:"invitee_name"`
	TenantID            uint       `json:"tenant_id"`
	Type                string     `json:"type"`
	OrderAmount         int64      `json:"order_amount"`
	OrderAmountRMB      float64    `json:"order_amount_rmb"`
	EffectiveRate       float64    `json:"effective_rate"`
	CommissionRate      float64    `json:"commission_rate"`
	CommissionAmount    int64      `json:"commission_amount"`
	CommissionAmountRMB float64    `json:"commission_amount_rmb"`
	Status              string     `json:"status"`
	Credited            bool       `json:"credited"`
	SettleAt            *time.Time `json:"settle_at,omitempty"`
	RelatedID           string     `json:"related_id,omitempty"`
	AttributionID       *uint      `json:"attribution_id,omitempty"`
	OverrideID          *uint      `json:"override_id,omitempty"`
	ReferralCode        string     `json:"referral_code,omitempty"`
	AttributedAt        *time.Time `json:"attributed_at,omitempty"`
	ExpiresAt           *time.Time `json:"expires_at,omitempty"`
	UnlockedAt          *time.Time `json:"unlocked_at,omitempty"`
}

// applyCommissionFilters 共享筛选
func (h *ReferralCommissionHandler) applyCommissionFilters(q *gorm.DB, c *gin.Context) *gorm.DB {
	if sd := c.Query("start_date"); sd != "" {
		q = q.Where("cr.created_at >= ?", sd)
	}
	if ed := c.Query("end_date"); ed != "" {
		q = q.Where("cr.created_at <= ?", ed+" 23:59:59")
	}
	if invID := c.Query("inviter_id"); invID != "" {
		q = q.Where("cr.user_id = ?", invID)
	}
	if invEmail := c.Query("inviter_email"); invEmail != "" {
		q = q.Where("inv.email LIKE ?", "%"+invEmail+"%")
	}
	if ieID := c.Query("invitee_id"); ieID != "" {
		q = q.Where("cr.source_user_id = ?", ieID)
	}
	if ieEmail := c.Query("invitee_email"); ieEmail != "" {
		q = q.Where("ie.email LIKE ?", "%"+ieEmail+"%")
	}
	if statuses := c.QueryArray("status"); len(statuses) > 0 {
		q = q.Where("cr.status IN ?", statuses)
	}
	if typ := c.Query("type"); typ != "" {
		q = q.Where("cr.type = ?", typ)
	}
	if minAmt := c.Query("min_amount"); minAmt != "" {
		q = q.Where("cr.commission_amount >= ?", minAmt)
	}
	if maxAmt := c.Query("max_amount"); maxAmt != "" {
		q = q.Where("cr.commission_amount <= ?", maxAmt)
	}
	if rel := c.Query("related_id"); rel != "" {
		q = q.Where("cr.related_id LIKE ?", "%"+rel+"%")
	}
	if cred := c.Query("credited"); cred != "" {
		if cred == "true" || cred == "1" {
			q = q.Where("cr.credited = ?", true)
		} else if cred == "false" || cred == "0" {
			q = q.Where("cr.credited = ?", false)
		}
	}
	return q
}

// buildBaseQuery 构造 commission_records + JOIN
func (h *ReferralCommissionHandler) buildBaseQuery(ctx *gin.Context) *gorm.DB {
	return h.db.WithContext(ctx.Request.Context()).
		Table("commission_records AS cr").
		Joins("LEFT JOIN users AS inv ON inv.id = cr.user_id").
		Joins("LEFT JOIN users AS ie ON ie.id = cr.source_user_id").
		Joins("LEFT JOIN referral_attributions AS ra ON ra.id = cr.attribution_id")
}

// List GET /admin/referral-commissions
func (h *ReferralCommissionHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	q := h.buildBaseQuery(c)
	q = h.applyCommissionFilters(q, c)

	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	var rows []commissionRow
	offset := (page - 1) * pageSize
	err := q.Select(`cr.id, cr.created_at,
		cr.user_id AS inviter_id, inv.email AS inviter_email, inv.name AS inviter_name,
		cr.source_user_id AS invitee_id, ie.email AS invitee_email, ie.name AS invitee_name,
		cr.tenant_id, cr.type, cr.order_amount, cr.order_amount_rmb,
		cr.effective_rate, cr.commission_rate,
		cr.commission_amount, cr.commission_amount_rmb,
		cr.status, cr.credited, cr.settle_at, cr.related_id,
		cr.attribution_id, cr.override_id,
		ra.referral_code, ra.attributed_at, ra.expires_at, ra.unlocked_at`).
		Order("cr.created_at DESC").
		Offset(offset).Limit(pageSize).
		Scan(&rows).Error
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.PageResult(c, rows, total, page, pageSize)
}

// Daily GET /admin/referral-commissions/daily
// 支持 group_by: date_inviter(默认) / inviter / date
func (h *ReferralCommissionHandler) Daily(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	groupBy := c.DefaultQuery("group_by", "date_inviter")
	var groupCols, selectCols, defaultOrder string
	switch groupBy {
	case "inviter":
		groupCols = "cr.user_id"
		selectCols = `cr.user_id AS inviter_id, MAX(inv.email) AS inviter_email, MAX(inv.name) AS inviter_name,
			COUNT(*) AS commission_count,
			COUNT(DISTINCT cr.source_user_id) AS active_invitees,
			COUNT(DISTINCT DATE(cr.created_at)) AS active_days,
			SUM(cr.order_amount) AS total_order_credits,
			SUM(cr.order_amount_rmb) AS total_order_rmb,
			SUM(cr.commission_amount) AS total_commission_credits,
			SUM(cr.commission_amount_rmb) AS total_commission_rmb,
			SUM(CASE WHEN cr.status='PENDING'   THEN cr.commission_amount ELSE 0 END) AS pending_credits,
			SUM(CASE WHEN cr.status='SETTLED'   THEN cr.commission_amount ELSE 0 END) AS settled_credits,
			SUM(CASE WHEN cr.status='WITHDRAWN' THEN cr.commission_amount ELSE 0 END) AS withdrawn_credits,
			SUM(CASE WHEN cr.status='REFUNDED'  THEN cr.commission_amount ELSE 0 END) AS refunded_credits`
		defaultOrder = "total_commission_rmb DESC"
	case "date":
		groupCols = "DATE(cr.created_at)"
		selectCols = `DATE(cr.created_at) AS date,
			COUNT(*) AS commission_count,
			COUNT(DISTINCT cr.user_id) AS active_inviters,
			COUNT(DISTINCT cr.source_user_id) AS active_invitees,
			SUM(cr.order_amount) AS total_order_credits,
			SUM(cr.order_amount_rmb) AS total_order_rmb,
			SUM(cr.commission_amount) AS total_commission_credits,
			SUM(cr.commission_amount_rmb) AS total_commission_rmb,
			SUM(CASE WHEN cr.status='PENDING'   THEN cr.commission_amount ELSE 0 END) AS pending_credits,
			SUM(CASE WHEN cr.status='SETTLED'   THEN cr.commission_amount ELSE 0 END) AS settled_credits,
			SUM(CASE WHEN cr.status='WITHDRAWN' THEN cr.commission_amount ELSE 0 END) AS withdrawn_credits,
			SUM(CASE WHEN cr.status='REFUNDED'  THEN cr.commission_amount ELSE 0 END) AS refunded_credits`
		defaultOrder = "date DESC"
	default: // date_inviter
		groupBy = "date_inviter"
		groupCols = "DATE(cr.created_at), cr.user_id"
		selectCols = `DATE(cr.created_at) AS date,
			cr.user_id AS inviter_id, MAX(inv.email) AS inviter_email, MAX(inv.name) AS inviter_name,
			COUNT(*) AS commission_count,
			COUNT(DISTINCT cr.source_user_id) AS active_invitees,
			SUM(cr.order_amount) AS total_order_credits,
			SUM(cr.order_amount_rmb) AS total_order_rmb,
			SUM(cr.commission_amount) AS total_commission_credits,
			SUM(cr.commission_amount_rmb) AS total_commission_rmb,
			SUM(CASE WHEN cr.status='PENDING'   THEN cr.commission_amount ELSE 0 END) AS pending_credits,
			SUM(CASE WHEN cr.status='SETTLED'   THEN cr.commission_amount ELSE 0 END) AS settled_credits,
			SUM(CASE WHEN cr.status='WITHDRAWN' THEN cr.commission_amount ELSE 0 END) AS withdrawn_credits,
			SUM(CASE WHEN cr.status='REFUNDED'  THEN cr.commission_amount ELSE 0 END) AS refunded_credits`
		defaultOrder = "date DESC, total_commission_rmb DESC"
	}

	// 排序覆盖
	sort := c.Query("sort")
	orderBy := defaultOrder
	switch sort {
	case "commission_desc":
		orderBy = "total_commission_rmb DESC"
	case "pending_desc":
		orderBy = "pending_credits DESC"
	case "settled_desc":
		orderBy = "settled_credits DESC"
	case "count_desc":
		orderBy = "commission_count DESC"
	case "date_asc":
		if groupBy != "inviter" {
			orderBy = "date ASC"
		}
	}

	base := h.buildBaseQuery(c)
	base = h.applyCommissionFilters(base, c)

	// total 计数：用 COUNT(DISTINCT ...) 覆盖三种 group_by
	var countExpr string
	switch groupBy {
	case "inviter":
		countExpr = "COUNT(DISTINCT cr.user_id)"
	case "date":
		countExpr = "COUNT(DISTINCT DATE(cr.created_at))"
	default: // date_inviter
		countExpr = "COUNT(DISTINCT DATE(cr.created_at), cr.user_id)"
	}
	var total int64
	if err := base.Session(&gorm.Session{}).Select(countExpr + " AS c").Scan(&total).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	offset := (page - 1) * pageSize
	rows := []map[string]interface{}{}
	err := base.Select(selectCols).
		Group(groupCols).
		Order(orderBy).
		Offset(offset).Limit(pageSize).
		Scan(&rows).Error
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.PageResult(c, rows, total, page, pageSize)
}

// Stats GET /admin/referral-commissions/stats
func (h *ReferralCommissionHandler) Stats(c *gin.Context) {
	ctx := c.Request.Context()

	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	type aggResult struct {
		Count     int64   `json:"count"`
		Credits   int64   `json:"credits"`
		RMB       float64 `json:"rmb"`
		Invitees  int64   `json:"invitees"`
		Inviters  int64   `json:"inviters"`
	}

	type statsResp struct {
		Today           aggResult `json:"today"`
		Month           aggResult `json:"month"`
		PendingTotal    aggResult `json:"pending_total"`    // 未结算
		SettledMonth    aggResult `json:"settled_month"`    // 本月已结算
		WithdrawnMonth  aggResult `json:"withdrawn_month"`  // 本月已提现
		RefundedMonth   aggResult `json:"refunded_month"`   // 本月已退款
	}
	var out statsResp

	base := func() *gorm.DB {
		return h.db.WithContext(ctx).Table("commission_records")
	}

	selectAgg := `COUNT(*) AS count,
		COALESCE(SUM(commission_amount),0) AS credits,
		COALESCE(SUM(commission_amount_rmb),0) AS rmb,
		COUNT(DISTINCT source_user_id) AS invitees,
		COUNT(DISTINCT user_id) AS inviters`

	base().Where("created_at >= ?", todayStart).Select(selectAgg).Scan(&out.Today)
	base().Where("created_at >= ?", monthStart).Select(selectAgg).Scan(&out.Month)
	base().Where("status = ?", "PENDING").Select(selectAgg).Scan(&out.PendingTotal)
	base().Where("status = ? AND created_at >= ?", "SETTLED", monthStart).Select(selectAgg).Scan(&out.SettledMonth)
	base().Where("status = ? AND created_at >= ?", "WITHDRAWN", monthStart).Select(selectAgg).Scan(&out.WithdrawnMonth)
	base().Where("status = ? AND created_at >= ?", "REFUNDED", monthStart).Select(selectAgg).Scan(&out.RefundedMonth)

	response.Success(c, out)
}

// ExportCSV POST /admin/referral-commissions/export
func (h *ReferralCommissionHandler) ExportCSV(c *gin.Context) {
	const exportCap = 100000

	q := h.buildBaseQuery(c)
	q = h.applyCommissionFilters(q, c)

	var rows []commissionRow
	err := q.Select(`cr.id, cr.created_at,
		cr.user_id AS inviter_id, inv.email AS inviter_email, inv.name AS inviter_name,
		cr.source_user_id AS invitee_id, ie.email AS invitee_email, ie.name AS invitee_name,
		cr.tenant_id, cr.type, cr.order_amount, cr.order_amount_rmb,
		cr.effective_rate, cr.commission_rate,
		cr.commission_amount, cr.commission_amount_rmb,
		cr.status, cr.credited, cr.settle_at, cr.related_id,
		cr.attribution_id, cr.override_id,
		ra.referral_code, ra.attributed_at, ra.expires_at, ra.unlocked_at`).
		Order("cr.created_at DESC").Limit(exportCap).Scan(&rows).Error
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	filename := fmt.Sprintf("referral_commissions_%s.csv", time.Now().Format("20060102_150405"))
	c.Header("Content-Type", "text/csv; charset=utf-8-sig")
	c.Header("Content-Disposition", "attachment; filename=\""+filename+"\"")
	c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})
	w := csv.NewWriter(c.Writer)
	defer w.Flush()
	_ = w.Write([]string{
		"产生时间", "ID", "邀请人ID", "邀请人邮箱", "邀请人昵称",
		"被邀人ID", "被邀人邮箱", "被邀人昵称",
		"类型", "订单积分", "订单RMB", "生效比例",
		"佣金积分", "佣金RMB", "状态", "是否入账",
		"结算时间", "关联订单", "归因ID", "覆盖规则ID",
		"邀请码", "归因时间", "归因过期", "解锁时间",
	})
	for _, r := range rows {
		credited := "否"
		if r.Credited {
			credited = "是"
		}
		settleAt := ""
		if r.SettleAt != nil {
			settleAt = r.SettleAt.Format("2006-01-02 15:04:05")
		}
		attributedAt := ""
		if r.AttributedAt != nil {
			attributedAt = r.AttributedAt.Format("2006-01-02 15:04:05")
		}
		expiresAt := ""
		if r.ExpiresAt != nil {
			expiresAt = r.ExpiresAt.Format("2006-01-02 15:04:05")
		}
		unlockedAt := ""
		if r.UnlockedAt != nil {
			unlockedAt = r.UnlockedAt.Format("2006-01-02 15:04:05")
		}
		attrID := ""
		if r.AttributionID != nil {
			attrID = strconv.FormatUint(uint64(*r.AttributionID), 10)
		}
		ovID := ""
		if r.OverrideID != nil {
			ovID = strconv.FormatUint(uint64(*r.OverrideID), 10)
		}
		_ = w.Write([]string{
			r.CreatedAt.Format("2006-01-02 15:04:05"),
			strconv.FormatUint(uint64(r.ID), 10),
			strconv.FormatUint(uint64(r.InviterID), 10), r.InviterEmail, r.InviterName,
			strconv.FormatUint(uint64(r.InviteeID), 10), r.InviteeEmail, r.InviteeName,
			r.Type,
			strconv.FormatInt(r.OrderAmount, 10),
			fmt.Sprintf("%.4f", r.OrderAmountRMB),
			fmt.Sprintf("%.4f", r.EffectiveRate),
			strconv.FormatInt(r.CommissionAmount, 10),
			fmt.Sprintf("%.4f", r.CommissionAmountRMB),
			r.Status, credited, settleAt, r.RelatedID,
			attrID, ovID, r.ReferralCode,
			attributedAt, expiresAt, unlockedAt,
		})
	}
}
