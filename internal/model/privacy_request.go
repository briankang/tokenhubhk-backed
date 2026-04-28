package model

import "time"

const (
	PrivacyRequestExportData      = "export_data"
	PrivacyRequestDeleteAccount   = "delete_account"
	PrivacyRequestDeleteAPILogs   = "delete_api_logs"
	PrivacyRequestMarketingOptOut = "marketing_opt_out"
)

const (
	PrivacyRequestStatusReceived   = "received"
	PrivacyRequestStatusVerify     = "identity_verification_required"
	PrivacyRequestStatusInReview   = "in_review"
	PrivacyRequestStatusProcessing = "processing"
	PrivacyRequestStatusCompleted  = "completed"
	PrivacyRequestStatusRejected   = "rejected"
	PrivacyRequestStatusCancelled  = "cancelled"
)

// PrivacyRequest stores data-subject and privacy operations requests.
type PrivacyRequest struct {
	BaseModel

	UserID   uint   `gorm:"index;not null" json:"user_id"`
	Email    string `gorm:"type:varchar(255);index" json:"email"`
	Type     string `gorm:"type:varchar(40);not null;index" json:"type"`
	Status   string `gorm:"type:varchar(40);not null;default:'received';index" json:"status"`
	Region   string `gorm:"type:varchar(16);index" json:"region"`
	Language string `gorm:"type:varchar(16)" json:"language"`
	Reason   string `gorm:"type:text" json:"reason,omitempty"`
	Scope    string `gorm:"type:varchar(64)" json:"scope,omitempty"`
	Metadata JSON   `gorm:"type:json" json:"metadata,omitempty"`

	AssignedTo uint       `gorm:"index" json:"assigned_to,omitempty"`
	VerifiedAt *time.Time `json:"verified_at,omitempty"`
	DueAt      *time.Time `gorm:"index" json:"due_at,omitempty"`
	ClosedAt   *time.Time `gorm:"index" json:"closed_at,omitempty"`

	ResolutionNote string `gorm:"type:text" json:"resolution_note,omitempty"`
	RejectReason   string `gorm:"type:text" json:"reject_reason,omitempty"`
}

func (PrivacyRequest) TableName() string {
	return "privacy_requests"
}
