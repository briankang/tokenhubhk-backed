package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
	"tokenhub-server/internal/pkg/logger"
)

const (
	// 单用户每日退款申请上限
	dailyRefundLimit = 3
	// 防刷：同 payment 存在 pending 申请锁 TTL
	refundPendingLockTTL = 24 * time.Hour
)

// RefundGatewayInvoker 执行网关退款（PaymentService 实现）
type RefundGatewayInvoker interface {
	// InvokeGatewayRefund 根据 payment 调对应网关退款
	InvokeGatewayRefund(ctx context.Context, payment *model.Payment, amount float64, reason string) (*RefundResult, error)
}

// RefundService 退款服务
// 工作流：用户申请 → 管理员审核 → 通过时调网关 → DB 事务扣余额
type RefundService struct {
	db            *gorm.DB
	redis         *goredis.Client
	eventLogger   *EventLogger
	gatewayInvoker RefundGatewayInvoker
}

// NewRefundService 构造函数
func NewRefundService(db *gorm.DB, redis *goredis.Client, eventLogger *EventLogger) *RefundService {
	return &RefundService{
		db:          db,
		redis:       redis,
		eventLogger: eventLogger,
	}
}

// SetGatewayInvoker 注入网关调用器（PaymentService 实现，避免循环依赖）
func (s *RefundService) SetGatewayInvoker(invoker RefundGatewayInvoker) {
	s.gatewayInvoker = invoker
}

// SubmitUserRequestInput 用户提交退款申请的入参
type SubmitUserRequestInput struct {
	UserID      uint64
	TenantID    uint64
	PaymentID   uint64
	AmountRMB   float64
	Reason      string
	Attachments []string // 图片 URL 列表
	IP          string
	UserAgent   string
}

// SubmitUserRequest 用户提交退款申请
func (s *RefundService) SubmitUserRequest(ctx context.Context, in SubmitUserRequestInput) (*model.PaymentRefundRequest, error) {
	if in.UserID == 0 || in.PaymentID == 0 {
		return nil, fmt.Errorf("invalid user or payment id")
	}
	if in.AmountRMB <= 0 {
		return nil, fmt.Errorf("refund amount must be positive")
	}
	if strings.TrimSpace(in.Reason) == "" {
		return nil, fmt.Errorf("refund reason is required")
	}
	if len([]rune(in.Reason)) > 500 {
		return nil, fmt.Errorf("reason too long (max 500 chars)")
	}

	// 1. 校验原订单
	var payment model.Payment
	if err := s.db.WithContext(ctx).First(&payment, in.PaymentID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("payment not found")
		}
		return nil, fmt.Errorf("load payment: %w", err)
	}
	if uint64(payment.UserID) != in.UserID {
		return nil, fmt.Errorf("payment does not belong to user")
	}
	if payment.Status != model.PaymentStatusCompleted && payment.Status != model.PaymentStatusPartialRefunded {
		return nil, fmt.Errorf("payment status %s is not refundable", payment.Status)
	}

	remainingRefundable := roundHalf(payment.Amount - payment.RefundedAmount)
	if in.AmountRMB > remainingRefundable+0.001 {
		return nil, fmt.Errorf("refund amount %.2f exceeds remaining refundable %.2f", in.AmountRMB, remainingRefundable)
	}

	// 2. 校验同 payment 无 pending 申请
	var existing int64
	if err := s.db.WithContext(ctx).Model(&model.PaymentRefundRequest{}).
		Where("payment_id = ? AND status IN ?", in.PaymentID,
			[]string{model.RefundStatusPending, model.RefundStatusApproved, model.RefundStatusProcessing}).
		Count(&existing).Error; err != nil {
		return nil, fmt.Errorf("count pending: %w", err)
	}
	if existing > 0 {
		return nil, fmt.Errorf("pending refund request already exists for this payment")
	}

	// 3. 单用户每日限额
	if err := s.checkDailyLimit(ctx, in.UserID); err != nil {
		return nil, err
	}

	// 4. 序列化附件
	var attachJSON string
	if len(in.Attachments) > 0 {
		if len(in.Attachments) > 3 {
			return nil, fmt.Errorf("max 3 attachments allowed")
		}
		b, err := json.Marshal(in.Attachments)
		if err != nil {
			return nil, fmt.Errorf("marshal attachments: %w", err)
		}
		attachJSON = string(b)
	}

	// 5. 写 DB
	req := &model.PaymentRefundRequest{
		PaymentID:       in.PaymentID,
		UserID:          in.UserID,
		TenantID:        in.TenantID,
		OrderNo:         s.extractOrderNo(&payment),
		RefundAmountRMB: in.AmountRMB,
		Reason:          in.Reason,
		Attachments:     attachJSON,
		Status:          model.RefundStatusPending,
	}
	if err := s.db.WithContext(ctx).Create(req).Error; err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// 6. 写事件日志
	uid := in.UserID
	s.eventLogger.Log(ctx, PaymentEvent{
		PaymentID: pUint64(payment.ID),
		RefundID:  &req.ID,
		OrderNo:   req.OrderNo,
		EventType: model.EventRefundRequested,
		ActorType: model.ActorUser,
		ActorID:   &uid,
		Gateway:   payment.Gateway,
		IP:        in.IP,
		UserAgent: in.UserAgent,
		Payload: map[string]interface{}{
			"amount_rmb": in.AmountRMB,
			"reason":     in.Reason,
			"attachments": in.Attachments,
		},
		Success: true,
	})

	return req, nil
}

// ApproveByAdmin 管理员通过退款申请 → 自动触发网关退款
func (s *RefundService) ApproveByAdmin(ctx context.Context, requestID, adminID uint64, remark string) error {
	var req model.PaymentRefundRequest
	if err := s.db.WithContext(ctx).First(&req, requestID).Error; err != nil {
		return fmt.Errorf("load request: %w", err)
	}
	if req.Status != model.RefundStatusPending {
		return fmt.Errorf("request status %s cannot be approved", req.Status)
	}

	now := time.Now()
	aid := adminID
	updates := map[string]interface{}{
		"status":       model.RefundStatusApproved,
		"admin_id":     &aid,
		"admin_remark": remark,
		"processed_at": &now,
	}
	if err := s.db.WithContext(ctx).Model(&model.PaymentRefundRequest{}).Where("id = ?", req.ID).Updates(updates).Error; err != nil {
		return fmt.Errorf("update approve: %w", err)
	}

	s.eventLogger.Log(ctx, PaymentEvent{
		PaymentID: &req.PaymentID,
		RefundID:  &req.ID,
		OrderNo:   req.OrderNo,
		EventType: model.EventRefundApproved,
		ActorType: model.ActorAdmin,
		ActorID:   &aid,
		Payload:   map[string]interface{}{"remark": remark},
		Success:   true,
	})

	// 异步执行网关退款（不阻塞 HTTP 响应）
	go s.executeRefundAsync(context.Background(), req.ID, adminID)
	return nil
}

// RejectByAdmin 管理员拒绝退款申请
func (s *RefundService) RejectByAdmin(ctx context.Context, requestID, adminID uint64, reason string) error {
	var req model.PaymentRefundRequest
	if err := s.db.WithContext(ctx).First(&req, requestID).Error; err != nil {
		return fmt.Errorf("load request: %w", err)
	}
	if req.Status != model.RefundStatusPending {
		return fmt.Errorf("request status %s cannot be rejected", req.Status)
	}
	now := time.Now()
	aid := adminID
	updates := map[string]interface{}{
		"status":       model.RefundStatusRejected,
		"admin_id":     &aid,
		"admin_remark": reason,
		"processed_at": &now,
	}
	if err := s.db.WithContext(ctx).Model(&model.PaymentRefundRequest{}).Where("id = ?", req.ID).Updates(updates).Error; err != nil {
		return err
	}
	s.eventLogger.Log(ctx, PaymentEvent{
		PaymentID: &req.PaymentID,
		RefundID:  &req.ID,
		OrderNo:   req.OrderNo,
		EventType: model.EventRefundRejected,
		ActorType: model.ActorAdmin,
		ActorID:   &aid,
		Payload:   map[string]interface{}{"reason": reason},
		Success:   true,
	})
	return nil
}

// executeRefundAsync 执行网关退款 + DB 事务扣余额
func (s *RefundService) executeRefundAsync(ctx context.Context, requestID, adminID uint64) {
	// 置 processing
	_ = s.db.WithContext(ctx).Model(&model.PaymentRefundRequest{}).
		Where("id = ?", requestID).Update("status", model.RefundStatusProcessing).Error

	var req model.PaymentRefundRequest
	if err := s.db.WithContext(ctx).First(&req, requestID).Error; err != nil {
		logger.L.Error("refund async load request failed", zap.Error(err))
		return
	}
	var payment model.Payment
	if err := s.db.WithContext(ctx).First(&payment, req.PaymentID).Error; err != nil {
		logger.L.Error("refund async load payment failed", zap.Error(err))
		s.markRefundFailed(ctx, &req, "load payment failed: "+err.Error(), adminID)
		return
	}

	// 调网关
	start := time.Now()
	var refundResult *RefundResult
	var gwErr error
	if s.gatewayInvoker != nil {
		refundResult, gwErr = s.gatewayInvoker.InvokeGatewayRefund(ctx, &payment, req.RefundAmountRMB, req.Reason)
	} else {
		gwErr = fmt.Errorf("gateway invoker not configured")
	}
	duration := time.Since(start).Milliseconds()

	aid := adminID
	s.eventLogger.Log(ctx, PaymentEvent{
		PaymentID: pUint64(payment.ID),
		RefundID:  &req.ID,
		OrderNo:   req.OrderNo,
		EventType: model.EventRefundGatewayCalled,
		ActorType: model.ActorSystem,
		ActorID:   &aid,
		Gateway:   payment.Gateway,
		Payload: map[string]interface{}{
			"amount": req.RefundAmountRMB,
			"reason": req.Reason,
		},
		Result:     refundResult,
		Success:    gwErr == nil,
		Err:        gwErr,
		DurationMs: duration,
	})

	if gwErr != nil {
		logger.L.Error("refund gateway call failed",
			zap.Uint64("refund_id", req.ID),
			zap.Error(gwErr))
		s.markRefundFailed(ctx, &req, gwErr.Error(), adminID)
		return
	}

	// DB 事务：更新 refund + payment + user_balance + balance_record
	refundCredits := credits.RMBToCredits(req.RefundAmountRMB)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		// 1. 更新 refund_requests
		if err := tx.Model(&model.PaymentRefundRequest{}).
			Where("id = ?", req.ID).
			Updates(map[string]interface{}{
				"status":            model.RefundStatusCompleted,
				"gateway_refund_id": refundResult.GatewayRefundID,
				"gateway_response":  marshalSafe(refundResult),
				"completed_at":      &now,
			}).Error; err != nil {
			return fmt.Errorf("update refund request: %w", err)
		}

		// 2. 更新 payments
		newRefunded := roundHalf(payment.RefundedAmount + req.RefundAmountRMB)
		newStatus := model.PaymentStatusPartialRefunded
		if newRefunded >= payment.Amount-0.001 {
			newStatus = model.PaymentStatusRefunded
		}
		if err := tx.Model(&model.Payment{}).
			Where("id = ?", payment.ID).
			Updates(map[string]interface{}{
				"refunded_amount": newRefunded,
				"refund_count":    gorm.Expr("refund_count + 1"),
				"status":          newStatus,
			}).Error; err != nil {
			return fmt.Errorf("update payment: %w", err)
		}

		// 3. 锁行扣余额
		var ub model.UserBalance
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ?", payment.UserID).First(&ub).Error; err != nil {
			return fmt.Errorf("lock balance: %w", err)
		}
		before := ub.Balance
		ub.Balance -= refundCredits
		if ub.Balance < 0 {
			logger.L.Error("refund caused negative balance, admin attention required",
				zap.Uint64("user_id", uint64(payment.UserID)),
				zap.Int64("refund_credits", refundCredits),
				zap.Int64("before", before))
			// 允许负值，但在 event_log 记录警告
		}
		ub.BalanceRMB = credits.CreditsToRMB(ub.Balance)
		if err := tx.Save(&ub).Error; err != nil {
			return fmt.Errorf("update balance: %w", err)
		}

		// 4. 写 balance_records
		record := &model.BalanceRecord{
			UserID:        payment.UserID,
			TenantID:      payment.TenantID,
			Type:          "REFUND",
			Amount:        -refundCredits,
			AmountRMB:     -req.RefundAmountRMB,
			BeforeBalance: before,
			AfterBalance:  ub.Balance,
			Remark:        fmt.Sprintf("退款 %s", req.OrderNo),
			RelatedID:     fmt.Sprintf("refund:%d", req.ID),
		}
		return tx.Create(record).Error
	})

	if err != nil {
		logger.L.Error("refund db transaction failed",
			zap.Uint64("refund_id", req.ID),
			zap.Error(err))
		// DB 失败但网关已退款 — 需要人工介入
		s.markRefundFailed(ctx, &req, "db tx failed after gateway success: "+err.Error(), adminID)
		s.eventLogger.Log(ctx, PaymentEvent{
			PaymentID: pUint64(payment.ID),
			RefundID:  &req.ID,
			OrderNo:   req.OrderNo,
			EventType: model.EventRefundFailed,
			ActorType: model.ActorSystem,
			ActorID:   &aid,
			Success:   false,
			Err:       err,
		})
		return
	}

	s.eventLogger.Log(ctx, PaymentEvent{
		PaymentID: pUint64(payment.ID),
		RefundID:  &req.ID,
		OrderNo:   req.OrderNo,
		EventType: model.EventRefundCompleted,
		ActorType: model.ActorSystem,
		ActorID:   &aid,
		Gateway:   payment.Gateway,
		Payload: map[string]interface{}{
			"amount_rmb":     req.RefundAmountRMB,
			"refund_credits": refundCredits,
		},
		Result:  refundResult,
		Success: true,
	})
	logger.L.Info("refund completed",
		zap.Uint64("refund_id", req.ID),
		zap.Uint64("payment_id", uint64(payment.ID)),
		zap.Float64("amount_rmb", req.RefundAmountRMB))
}

// markRefundFailed 置退款为失败
func (s *RefundService) markRefundFailed(ctx context.Context, req *model.PaymentRefundRequest, reason string, adminID uint64) {
	now := time.Now()
	_ = s.db.WithContext(ctx).Model(&model.PaymentRefundRequest{}).
		Where("id = ?", req.ID).
		Updates(map[string]interface{}{
			"status":           model.RefundStatusFailed,
			"gateway_response": reason,
			"completed_at":     &now,
		}).Error
	aid := adminID
	s.eventLogger.Log(ctx, PaymentEvent{
		RefundID:  &req.ID,
		OrderNo:   req.OrderNo,
		EventType: model.EventRefundFailed,
		ActorType: model.ActorSystem,
		ActorID:   &aid,
		Success:   false,
		ErrorMsg:  reason,
	})
}

// checkDailyLimit 单用户每日退款限额
func (s *RefundService) checkDailyLimit(ctx context.Context, userID uint64) error {
	var count int64
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if err := s.db.WithContext(ctx).Model(&model.PaymentRefundRequest{}).
		Where("user_id = ? AND created_at >= ?", userID, today).
		Count(&count).Error; err != nil {
		return fmt.Errorf("count daily: %w", err)
	}
	if count >= dailyRefundLimit {
		return fmt.Errorf("daily refund limit (%d) exceeded", dailyRefundLimit)
	}
	return nil
}

// extractOrderNo 从 payment.Metadata 提取 order_no
func (s *RefundService) extractOrderNo(p *model.Payment) string {
	if len(p.Metadata) == 0 {
		return fmt.Sprintf("PAY%d", p.ID)
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(p.Metadata, &meta); err != nil {
		return fmt.Sprintf("PAY%d", p.ID)
	}
	if v, ok := meta["order_no"]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return fmt.Sprintf("PAY%d", p.ID)
}

// ==================== 查询接口 ====================

// ListUserRequests 列出指定用户的退款申请
func (s *RefundService) ListUserRequests(ctx context.Context, userID uint64, page, pageSize int) ([]model.PaymentRefundRequest, int64, error) {
	return s.listWithFilter(ctx, map[string]interface{}{"user_id": userID}, "", "", page, pageSize)
}

// RefundListFilter 管理员列表过滤
type RefundListFilter struct {
	Status     string
	UserID     uint64
	Gateway    string
	OrderNo    string
	StartDate  *time.Time
	EndDate    *time.Time
	Page       int
	PageSize   int
}

// ListAdmin 管理员列出退款申请
func (s *RefundService) ListAdmin(ctx context.Context, f RefundListFilter) ([]model.PaymentRefundRequest, int64, error) {
	q := s.db.WithContext(ctx).Model(&model.PaymentRefundRequest{})
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.UserID > 0 {
		q = q.Where("user_id = ?", f.UserID)
	}
	if f.OrderNo != "" {
		q = q.Where("order_no = ?", f.OrderNo)
	}
	if f.StartDate != nil {
		q = q.Where("created_at >= ?", *f.StartDate)
	}
	if f.EndDate != nil {
		q = q.Where("created_at <= ?", *f.EndDate)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 200 {
		f.PageSize = 20
	}

	var list []model.PaymentRefundRequest
	err := q.Order("created_at DESC").
		Offset((f.Page - 1) * f.PageSize).
		Limit(f.PageSize).
		Find(&list).Error
	return list, total, err
}

func (s *RefundService) listWithFilter(ctx context.Context, where map[string]interface{}, status, orderNo string, page, pageSize int) ([]model.PaymentRefundRequest, int64, error) {
	q := s.db.WithContext(ctx).Model(&model.PaymentRefundRequest{}).Where(where)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if orderNo != "" {
		q = q.Where("order_no = ?", orderNo)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 20
	}
	var list []model.PaymentRefundRequest
	err := q.Order("created_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&list).Error
	return list, total, err
}

// GetByID 获取单条退款申请
func (s *RefundService) GetByID(ctx context.Context, id uint64) (*model.PaymentRefundRequest, error) {
	var req model.PaymentRefundRequest
	if err := s.db.WithContext(ctx).First(&req, id).Error; err != nil {
		return nil, err
	}
	return &req, nil
}

// BatchApprove 批量通过
func (s *RefundService) BatchApprove(ctx context.Context, ids []uint64, adminID uint64, remark string) (okIDs, failedIDs []uint64, err error) {
	for _, id := range ids {
		if e := s.ApproveByAdmin(ctx, id, adminID, remark); e != nil {
			failedIDs = append(failedIDs, id)
			logger.L.Warn("batch approve refund failed", zap.Uint64("id", id), zap.Error(e))
		} else {
			okIDs = append(okIDs, id)
		}
	}
	return
}

// BatchReject 批量拒绝
func (s *RefundService) BatchReject(ctx context.Context, ids []uint64, adminID uint64, reason string) (okIDs, failedIDs []uint64, err error) {
	for _, id := range ids {
		if e := s.RejectByAdmin(ctx, id, adminID, reason); e != nil {
			failedIDs = append(failedIDs, id)
		} else {
			okIDs = append(okIDs, id)
		}
	}
	return
}

// pUint64 指针辅助
func pUint64(v uint) *uint64 {
	u := uint64(v)
	return &u
}
