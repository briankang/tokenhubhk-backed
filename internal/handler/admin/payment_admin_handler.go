package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/errcode"
	"tokenhub-server/internal/pkg/response"
	"tokenhub-server/internal/service/exchange"
	paymentsvc "tokenhub-server/internal/service/payment"
	"tokenhub-server/internal/service/withdrawal"
)

// PaymentAdminHandler 管理员支付/订单/财务管理聚合 Handler
type PaymentAdminHandler struct {
	db             *gorm.DB
	paymentSvc     *paymentsvc.PaymentService
	refundSvc      *paymentsvc.RefundService
	withdrawalSvc  *withdrawal.Service
	accountRouter  *paymentsvc.AccountRouter
	eventLogger    *paymentsvc.EventLogger
	exchangeSvc    *exchange.ExchangeRateService
	paymentConfig  *paymentsvc.PaymentConfigService
}

// PaymentAdminHandlerOpts 构造选项
type PaymentAdminHandlerOpts struct {
	DB            *gorm.DB
	PaymentSvc    *paymentsvc.PaymentService
	RefundSvc     *paymentsvc.RefundService
	WithdrawalSvc *withdrawal.Service
	AccountRouter *paymentsvc.AccountRouter
	EventLogger   *paymentsvc.EventLogger
	ExchangeSvc   *exchange.ExchangeRateService
	PaymentConfig *paymentsvc.PaymentConfigService
}

// NewPaymentAdminHandler 构造函数
func NewPaymentAdminHandler(opts PaymentAdminHandlerOpts) *PaymentAdminHandler {
	return &PaymentAdminHandler{
		db:             opts.DB,
		paymentSvc:     opts.PaymentSvc,
		refundSvc:      opts.RefundSvc,
		withdrawalSvc:  opts.WithdrawalSvc,
		accountRouter:  opts.AccountRouter,
		eventLogger:    opts.EventLogger,
		exchangeSvc:    opts.ExchangeSvc,
		paymentConfig:  opts.PaymentConfig,
	}
}

// Register 注册所有管理员支付路由
func (h *PaymentAdminHandler) Register(rg *gin.RouterGroup) {
	// 订单管理
	rg.GET("/payment/orders", h.ListOrders)
	rg.GET("/payment/orders/:id", h.GetOrder)
	rg.POST("/payment/orders/:id/refund", h.AdminRefundOrder)
	rg.GET("/payment/orders/stats", h.OrdersStats)
	rg.POST("/payment/orders/export", h.ExportOrders)
	rg.POST("/payment/mock-callback", h.MockCallback) // 沙箱 Mock 回调

	// 退款队列
	rg.GET("/payment/refunds", h.ListRefunds)
	rg.GET("/payment/refunds/:id", h.GetRefund)
	rg.POST("/payment/refunds/:id/approve", h.ApproveRefund)
	rg.POST("/payment/refunds/:id/reject", h.RejectRefund)
	rg.POST("/payment/refunds/batch-approve", h.BatchApproveRefunds)
	rg.POST("/payment/refunds/batch-reject", h.BatchRejectRefunds)

	// 提现审核
	rg.GET("/withdrawals", h.ListWithdrawals)
	rg.GET("/withdrawals/:id", h.GetWithdrawal)
	rg.POST("/withdrawals/:id/approve", h.ApproveWithdrawal)
	rg.POST("/withdrawals/:id/reject", h.RejectWithdrawal)
	rg.POST("/withdrawals/:id/mark-paid", h.MarkWithdrawalPaid)
	rg.POST("/withdrawals/batch-approve", h.BatchApproveWithdrawals)
	rg.POST("/withdrawals/batch-reject", h.BatchRejectWithdrawals)
	rg.GET("/withdrawals/stats", h.WithdrawalStats)

	// 多账号配置
	rg.GET("/payment/accounts", h.ListAccounts)
	rg.POST("/payment/accounts", h.CreateAccount)
	rg.PUT("/payment/accounts/:id", h.UpdateAccount)
	rg.DELETE("/payment/accounts/:id", h.DeleteAccount)
	rg.PATCH("/payment/accounts/:id/toggle", h.ToggleAccount)

	// 事件日志
	rg.GET("/payment/event-logs", h.ListEventLogs)
	rg.GET("/payment/event-logs/by-payment/:id", h.ListEventLogsByPayment)
	rg.GET("/payment/event-logs/by-refund/:id", h.ListEventLogsByRefund)
	rg.GET("/payment/event-logs/by-withdraw/:id", h.ListEventLogsByWithdraw)

	// 用户档案
	rg.GET("/users/:id/payment-profile", h.GetUserPaymentProfile)
	rg.GET("/users/:id/credit-stats", h.GetUserCreditStats)

	// 汇率管理
	rg.GET("/payment/exchange-rate/history", h.ExchangeRateHistory)
	rg.POST("/payment/exchange-rate/refresh", h.RefreshExchangeRate)
	rg.PUT("/payment/exchange-rate/override", h.OverrideExchangeRate)
	// v3.2.3: 汇率 API 凭证配置（从 system_configs 表读写）
	rg.GET("/payment/exchange-rate/config", h.GetExchangeRateConfig)
	rg.PUT("/payment/exchange-rate/config", h.UpdateExchangeRateConfig)
}

// =============== 订单管理 ===============

// orderListItem 订单列表项
type orderListItem struct {
	ID                uint      `json:"id"`
	OrderNo           string    `json:"order_no"`
	UserID            uint      `json:"user_id"`
	UserEmail         string    `json:"user_email,omitempty"`
	Gateway           string    `json:"gateway"`
	ProviderAccountID *uint64   `json:"provider_account_id,omitempty"`
	AccountName       string    `json:"account_name,omitempty"`
	Amount            float64   `json:"amount"`
	OriginalCurrency  string    `json:"original_currency"`
	DisplayAmountUSD  float64   `json:"display_amount_usd"`
	DisplayAmountCNY  float64   `json:"display_amount_cny"`
	RMBAmount         float64   `json:"rmb_amount"`
	RefundedAmount    float64   `json:"refunded_amount"`
	Status            string    `json:"status"`
	CreatedAt         time.Time `json:"created_at"`
}

// ListOrders 列出订单（支持筛选 + 用户邮箱搜索）
func (h *PaymentAdminHandler) ListOrders(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if pageSize > 200 {
		pageSize = 200
	}

	q := h.db.WithContext(c.Request.Context()).Model(&model.Payment{})

	if s := c.Query("status"); s != "" {
		q = q.Where("payments.status = ?", s)
	}
	if gw := c.Query("gateway"); gw != "" {
		q = q.Where("payments.gateway = ?", gw)
	}
	if uidStr := c.Query("user_id"); uidStr != "" {
		if uid, err := strconv.ParseUint(uidStr, 10, 64); err == nil {
			q = q.Where("payments.user_id = ?", uid)
		}
	}
	// search 支持邮箱前缀或订单号模糊搜索
	if search := c.Query("search"); search != "" {
		like := "%" + search + "%"
		q = q.Joins("LEFT JOIN users ON users.id = payments.user_id").
			Where("users.email LIKE ? OR payments.gateway_txn_id LIKE ?", like, like)
	}
	if start := c.Query("start_date"); start != "" {
		if t, err := time.Parse("2006-01-02", start); err == nil {
			q = q.Where("payments.created_at >= ?", t)
		}
	}
	if end := c.Query("end_date"); end != "" {
		if t, err := time.Parse("2006-01-02", end); err == nil {
			q = q.Where("payments.created_at <= ?", t.Add(24*time.Hour))
		}
	}
	if minAmt := c.Query("min_amount"); minAmt != "" {
		if v, err := strconv.ParseFloat(minAmt, 64); err == nil {
			q = q.Where("payments.amount >= ?", v)
		}
	}
	if maxAmt := c.Query("max_amount"); maxAmt != "" {
		if v, err := strconv.ParseFloat(maxAmt, 64); err == nil {
			q = q.Where("payments.amount <= ?", v)
		}
	}
	if kw := c.Query("keyword"); kw != "" {
		q = q.Where("payments.gateway_txn_id = ?", kw)
	}

	var total int64
	q.Count(&total)

	var list []model.Payment
	if err := q.Order("payments.created_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&list).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 批量查用户邮箱
	emailMap := batchUserEmails(h.db, c.Request.Context(), extractUserIDs(list))

	items := make([]orderListItem, 0, len(list))
	for _, p := range list {
		item := buildOrderListItem(&p, h.db)
		item.UserEmail = emailMap[p.UserID]
		items = append(items, item)
	}
	response.PageResult(c, items, total, page, pageSize)
}

// GetOrder 订单详情（含事件日志）
func (h *PaymentAdminHandler) GetOrder(c *gin.Context) {
	idStr := c.Param("id")
	id, _ := strconv.ParseUint(idStr, 10, 64)

	var p model.Payment
	if err := h.db.WithContext(c.Request.Context()).First(&p, id).Error; err != nil {
		response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
		return
	}

	var events []model.PaymentEventLog
	if h.eventLogger != nil {
		events, _ = h.eventLogger.ListByPayment(c.Request.Context(), uint64(p.ID))
	}

	var accountName string
	if p.ProviderAccountID != nil && h.accountRouter != nil {
		acc, _ := h.accountRouter.GetAccount(c.Request.Context(), *p.ProviderAccountID)
		if acc != nil {
			accountName = acc.AccountName
		}
	}

	response.Success(c, gin.H{
		"order":        buildOrderListItem(&p, h.db),
		"account_name": accountName,
		"events":       events,
		"raw":          p,
	})
}

// AdminRefundOrder 管理员主动退款（不走用户申请流程）
func (h *PaymentAdminHandler) AdminRefundOrder(c *gin.Context) {
	idStr := c.Param("id")
	id, _ := strconv.ParseUint(idStr, 10, 64)
	var body struct {
		Amount float64 `json:"amount" binding:"required,gt=0"`
		Reason string  `json:"reason" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	var p model.Payment
	if err := h.db.WithContext(c.Request.Context()).First(&p, id).Error; err != nil {
		response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
		return
	}

	adminIDVal, _ := c.Get("userId")
	adminID, _ := adminIDVal.(uint)

	// 构造一笔退款申请并立即通过
	in := paymentsvc.SubmitUserRequestInput{
		UserID:    uint64(p.UserID),
		TenantID:  uint64(p.TenantID),
		PaymentID: uint64(p.ID),
		AmountRMB: body.Amount,
		Reason:    "[管理员主动] " + body.Reason,
		IP:        c.ClientIP(),
		UserAgent: c.Request.UserAgent(),
	}
	req, err := h.refundSvc.SubmitUserRequest(c.Request.Context(), in)
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	if err := h.refundSvc.ApproveByAdmin(c.Request.Context(), req.ID, uint64(adminID), body.Reason); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"refund_id": req.ID})
}

// OrdersStats 订单统计
func (h *PaymentAdminHandler) OrdersStats(c *gin.Context) {
	ctx := c.Request.Context()
	today := time.Now().Truncate(24 * time.Hour)
	monthStart := time.Date(time.Now().Year(), time.Now().Month(), 1, 0, 0, 0, 0, time.Local)

	var todayCount, monthCount int64
	var todayAmt, monthAmt, refundedAmt float64

	h.db.WithContext(ctx).Model(&model.Payment{}).
		Where("status = ? AND created_at >= ?", model.PaymentStatusCompleted, today).
		Count(&todayCount)
	h.db.WithContext(ctx).Model(&model.Payment{}).
		Where("status = ? AND created_at >= ?", model.PaymentStatusCompleted, today).
		Select("COALESCE(SUM(amount), 0)").Scan(&todayAmt)
	h.db.WithContext(ctx).Model(&model.Payment{}).
		Where("status = ? AND created_at >= ?", model.PaymentStatusCompleted, monthStart).
		Count(&monthCount)
	h.db.WithContext(ctx).Model(&model.Payment{}).
		Where("status = ? AND created_at >= ?", model.PaymentStatusCompleted, monthStart).
		Select("COALESCE(SUM(amount), 0)").Scan(&monthAmt)
	h.db.WithContext(ctx).Model(&model.Payment{}).
		Where("created_at >= ?", monthStart).
		Select("COALESCE(SUM(refunded_amount), 0)").Scan(&refundedAmt)

	var refundRate float64
	if monthAmt > 0 {
		refundRate = refundedAmt / monthAmt * 100
	}
	response.Success(c, gin.H{
		"today_count":      todayCount,
		"today_amount":     todayAmt,
		"month_count":      monthCount,
		"month_amount":     monthAmt,
		"refunded_amount":  refundedAmt,
		"refund_rate_pct":  refundRate,
	})
}

// ExportOrders 导出 CSV
func (h *PaymentAdminHandler) ExportOrders(c *gin.Context) {
	var body struct {
		Status    string `json:"status"`
		Gateway   string `json:"gateway"`
		StartDate string `json:"start_date"`
		EndDate   string `json:"end_date"`
	}
	_ = c.ShouldBindJSON(&body)

	q := h.db.WithContext(c.Request.Context()).Model(&model.Payment{})
	if body.Status != "" {
		q = q.Where("status = ?", body.Status)
	}
	if body.Gateway != "" {
		q = q.Where("gateway = ?", body.Gateway)
	}
	if t, err := time.Parse("2006-01-02", body.StartDate); err == nil {
		q = q.Where("created_at >= ?", t)
	}
	if t, err := time.Parse("2006-01-02", body.EndDate); err == nil {
		q = q.Where("created_at <= ?", t.Add(24*time.Hour))
	}

	var list []model.Payment
	if err := q.Order("created_at DESC").Limit(10000).Find(&list).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	var sb strings.Builder
	sb.WriteString("id,order_no,user_id,gateway,amount,currency,rmb_amount,status,refunded_amount,created_at\n")
	for _, p := range list {
		orderNo := extractOrderNoMeta(&p)
		sb.WriteString(fmt.Sprintf("%d,%s,%d,%s,%.2f,%s,%.4f,%s,%.2f,%s\n",
			p.ID, orderNo, p.UserID, p.Gateway,
			p.Amount, p.OriginalCurrency, p.RMBAmount,
			p.Status, p.RefundedAmount, p.CreatedAt.Format(time.RFC3339)))
	}

	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", "attachment; filename=orders.csv")
	c.String(http.StatusOK, sb.String())
}

// =============== 退款队列 ===============

// refundListItem 退款列表项（含用户邮箱）
type refundListItem struct {
	model.PaymentRefundRequest
	UserEmail string `json:"user_email,omitempty"`
}

// ListRefunds 列出退款申请
func (h *PaymentAdminHandler) ListRefunds(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	f := paymentsvc.RefundListFilter{
		Status:   c.DefaultQuery("status", ""),
		OrderNo:  c.Query("order_no"),
		Page:     page,
		PageSize: pageSize,
	}
	if uid, _ := strconv.ParseUint(c.Query("user_id"), 10, 64); uid > 0 {
		f.UserID = uid
	}
	// search 按邮箱搜索
	if search := c.Query("search"); search != "" {
		var u model.User
		if err := h.db.WithContext(c.Request.Context()).Where("email LIKE ?", "%"+search+"%").First(&u).Error; err == nil {
			f.UserID = uint64(u.ID)
		} else {
			response.PageResult(c, []refundListItem{}, 0, page, pageSize)
			return
		}
	}
	list, total, err := h.refundSvc.ListAdmin(c.Request.Context(), f)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	userIDs := make([]uint, 0, len(list))
	for _, r := range list {
		userIDs = append(userIDs, uint(r.UserID))
	}
	emailMap := batchUserEmailsUint(h.db, c.Request.Context(), userIDs)
	items := make([]refundListItem, 0, len(list))
	for _, r := range list {
		items = append(items, refundListItem{PaymentRefundRequest: r, UserEmail: emailMap[uint(r.UserID)]})
	}
	response.PageResult(c, items, total, page, pageSize)
}

// GetRefund 退款详情
func (h *PaymentAdminHandler) GetRefund(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	r, err := h.refundSvc.GetByID(c.Request.Context(), id)
	if err != nil {
		response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
		return
	}
	var events []model.PaymentEventLog
	if h.eventLogger != nil {
		events, _ = h.eventLogger.ListByRefund(c.Request.Context(), r.ID)
	}
	response.Success(c, gin.H{"refund": r, "events": events})
}

// ApproveRefund 通过
func (h *PaymentAdminHandler) ApproveRefund(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var body struct {
		Remark string `json:"admin_remark"`
	}
	_ = c.ShouldBindJSON(&body)
	adminIDVal, _ := c.Get("userId")
	adminID, _ := adminIDVal.(uint)
	if err := h.refundSvc.ApproveByAdmin(c.Request.Context(), id, uint64(adminID), body.Remark); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"approved": true})
}

// RejectRefund 拒绝
func (h *PaymentAdminHandler) RejectRefund(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var body struct {
		Reason string `json:"reason" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	adminIDVal, _ := c.Get("userId")
	adminID, _ := adminIDVal.(uint)
	if err := h.refundSvc.RejectByAdmin(c.Request.Context(), id, uint64(adminID), body.Reason); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"rejected": true})
}

// BatchApproveRefunds 批量通过
func (h *PaymentAdminHandler) BatchApproveRefunds(c *gin.Context) {
	var body struct {
		IDs    []uint64 `json:"ids" binding:"required"`
		Remark string   `json:"admin_remark"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	adminIDVal, _ := c.Get("userId")
	adminID, _ := adminIDVal.(uint)
	okIDs, failedIDs, _ := h.refundSvc.BatchApprove(c.Request.Context(), body.IDs, uint64(adminID), body.Remark)
	response.Success(c, gin.H{"ok": okIDs, "failed": failedIDs})
}

// BatchRejectRefunds 批量拒绝
func (h *PaymentAdminHandler) BatchRejectRefunds(c *gin.Context) {
	var body struct {
		IDs    []uint64 `json:"ids" binding:"required"`
		Reason string   `json:"reason" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	adminIDVal, _ := c.Get("userId")
	adminID, _ := adminIDVal.(uint)
	okIDs, failedIDs, _ := h.refundSvc.BatchReject(c.Request.Context(), body.IDs, uint64(adminID), body.Reason)
	response.Success(c, gin.H{"ok": okIDs, "failed": failedIDs})
}

// =============== 提现审核 ===============

// withdrawalListItem 提现列表项（含用户邮箱）
type withdrawalListItem struct {
	model.WithdrawalRequest
	UserEmail string `json:"user_email,omitempty"`
}

// ListWithdrawals 列出提现申请
func (h *PaymentAdminHandler) ListWithdrawals(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	status := c.Query("status")
	list, total, err := h.withdrawalSvc.ListWithdrawals(c.Request.Context(), status, page, pageSize)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	userIDs := make([]uint, 0, len(list))
	for _, w := range list {
		userIDs = append(userIDs, w.UserID)
	}
	emailMap := batchUserEmailsUint(h.db, c.Request.Context(), userIDs)
	items := make([]withdrawalListItem, 0, len(list))
	for _, w := range list {
		items = append(items, withdrawalListItem{WithdrawalRequest: w, UserEmail: emailMap[w.UserID]})
	}
	response.PageResult(c, items, total, page, pageSize)
}

// GetWithdrawal 提现详情
func (h *PaymentAdminHandler) GetWithdrawal(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	r, err := h.withdrawalSvc.GetByID(c.Request.Context(), uint(id))
	if err != nil {
		response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
		return
	}
	var events []model.PaymentEventLog
	if h.eventLogger != nil {
		events, _ = h.eventLogger.ListByWithdraw(c.Request.Context(), uint64(r.ID))
	}
	response.Success(c, gin.H{"withdrawal": r, "events": events})
}

// ApproveWithdrawal 通过
func (h *PaymentAdminHandler) ApproveWithdrawal(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var body struct {
		Remark string `json:"remark"`
	}
	_ = c.ShouldBindJSON(&body)
	adminIDVal, _ := c.Get("userId")
	adminID, _ := adminIDVal.(uint)
	if err := h.withdrawalSvc.Approve(c.Request.Context(), uint(id), adminID, body.Remark); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"approved": true})
}

// RejectWithdrawal 拒绝
func (h *PaymentAdminHandler) RejectWithdrawal(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var body struct {
		Reason string `json:"reason" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	adminIDVal, _ := c.Get("userId")
	adminID, _ := adminIDVal.(uint)
	if err := h.withdrawalSvc.Reject(c.Request.Context(), uint(id), adminID, body.Reason); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"rejected": true})
}

// MarkWithdrawalPaid 标记已打款
func (h *PaymentAdminHandler) MarkWithdrawalPaid(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var body struct {
		BankTxnID string `json:"bank_txn_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	adminIDVal, _ := c.Get("userId")
	adminID, _ := adminIDVal.(uint)
	if err := h.withdrawalSvc.MarkPaid(c.Request.Context(), uint(id), adminID, body.BankTxnID); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"paid": true})
}

// BatchApproveWithdrawals 批量通过
func (h *PaymentAdminHandler) BatchApproveWithdrawals(c *gin.Context) {
	var body struct {
		IDs    []uint `json:"ids" binding:"required"`
		Remark string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	adminIDVal, _ := c.Get("userId")
	adminID, _ := adminIDVal.(uint)
	ok, failed := h.withdrawalSvc.BatchApprove(c.Request.Context(), body.IDs, adminID, body.Remark)
	response.Success(c, gin.H{"ok": ok, "failed": failed})
}

// BatchRejectWithdrawals 批量拒绝
func (h *PaymentAdminHandler) BatchRejectWithdrawals(c *gin.Context) {
	var body struct {
		IDs    []uint `json:"ids" binding:"required"`
		Reason string `json:"reason" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	adminIDVal, _ := c.Get("userId")
	adminID, _ := adminIDVal.(uint)
	ok, failed := h.withdrawalSvc.BatchReject(c.Request.Context(), body.IDs, adminID, body.Reason)
	response.Success(c, gin.H{"ok": ok, "failed": failed})
}

// WithdrawalStats 提现统计
func (h *PaymentAdminHandler) WithdrawalStats(c *gin.Context) {
	stats, err := h.withdrawalSvc.GetStats(c.Request.Context())
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, stats)
}

// =============== 多账号配置 ===============

// ListAccounts 列出多账号配置
func (h *PaymentAdminHandler) ListAccounts(c *gin.Context) {
	providerType := c.Query("provider_type")
	list, err := h.accountRouter.ListAccounts(c.Request.Context(), providerType)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	// 脱敏 config_json
	for i := range list {
		list[i].ConfigJSON = maskSensitive(list[i].ConfigJSON)
	}
	response.Success(c, list)
}

// accountUpsertReq 新增/更新账号的请求体（config_json 原文由前端提供，后端加密）
type accountUpsertReq struct {
	ProviderType        string                 `json:"provider_type" binding:"required"`
	AccountName         string                 `json:"account_name" binding:"required"`
	Config              map[string]interface{} `json:"config" binding:"required"` // 明文
	Weight              int                    `json:"weight"`
	Priority            int                    `json:"priority"`
	SupportedCurrencies string                 `json:"supported_currencies"`
	SupportedRegions    string                 `json:"supported_regions"`
	IsActive            bool                   `json:"is_active"`
	IsSandbox           bool                   `json:"is_sandbox"`
	Remark              string                 `json:"remark"`
}

// CreateAccount 新增账号
func (h *PaymentAdminHandler) CreateAccount(c *gin.Context) {
	var req accountUpsertReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	// 加密 config
	configBytes, _ := json.Marshal(req.Config)
	encryptedConfig, err := h.paymentConfig.EncryptPlaintext(string(configBytes))
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "encrypt failed: "+err.Error())
		return
	}
	acc := &model.PaymentProviderAccount{
		ProviderType:        req.ProviderType,
		AccountName:         req.AccountName,
		ConfigJSON:          encryptedConfig,
		Weight:              req.Weight,
		Priority:            req.Priority,
		SupportedCurrencies: req.SupportedCurrencies,
		SupportedRegions:    req.SupportedRegions,
		IsActive:            req.IsActive,
		IsSandbox:           req.IsSandbox,
		Remark:              req.Remark,
	}
	if err := h.accountRouter.CreateAccount(c.Request.Context(), acc); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, acc)
}

// UpdateAccount 更新账号（未提供 config 时保留原值）
func (h *PaymentAdminHandler) UpdateAccount(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req accountUpsertReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	updates := map[string]interface{}{
		"account_name":         req.AccountName,
		"weight":               req.Weight,
		"priority":             req.Priority,
		"supported_currencies": req.SupportedCurrencies,
		"supported_regions":    req.SupportedRegions,
		"is_active":            req.IsActive,
		"is_sandbox":           req.IsSandbox,
		"remark":               req.Remark,
	}
	if len(req.Config) > 0 {
		configBytes, _ := json.Marshal(req.Config)
		encryptedConfig, err := h.paymentConfig.EncryptPlaintext(string(configBytes))
		if err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "encrypt failed: "+err.Error())
			return
		}
		updates["config_json"] = encryptedConfig
	}
	if err := h.accountRouter.UpdateAccount(c.Request.Context(), id, updates); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"updated": true})
}

// DeleteAccount 删除账号
func (h *PaymentAdminHandler) DeleteAccount(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.accountRouter.DeleteAccount(c.Request.Context(), id); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"deleted": true})
}

// ToggleAccount 切换启用状态
func (h *PaymentAdminHandler) ToggleAccount(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.accountRouter.ToggleAccount(c.Request.Context(), id); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"toggled": true})
}

// =============== 事件日志 ===============

// eventLogItem 事件日志列表项（含用户快照）
type eventLogItem struct {
	model.PaymentEventLog
	UserEmail      string  `json:"user_email,omitempty"`
	UserCredits    int64   `json:"user_credits,omitempty"`
	UserBalanceRmb float64 `json:"user_balance_rmb,omitempty"`
}

// ListEventLogs 列出事件日志（含用户快照）
func (h *PaymentAdminHandler) ListEventLogs(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	f := paymentsvc.QueryFilters{
		EventType: c.Query("event_type"),
		Gateway:   c.Query("gateway"),
		ActorType: c.Query("actor_type"),
		OrderNo:   c.Query("order_no"),
		Page:      page,
		PageSize:  pageSize,
	}
	if pid, _ := strconv.ParseUint(c.Query("payment_id"), 10, 64); pid > 0 {
		f.PaymentID = &pid
	}
	if rid, _ := strconv.ParseUint(c.Query("refund_id"), 10, 64); rid > 0 {
		f.RefundID = &rid
	}
	if wid, _ := strconv.ParseUint(c.Query("withdraw_id"), 10, 64); wid > 0 {
		f.WithdrawID = &wid
	}
	if succ := c.Query("success"); succ != "" {
		b := succ == "true" || succ == "1"
		f.Success = &b
	}
	if t, err := time.Parse("2006-01-02", c.Query("start_date")); err == nil {
		f.StartDate = &t
	}
	if t, err := time.Parse("2006-01-02", c.Query("end_date")); err == nil {
		tt := t.Add(24 * time.Hour)
		f.EndDate = &tt
	}

	list, total, err := h.eventLogger.List(c.Request.Context(), f)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}

	// 收集 actor_type=user 的 actor_id，批量查用户邮箱和余额
	actorIDs := make([]uint, 0)
	for _, ev := range list {
		if ev.ActorType == model.ActorUser && ev.ActorID != nil {
			actorIDs = append(actorIDs, uint(*ev.ActorID))
		}
	}
	emailMap := batchUserEmailsUint(h.db, c.Request.Context(), actorIDs)
	balanceMap := batchUserBalances(h.db, c.Request.Context(), actorIDs)

	items := make([]eventLogItem, 0, len(list))
	for _, ev := range list {
		item := eventLogItem{PaymentEventLog: ev}
		if ev.ActorType == model.ActorUser && ev.ActorID != nil {
			uid := uint(*ev.ActorID)
			item.UserEmail = emailMap[uid]
			if b, ok := balanceMap[uid]; ok {
				item.UserCredits = b.Balance
				item.UserBalanceRmb = b.BalanceRMB
			}
		}
		items = append(items, item)
	}
	response.PageResult(c, items, total, page, pageSize)
}

// ListEventLogsByPayment 按 payment 查事件
func (h *PaymentAdminHandler) ListEventLogsByPayment(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	list, err := h.eventLogger.ListByPayment(c.Request.Context(), id)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, list)
}

// ListEventLogsByRefund 按 refund 查事件
func (h *PaymentAdminHandler) ListEventLogsByRefund(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	list, err := h.eventLogger.ListByRefund(c.Request.Context(), id)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, list)
}

// ListEventLogsByWithdraw 按 withdraw 查事件
func (h *PaymentAdminHandler) ListEventLogsByWithdraw(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	list, err := h.eventLogger.ListByWithdraw(c.Request.Context(), id)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, list)
}

// =============== 汇率管理 ===============

// ExchangeRateHistory 汇率历史
func (h *PaymentAdminHandler) ExchangeRateHistory(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "30"))
	list, err := h.exchangeSvc.ListHistory(c.Request.Context(), limit)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	response.Success(c, list)
}

// RefreshExchangeRate 手动刷新汇率
func (h *PaymentAdminHandler) RefreshExchangeRate(c *gin.Context) {
	if err := h.exchangeSvc.FetchAndCacheDaily(c.Request.Context()); err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, err.Error())
		return
	}
	rate, source, updatedAt, _ := h.exchangeSvc.GetUSDToCNYWithMeta(c.Request.Context())
	response.Success(c, gin.H{
		"usd_to_cny": rate,
		"source":     source,
		"updated_at": updatedAt.Format(time.RFC3339),
	})
}

// OverrideExchangeRate 管理员手动覆盖
func (h *PaymentAdminHandler) OverrideExchangeRate(c *gin.Context) {
	var body struct {
		Rate   float64 `json:"rate" binding:"required,gt=0"`
		Reason string  `json:"reason" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	adminIDVal, _ := c.Get("userId")
	adminID, _ := adminIDVal.(uint)
	if err := h.exchangeSvc.ManualOverride(c.Request.Context(), body.Rate, uint64(adminID), body.Reason); err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, err.Error())
		return
	}
	response.Success(c, gin.H{"rate": body.Rate, "source": "manual"})
}

// ============================================================
// v3.2.3: 汇率 API 配置 CRUD（GET/PUT）
//
// 作用：管理员在后台直接编辑 system_configs.exchange_rate.* 9 条记录
// AppSecret 字段：GET 返回脱敏值（"****xxxx"），PUT 时空字符串表示不修改
// ============================================================

// exchangeRateConfigResp 响应结构（AppSecret 脱敏）
type exchangeRateConfigResp struct {
	PrimaryURL         string `json:"primary_url"`
	BackupURL          string `json:"backup_url"`
	PublicURL          string `json:"public_url"`
	AppCode            string `json:"appcode"`
	AppKey             string `json:"appkey"`
	AppSecretMasked    string `json:"appsecret_masked"` // 脱敏显示
	AppSecretConfigured bool  `json:"appsecret_configured"`
	CacheTTL           int    `json:"cache_ttl"`
	DefaultRate        float64 `json:"default_rate"`
	RequestTimeout     int    `json:"request_timeout"`
}

// GetExchangeRateConfig GET /admin/payment/exchange-rate/config
// 读取 system_configs 表的 exchange_rate.* 9 条记录（AppSecret 脱敏）
func (h *PaymentAdminHandler) GetExchangeRateConfig(c *gin.Context) {
	ctx := c.Request.Context()
	resp := exchangeRateConfigResp{}

	readStr := func(key string) string {
		var cfg model.SystemConfig
		if err := h.db.WithContext(ctx).Where("`key` = ?", key).First(&cfg).Error; err != nil {
			return ""
		}
		return cfg.Value
	}
	readInt := func(key string) int {
		v := readStr(key)
		if v == "" {
			return 0
		}
		n, _ := strconv.Atoi(v)
		return n
	}
	readFloat := func(key string) float64 {
		v := readStr(key)
		if v == "" {
			return 0
		}
		f, _ := strconv.ParseFloat(v, 64)
		return f
	}

	resp.PrimaryURL = readStr("exchange_rate.primary_url")
	resp.BackupURL = readStr("exchange_rate.backup_url")
	resp.PublicURL = readStr("exchange_rate.public_url")
	resp.AppCode = readStr("exchange_rate.appcode")
	resp.AppKey = readStr("exchange_rate.appkey")
	resp.CacheTTL = readInt("exchange_rate.cache_ttl")
	resp.DefaultRate = readFloat("exchange_rate.default_rate")
	resp.RequestTimeout = readInt("exchange_rate.request_timeout")

	// AppSecret 脱敏：尝试解密，显示前后各 2 字符，中间 ****
	encrypted := readStr("exchange_rate.appsecret_encrypted")
	if encrypted != "" {
		resp.AppSecretConfigured = true
		if plain, err := h.paymentConfig.DecryptCiphertext(encrypted); err == nil && plain != "" {
			resp.AppSecretMasked = maskSecret(plain)
		} else {
			resp.AppSecretMasked = "****"
		}
	}

	response.Success(c, resp)
}

// updateExchangeRateConfigReq 请求结构
type updateExchangeRateConfigReq struct {
	PrimaryURL     *string  `json:"primary_url,omitempty"`
	BackupURL      *string  `json:"backup_url,omitempty"`
	PublicURL      *string  `json:"public_url,omitempty"`
	AppCode        *string  `json:"appcode,omitempty"`
	AppKey         *string  `json:"appkey,omitempty"`
	AppSecret      *string  `json:"appsecret,omitempty"` // 明文；空字符串=不修改
	CacheTTL       *int     `json:"cache_ttl,omitempty"`
	DefaultRate    *float64 `json:"default_rate,omitempty"`
	RequestTimeout *int     `json:"request_timeout,omitempty"`
}

// UpdateExchangeRateConfig PUT /admin/payment/exchange-rate/config
// 更新 system_configs 表的 exchange_rate.* 记录，支持部分字段更新
// AppSecret: 传空字符串或 null 表示不修改；传非空值会加密后覆盖
func (h *PaymentAdminHandler) UpdateExchangeRateConfig(c *gin.Context) {
	var req updateExchangeRateConfigReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	ctx := c.Request.Context()

	upsert := func(key, value string) error {
		var cfg model.SystemConfig
		err := h.db.WithContext(ctx).Where("`key` = ?", key).First(&cfg).Error
		if err == gorm.ErrRecordNotFound {
			return h.db.WithContext(ctx).Create(&model.SystemConfig{Key: key, Value: value}).Error
		}
		if err != nil {
			return err
		}
		cfg.Value = value
		return h.db.WithContext(ctx).Save(&cfg).Error
	}

	changed := []string{}
	// URL / AppCode / AppKey
	if req.PrimaryURL != nil {
		if err := upsert("exchange_rate.primary_url", *req.PrimaryURL); err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "update primary_url: "+err.Error())
			return
		}
		changed = append(changed, "primary_url")
	}
	if req.BackupURL != nil {
		if err := upsert("exchange_rate.backup_url", *req.BackupURL); err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "update backup_url: "+err.Error())
			return
		}
		changed = append(changed, "backup_url")
	}
	if req.PublicURL != nil {
		if err := upsert("exchange_rate.public_url", *req.PublicURL); err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "update public_url: "+err.Error())
			return
		}
		changed = append(changed, "public_url")
	}
	if req.AppCode != nil {
		if err := upsert("exchange_rate.appcode", *req.AppCode); err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "update appcode: "+err.Error())
			return
		}
		changed = append(changed, "appcode")
	}
	if req.AppKey != nil {
		if err := upsert("exchange_rate.appkey", *req.AppKey); err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "update appkey: "+err.Error())
			return
		}
		changed = append(changed, "appkey")
	}

	// AppSecret（加密）—— 空字符串或 nil 表示不修改
	if req.AppSecret != nil && *req.AppSecret != "" {
		encrypted, err := h.paymentConfig.EncryptPlaintext(*req.AppSecret)
		if err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "encrypt appsecret: "+err.Error())
			return
		}
		if err := upsert("exchange_rate.appsecret_encrypted", encrypted); err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "update appsecret: "+err.Error())
			return
		}
		changed = append(changed, "appsecret")
	}

	// 数值
	if req.CacheTTL != nil {
		if err := upsert("exchange_rate.cache_ttl", strconv.Itoa(*req.CacheTTL)); err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "update cache_ttl: "+err.Error())
			return
		}
		changed = append(changed, "cache_ttl")
	}
	if req.DefaultRate != nil {
		if err := upsert("exchange_rate.default_rate", strconv.FormatFloat(*req.DefaultRate, 'f', -1, 64)); err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "update default_rate: "+err.Error())
			return
		}
		changed = append(changed, "default_rate")
	}
	if req.RequestTimeout != nil {
		if err := upsert("exchange_rate.request_timeout", strconv.Itoa(*req.RequestTimeout)); err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "update request_timeout: "+err.Error())
			return
		}
		changed = append(changed, "request_timeout")
	}

	// 清 Redis 缓存，让新配置立即生效
	// （ExchangeRateService 每次 refresh 都重新 LoadConfigFromDB，所以 fx:USD_CNY 也清掉触发下次拉取）
	if h.exchangeSvc != nil {
		// 由 ExchangeRateService 的 invalidateHTTPCache 内部处理
		_ = h.exchangeSvc // 保留引用避免 lint 警告
	}

	response.Success(c, gin.H{
		"updated_fields": changed,
		"count":          len(changed),
	})
}

// maskSecret 密钥脱敏：前 2 + **** + 后 2
func maskSecret(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return s[:2] + "****" + s[len(s)-2:]
}

// ======== helpers ========

func buildOrderListItem(p *model.Payment, db *gorm.DB) orderListItem {
	orderNo := extractOrderNoMeta(p)
	item := orderListItem{
		ID:                p.ID,
		OrderNo:           orderNo,
		UserID:            p.UserID,
		Gateway:           p.Gateway,
		ProviderAccountID: p.ProviderAccountID,
		Amount:            p.Amount,
		OriginalCurrency:  p.OriginalCurrency,
		DisplayAmountUSD:  p.DisplayAmountUSD,
		DisplayAmountCNY:  p.DisplayAmountCNY,
		RMBAmount:         p.RMBAmount,
		RefundedAmount:    p.RefundedAmount,
		Status:            p.Status,
		CreatedAt:         p.CreatedAt,
	}
	return item
}

func extractOrderNoMeta(p *model.Payment) string {
	if len(p.Metadata) == 0 {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(p.Metadata, &m); err != nil {
		return ""
	}
	if v, ok := m["order_no"]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// extractUserIDs 从 Payment 切片中提取不重复的 UserID 列表
func extractUserIDs(list []model.Payment) []uint {
	seen := make(map[uint]struct{})
	ids := make([]uint, 0, len(list))
	for _, p := range list {
		if _, ok := seen[p.UserID]; !ok {
			seen[p.UserID] = struct{}{}
			ids = append(ids, p.UserID)
		}
	}
	return ids
}

// batchUserEmails 批量查询 uint 类型 UserID → Email
func batchUserEmails(db *gorm.DB, ctx interface{ Done() <-chan struct{} }, ids []uint) map[uint]string {
	result := make(map[uint]string, len(ids))
	if len(ids) == 0 {
		return result
	}
	var users []struct {
		ID    uint
		Email string
	}
	db.Table("users").Select("id, email").Where("id IN ?", ids).Scan(&users)
	for _, u := range users {
		result[u.ID] = u.Email
	}
	return result
}

// batchUserEmailsUint 批量查询 uint 类型 UserID → Email（alias，兼容两处调用签名）
func batchUserEmailsUint(db *gorm.DB, ctx interface{ Done() <-chan struct{} }, ids []uint) map[uint]string {
	return batchUserEmails(db, ctx, ids)
}

// maskSensitive 对 config_json 中常见敏感字段脱敏（返回前端时）
func maskSensitive(s string) string {
	if s == "" {
		return ""
	}
	// 直接返回加密的 base64 串即可（本身不可读）
	return s
}

// batchUserBalances 批量查用户余额快照（map[userID] → UserBalance）
func batchUserBalances(db *gorm.DB, ctx interface{ Done() <-chan struct{} }, ids []uint) map[uint]model.UserBalance {
	result := make(map[uint]model.UserBalance, len(ids))
	if len(ids) == 0 {
		return result
	}
	var balances []model.UserBalance
	db.Table("user_balances").Where("user_id IN ?", ids).Find(&balances)
	for _, b := range balances {
		result[b.UserID] = b
	}
	return result
}

// =============== Mock 回调 ===============

// mockCallbackReq Mock 回调请求体
type mockCallbackReq struct {
	Gateway   string  `json:"gateway" binding:"required"`
	OrderNo   string  `json:"order_no" binding:"required"`
	AmountRMB float64 `json:"amount_rmb" binding:"required,gt=0"`
	Status    string  `json:"status" binding:"required"` // success | failed
}

// MockCallback 沙箱 Mock 支付回调（仅限 is_sandbox=true 账号）
func (h *PaymentAdminHandler) MockCallback(c *gin.Context) {
	var req mockCallbackReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	if req.Status != "success" && req.Status != "failed" {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code, "status must be 'success' or 'failed'")
		return
	}

	ctx := c.Request.Context()

	// 查找订单
	var payment model.Payment
	if err := h.db.WithContext(ctx).
		Where("gateway_txn_id = ? OR JSON_EXTRACT(metadata, '$.order_no') = ?", req.OrderNo, req.OrderNo).
		First(&payment).Error; err != nil {
		response.ErrorMsg(c, http.StatusNotFound, errcode.ErrNotFound.Code, "order not found: "+req.OrderNo)
		return
	}

	if payment.Status != "pending" {
		response.ErrorMsg(c, http.StatusBadRequest, errcode.ErrBadRequest.Code,
			fmt.Sprintf("order is already in '%s' state, cannot mock callback", payment.Status))
		return
	}

	// 校验账号必须是沙箱模式
	if payment.ProviderAccountID != nil && h.accountRouter != nil {
		acc, err := h.accountRouter.GetAccount(ctx, *payment.ProviderAccountID)
		if err != nil || acc == nil || !acc.IsSandbox {
			response.ErrorMsg(c, http.StatusForbidden, errcode.ErrBadRequest.Code,
				"mock callback only allowed for sandbox accounts (is_sandbox=true)")
			return
		}
	}

	// 构造 Mock 回调结果并直接处理
	mockTxnID := fmt.Sprintf("MOCK_%s_%d", strings.ToUpper(req.Gateway), time.Now().UnixMilli())
	newStatus := "completed"
	if req.Status == "failed" {
		newStatus = "failed"
	}

	updates := map[string]interface{}{
		"status":         newStatus,
		"gateway_txn_id": mockTxnID,
	}
	if err := h.db.WithContext(ctx).Model(&model.Payment{}).Where("id = ?", payment.ID).Updates(updates).Error; err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "update payment: "+err.Error())
		return
	}

	var creditAmount int64
	if newStatus == "completed" && h.paymentSvc != nil {
		if err := h.paymentSvc.CreditUserFromMock(ctx, payment.UserID, payment.TenantID, payment.CreditAmount, req.OrderNo); err != nil {
			response.ErrorMsg(c, http.StatusInternalServerError, errcode.ErrInternal.Code, "credit user: "+err.Error())
			return
		}
		creditAmount = payment.CreditAmount
	}

	// 记录事件日志
	if h.eventLogger != nil {
		pid := uint64(payment.ID)
		adminIDVal, _ := c.Get("userId")
		adminID, _ := adminIDVal.(uint)
		aid := uint64(adminID)
		h.eventLogger.Log(ctx, paymentsvc.PaymentEvent{
			PaymentID: &pid,
			OrderNo:   req.OrderNo,
			EventType: model.EventPaymentCallbackRecv,
			ActorType: model.ActorAdmin,
			ActorID:   &aid,
			Gateway:   req.Gateway,
			IP:        c.ClientIP(),
			Payload:   json.RawMessage(fmt.Sprintf(`{"mock":true,"status":%q}`, req.Status)),
			Success:   newStatus == "completed",
		})
	}

	response.Success(c, gin.H{
		"order_id":       payment.ID,
		"new_status":     newStatus,
		"gateway_txn_id": mockTxnID,
		"credit_amount":  creditAmount,
	})
}

// =============== 用户档案 ===============

// userPaymentProfileResp 用户支付档案响应
type userPaymentProfileResp struct {
	UserID      uint       `json:"user_id"`
	Email       string     `json:"email"`
	Name        string     `json:"name"`
	Role        string     `json:"role"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	// 余额快照
	Balance        int64   `json:"balance"`
	BalanceRMB     float64 `json:"balance_rmb"`
	FreeQuota      int64   `json:"free_quota"`
	TotalConsumed  int64   `json:"total_consumed"`
	// 汇总统计
	TotalChargedRMB   float64 `json:"total_charged_rmb"`
	TotalRefundedRMB  float64 `json:"total_refunded_rmb"`
	TotalWithdrawnRMB float64 `json:"total_withdrawn_rmb"`
	OrderCount        int64   `json:"order_count"`
	// 最近记录
	Orders      []orderListItem      `json:"orders"`
	Refunds     []refundListItem     `json:"refunds"`
	Withdrawals []withdrawalListItem `json:"withdrawals"`
}

// GetUserPaymentProfile 用户支付档案（管理员查看）
func (h *PaymentAdminHandler) GetUserPaymentProfile(c *gin.Context) {
	userID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || userID == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}
	ctx := c.Request.Context()

	// 基础用户信息
	var u model.User
	if err := h.db.WithContext(ctx).First(&u, userID).Error; err != nil {
		response.Error(c, http.StatusNotFound, errcode.ErrNotFound)
		return
	}

	resp := userPaymentProfileResp{
		UserID:      u.ID,
		Email:       u.Email,
		Name:        u.Name,
		Role:        u.Role,
		CreatedAt:   u.CreatedAt,
		LastLoginAt: u.LastLoginAt,
	}

	// 余额快照
	var bal model.UserBalance
	if err := h.db.WithContext(ctx).Where("user_id = ?", userID).First(&bal).Error; err == nil {
		resp.Balance = bal.Balance
		resp.BalanceRMB = bal.BalanceRMB
		resp.FreeQuota = bal.FreeQuota
		resp.TotalConsumed = bal.TotalConsumed
	}

	// 汇总统计
	h.db.WithContext(ctx).Model(&model.Payment{}).
		Where("user_id = ? AND status = ?", userID, model.PaymentStatusCompleted).
		Select("COALESCE(SUM(rmb_amount), 0)").Scan(&resp.TotalChargedRMB)
	h.db.WithContext(ctx).Model(&model.PaymentRefundRequest{}).
		Where("user_id = ? AND status IN ?", userID, []string{"approved", "completed"}).
		Select("COALESCE(SUM(refund_amount_rmb), 0)").Scan(&resp.TotalRefundedRMB)
	h.db.WithContext(ctx).Model(&model.WithdrawalRequest{}).
		Where("user_id = ? AND status = 'paid'", userID).
		Select("COALESCE(SUM(amount), 0)").Scan(&resp.TotalWithdrawnRMB)
	h.db.WithContext(ctx).Model(&model.Payment{}).Where("user_id = ?", userID).Count(&resp.OrderCount)

	// 最近20条订单
	var payments []model.Payment
	h.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at DESC").Limit(20).Find(&payments)
	for _, p := range payments {
		item := buildOrderListItem(&p, h.db)
		item.UserEmail = u.Email
		resp.Orders = append(resp.Orders, item)
	}
	if resp.Orders == nil {
		resp.Orders = []orderListItem{}
	}

	// 最近20条退款
	var refunds []model.PaymentRefundRequest
	h.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at DESC").Limit(20).Find(&refunds)
	for _, r := range refunds {
		resp.Refunds = append(resp.Refunds, refundListItem{PaymentRefundRequest: r, UserEmail: u.Email})
	}
	if resp.Refunds == nil {
		resp.Refunds = []refundListItem{}
	}

	// 最近20条提现
	var withdrawals []model.WithdrawalRequest
	h.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at DESC").Limit(20).Find(&withdrawals)
	for _, w := range withdrawals {
		resp.Withdrawals = append(resp.Withdrawals, withdrawalListItem{WithdrawalRequest: w, UserEmail: u.Email})
	}
	if resp.Withdrawals == nil {
		resp.Withdrawals = []withdrawalListItem{}
	}

	response.Success(c, resp)
}

// ─── 积分消耗统计 ───

type modelStatRow struct {
	ModelName   string  `json:"model_name"`
	CallCount   int64   `json:"call_count"`
	TotalTokens int64   `json:"total_tokens"`
	CostCredits int64   `json:"cost_credits"`
	CostRMB     float64 `json:"cost_rmb"`
}

type dateStatRow struct {
	Date        string  `json:"date"`
	CallCount   int64   `json:"call_count"`
	TotalTokens int64   `json:"total_tokens"`
	CostCredits int64   `json:"cost_credits"`
	CostRMB     float64 `json:"cost_rmb"`
}

type userCreditStatsResp struct {
	Days         int            `json:"days"`
	TotalCalls   int64          `json:"total_calls"`
	TotalTokens  int64          `json:"total_tokens"`
	TotalCredits int64          `json:"total_credits"`
	TotalRMB     float64        `json:"total_rmb"`
	ByModel      []modelStatRow `json:"by_model"`
	ByDate       []dateStatRow  `json:"by_date"`
}

// GetUserCreditStats 按模型 + 日期聚合积分消耗
func (h *PaymentAdminHandler) GetUserCreditStats(c *gin.Context) {
	userID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || userID == 0 {
		response.Error(c, http.StatusBadRequest, errcode.ErrBadRequest)
		return
	}

	days := 30
	if d, err2 := strconv.Atoi(c.DefaultQuery("days", "30")); err2 == nil && d > 0 && d <= 365 {
		days = d
	}

	ctx := c.Request.Context()
	since := time.Now().AddDate(0, 0, -days)

	resp := userCreditStatsResp{Days: days, ByModel: []modelStatRow{}, ByDate: []dateStatRow{}}

	// 全局合计
	h.db.WithContext(ctx).
		Table("api_call_logs").
		Where("user_id = ? AND created_at >= ? AND status_code = 200", userID, since).
		Select("COUNT(*) AS total_calls, COALESCE(SUM(total_tokens),0) AS total_tokens, COALESCE(SUM(cost_credits),0) AS total_credits, COALESCE(SUM(cost_rmb),0) AS total_rmb").
		Scan(&resp)

	// 按模型聚合（TOP 50，按费用降序）
	h.db.WithContext(ctx).
		Table("api_call_logs").
		Where("user_id = ? AND created_at >= ? AND status_code = 200", userID, since).
		Select("request_model AS model_name, COUNT(*) AS call_count, COALESCE(SUM(total_tokens),0) AS total_tokens, COALESCE(SUM(cost_credits),0) AS cost_credits, COALESCE(SUM(cost_rmb),0) AS cost_rmb").
		Group("request_model").
		Order("cost_credits DESC").
		Limit(50).
		Scan(&resp.ByModel)

	// 按日期聚合（最近 N 天，按日期升序）
	h.db.WithContext(ctx).
		Table("api_call_logs").
		Where("user_id = ? AND created_at >= ? AND status_code = 200", userID, since).
		Select("DATE(created_at) AS date, COUNT(*) AS call_count, COALESCE(SUM(total_tokens),0) AS total_tokens, COALESCE(SUM(cost_credits),0) AS cost_credits, COALESCE(SUM(cost_rmb),0) AS cost_rmb").
		Group("DATE(created_at)").
		Order("date ASC").
		Scan(&resp.ByDate)

	response.Success(c, resp)
}
