package report

import (
	"context"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/credits"
)

func TestBillingReconciliationBuildIncludesUnitsAndVariance(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.ApiCallLog{},
		&model.FreezeRecord{},
		&model.UserBalance{},
		&model.BillingReconciliationSnapshot{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}

	now := time.Now()
	if err := db.Create(&model.ApiCallLog{
		CreatedAt:             now,
		RequestID:             "rec-units-1",
		UserID:                1,
		TenantID:              1,
		Endpoint:              "/v1/chat/completions",
		RequestModel:          "qwen-test",
		StatusCode:            200,
		CostCredits:           20,
		CostUnits:             credits.CreditsToBillingUnits(20),
		EstimatedCostCredits:  30,
		EstimatedCostUnits:    credits.CreditsToBillingUnits(30),
		ActualCostCredits:     20,
		ActualCostUnits:       credits.CreditsToBillingUnits(20),
		PlatformCostRMB:       0.001,
		PlatformCostUnits:     credits.RMBToBillingUnits(0.001),
		BillingStatus:         "settled",
		UnderCollectedCredits: 0,
		UnderCollectedUnits:   0,
		UsageSource:           "provider",
	}).Error; err != nil {
		t.Fatalf("seed api log: %v", err)
	}

	svc := NewBillingReconciliationService(db)
	snap, err := svc.Build(context.Background(), now.Format(reconciliationDateLayout))
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if snap.ActualRevenueUnits != credits.CreditsToBillingUnits(20) {
		t.Fatalf("actual units = %d", snap.ActualRevenueUnits)
	}
	if snap.EstimatedCostUnits != credits.CreditsToBillingUnits(30) {
		t.Fatalf("estimated units = %d", snap.EstimatedCostUnits)
	}
	if snap.EstimateVarianceCredits != 10 {
		t.Fatalf("variance credits = %d, want 10", snap.EstimateVarianceCredits)
	}
	if snap.EstimateVarianceUnits != credits.CreditsToBillingUnits(10) {
		t.Fatalf("variance units = %d", snap.EstimateVarianceUnits)
	}
}
