// Package authlog 用户认证行为日志异步记录器
// 与 ratelimit.EventRecorder / audit.AuditService 共享相同的异步批量写入模式
package authlog

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// 异步队列默认配置（与 audit_service / ratelimit.EventRecorder 对齐）
const (
	defaultBufferSize = 1024
	defaultBatchSize  = 50
	defaultFlushEvery = 500 * time.Millisecond
	cleanupBatchSize  = 1000
)

// Recorder 用户认证日志异步记录器
type Recorder struct {
	db   *gorm.DB
	ch   chan *model.UserAuthLog
	done chan struct{}

	consumerOnce sync.Once
	closeOnce    sync.Once
	closed       bool
	closeMu      sync.Mutex
}

// NewRecorder 创建记录器实例
func NewRecorder(db *gorm.DB) *Recorder {
	return &Recorder{
		db:   db,
		ch:   make(chan *model.UserAuthLog, defaultBufferSize),
		done: make(chan struct{}),
	}
}

// Default 全局单例；由 router.Setup / SetupBackend 在启动时初始化
var Default *Recorder

// InitDefault 初始化全局单例并启动 consumer
func InitDefault(db *gorm.DB, ctx context.Context) *Recorder {
	if Default != nil {
		return Default
	}
	Default = NewRecorder(db)
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
func (s *Recorder) Enqueue(ev *model.UserAuthLog) {
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
			logger.L.Warn("auth log enqueue panic recovered", zap.Any("recover", r))
		}
	}()

	// UA 安全截断（防止超长 header 污染数据）
	if len(ev.UserAgent) > 512 {
		ev.UserAgent = ev.UserAgent[:512]
	}

	select {
	case s.ch <- ev:
	default:
		logger.L.Warn("auth log buffer full, dropping",
			zap.String("event_type", ev.EventType),
			zap.String("email", ev.Email),
		)
	}
}

// RunConsumer 启动消费 goroutine
func (s *Recorder) RunConsumer(ctx context.Context) {
	s.consumerOnce.Do(func() {
		go s.runLoop(ctx)
	})
}

func (s *Recorder) runLoop(ctx context.Context) {
	defer close(s.done)
	defer func() {
		if r := recover(); r != nil {
			logger.L.Error("auth log consumer panic", zap.Any("recover", r))
		}
	}()

	batch := make([]*model.UserAuthLog, 0, defaultBatchSize)
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

func (s *Recorder) writeBatch(batch []*model.UserAuthLog) {
	defer func() {
		if r := recover(); r != nil {
			logger.L.Error("auth log writeBatch panic", zap.Any("recover", r))
		}
	}()
	wctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.db.WithContext(wctx).CreateInBatches(batch, defaultBatchSize).Error; err != nil {
		logger.L.Error("auth log batch insert failed", zap.Error(err), zap.Int("count", len(batch)))
	}
}

// Shutdown 优雅关闭
func (s *Recorder) Shutdown() {
	s.closeOnce.Do(func() {
		s.closeMu.Lock()
		s.closed = true
		s.closeMu.Unlock()
		close(s.ch)
		select {
		case <-s.done:
		case <-time.After(10 * time.Second):
			logger.L.Warn("auth log consumer shutdown timeout")
		}
	})
}

// List 分页查询
func (s *Recorder) List(ctx context.Context, q *model.UserAuthLogQuery) ([]model.UserAuthLog, int64, error) {
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("recorder not initialized")
	}
	db := s.db.WithContext(ctx).Model(&model.UserAuthLog{})
	if q.UserID > 0 {
		db = db.Where("user_id = ?", q.UserID)
	}
	if q.Email != "" {
		db = db.Where("email LIKE ?", like(q.Email))
	}
	if q.EventType != "" {
		db = db.Where("event_type = ?", q.EventType)
	}
	if q.IP != "" {
		db = db.Where("ip LIKE ?", like(q.IP))
	}
	if q.Keyword != "" {
		kw := like(q.Keyword)
		keywordQuery := "email LIKE ? OR ip LIKE ? OR request_id LIKE ? OR user_agent LIKE ? OR fail_reason LIKE ? OR country LIKE ? OR city LIKE ?"
		args := []interface{}{kw, kw, kw, kw, kw, kw, kw}
		if uid, err := strconv.ParseUint(strings.TrimSpace(q.Keyword), 10, 64); err == nil && uid > 0 {
			keywordQuery = "user_id = ? OR " + keywordQuery
			args = append([]interface{}{uint(uid)}, args...)
		}
		db = db.Where(keywordQuery, args...)
	}
	if q.RequestID != "" {
		db = db.Where("request_id LIKE ?", like(q.RequestID))
	}
	if q.UserAgent != "" {
		db = db.Where("user_agent LIKE ?", like(q.UserAgent))
	}
	if q.Country != "" {
		db = db.Where("country LIKE ?", like(q.Country))
	}
	if q.City != "" {
		db = db.Where("city LIKE ?", like(q.City))
	}
	if q.FailReason != "" {
		db = db.Where("fail_reason LIKE ?", like(q.FailReason))
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
	var list []model.UserAuthLog
	if err := db.Order("created_at DESC").Offset((page - 1) * ps).Limit(ps).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

func like(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "%", "\\%")
	value = strings.ReplaceAll(value, "_", "\\_")
	return "%" + value + "%"
}

// Stats 返回指定天数内的统计摘要
type Stats struct {
	Days        int              `json:"days"`
	TotalEvents int64            `json:"total_events"`
	ByEventType map[string]int64 `json:"by_event_type"`
	TodayCounts map[string]int64 `json:"today_counts"`
}

// GetStats 获取近 N 天事件聚合
func (s *Recorder) GetStats(ctx context.Context, days int) (*Stats, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("recorder not initialized")
	}
	if days <= 0 {
		days = 7
	}
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	st := &Stats{
		Days:        days,
		ByEventType: map[string]int64{},
		TodayCounts: map[string]int64{},
	}

	// 总数
	if err := s.db.WithContext(ctx).Model(&model.UserAuthLog{}).
		Where("created_at >= ?", since).Count(&st.TotalEvents).Error; err != nil {
		return nil, err
	}

	// 按事件类型分组（窗口期内）
	type kv struct {
		EventType string
		Cnt       int64
	}
	var rows []kv
	if err := s.db.WithContext(ctx).
		Model(&model.UserAuthLog{}).
		Select("event_type, COUNT(*) AS cnt").
		Where("created_at >= ?", since).
		Group("event_type").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		st.ByEventType[r.EventType] = r.Cnt
	}

	// 今日各事件数
	today := time.Now().Truncate(24 * time.Hour)
	var todayRows []kv
	if err := s.db.WithContext(ctx).
		Model(&model.UserAuthLog{}).
		Select("event_type, COUNT(*) AS cnt").
		Where("created_at >= ?", today).
		Group("event_type").
		Scan(&todayRows).Error; err != nil {
		return nil, err
	}
	for _, r := range todayRows {
		st.TodayCounts[r.EventType] = r.Cnt
	}

	return st, nil
}

// DeleteOld 分批删除指定日期前记录
func (s *Recorder) DeleteOld(ctx context.Context, before time.Time) (int64, error) {
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
		r := s.db.WithContext(ctx).Where("created_at < ?", before).Limit(cleanupBatchSize).Delete(&model.UserAuthLog{})
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
func (s *Recorder) DeleteOlderThan(ctx context.Context, days int) (int64, error) {
	if days <= 0 {
		days = 90
	}
	return s.DeleteOld(ctx, time.Now().Add(-time.Duration(days)*24*time.Hour))
}
