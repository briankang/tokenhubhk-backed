package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// 异步队列默认配置
const (
	defaultBufferSize = 1024 // channel 容量
	defaultBatchSize  = 50   // 单次 batch insert 最大条数
	defaultFlushEvery = 500 * time.Millisecond
	cleanupBatchSize  = 1000 // 定时清理时单次删除上限，避免锁表
)

// AuditService 审计日志服务（支持异步队列写入）
type AuditService struct {
	db   *gorm.DB
	ch   chan *model.AuditLog
	done chan struct{}

	consumerOnce sync.Once
	closeOnce    sync.Once
	closed       bool
	closeMu      sync.Mutex
}

// NewAuditService 创建审计日志服务实例
func NewAuditService(db *gorm.DB) *AuditService {
	return &AuditService{
		db:   db,
		ch:   make(chan *model.AuditLog, defaultBufferSize),
		done: make(chan struct{}),
	}
}

// 全局共享 AuditService 单例。
//
// 由 router.Setup() / SetupBackend() 在启动时初始化（同时启动 consumer goroutine）。
// 中间件、cron 清理任务、定向写入的 handler 都从这里取，确保异步队列只有一份。
var Default *AuditService

// InitDefault 初始化全局单例并启动消费者。重复调用是幂等的（仅首次生效）。
func InitDefault(db *gorm.DB, ctx context.Context) *AuditService {
	if Default != nil {
		return Default
	}
	Default = NewAuditService(db)
	Default.RunConsumer(ctx)
	return Default
}

// ShutdownDefault 优雅关闭全局单例（在 main.go 的 graceful shutdown 中调用）
func ShutdownDefault() {
	if Default != nil {
		Default.Shutdown()
	}
}

// Enqueue 异步入队（非阻塞），channel 满时直接丢弃并记录错误日志
func (s *AuditService) Enqueue(log *model.AuditLog) {
	if log == nil {
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
			logger.L.Warn("audit enqueue panic recovered", zap.Any("recover", r))
		}
	}()

	select {
	case s.ch <- log:
	default:
		logger.L.Warn("audit buffer full, dropping log",
			zap.String("action", log.Action),
			zap.String("path", log.Path),
			zap.Uint("operator_id", log.OperatorID),
		)
	}
}

// RunConsumer 启动消费 goroutine，按 batch 写入数据库（每 500ms 或攒满 50 条）
func (s *AuditService) RunConsumer(ctx context.Context) {
	s.consumerOnce.Do(func() {
		go s.runLoop(ctx)
	})
}

func (s *AuditService) runLoop(ctx context.Context) {
	defer close(s.done)
	defer func() {
		if r := recover(); r != nil {
			logger.L.Error("audit consumer panic recovered", zap.Any("recover", r))
		}
	}()

	batch := make([]*model.AuditLog, 0, defaultBatchSize)
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
			// 排空通道剩余消息
			for {
				select {
				case log, ok := <-s.ch:
					if !ok {
						flush()
						return
					}
					batch = append(batch, log)
					if len(batch) >= defaultBatchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		case log, ok := <-s.ch:
			if !ok {
				flush()
				return
			}
			batch = append(batch, log)
			if len(batch) >= defaultBatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (s *AuditService) writeBatch(batch []*model.AuditLog) {
	defer func() {
		if r := recover(); r != nil {
			logger.L.Error("audit writeBatch panic", zap.Any("recover", r))
		}
	}()
	// 用独立 ctx + 短超时避免阻塞过长
	wctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.db.WithContext(wctx).CreateInBatches(batch, defaultBatchSize).Error; err != nil {
		logger.L.Error("audit batch insert failed, falling back to single insert", zap.Error(err), zap.Int("count", len(batch)))
		// 回退单条
		for _, item := range batch {
			if err := s.db.WithContext(wctx).Create(item).Error; err != nil {
				logger.L.Error("audit single insert failed", zap.Error(err), zap.String("action", item.Action))
			}
		}
	}
}

// Shutdown 优雅关闭：关闭 channel 等 consumer drain 完成
func (s *AuditService) Shutdown() {
	s.closeOnce.Do(func() {
		s.closeMu.Lock()
		s.closed = true
		s.closeMu.Unlock()
		close(s.ch)
		// 最多等 10s 让 consumer 处理完
		select {
		case <-s.done:
		case <-time.After(10 * time.Second):
			logger.L.Warn("audit consumer shutdown timeout, some logs may be lost")
		}
	})
}

// Create 创建审计日志记录
func (s *AuditService) Create(ctx context.Context, log *model.AuditLog) error {
	return s.db.WithContext(ctx).Create(log).Error
}

// CreateWithValues 创建审计日志（带旧值和新值）
func (s *AuditService) CreateWithValues(ctx context.Context, action string, resourceID, operatorID uint, oldValue, newValue interface{}, ip, remark string) error {
	oldJSON, _ := json.Marshal(oldValue)
	newJSON, _ := json.Marshal(newValue)

	log := &model.AuditLog{
		Action:     action,
		ResourceID: resourceID,
		OperatorID: operatorID,
		OldValue:   string(oldJSON),
		NewValue:   string(newJSON),
		IP:         ip,
		Remark:     remark,
	}

	return s.db.WithContext(ctx).Create(log).Error
}

// List 分页查询审计日志
func (s *AuditService) List(ctx context.Context, query *model.AuditLogQuery) ([]model.AuditLog, int64, error) {
	db := s.db.WithContext(ctx).Model(&model.AuditLog{})

	// 应用过滤条件
	if query.Action != "" {
		db = db.Where("action = ?", query.Action)
	}
	if query.OperatorID > 0 {
		db = db.Where("operator_id = ?", query.OperatorID)
	}
	if query.Menu != "" {
		db = db.Where("menu = ?", query.Menu)
	}
	if query.Feature != "" {
		db = db.Where("feature LIKE ?", "%"+query.Feature+"%")
	}
	if query.Resource != "" {
		db = db.Where("resource = ?", query.Resource)
	}
	if !query.StartDate.IsZero() {
		db = db.Where("created_at >= ?", query.StartDate)
	}
	if !query.EndDate.IsZero() {
		db = db.Where("created_at <= ?", query.EndDate)
	}

	// 统计总数
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count audit logs: %w", err)
	}

	// 分页查询
	page := query.Page
	if page < 1 {
		page = 1
	}
	pageSize := query.PageSize
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	var logs []model.AuditLog
	if err := db.Order("created_at DESC").Offset(offset).Limit(pageSize).Find(&logs).Error; err != nil {
		return nil, 0, fmt.Errorf("list audit logs: %w", err)
	}

	return logs, total, nil
}

// GetByID 根据ID获取审计日志
func (s *AuditService) GetByID(ctx context.Context, id uint) (*model.AuditLog, error) {
	var log model.AuditLog
	if err := s.db.WithContext(ctx).First(&log, id).Error; err != nil {
		return nil, err
	}
	return &log, nil
}

// GetByResource 获取指定资源的审计日志
func (s *AuditService) GetByResource(ctx context.Context, resource string, resourceID uint) ([]model.AuditLog, error) {
	var logs []model.AuditLog
	if err := s.db.WithContext(ctx).
		Where("resource = ? AND resource_id = ?", resource, resourceID).
		Order("created_at DESC").
		Find(&logs).Error; err != nil {
		return nil, err
	}
	return logs, nil
}

// GetByUser 获取指定用户的审计日志
func (s *AuditService) GetByUser(ctx context.Context, userID uint, page, pageSize int) ([]model.AuditLog, int64, error) {
	var total int64
	db := s.db.WithContext(ctx).Model(&model.AuditLog{}).Where("user_id = ?", userID)
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	var logs []model.AuditLog
	if err := db.Order("created_at DESC").Offset(offset).Limit(pageSize).Find(&logs).Error; err != nil {
		return nil, 0, err
	}

	return logs, total, nil
}

// GetRecent 获取最近的审计日志
func (s *AuditService) GetRecent(ctx context.Context, limit int) ([]model.AuditLog, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}

	var logs []model.AuditLog
	if err := s.db.WithContext(ctx).
		Order("created_at DESC").
		Limit(limit).
		Find(&logs).Error; err != nil {
		return nil, err
	}
	return logs, nil
}

// DeleteOld 删除指定日期之前的审计日志（分批删除避免锁表）
// 每次循环最多删除 cleanupBatchSize 条，直到一次循环影响行数为 0 或上下文取消
func (s *AuditService) DeleteOld(ctx context.Context, before time.Time) (int64, error) {
	var totalDeleted int64
	for {
		select {
		case <-ctx.Done():
			return totalDeleted, ctx.Err()
		default:
		}
		result := s.db.WithContext(ctx).
			Where("created_at < ?", before).
			Limit(cleanupBatchSize).
			Delete(&model.AuditLog{})
		if result.Error != nil {
			return totalDeleted, result.Error
		}
		totalDeleted += result.RowsAffected
		if result.RowsAffected < cleanupBatchSize {
			return totalDeleted, nil
		}
		// 让出 CPU + IO，避免持续占用 DB
		time.Sleep(50 * time.Millisecond)
	}
}

// DeleteOlderThan 便捷方法：删除 retentionDays 天之前的日志
func (s *AuditService) DeleteOlderThan(ctx context.Context, retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		retentionDays = 30
	}
	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour)
	return s.DeleteOld(ctx, cutoff)
}
