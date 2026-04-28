package modelapidoc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

type Service struct {
	db *gorm.DB
}

type ListOptions struct {
	SupplierCode string
	ModelName    string
	Keyword      string
	Locale       string
	Page         int
	PageSize     int
	Admin        bool
}

type PublicParamVerification struct {
	TokenHubParam      string `json:"tokenhub_param"`
	ProviderParam      string `json:"provider_param,omitempty"`
	ParamType          string `json:"param_type,omitempty"`
	Required           bool   `json:"required"`
	DefaultValue       string `json:"default_value,omitempty"`
	AllowedValues      string `json:"allowed_values,omitempty"`
	SupportStatus      string `json:"support_status"`
	VerificationStatus string `json:"verification_status"`
	PlatformBehavior   string `json:"platform_behavior,omitempty"`
	VerifiedAt         string `json:"verified_at,omitempty"`
}

type PublicDoc struct {
	ID                  uint                      `json:"id"`
	Slug                string                    `json:"slug"`
	Locale              string                    `json:"locale"`
	Title               string                    `json:"title"`
	Summary             string                    `json:"summary"`
	SupplierID          uint                      `json:"supplier_id"`
	SupplierCode        string                    `json:"supplier_code"`
	SupplierName        string                    `json:"supplier_name"`
	ModelID             *uint                     `json:"model_id,omitempty"`
	ModelName           string                    `json:"model_name"`
	ModelType           string                    `json:"model_type"`
	EndpointPath        string                    `json:"endpoint_path"`
	TokenHubAuth        string                    `json:"tokenhub_auth"`
	PublicOverview      string                    `json:"public_overview"`
	DeveloperGuide      string                    `json:"developer_guide"`
	CapabilityMatrix    json.RawMessage           `json:"capability_matrix,omitempty"`
	RequestSchema       json.RawMessage           `json:"request_schema,omitempty"`
	ResponseSchema      json.RawMessage           `json:"response_schema,omitempty"`
	StreamSchema        json.RawMessage           `json:"stream_schema,omitempty"`
	ParameterMappings   json.RawMessage           `json:"parameter_mappings,omitempty"`
	CodeExamples        json.RawMessage           `json:"code_examples,omitempty"`
	FAQs                json.RawMessage           `json:"faqs,omitempty"`
	VerificationSummary json.RawMessage           `json:"verification_summary,omitempty"`
	VerifiedAt          string                    `json:"verified_at,omitempty"`
	ParamVerifications  []PublicParamVerification `json:"param_verifications,omitempty"`
}

func New(db *gorm.DB) *Service {
	if db == nil {
		panic("modelapidoc: db is nil")
	}
	return &Service{db: db}
}

func (s *Service) ListPublic(ctx context.Context, opts ListOptions) ([]PublicDoc, int64, error) {
	opts.Locale = NormalizeLocale(opts.Locale)
	docs, total, err := s.list(ctx, opts)
	if err != nil {
		return nil, 0, err
	}
	if total == 0 && opts.Locale != "zh" {
		opts.Locale = "zh"
		docs, total, err = s.list(ctx, opts)
		if err != nil {
			return nil, 0, err
		}
	}
	out := make([]PublicDoc, 0, len(docs))
	for i := range docs {
		out = append(out, toPublicDoc(&docs[i], false))
	}
	return out, total, nil
}

func (s *Service) GetPublicBySlug(ctx context.Context, slug string) (*PublicDoc, error) {
	return s.GetPublicBySlugLocale(ctx, slug, "zh")
}

func (s *Service) GetPublicBySlugLocale(ctx context.Context, slug, locale string) (*PublicDoc, error) {
	if strings.TrimSpace(slug) == "" {
		return nil, fmt.Errorf("slug is required")
	}
	locale = NormalizeLocale(locale)
	var doc model.ModelAPIDoc
	err := s.db.WithContext(ctx).
		Preload("Supplier").
		Preload("Model").
		Preload("ParamVerifications", func(db *gorm.DB) *gorm.DB {
			return db.Order("required DESC, id ASC")
		}).
		Where("slug = ? AND locale = ? AND is_published = ? AND status = ?", slug, locale, true, model.ModelAPIDocStatusPublished).
		First(&doc).Error
	if err != nil && locale != "zh" {
		err = s.db.WithContext(ctx).
			Preload("Supplier").
			Preload("Model").
			Preload("ParamVerifications", func(db *gorm.DB) *gorm.DB {
				return db.Order("required DESC, id ASC")
			}).
			Where("slug = ? AND locale = ? AND is_published = ? AND status = ?", slug, "zh", true, model.ModelAPIDocStatusPublished).
			First(&doc).Error
	}
	if err != nil {
		return nil, err
	}
	res := toPublicDoc(&doc, true)
	return &res, nil
}

func (s *Service) ListAdmin(ctx context.Context, opts ListOptions) ([]model.ModelAPIDoc, int64, error) {
	opts.Admin = true
	return s.list(ctx, opts)
}

func (s *Service) GetAdminByID(ctx context.Context, id uint) (*model.ModelAPIDoc, error) {
	if id == 0 {
		return nil, fmt.Errorf("id is required")
	}
	var doc model.ModelAPIDoc
	err := s.db.WithContext(ctx).
		Preload("Supplier").
		Preload("Model").
		Preload("Sources", func(db *gorm.DB) *gorm.DB {
			return db.Order("id ASC")
		}).
		Preload("ParamVerifications", func(db *gorm.DB) *gorm.DB {
			return db.Order("required DESC, id ASC")
		}).
		First(&doc, id).Error
	if err != nil {
		return nil, err
	}
	return &doc, nil
}

func (s *Service) Create(ctx context.Context, doc *model.ModelAPIDoc) error {
	if doc == nil {
		return fmt.Errorf("doc is nil")
	}
	if strings.TrimSpace(doc.Slug) == "" || strings.TrimSpace(doc.Title) == "" {
		return fmt.Errorf("slug and title are required")
	}
	return s.db.WithContext(ctx).Create(doc).Error
}

func (s *Service) Update(ctx context.Context, id uint, updates map[string]interface{}) error {
	if id == 0 {
		return fmt.Errorf("id is required")
	}
	delete(updates, "id")
	delete(updates, "created_at")
	return s.db.WithContext(ctx).Model(&model.ModelAPIDoc{}).Where("id = ?", id).Updates(updates).Error
}

func (s *Service) UpsertSource(ctx context.Context, source *model.ModelAPIDocSource) error {
	if source == nil || source.DocID == 0 || source.SourceURL == "" {
		return fmt.Errorf("doc_id and source_url are required")
	}
	return s.db.WithContext(ctx).Create(source).Error
}

func (s *Service) UpsertParamVerification(ctx context.Context, item *model.ModelAPIParamVerification) error {
	if item == nil || item.DocID == 0 || item.TokenHubParam == "" {
		return fmt.Errorf("doc_id and tokenhub_param are required")
	}
	var existing model.ModelAPIParamVerification
	err := s.db.WithContext(ctx).
		Where("doc_id = ? AND token_hub_param = ?", item.DocID, item.TokenHubParam).
		First(&existing).Error
	if err == gorm.ErrRecordNotFound {
		return s.db.WithContext(ctx).Create(item).Error
	}
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Model(&model.ModelAPIParamVerification{}).
		Where("id = ?", existing.ID).
		Updates(map[string]interface{}{
			"provider_param":       item.ProviderParam,
			"param_type":           item.ParamType,
			"required":             item.Required,
			"default_value":        item.DefaultValue,
			"allowed_values":       item.AllowedValues,
			"support_status":       item.SupportStatus,
			"verification_status":  item.VerificationStatus,
			"platform_behavior":    item.PlatformBehavior,
			"test_payload_summary": item.TestPayloadSummary,
			"test_result_summary":  item.TestResultSummary,
			"verified_at":          item.VerifiedAt,
		}).Error
}

func (s *Service) list(ctx context.Context, opts ListOptions) ([]model.ModelAPIDoc, int64, error) {
	if opts.Page < 1 {
		opts.Page = 1
	}
	if opts.PageSize < 1 || opts.PageSize > 100 {
		opts.PageSize = 20
	}
	q := s.db.WithContext(ctx).Model(&model.ModelAPIDoc{}).
		Joins("LEFT JOIN suppliers ON suppliers.id = model_api_docs.supplier_id")
	if !opts.Admin {
		q = q.Where("model_api_docs.is_published = ? AND model_api_docs.status = ?", true, model.ModelAPIDocStatusPublished)
	}
	if opts.SupplierCode != "" {
		q = q.Where("suppliers.code = ?", opts.SupplierCode)
	}
	if opts.ModelName != "" {
		q = q.Where("model_api_docs.model_name = ?", opts.ModelName)
	}
	if opts.Locale != "" {
		q = q.Where("model_api_docs.locale = ?", NormalizeLocale(opts.Locale))
	}
	if opts.Keyword != "" {
		like := "%" + opts.Keyword + "%"
		q = q.Where("model_api_docs.title LIKE ? OR model_api_docs.summary LIKE ? OR model_api_docs.model_name LIKE ? OR suppliers.name LIKE ?", like, like, like, like)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var docs []model.ModelAPIDoc
	err := q.Preload("Supplier").
		Preload("Model").
		Order("model_api_docs.sort_order ASC, model_api_docs.id ASC").
		Offset((opts.Page - 1) * opts.PageSize).
		Limit(opts.PageSize).
		Find(&docs).Error
	return docs, total, err
}

func toPublicDoc(doc *model.ModelAPIDoc, includeParams bool) PublicDoc {
	res := PublicDoc{
		ID:                  doc.ID,
		Slug:                doc.Slug,
		Locale:              doc.Locale,
		Title:               doc.Title,
		Summary:             doc.Summary,
		SupplierID:          doc.SupplierID,
		SupplierCode:        doc.Supplier.Code,
		SupplierName:        doc.Supplier.Name,
		ModelID:             doc.ModelID,
		ModelName:           doc.ModelName,
		ModelType:           doc.ModelType,
		EndpointPath:        doc.EndpointPath,
		TokenHubAuth:        doc.TokenHubAuth,
		PublicOverview:      doc.PublicOverview,
		DeveloperGuide:      doc.DeveloperGuide,
		CapabilityMatrix:    raw(doc.CapabilityMatrix),
		RequestSchema:       raw(doc.RequestSchema),
		ResponseSchema:      raw(doc.ResponseSchema),
		StreamSchema:        raw(doc.StreamSchema),
		ParameterMappings:   raw(doc.ParameterMappings),
		CodeExamples:        raw(doc.CodeExamples),
		FAQs:                raw(doc.FAQs),
		VerificationSummary: raw(doc.VerificationSummary),
	}
	if doc.VerifiedAt != nil {
		res.VerifiedAt = doc.VerifiedAt.Format("2006-01-02T15:04:05Z07:00")
	}
	if includeParams {
		for _, item := range doc.ParamVerifications {
			p := PublicParamVerification{
				TokenHubParam:      item.TokenHubParam,
				ProviderParam:      item.ProviderParam,
				ParamType:          item.ParamType,
				Required:           item.Required,
				DefaultValue:       item.DefaultValue,
				AllowedValues:      item.AllowedValues,
				SupportStatus:      item.SupportStatus,
				VerificationStatus: item.VerificationStatus,
				PlatformBehavior:   item.PlatformBehavior,
			}
			if item.VerifiedAt != nil {
				p.VerifiedAt = item.VerifiedAt.Format("2006-01-02T15:04:05Z07:00")
			}
			res.ParamVerifications = append(res.ParamVerifications, p)
		}
	}
	return res
}

func raw(v model.JSON) json.RawMessage {
	if len(v) == 0 {
		return nil
	}
	return json.RawMessage(v)
}
