package model

const (
	DataCollectionNone             = "none"
	DataCollectionAbuseMonitoring  = "abuse_monitoring"
	DataCollectionTrainingPossible = "training_possible"
	DataCollectionUnknown          = "unknown"
)

// ProviderComplianceProfile describes retention, routing, and privacy traits
// for a supplier or channel. It is separate from pricing data so compliance
// routing can evolve without rewriting billing models.
type ProviderComplianceProfile struct {
	BaseModel

	SupplierID uint `gorm:"index" json:"supplier_id,omitempty"`
	ChannelID  uint `gorm:"index" json:"channel_id,omitempty"`

	DataCollectionPolicy string `gorm:"type:varchar(40);not null;default:'unknown';index" json:"data_collection_policy"`
	SupportsZDR          bool   `gorm:"default:false;index" json:"supports_zdr"`
	RetentionDays        int    `gorm:"default:-1;index" json:"retention_days"`
	ProcessingRegions    JSON   `gorm:"type:json" json:"processing_regions,omitempty"`
	SupportsEURouting    bool   `gorm:"default:false;index" json:"supports_eu_routing"`
	SubprocessorType     string `gorm:"type:varchar(40);index" json:"subprocessor_type"`
	DPAAvailable         bool   `gorm:"default:false" json:"dpa_available"`
	SensitiveDataAllowed bool   `gorm:"default:false" json:"sensitive_data_allowed"`

	PolicyURL    string `gorm:"type:varchar(500)" json:"policy_url,omitempty"`
	DPAURL       string `gorm:"type:varchar(500)" json:"dpa_url,omitempty"`
	LastReviewed string `gorm:"type:varchar(32);index" json:"last_reviewed,omitempty"`
	Notes        string `gorm:"type:text" json:"notes,omitempty"`
}

func (ProviderComplianceProfile) TableName() string {
	return "provider_compliance_profiles"
}
