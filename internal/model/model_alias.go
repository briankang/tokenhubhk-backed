package model

import "time"

const (
	ModelAliasTypeStable        = "stable"
	ModelAliasTypeLatest        = "latest"
	ModelAliasTypeSnapshotShort = "snapshot_short"
	ModelAliasTypeCompat        = "compat"
	ModelAliasTypeCustom        = "custom"

	ModelAliasResolutionFixed          = "fixed"
	ModelAliasResolutionLatestByFamily = "latest_by_family"
	ModelAliasResolutionManual         = "manual"

	ModelAliasSourceManual      = "manual"
	ModelAliasSourceProvider    = "provider_list"
	ModelAliasSourceDocs        = "docs"
	ModelAliasSourceDeprecation = "deprecation"
	ModelAliasSourceRule        = "rule"
)

// ModelAlias stores platform-level model alias metadata.
// Supplier APIs usually return model IDs only, so alias relationships are
// maintained by TokenHub and can be reviewed separately from channel routes.
type ModelAlias struct {
	BaseModel
	AliasName          string     `gorm:"type:varchar(128);not null;index:idx_model_alias_lookup,priority:1;index" json:"alias_name"`
	TargetModelName    string     `gorm:"type:varchar(128);not null;index" json:"target_model_name"`
	SupplierID         uint       `gorm:"default:0;index:idx_model_alias_lookup,priority:2;index" json:"supplier_id"`
	AliasType          string     `gorm:"type:varchar(32);default:'custom';index" json:"alias_type"`
	ResolutionStrategy string     `gorm:"type:varchar(32);default:'fixed'" json:"resolution_strategy"`
	IsPublic           bool       `gorm:"default:true;index" json:"is_public"`
	IsActive           bool       `gorm:"default:true;index" json:"is_active"`
	Source             string     `gorm:"type:varchar(32);default:'manual';index" json:"source"`
	Confidence         float64    `gorm:"type:decimal(5,4);default:1.0" json:"confidence"`
	Notes              string     `gorm:"type:text CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci" json:"notes,omitempty"`
	EffectiveFrom      *time.Time `json:"effective_from,omitempty"`
	EffectiveUntil     *time.Time `json:"effective_until,omitempty"`
	LastResolvedAt     *time.Time `json:"last_resolved_at,omitempty"`

	Supplier Supplier `gorm:"foreignKey:SupplierID" json:"supplier,omitempty"`
}

func (ModelAlias) TableName() string {
	return "model_aliases"
}
