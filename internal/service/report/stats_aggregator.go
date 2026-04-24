package report

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/safego"
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
	safego.Go("stats-aggregator", func() {
		// Run once at startup for today（用 Run 隔离初次执行的 panic）
		today := time.Now().Format("2006-01-02")
		safego.Run("stats-aggregator-initial", func() {
			if err := a.AggregateDaily(ctx, today); err != nil {
				a.logger.Error("initial aggregation failed", zap.String("date", today), zap.Error(err))
			}
		})

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
	})

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
		Table("api_call_logs as acl").
		Select(`
			DATE(acl.created_at) as date,
			acl.tenant_id,
			COALESCE(am.id, 0) as model_id,
			acl.channel_id,
			COALESCE(t.level, 0) as agent_level,
			COUNT(acl.id) as total_requests,
			COALESCE(SUM(acl.prompt_tokens), 0) as total_input_tokens,
			COALESCE(SUM(acl.completion_tokens), 0) as total_output_tokens,
			COALESCE(SUM(CASE
				WHEN acl.platform_cost_rmb > 0 THEN acl.platform_cost_rmb
				WHEN COALESCE(am.pricing_unit, '') = 'per_image' THEN acl.image_count * am.input_cost_rmb
				WHEN am.pricing_unit = 'per_second' THEN acl.duration_sec * am.input_cost_rmb
				WHEN am.pricing_unit = 'per_minute' THEN (acl.duration_sec / 60.0) * am.input_cost_rmb
				WHEN am.pricing_unit IN ('per_10k_characters', 'per_k_chars') THEN (acl.char_count / 10000.0) * am.input_cost_rmb
				WHEN am.pricing_unit = 'per_million_characters' THEN (acl.char_count / 1000000.0) * am.input_cost_rmb
				WHEN am.pricing_unit = 'per_call' THEN acl.call_count * am.input_cost_rmb
				WHEN am.pricing_unit = 'per_hour' THEN (acl.duration_sec / 3600.0) * am.input_cost_rmb
				ELSE
					(((CASE WHEN acl.prompt_tokens - acl.cache_read_tokens - acl.cache_write_tokens > 0 THEN acl.prompt_tokens - acl.cache_read_tokens - acl.cache_write_tokens ELSE 0 END) / 1000000.0) * am.input_cost_rmb) +
					((acl.cache_read_tokens / 1000000.0) * COALESCE(NULLIF(am.cache_input_price_rmb, 0), am.input_cost_rmb)) +
					((acl.cache_write_tokens / 1000000.0) * COALESCE(NULLIF(am.cache_write_price_rmb, 0), am.input_cost_rmb)) +
					((acl.completion_tokens / 1000000.0) * CASE WHEN acl.thinking_mode AND am.output_cost_thinking_rmb > 0 THEN am.output_cost_thinking_rmb ELSE am.output_cost_rmb END)
			END), 0) as total_cost,
			COALESCE(SUM(CASE WHEN COALESCE(acl.billing_status, 'settled') IN ('settled', 'no_charge') THEN acl.cost_rmb ELSE 0 END), 0) as total_revenue,
			COALESCE(AVG(acl.total_latency_ms), 0) as avg_latency_ms,
			SUM(CASE WHEN acl.status != 'success' THEN 1 ELSE 0 END) as error_count
		`).
		Joins("LEFT JOIN tenants t ON t.id = acl.tenant_id").
		Joins("LEFT JOIN ai_models am ON am.model_name = COALESCE(NULLIF(acl.actual_model, ''), acl.request_model)").
		Where("DATE(acl.created_at) = ?", date).
		Group("DATE(acl.created_at), acl.tenant_id, COALESCE(am.id, 0), acl.channel_id, t.level").
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
