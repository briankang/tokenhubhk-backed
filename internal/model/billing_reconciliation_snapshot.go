package model

// BillingReconciliationSnapshot stores the daily billing reconciliation summary.
// It preserves the key revenue/cost/audit counters after raw api_call_logs expire.
type BillingReconciliationSnapshot struct {
	BaseModel
	Date string `gorm:"type:varchar(10);uniqueIndex;not null" json:"date"`

	TotalRequests                int64   `json:"total_requests"`
	SettledRequests              int64   `json:"settled_requests"`
	NoChargeRequests             int64   `json:"no_charge_requests"`
	DeductFailedRequests         int64   `json:"deduct_failed_requests"`
	EstimatedRequests            int64   `json:"estimated_requests"`
	MissingUsageRequests         int64   `json:"missing_usage_requests"`
	MissingPlatformCostRequests  int64   `json:"missing_platform_cost_requests"`
	ActualRevenueCredits         int64   `json:"actual_revenue_credits"`
	ActualRevenueUnits           int64   `json:"actual_revenue_units"`
	ActualRevenueRMB             float64 `json:"actual_revenue_rmb"`
	EstimatedCostCredits         int64   `json:"estimated_cost_credits"`
	EstimatedCostUnits           int64   `json:"estimated_cost_units"`
	EstimateVarianceCredits      int64   `json:"estimate_variance_credits"`
	EstimateVarianceUnits        int64   `json:"estimate_variance_units"`
	FrozenCredits                int64   `json:"frozen_credits"`
	FrozenUnits                  int64   `json:"frozen_units"`
	UnderCollectedCredits        int64   `json:"under_collected_credits"`
	UnderCollectedUnits          int64   `json:"under_collected_units"`
	UnderCollectedRMB            float64 `json:"under_collected_rmb"`
	PlatformCostUnits            int64   `json:"platform_cost_units"`
	PlatformCostRMB              float64 `json:"platform_cost_rmb"`
	GrossProfitRMB               float64 `json:"gross_profit_rmb"`
	GrossProfitMargin            float64 `json:"gross_profit_margin"`
	ExpiredFreezeCount           int64   `json:"expired_freeze_count"`
	ExpiredFreezeCredits         int64   `json:"expired_freeze_credits"`
	ExpiredFreezeUnits           int64   `json:"expired_freeze_units"`
	NegativeBalanceUserCount     int64   `json:"negative_balance_user_count"`
	OpenFrozenCredits            int64   `json:"open_frozen_credits"`
	OpenFrozenUnits              int64   `json:"open_frozen_units"`
	OpenFrozenRecordCount        int64   `json:"open_frozen_record_count"`
	ReconciliationHealth         string  `gorm:"size:20;index" json:"reconciliation_health"`
	ReconciliationWarningReasons string  `gorm:"type:text" json:"reconciliation_warning_reasons,omitempty"`
}

func (BillingReconciliationSnapshot) TableName() string {
	return "billing_reconciliation_snapshots"
}
