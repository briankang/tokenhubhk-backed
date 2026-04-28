package model

import "time"

const (
	ModelAPIDocStatusDraft     = "draft"
	ModelAPIDocStatusPublished = "published"

	ParamSupportOfficialConfirmed = "official_confirmed"
	ParamSupportPlatformMapped    = "platform_mapped"
	ParamSupportRuntimeVerified   = "runtime_verified"
	ParamSupportUnsupported       = "unsupported"
)

// ModelAPIDoc stores developer-facing API documentation for one TokenHubHK model.
type ModelAPIDoc struct {
	BaseModel
	SupplierID uint  `gorm:"index;not null" json:"supplier_id"`
	ModelID    *uint `gorm:"index" json:"model_id,omitempty"`

	Slug        string `gorm:"type:varchar(200);not null;uniqueIndex:uidx_model_api_doc_slug_locale" json:"slug"`
	Locale      string `gorm:"type:varchar(10);not null;default:'zh';uniqueIndex:uidx_model_api_doc_slug_locale;index" json:"locale"`
	Title       string `gorm:"type:varchar(255);not null" json:"title"`
	Summary     string `gorm:"type:varchar(800)" json:"summary"`
	ModelName   string `gorm:"type:varchar(150);index" json:"model_name"`
	ModelType   string `gorm:"type:varchar(50);default:'LLM';index" json:"model_type"`
	SortOrder   int    `gorm:"default:0" json:"sort_order"`
	Status      string `gorm:"type:varchar(20);default:'draft';index" json:"status"`
	IsPublished bool   `gorm:"default:false;index" json:"is_published"`

	EndpointPath        string     `gorm:"type:varchar(200);default:'/v1/chat/completions'" json:"endpoint_path"`
	TokenHubAuth        string     `gorm:"type:varchar(300);default:'Authorization: Bearer sk-your-tokenhubhk-key'" json:"tokenhub_auth"`
	PublicOverview      string     `gorm:"type:longtext" json:"public_overview"`
	DeveloperGuide      string     `gorm:"type:longtext" json:"developer_guide"`
	CapabilityMatrix    JSON       `gorm:"type:json" json:"capability_matrix,omitempty"`
	RequestSchema       JSON       `gorm:"type:json" json:"request_schema,omitempty"`
	ResponseSchema      JSON       `gorm:"type:json" json:"response_schema,omitempty"`
	StreamSchema        JSON       `gorm:"type:json" json:"stream_schema,omitempty"`
	ParameterMappings   JSON       `gorm:"type:json" json:"parameter_mappings,omitempty"`
	CodeExamples        JSON       `gorm:"type:json" json:"code_examples,omitempty"`
	FAQs                JSON       `gorm:"column:faqs;type:json" json:"faqs,omitempty"`
	VerificationSummary JSON       `gorm:"type:json" json:"verification_summary,omitempty"`
	VerifiedAt          *time.Time `json:"verified_at,omitempty"`

	Supplier           Supplier                    `gorm:"foreignKey:SupplierID" json:"supplier,omitempty"`
	Model              *AIModel                    `gorm:"foreignKey:ModelID" json:"model,omitempty"`
	Sources            []ModelAPIDocSource         `gorm:"foreignKey:DocID" json:"sources,omitempty"`
	ParamVerifications []ModelAPIParamVerification `gorm:"foreignKey:DocID" json:"param_verifications,omitempty"`
}

func (ModelAPIDoc) TableName() string { return "model_api_docs" }

// ModelAPIDocSource stores official upstream references. It is only returned by admin APIs.
type ModelAPIDocSource struct {
	BaseModel
	DocID               uint       `gorm:"index;not null" json:"doc_id"`
	ProviderName        string     `gorm:"type:varchar(100);index" json:"provider_name"`
	SourceTitle         string     `gorm:"type:varchar(300);not null" json:"source_title"`
	SourceURL           string     `gorm:"type:varchar(800);not null" json:"source_url"`
	OriginalEndpoint    string     `gorm:"type:varchar(500)" json:"original_endpoint,omitempty"`
	OriginalAuthSummary string     `gorm:"type:varchar(800)" json:"original_auth_summary,omitempty"`
	CheckedAt           *time.Time `json:"checked_at,omitempty"`
	VerificationStatus  string     `gorm:"type:varchar(40);default:'official_confirmed';index" json:"verification_status"`
	AdminNotes          string     `gorm:"type:text" json:"admin_notes,omitempty"`
}

func (ModelAPIDocSource) TableName() string { return "model_api_doc_sources" }

// ModelAPIParamVerification stores parameter support and runtime validation status.
type ModelAPIParamVerification struct {
	BaseModel
	DocID              uint       `gorm:"index;not null" json:"doc_id"`
	TokenHubParam      string     `gorm:"type:varchar(120);not null;index" json:"tokenhub_param"`
	ProviderParam      string     `gorm:"type:varchar(200)" json:"provider_param,omitempty"`
	ParamType          string     `gorm:"type:varchar(30)" json:"param_type,omitempty"`
	Required           bool       `gorm:"default:false" json:"required"`
	DefaultValue       string     `gorm:"type:varchar(500)" json:"default_value,omitempty"`
	AllowedValues      string     `gorm:"type:varchar(800)" json:"allowed_values,omitempty"`
	SupportStatus      string     `gorm:"type:varchar(40);not null;default:'official_confirmed';index" json:"support_status"`
	VerificationStatus string     `gorm:"type:varchar(40);not null;default:'official_confirmed';index" json:"verification_status"`
	PlatformBehavior   string     `gorm:"type:varchar(800)" json:"platform_behavior,omitempty"`
	TestPayloadSummary string     `gorm:"type:text" json:"test_payload_summary,omitempty"`
	TestResultSummary  string     `gorm:"type:text" json:"test_result_summary,omitempty"`
	VerifiedAt         *time.Time `json:"verified_at,omitempty"`
}

func (ModelAPIParamVerification) TableName() string { return "model_api_param_verifications" }
