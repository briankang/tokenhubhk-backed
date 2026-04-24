package report

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
)

const reconciliationDateLayout = "2006-01-02"

type BillingReconciliationService struct {
	db *gorm.DB
}

func NewBillingReconciliationService(db *gorm.DB) *BillingReconciliationService {
	return &BillingReconciliationService{db: db}
}

func (s *BillingReconciliationService) Build(ctx context.Context, date string) (*model.BillingReconciliationSnapshot, error) {
	if date == "" {
		date = time.Now().Format(reconciliationDateLayout)
	}
	day, err := time.ParseInLocation(reconciliationDateLayout, date, time.Local)
	if err != nil {
		return nil, err
	}
	next := day.AddDate(0, 0, 1)

	snap := &model.BillingReconciliationSnapshot{Date: date}
	err = s.db.WithContext(ctx).Table("api_call_logs").
		Where("created_at >= ? AND created_at < ?", day, next).
		Select(
			"COUNT(*) AS total_requests," +
				"SUM(CASE WHEN COALESCE(billing_status,'settled') = 'settled' THEN 1 ELSE 0 END) AS settled_requests," +
				"SUM(CASE WHEN billing_status = 'no_charge' THEN 1 ELSE 0 END) AS no_charge_requests," +
				"SUM(CASE WHEN billing_status = 'deduct_failed' THEN 1 ELSE 0 END) AS deduct_failed_requests," +
				"SUM(CASE WHEN usage_estimated = TRUE OR usage_source = 'estimated' THEN 1 ELSE 0 END) AS estimated_requests," +
				"SUM(CASE WHEN COALESCE(usage_source,'') = '' OR usage_source = 'unknown' THEN 1 ELSE 0 END) AS missing_usage_requests," +
				"SUM(CASE WHEN COALESCE(platform_cost_rmb,0) = 0 AND COALESCE(total_tokens,0) > 0 THEN 1 ELSE 0 END) AS missing_platform_cost_requests," +
				"COALESCE(SUM(CASE WHEN COALESCE(actual_cost_credits,0) > 0 THEN actual_cost_credits WHEN COALESCE(billing_status,'settled') = 'settled' THEN cost_credits ELSE 0 END),0) AS actual_revenue_credits," +
				"COALESCE(SUM(CASE WHEN COALESCE(actual_cost_units,0) > 0 THEN actual_cost_units WHEN COALESCE(billing_status,'settled') = 'settled' THEN cost_units ELSE 0 END),0) AS actual_revenue_units," +
				"COALESCE(SUM(CASE WHEN COALESCE(actual_cost_credits,0) > 0 THEN actual_cost_credits WHEN COALESCE(billing_status,'settled') = 'settled' THEN cost_credits ELSE 0 END),0)/10000.0 AS actual_revenue_rmb," +
				"COALESCE(SUM(estimated_cost_credits),0) AS estimated_cost_credits," +
				"COALESCE(SUM(estimated_cost_units),0) AS estimated_cost_units," +
				"COALESCE(SUM(estimated_cost_credits),0) - COALESCE(SUM(CASE WHEN COALESCE(actual_cost_credits,0) > 0 THEN actual_cost_credits WHEN COALESCE(billing_status,'settled') = 'settled' THEN cost_credits ELSE 0 END),0) AS estimate_variance_credits," +
				"COALESCE(SUM(estimated_cost_units),0) - COALESCE(SUM(CASE WHEN COALESCE(actual_cost_units,0) > 0 THEN actual_cost_units WHEN COALESCE(billing_status,'settled') = 'settled' THEN cost_units ELSE 0 END),0) AS estimate_variance_units," +
				"COALESCE(SUM(frozen_credits),0) AS frozen_credits," +
				"COALESCE(SUM(frozen_units),0) AS frozen_units," +
				"COALESCE(SUM(under_collected_credits),0) AS under_collected_credits," +
				"COALESCE(SUM(under_collected_units),0) AS under_collected_units," +
				"COALESCE(SUM(under_collected_credits),0)/10000.0 AS under_collected_rmb," +
				"COALESCE(SUM(platform_cost_units),0) AS platform_cost_units," +
				"COALESCE(SUM(platform_cost_rmb),0) AS platform_cost_rmb," +
				"(COALESCE(SUM(CASE WHEN COALESCE(actual_cost_credits,0) > 0 THEN actual_cost_credits WHEN COALESCE(billing_status,'settled') = 'settled' THEN cost_credits ELSE 0 END),0)/10000.0 - COALESCE(SUM(platform_cost_rmb),0)) AS gross_profit_rmb",
		).Scan(snap).Error
	if err != nil {
		return nil, err
	}
	snap.Date = date

	expireTime := time.Now().Add(-5 * time.Minute)
	if err := s.db.WithContext(ctx).Model(&model.FreezeRecord{}).
		Where("status = 'FROZEN' AND created_at < ?", expireTime).
		Count(&snap.ExpiredFreezeCount).Error; err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Model(&model.FreezeRecord{}).
		Where("status = 'FROZEN' AND created_at < ?", expireTime).
		Select("COALESCE(SUM(frozen_amount), 0)").Scan(&snap.ExpiredFreezeCredits).Error; err != nil {
		return nil, err
	}
	snap.ExpiredFreezeUnits = credits.CreditsToBillingUnits(snap.ExpiredFreezeCredits)
	if err := s.db.WithContext(ctx).Model(&model.FreezeRecord{}).
		Where("status = 'FROZEN'").
		Count(&snap.OpenFrozenRecordCount).Error; err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Model(&model.FreezeRecord{}).
		Where("status = 'FROZEN'").
		Select("COALESCE(SUM(frozen_amount), 0)").Scan(&snap.OpenFrozenCredits).Error; err != nil {
		return nil, err
	}
	snap.OpenFrozenUnits = credits.CreditsToBillingUnits(snap.OpenFrozenCredits)
	if err := s.db.WithContext(ctx).Model(&model.UserBalance{}).
		Where("balance < 0 OR free_quota < 0 OR frozen_amount < 0").
		Count(&snap.NegativeBalanceUserCount).Error; err != nil {
		return nil, err
	}

	if snap.ActualRevenueRMB > 0 {
		snap.GrossProfitMargin = snap.GrossProfitRMB / snap.ActualRevenueRMB
	}
	snap.ReconciliationHealth, snap.ReconciliationWarningReasons = classifyBillingHealth(snap)
	return snap, nil
}

func (s *BillingReconciliationService) Upsert(ctx context.Context, snap *model.BillingReconciliationSnapshot) error {
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "date"}},
		UpdateAll: true,
	}).Create(snap).Error
}

func (s *BillingReconciliationService) UpsertDate(ctx context.Context, date string) (*model.BillingReconciliationSnapshot, error) {
	snap, err := s.Build(ctx, date)
	if err != nil {
		return nil, err
	}
	if err := s.Upsert(ctx, snap); err != nil {
		return nil, err
	}
	var persisted model.BillingReconciliationSnapshot
	if err := s.db.WithContext(ctx).Where("date = ?", snap.Date).First(&persisted).Error; err != nil {
		return nil, err
	}
	return &persisted, nil
}

func (s *BillingReconciliationService) List(ctx context.Context, page, pageSize int) ([]model.BillingReconciliationSnapshot, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	var total int64
	q := s.db.WithContext(ctx).Model(&model.BillingReconciliationSnapshot{})
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var list []model.BillingReconciliationSnapshot
	err := q.Order("date DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error
	return list, total, err
}

func classifyBillingHealth(s *model.BillingReconciliationSnapshot) (string, string) {
	var warnings []string
	if s.DeductFailedRequests > 0 {
		warnings = append(warnings, "deduct_failed")
	}
	if s.UnderCollectedCredits > 0 {
		warnings = append(warnings, "under_collected")
	}
	if s.MissingUsageRequests > 0 {
		warnings = append(warnings, "missing_usage")
	}
	if s.MissingPlatformCostRequests > 0 {
		warnings = append(warnings, "missing_platform_cost")
	}
	if s.ExpiredFreezeCount > 0 {
		warnings = append(warnings, "expired_freezes")
	}
	if s.NegativeBalanceUserCount > 0 {
		warnings = append(warnings, "negative_balance")
	}
	if len(warnings) == 0 {
		return "healthy", ""
	}
	if s.DeductFailedRequests > 0 || s.UnderCollectedCredits > 0 || s.NegativeBalanceUserCount > 0 {
		return "critical", strings.Join(warnings, ",")
	}
	return "warning", strings.Join(warnings, ",")
}
