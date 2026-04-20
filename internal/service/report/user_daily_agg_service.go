package report

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// UserDailyAggService 用户调用日表聚合服务
// 从 api_call_logs 按 (date × user_id × request_model) 聚合，写入 user_daily_stats
// 设计原则：
//   - 每日01:00聚合前一天数据，在 api_call_logs 7天清理前完成持久化
//   - 全量覆盖当天数据（ON DUPLICATE KEY UPDATE），支持重跑补聚
//   - 仅聚合 user_id > 0 的有效用户请求，过滤匿名调用
type UserDailyAggService struct {
	db *gorm.DB
}

// NewUserDailyAggService 创建日表聚合服务
func NewUserDailyAggService(db *gorm.DB) *UserDailyAggService {
	return &UserDailyAggService{db: db}
}

// AggregateYesterday 聚合昨天的调用数据（供 cron 调用）
func (s *UserDailyAggService) AggregateYesterday(ctx context.Context) (int64, error) {
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	return s.AggregateDay(ctx, yesterday)
}

// AggregateDay 聚合指定日期的调用数据（幂等，支持重跑）
// date 格式：YYYY-MM-DD（如 "2026-04-18"）
func (s *UserDailyAggService) AggregateDay(ctx context.Context, date string) (int64, error) {
	// 验证日期格式
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return 0, fmt.Errorf("日期格式无效（需 YYYY-MM-DD）: %w", err)
	}

	// INSERT INTO user_daily_stats ... SELECT ... FROM api_call_logs WHERE DATE(created_at)=date
	// ON DUPLICATE KEY UPDATE 实现幂等：重跑同一天会用最新数据覆盖
	sql := `
INSERT INTO user_daily_stats
  (date, user_id, request_model,
   request_count, success_count, error_count,
   input_tokens, output_tokens, total_tokens,
   cost_credits, avg_latency_ms,
   created_at, updated_at)
SELECT
  DATE(created_at),
  user_id,
  request_model,
  COUNT(*)                                              AS request_count,
  SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END)  AS success_count,
  SUM(CASE WHEN status != 'success' THEN 1 ELSE 0 END) AS error_count,
  COALESCE(SUM(prompt_tokens), 0)                       AS input_tokens,
  COALESCE(SUM(completion_tokens), 0)                   AS output_tokens,
  COALESCE(SUM(total_tokens), 0)                        AS total_tokens,
  COALESCE(SUM(cost_credits), 0)                        AS cost_credits,
  COALESCE(AVG(total_latency_ms), 0)                    AS avg_latency_ms,
  NOW(),
  NOW()
FROM api_call_logs
WHERE DATE(created_at) = ?
  AND user_id > 0
  AND request_model != ''
GROUP BY DATE(created_at), user_id, request_model
ON DUPLICATE KEY UPDATE
  request_count = VALUES(request_count),
  success_count = VALUES(success_count),
  error_count   = VALUES(error_count),
  input_tokens  = VALUES(input_tokens),
  output_tokens = VALUES(output_tokens),
  total_tokens  = VALUES(total_tokens),
  cost_credits  = VALUES(cost_credits),
  avg_latency_ms = VALUES(avg_latency_ms),
  updated_at    = NOW()
`
	result := s.db.WithContext(ctx).Exec(sql, date)
	if result.Error != nil {
		return 0, fmt.Errorf("聚合 %s 失败: %w", date, result.Error)
	}

	zap.L().Info("用户调用日表聚合完成",
		zap.String("date", date),
		zap.Int64("rows_affected", result.RowsAffected),
	)
	return result.RowsAffected, nil
}
