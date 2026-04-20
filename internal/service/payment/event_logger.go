package payment

import (
	"context"
	"encoding/json"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

const (
	maxPayloadSize = 8 * 1024 // 8KB 截断
)

// EventLogger 支付事件日志写入器（异步、非阻塞）
type EventLogger struct {
	db *gorm.DB
}

// NewEventLogger 构造函数
func NewEventLogger(db *gorm.DB) *EventLogger {
	return &EventLogger{db: db}
}

// PaymentEvent 一次事件的完整描述
type PaymentEvent struct {
	PaymentID  *uint64
	RefundID   *uint64
	WithdrawID *uint64
	OrderNo    string
	EventType  string
	ActorType  string
	ActorID    *uint64
	Gateway    string
	AccountID  *uint64
	IP         string
	UserAgent  string
	Payload    interface{}
	Result     interface{}
	Success    bool
	Err        error  // 业务错误对象（优先使用）
	ErrorMsg   string // 预格式化错误字符串（Err 为 nil 时使用）
	DurationMs int64
}

// Log 异步写入事件日志（不阻塞主流程）
//
// 失败仅 zap warn，不返回错误（日志写入失败不能影响业务流程）
func (l *EventLogger) Log(ctx context.Context, evt PaymentEvent) {
	if l == nil || l.db == nil {
		return
	}
	go l.write(context.Background(), evt) // 主 ctx 可能已 cancel，用独立 ctx
}

// LogSync 同步写入（测试场景使用，便于断言）
func (l *EventLogger) LogSync(ctx context.Context, evt PaymentEvent) error {
	if l == nil || l.db == nil {
		return nil
	}
	return l.writeOnce(ctx, evt)
}

// LogExchangeEvent 实现 exchange.EventSink 接口
func (l *EventLogger) LogExchangeEvent(ctx context.Context, eventType, source string, success bool, payload, result interface{}, err error, durationMs int64) {
	l.Log(ctx, PaymentEvent{
		EventType:  eventType,
		ActorType:  model.ActorCron,
		Gateway:    source,
		Payload:    payload,
		Result:     result,
		Success:    success,
		Err:        err,
		DurationMs: durationMs,
	})
}

// WithdrawalEventInput 提现事件输入（避免循环依赖 withdrawal 包的结构定义）
type WithdrawalEventInput struct {
	WithdrawID uint64
	UserID     uint64
	EventType  string
	ActorType  string
	ActorID    *uint64
	IP         string
	Payload    interface{}
	Success    bool
	ErrorMsg   string
}

// LogWithdrawalEventInput 写入提现事件（传入结构体版本）
func (l *EventLogger) LogWithdrawalEventInput(ctx context.Context, evt WithdrawalEventInput) {
	wid := evt.WithdrawID
	l.Log(ctx, PaymentEvent{
		WithdrawID: &wid,
		EventType:  evt.EventType,
		ActorType:  evt.ActorType,
		ActorID:    evt.ActorID,
		IP:         evt.IP,
		Payload:    evt.Payload,
		Success:    evt.Success,
		ErrorMsg:   evt.ErrorMsg,
	})
}

func (l *EventLogger) write(ctx context.Context, evt PaymentEvent) {
	if err := l.writeOnce(ctx, evt); err != nil {
		logger.L.Warn("payment event log write failed",
			zap.String("event_type", evt.EventType),
			zap.Error(err),
		)
	}
}

func (l *EventLogger) writeOnce(ctx context.Context, evt PaymentEvent) error {
	row := &model.PaymentEventLog{
		PaymentID:   evt.PaymentID,
		RefundID:    evt.RefundID,
		WithdrawID:  evt.WithdrawID,
		OrderNo:     truncate(evt.OrderNo, 64),
		EventType:   truncate(evt.EventType, 40),
		ActorType:   truncate(evt.ActorType, 20),
		ActorID:     evt.ActorID,
		Gateway:     truncate(evt.Gateway, 20),
		AccountID:   evt.AccountID,
		IP:          truncate(evt.IP, 64),
		UserAgent:   truncate(evt.UserAgent, 500),
		PayloadJSON: marshalSafe(evt.Payload),
		ResultJSON:  marshalSafe(evt.Result),
		Success:     evt.Success,
		ErrorMsg:    firstNonEmpty(truncateErr(evt.Err, 1000), truncate(evt.ErrorMsg, 1000)),
		DurationMs:  evt.DurationMs,
		CreatedAt:   time.Now(),
	}
	return l.db.WithContext(ctx).Create(row).Error
}

// QueryFilters 事件日志查询过滤
type QueryFilters struct {
	PaymentID  *uint64
	RefundID   *uint64
	WithdrawID *uint64
	OrderNo    string
	EventType  string
	Gateway    string
	ActorType  string
	Success    *bool
	StartDate  *time.Time
	EndDate    *time.Time
	Page       int
	PageSize   int
}

// List 列出事件日志（管理员查询用）
func (l *EventLogger) List(ctx context.Context, f QueryFilters) ([]model.PaymentEventLog, int64, error) {
	q := l.db.WithContext(ctx).Model(&model.PaymentEventLog{})
	if f.PaymentID != nil {
		q = q.Where("payment_id = ?", *f.PaymentID)
	}
	if f.RefundID != nil {
		q = q.Where("refund_id = ?", *f.RefundID)
	}
	if f.WithdrawID != nil {
		q = q.Where("withdraw_id = ?", *f.WithdrawID)
	}
	if f.OrderNo != "" {
		q = q.Where("order_no = ?", f.OrderNo)
	}
	if f.EventType != "" {
		q = q.Where("event_type = ?", f.EventType)
	}
	if f.Gateway != "" {
		q = q.Where("gateway = ?", f.Gateway)
	}
	if f.ActorType != "" {
		q = q.Where("actor_type = ?", f.ActorType)
	}
	if f.Success != nil {
		q = q.Where("success = ?", *f.Success)
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
		f.PageSize = 50
	}

	var list []model.PaymentEventLog
	err := q.Order("created_at DESC").
		Offset((f.Page - 1) * f.PageSize).
		Limit(f.PageSize).
		Find(&list).Error
	return list, total, err
}

// ListByPayment 按 payment_id 查全链路事件（订单详情用）
func (l *EventLogger) ListByPayment(ctx context.Context, paymentID uint64) ([]model.PaymentEventLog, error) {
	var list []model.PaymentEventLog
	err := l.db.WithContext(ctx).
		Where("payment_id = ?", paymentID).
		Order("created_at ASC").
		Find(&list).Error
	return list, err
}

// ListByRefund 按 refund_id 查事件
func (l *EventLogger) ListByRefund(ctx context.Context, refundID uint64) ([]model.PaymentEventLog, error) {
	var list []model.PaymentEventLog
	err := l.db.WithContext(ctx).
		Where("refund_id = ?", refundID).
		Order("created_at ASC").
		Find(&list).Error
	return list, err
}

// ListByWithdraw 按 withdraw_id 查事件
func (l *EventLogger) ListByWithdraw(ctx context.Context, withdrawID uint64) ([]model.PaymentEventLog, error) {
	var list []model.PaymentEventLog
	err := l.db.WithContext(ctx).
		Where("withdraw_id = ?", withdrawID).
		Order("created_at ASC").
		Find(&list).Error
	return list, err
}

// marshalSafe 安全的 JSON 序列化（nil 安全 + 超大截断）
func marshalSafe(v interface{}) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return `{"_marshal_err":"` + err.Error() + `"}`
	}
	if len(b) > maxPayloadSize {
		return string(b[:maxPayloadSize]) + `..."[truncated]"`
	}
	return string(b)
}

func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max]
	}
	return s
}

func truncateErr(err error, max int) string {
	if err == nil {
		return ""
	}
	return truncate(err.Error(), max)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
