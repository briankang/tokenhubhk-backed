package database

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

func TestEnsureBillingCostUnitColumns(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE api_call_logs (id integer primary key, request_id text)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE billing_reconciliation_snapshots (id integer primary key, date text)`).Error; err != nil {
		t.Fatal(err)
	}

	prev := DB
	DB = db
	defer func() { DB = prev }()

	if err := ensureBillingCostUnitColumns(); err != nil {
		t.Fatal(err)
	}

	apiCallLogFields := []string{
		"CostUnits",
		"EstimatedCostUnits",
		"FrozenUnits",
		"ActualCostUnits",
		"PlatformCostUnits",
		"UnderCollectedUnits",
	}
	for _, field := range apiCallLogFields {
		if !db.Migrator().HasColumn(&model.ApiCallLog{}, field) {
			t.Fatalf("api_call_logs missing migrated column %s", field)
		}
	}

	snapshotFields := []string{
		"ActualRevenueUnits",
		"EstimatedCostUnits",
		"EstimateVarianceCredits",
		"EstimateVarianceUnits",
		"FrozenUnits",
		"UnderCollectedUnits",
		"PlatformCostUnits",
		"ExpiredFreezeUnits",
		"OpenFrozenUnits",
	}
	for _, field := range snapshotFields {
		if !db.Migrator().HasColumn(&model.BillingReconciliationSnapshot{}, field) {
			t.Fatalf("billing_reconciliation_snapshots missing migrated column %s", field)
		}
	}
}
