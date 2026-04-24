package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// 异步队列默认配置（与 audit_service 对齐）
const (
	defaultBufferSize = 1024
	defaultBatchSize  = 50
	defaultFlushEvery = 500 * time.Millisecond
	cleanupBatchSize  = 1000
)

// EventRecorder 限流 429 事件异步记录器
type EventRecorder struct {
	db   *gorm.DB
	ch   chan *model.RateLimitEvent
	done chan struct{}

	consumerOnce sync.Once
	closeOnce    sync.Once
	closed       bool
	closeMu      sync.Mutex
}

// NewEventRecorder 创建记录器实例
func NewEventRecorder(db *gorm.DB) *EventRecorder {
	return &EventRecorder{
		db:   db,
		ch:   make(chan *model.RateLimitEvent, defaultBufferSize),
		done: make(chan struct{}),
	}
}

// Default 全局单例；由 router.Setup/SetupBackend 在启动时初始化
var Default *EventRecorder

// InitDefault 初始化全局单例并启动 consumer
func InitDefault(db *gorm.DB, ctx context.Context) *EventRecorder {
	if Default != nil {
		return Default
	}
	Default = NewEventRecorder(db)
	Default.RunConsumer(ctx)
	return Default
}

// ShutdownDefault 优雅关闭全局单例
func ShutdownDefault() {
	if Default != nil {
		Default.Shutdown()
	}
}

// Enqueue 异步入队（非阻塞，channel 满时丢弃）
func (s *EventRecorder) Enqueue(ev *model.RateLimitEvent) {
	if s == nil || ev == nil {
		return
	}
	s.closeMu.Lock()
	if s.closed {
		s.closeMu.Unlock()
		return
	}
	s.closeMu.Unlock()

	defer func() {
		if r := recover(); r != nil {
			logger.L.Warn("ratelimit event enqueue panic recovered", zap.Any("recover", r))
		}
	}()

	select {
	case s.ch <- ev:
	default:
		logger.L.Warn("ratelimit event buffer full, dropping",
			zap.String("subject_type", ev.SubjectType),
			zap.String("subject_id", ev.SubjectID),
			zap.String("path", ev.Path),
		)
	}
}

// RunConsumer 启动消费 goroutine
func (s *EventRecorder) RunConsumer(ctx context.Context) {
	s.consumerOnce.Do(func() {
		go s.runLoop(ctx)
	})
}

func (s *EventRecorder) runLoop(ctx context.Context) {
	defer close(s.done)
	defer func() {
		if r := recover(); r != nil {
			logger.L.Error("ratelimit event consumer panic", zap.Any("recover", r))
		}
	}()

	batch := make([]*model.RateLimitEvent, 0, defaultBatchSize)
	ticker := time.NewTicker(defaultFlushEvery)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		s.writeBatch(batch)
		batch = batch[:0]
	}

	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case ev, ok := <-s.ch:
					if !ok {
						flush()
						return
					}
					batch = append(batch, ev)
					if len(batch) >= defaultBatchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		case ev, ok := <-s.ch:
			if !ok {
				flush()
				return
			}
			batch = append(batch, ev)
			if len(batch) >= defaultBatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (s *EventRecorder) writeBatch(batch []*model.RateLimitEvent) {
	defer func() {
		if r := recover(); r != nil {
			logger.L.Error("ratelimit event writeBatch panic", zap.Any("recover", r))
		}
	}()
	wctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.db.WithContext(wctx).CreateInBatches(batch, defaultBatchSize).Error; err != nil {
		logger.L.Error("ratelimit event batch insert failed", zap.Error(err), zap.Int("count", len(batch)))
	}
}

// Shutdown 优雅关闭
func (s *EventRecorder) Shutdown() {
	s.closeOnce.Do(func() {
		s.closeMu.Lock()
		s.closed = true
		s.closeMu.Unlock()
		close(s.ch)
		select {
		case <-s.done:
		case <-time.After(10 * time.Second):
			logger.L.Warn("ratelimit event consumer shutdown timeout")
		}
	})
}

// List 分页查询
func (s *EventRecorder) List(ctx context.Context, q *model.RateLimitEventQuery) ([]model.RateLimitEvent, int64, error) {
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("recorder not initialized")
	}
	db := s.db.WithContext(ctx).Model(&model.RateLimitEvent{})
	if q.SubjectType != "" {
		db = db.Where("subject_type = ?", q.SubjectType)
	}
	if q.SubjectID != "" {
		db = db.Where("subject_id = ?", q.SubjectID)
	}
	if q.Path != "" {
		db = db.Where("path LIKE ?", "%"+q.Path+"%")
	}
	if !q.StartDate.IsZero() {
		db = db.Where("created_at >= ?", q.StartDate)
	}
	if !q.EndDate.IsZero() {
		db = db.Where("created_at <= ?", q.EndDate)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page := q.Page
	if page < 1 {
		page = 1
	}
	ps := q.PageSize
	if ps < 1 || ps > 200 {
		ps = 50
	}
	var list []model.RateLimitEvent
	if err := db.Order("created_at DESC").Offset((page - 1) * ps).Limit(ps).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// DeleteOld 分批删除指定日期前记录
func (s *EventRecorder) DeleteOld(ctx context.Context, before time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("recorder not initialized")
	}
	var total int64
	for {
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}
		r := s.db.WithContext(ctx).Where("created_at < ?", before).Limit(cleanupBatchSize).Delete(&model.RateLimitEvent{})
		if r.Error != nil {
			return total, r.Error
		}
		total += r.RowsAffected
		if r.RowsAffected < cleanupBatchSize {
			return total, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// DeleteOlderThan 便捷方法
func (s *EventRecorder) DeleteOlderThan(ctx context.Context, days int) (int64, error) {
	if days <= 0 {
		days = 7
	}
	return s.DeleteOld(ctx, time.Now().Add(-time.Duration(days)*24*time.Hour))
}
