package report

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"tokenhub-server/internal/model"
)

// StatsAggregator 统计聚合器，将 ChannelLog 记录聚合为 DailyStats
type StatsAggregator struct {
	db     *gorm.DB
	logger *zap.Logger
}

// NewStatsAggregator 创建统计聚合器实例
func NewStatsAggregator(db *gorm.DB, logger *zap.Logger) *StatsAggregator {
	if db == nil {
		panic("stats aggregator: db is nil")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &StatsAggregator{db: db, logger: logger}
}

// Start 启动后台 goroutine，每小时执行一次聚合任务
func (a *StatsAggregator) Start(ctx context.Context) {
	go func() {
		// Run once at startup for today
		today := time.Now().Format("2006-01-02")
		if err := a.AggregateDaily(ctx, today); err != nil {
			a.logger.Error("initial aggregation failed", zap.String("date", today), zap.Error(err))
		}

		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				a.logger.Info("stats aggregator stopped")
				return
			case t := <-ticker.C:
				date := t.Format("2006-01-02")
				a.logger.Info("running scheduled aggregation", zap.String("date", date))
				if err := a.AggregateDaily(ctx, date); err != nil {
					a.logger.Error("scheduled aggregation failed", zap.String("date", date), zap.Error(err))
				}

				// Also re-aggregate yesterday if within the first few hours
				if t.Hour() < 3 {
					yesterday := t.AddDate(0, 0, -1).Format("2006-01-02")
					if err := a.AggregateDaily(ctx, yesterday); err != nil {
						a.logger.Error("yesterday aggregation failed", zap.String("date", yesterday), zap.Error(err))
					}
				}
			}
		}
	}()

	a.logger.Info("stats aggregator started (hourly)")
}

// AggregateDaily 将指定日期的所有 ChannelLog 聚合为 DailyStats
// 使用 UPSERT 逻辑保证幂等性
func (a *StatsAggregator) AggregateDaily(ctx context.Context, date string) error {
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return fmt.Errorf("invalid date format: %w", err)
	}

	a.logger.Info("aggregating daily stats", zap.String("date", date))

	// Query aggregated data from channel_logs
	type aggRow struct {
		Date              string
		TenantID          uint
		ModelID           uint
		ChannelID         uint
		AgentLevel        int
		TotalRequests     int64
		TotalInputTokens  int64
		TotalOutputTokens int64
		TotalCost         float64
		TotalRevenue      float64
		AvgLatencyMs      float64
		ErrorCount        int64
	}

	var rows []aggRow
	err := a.db.WithContext(ctx).
		Table("channel_logs").
		Select(`
			DATE(created_at) as date,
			tenant_id,
			0 as model_id,
			channel_id,
			agent_level,
			COUNT(*) as total_requests,
			COALESCE(SUM(request_tokens), 0) as total_input_tokens,
			COALESCE(SUM(response_tokens), 0) as total_output_tokens,
			0 as total_cost,
			0 as total_revenue,
			COALESCE(AVG(latency_ms), 0) as avg_latency_ms,
			SUM(CASE WHEN error_message != '' AND error_message IS NOT NULL THEN 1 ELSE 0 END) as error_count
		`).
		Where("DATE(created_at) = ?", date).
		Group("DATE(created_at), tenant_id, channel_id, agent_level").
		Scan(&rows).Error

	if err != nil {
		return fmt.Errorf("failed to query channel_logs for aggregation: %w", err)
	}

	if len(rows) == 0 {
		a.logger.Info("no channel_logs found for date", zap.String("date", date))
		return nil
	}

	// Convert to DailyStats models
	stats := make([]model.DailyStats, 0, len(rows))
	for _, r := range rows {
		stats = append(stats, model.DailyStats{
			Date:              date,
			TenantID:          r.TenantID,
			ModelID:           r.ModelID,
			ChannelID:         r.ChannelID,
			AgentLevel:        r.AgentLevel,
			TotalRequests:     r.TotalRequests,
			TotalInputTokens:  r.TotalInputTokens,
			TotalOutputTokens: r.TotalOutputTokens,
			TotalCost:         r.TotalCost,
			TotalRevenue:      r.TotalRevenue,
			AvgLatencyMs:      r.AvgLatencyMs,
			ErrorCount:        r.ErrorCount,
		})
	}

	// Batch upsert
	err = a.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "date"},
				{Name: "tenant_id"},
				{Name: "model_id"},
				{Name: "channel_id"},
				{Name: "agent_level"},
			},
			DoUpdates: clause.AssignmentColumns([]string{
				"total_requests", "total_input_tokens", "total_output_tokens",
				"total_cost", "total_revenue", "avg_latency_ms", "error_count",
				"updated_at",
			}),
		}).
		CreateInBatches(stats, 100).Error

	if err != nil {
		return fmt.Errorf("failed to upsert daily stats: %w", err)
	}

	a.logger.Info("aggregation complete",
		zap.String("date", date),
		zap.Int("rows", len(stats)),
	)

	return nil
}

// AggregateRange 聚合 [startDate, endDate] 区间的所有日期数据
func (a *StatsAggregator) AggregateRange(ctx context.Context, startDate, endDate string) error {
	start, err := time.Parse("2006-01-02", startDate)
	if err != nil {
		return fmt.Errorf("invalid start_date: %w", err)
	}
	end, err := time.Parse("2006-01-02", endDate)
	if err != nil {
		return fmt.Errorf("invalid end_date: %w", err)
	}
	if start.After(end) {
		return fmt.Errorf("start_date must not be after end_date")
	}

	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		date := d.Format("2006-01-02")
		if err := a.AggregateDaily(ctx, date); err != nil {
			a.logger.Error("range aggregation failed for date",
				zap.String("date", date),
				zap.Error(err),
			)
			// Continue with next date instead of aborting
		}
	}

	return nil
}
