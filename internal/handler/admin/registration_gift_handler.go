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
// RegistrationGiftHandler — 运营报表：注册赠送明细
// 数据源: balance_records WHERE type IN ('GIFT','INVITEE_BONUS','INVITER_BONUS')
// ========================================================================

// RegistrationGiftHandler 注册赠送明细处理器
type RegistrationGiftHandler struct {
	db *gorm.DB
}

// NewRegistrationGiftHandler 创建处理器实例
func NewRegistrationGiftHandler(db *gorm.DB) *RegistrationGiftHandler {
	if db == nil {
		panic("registration_gift handler: db is nil")
	}
	return &RegistrationGiftHandler{db: db}
}

// Register 注册路由
func (h *RegistrationGiftHandler) Register(rg *gin.RouterGroup) {
	rg.GET("/registration-gifts", h.List)
	rg.GET("/registration-gifts/stats", h.Stats)
	rg.POST("/registration-gifts/export", h.ExportCSV)
}

// giftRow 返回结构：BalanceRecord + 关联用户 + 关联邀请人
type giftRow struct {
	ID            uint      `json:"id"`
	CreatedAt     time.Time `json:"created_at"`
	UserID        uint      `json:"user_id"`
	UserEmail     string    `json:"user_email"`
	UserName      string    `json:"user_name"`
	TenantID      uint      `json:"tenant_id"`
	Type          string    `json:"type"`           // GIFT / INVITEE_BONUS / INVITER_BONUS
	Amount        int64     `json:"amount"`
	AmountRMB     float64   `json:"amount_rmb"`
	BeforeBalance int64     `json:"before_balance"`
	AfterBalance  int64     `json:"after_balance"`
	Remark        string    `json:"remark"`
	RelatedID     string    `json:"related_id,omitempty"` // 邀请码
	InviterID     *uint     `json:"inviter_id,omitempty"`
	InviterEmail  string    `json:"inviter_email,omitempty"`
}

// giftTypes 枚举：统一在一处维护，避免到处 hard-code
var giftTypes = []string{"GIFT", "INVITEE_BONUS", "INVITER_BONUS"}

// applyGiftFilters 公共筛选逻辑，供 List / ExportCSV 共用
func (h *RegistrationGiftHandler) applyGiftFilters(q *gorm.DB, c *gin.Context) *gorm.DB {
	// 默认限定为赠送类型
	if typesQ := c.QueryArray("type"); len(typesQ) > 0 {
		q = q.Where("r.type IN ?", typesQ)
	} else {
		q = q.Where("r.type IN ?", giftTypes)
	}
	if sd := c.Query("start_date"); sd != "" {
		q = q.Where("r.created_at >= ?", sd)
	}
	if ed := c.Query("end_date"); ed != "" {
		q = q.Where("r.created_at <= ?", ed+" 23:59:59")
	}
	if uid := c.Query("user_id"); uid != "" {
		q = q.Where("r.user_id = ?", uid)
	}
	if ue := c.Query("user_email"); ue != "" {
		q = q.Where("u.email LIKE ?", "%"+ue+"%")
	}
	if minAmt := c.Query("min_amount"); minAmt != "" {
		q = q.Where("r.amount >= ?", minAmt)
	}
	if maxAmt := c.Query("max_amount"); maxAmt != "" {
		q = q.Where("r.amount <= ?", maxAmt)
	}
	if code := c.Query("related_id"); code != "" {
		q = q.Where("r.related_id LIKE ?", "%"+code+"%")
	}
	return q
}

// List GET /admin/registration-gifts
func (h *RegistrationGiftHandler) List(c *gin.Context) {
	ctx := c.Request.Context()
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	q := h.db.WithContext(ctx).
		Table("balance_records AS r").
		Joins("LEFT JOIN users AS u ON u.id = r.user_id").
		Joins("LEFT JOIN referral_attributions AS a ON a.user_id = r.user_id").
		Joins("LEFT JOIN users AS inv ON inv.id = a.inviter_id")
	q = h.applyGiftFilters(q, c)

	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	var rows []giftRow
	offset := (page - 1) * pageSize
	err := q.Select(`r.id, r.created_at, r.user_id, u.email AS user_email, u.name AS user_name,
		r.tenant_id, r.type, r.amount, r.amount_rmb,
		r.before_balance, r.after_balance, r.remark, r.related_id,
		a.inviter_id, inv.email AS inviter_email`).
		Order("r.created_at DESC").
		Offset(offset).Limit(pageSize).
		Scan(&rows).Error
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.PageResult(c, rows, total, page, pageSize)
}

// Stats GET /admin/registration-gifts/stats
func (h *RegistrationGiftHandler) Stats(c *gin.Context) {
	ctx := c.Request.Context()

	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	type aggResult struct {
		Count  int64   `json:"count"`
		Amount int64   `json:"amount"`
		RMB    float64 `json:"rmb"`
	}

	type statsResp struct {
		Today                 aggResult `json:"today"`
		Month                 aggResult `json:"month"`
		PendingInviteeUnlock  int64     `json:"pending_invitee_unlock"`
		MonthInviterGranted   aggResult `json:"month_inviter_granted"`
	}
	var out statsResp

	// 今日汇总
	h.db.WithContext(ctx).Table("balance_records").
		Where("type IN ?", giftTypes).
		Where("created_at >= ?", todayStart).
		Select("COUNT(*) AS count, COALESCE(SUM(amount),0) AS amount, COALESCE(SUM(amount_rmb),0) AS rmb").
		Scan(&out.Today)

	// 本月汇总
	h.db.WithContext(ctx).Table("balance_records").
		Where("type IN ?", giftTypes).
		Where("created_at >= ?", monthStart).
		Select("COUNT(*) AS count, COALESCE(SUM(amount),0) AS amount, COALESCE(SUM(amount_rmb),0) AS rmb").
		Scan(&out.Month)

	// 待解锁邀请奖励数
	h.db.WithContext(ctx).Table("referral_attributions").
		Where("is_valid = ? AND invitee_bonus_granted = ?", true, false).
		Count(&out.PendingInviteeUnlock)

	// 本月邀请人奖励发放
	h.db.WithContext(ctx).Table("balance_records").
		Where("type = ?", "INVITER_BONUS").
		Where("created_at >= ?", monthStart).
		Select("COUNT(*) AS count, COALESCE(SUM(amount),0) AS amount, COALESCE(SUM(amount_rmb),0) AS rmb").
		Scan(&out.MonthInviterGranted)

	response.Success(c, out)
}

// ExportCSV POST /admin/registration-gifts/export
func (h *RegistrationGiftHandler) ExportCSV(c *gin.Context) {
	ctx := c.Request.Context()
	const exportCap = 100000

	q := h.db.WithContext(ctx).
		Table("balance_records AS r").
		Joins("LEFT JOIN users AS u ON u.id = r.user_id").
		Joins("LEFT JOIN referral_attributions AS a ON a.user_id = r.user_id").
		Joins("LEFT JOIN users AS inv ON inv.id = a.inviter_id")
	q = h.applyGiftFilters(q, c)

	var rows []giftRow
	err := q.Select(`r.id, r.created_at, r.user_id, u.email AS user_email, u.name AS user_name,
		r.tenant_id, r.type, r.amount, r.amount_rmb,
		r.before_balance, r.after_balance, r.remark, r.related_id,
		a.inviter_id, inv.email AS inviter_email`).
		Order("r.created_at DESC").Limit(exportCap).Scan(&rows).Error
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	filename := fmt.Sprintf("registration_gifts_%s.csv", time.Now().Format("20060102_150405"))
	c.Header("Content-Type", "text/csv; charset=utf-8-sig")
	c.Header("Content-Disposition", "attachment; filename=\""+filename+"\"")
	c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})
	w := csv.NewWriter(c.Writer)
	defer w.Flush()
	_ = w.Write([]string{"发放时间", "ID", "用户ID", "邮箱", "昵称", "赠送类型", "积分", "等值RMB", "发放前余额", "发放后余额", "关联邀请码", "邀请人邮箱", "备注"})
	for _, r := range rows {
		inviterEmail := r.InviterEmail
		_ = w.Write([]string{
			r.CreatedAt.Format("2006-01-02 15:04:05"),
			strconv.FormatUint(uint64(r.ID), 10),
			strconv.FormatUint(uint64(r.UserID), 10),
			r.UserEmail, r.UserName, r.Type,
			strconv.FormatInt(r.Amount, 10),
			fmt.Sprintf("%.4f", r.AmountRMB),
			strconv.FormatInt(r.BeforeBalance, 10),
			strconv.FormatInt(r.AfterBalance, 10),
			r.RelatedID, inviterEmail, r.Remark,
		})
	}
}
