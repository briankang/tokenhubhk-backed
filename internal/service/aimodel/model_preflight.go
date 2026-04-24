package aimodel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

const (
	PreflightSeverityError   = "error"
	PreflightSeverityWarning = "warning"
)

// ModelPreflightIssue describes one configuration problem found before a model is enabled.
type ModelPreflightIssue struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Field    string `json:"field,omitempty"`
	Message  string `json:"message"`
}

// ModelPreflightReport is the static enablement report shown to admins and used by Verify.
type ModelPreflightReport struct {
	ModelID             uint                  `json:"model_id"`
	ModelName           string                `json:"model_name"`
	ModelType           string                `json:"model_type"`
	SupplierID          uint                  `json:"supplier_id"`
	SupplierCode        string                `json:"supplier_code"`
	RequiredCapability  string                `json:"required_capability"`
	ImplementedEndpoint bool                  `json:"implemented_endpoint"`
	RouteCount          int                   `json:"route_count"`
	UsableChannelCount  int                   `json:"usable_channel_count"`
	CanEnable           bool                  `json:"can_enable"`
	Issues              []ModelPreflightIssue `json:"issues"`
	Warnings            []ModelPreflightIssue `json:"warnings"`
}

func (r *ModelPreflightReport) addIssue(severity, code, field, message string) {
	item := ModelPreflightIssue{Code: code, Severity: severity, Field: field, Message: message}
	if severity == PreflightSeverityWarning {
		r.Warnings = append(r.Warnings, item)
		return
	}
	r.Issues = append(r.Issues, item)
	r.CanEnable = false
}

// PreflightModelEnable performs fast static checks before a model is made visible.
func (s *AIModelService) PreflightModelEnable(ctx context.Context, id uint) (*ModelPreflightReport, error) {
	if id == 0 {
		return nil, fmt.Errorf("model id is required")
	}

	var aiModel model.AIModel
	if err := s.db.WithContext(ctx).Preload("Supplier").First(&aiModel, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("ai model not found")
		}
		return nil, fmt.Errorf("failed to get ai model: %w", err)
	}

	report := &ModelPreflightReport{
		ModelID:            aiModel.ID,
		ModelName:          aiModel.ModelName,
		ModelType:          aiModel.ModelType,
		SupplierID:         aiModel.SupplierID,
		SupplierCode:       aiModel.Supplier.Code,
		RequiredCapability: model.ModelTypeToCapability(aiModel.ModelType),
		CanEnable:          true,
	}

	if IsTemporaryModelName(aiModel.ModelName) {
		report.addIssue(PreflightSeverityError, "temporary_model", "model_name",
			"temporary or QA model names cannot be enabled")
	}
	if aiModel.SupplierID == 0 || aiModel.Supplier.ID == 0 {
		report.addIssue(PreflightSeverityError, "supplier_missing", "supplier_id",
			"model has no valid supplier")
		return report, nil
	}
	if !aiModel.Supplier.IsActive || aiModel.Supplier.Status != "active" {
		report.addIssue(PreflightSeverityError, "supplier_unavailable", "supplier",
			"supplier is inactive or not in active status")
	}
	if !isEndpointImplementedForModelType(aiModel.ModelType) {
		report.ImplementedEndpoint = false
		report.addIssue(PreflightSeverityError, "endpoint_not_implemented", "model_type",
			fmt.Sprintf("model type %q has no implemented public endpoint", aiModel.ModelType))
	} else {
		report.ImplementedEndpoint = true
	}

	channels, err := s.activeChannelsForSupplier(ctx, aiModel.SupplierID)
	if err != nil {
		return nil, err
	}
	report.UsableChannelCount = len(channels)
	if len(channels) == 0 {
		report.addIssue(PreflightSeverityError, "no_active_channel", "channels",
			"supplier has no active and verified channel")
	} else {
		report.checkChannels(channels)
	}

	report.RouteCount, err = s.routeCountForModel(ctx, aiModel.ModelName, channels)
	if err != nil {
		return nil, err
	}
	if report.RouteCount == 0 && len(channels) > 0 {
		report.addIssue(PreflightSeverityWarning, "no_explicit_route", "routes",
			"no explicit custom/channel model route found; calls will rely on provider default model id")
	}

	if err := s.checkParamMappings(ctx, report); err != nil {
		return nil, err
	}

	return report, nil
}

func (r *ModelPreflightReport) BlockerMessage() string {
	if r == nil || len(r.Issues) == 0 {
		return ""
	}
	parts := make([]string, 0, len(r.Issues))
	for _, issue := range r.Issues {
		parts = append(parts, issue.Code+": "+issue.Message)
	}
	return strings.Join(parts, "; ")
}

func (r *ModelPreflightReport) checkChannels(channels []model.Channel) {
	hasCapability := false
	for _, ch := range channels {
		if strings.TrimSpace(ch.Endpoint) == "" {
			r.addIssue(PreflightSeverityError, "channel_missing_endpoint", "channels.endpoint",
				fmt.Sprintf("channel %q has no endpoint", ch.Name))
			continue
		}
		if strings.TrimSpace(ch.APIKey) == "" {
			r.addIssue(PreflightSeverityError, "channel_missing_api_key", "channels.api_key",
				fmt.Sprintf("channel %q has no API key", ch.Name))
			continue
		}
		if ch.HasCapability(r.RequiredCapability) {
			hasCapability = true
		}
		if needsClientSecret(r.SupplierCode) && !jsonHasString(ch.CustomParams, "client_secret") {
			r.addIssue(PreflightSeverityError, "channel_missing_client_secret", "channels.custom_params.client_secret",
				fmt.Sprintf("channel %q requires custom_params.client_secret", ch.Name))
		}
	}
	if !hasCapability {
		r.addIssue(PreflightSeverityError, "channel_capability_mismatch", "channels.supported_capabilities",
			fmt.Sprintf("no active channel supports capability %q", r.RequiredCapability))
	}
}

func (s *AIModelService) activeChannelsForSupplier(ctx context.Context, supplierID uint) ([]model.Channel, error) {
	var channels []model.Channel
	err := s.db.WithContext(ctx).
		Where("supplier_id = ? AND status = ? AND verified = ?", supplierID, "active", true).
		Find(&channels).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list supplier channels: %w", err)
	}
	return channels, nil
}

func (s *AIModelService) routeCountForModel(ctx context.Context, modelName string, channels []model.Channel) (int, error) {
	if len(channels) == 0 {
		return 0, nil
	}
	channelIDs := make([]uint, 0, len(channels))
	for _, ch := range channels {
		channelIDs = append(channelIDs, ch.ID)
	}

	var customCount int64
	if err := s.db.WithContext(ctx).Model(&model.CustomChannelRoute{}).
		Where("alias_model = ? AND channel_id IN ? AND is_active = ?", modelName, channelIDs, true).
		Count(&customCount).Error; err != nil {
		return 0, fmt.Errorf("failed to count custom routes: %w", err)
	}
	var channelModelCount int64
	if err := s.db.WithContext(ctx).Model(&model.ChannelModel{}).
		Where("standard_model_id = ? AND channel_id IN ? AND is_active = ?", modelName, channelIDs, true).
		Count(&channelModelCount).Error; err != nil {
		return 0, fmt.Errorf("failed to count channel model routes: %w", err)
	}
	return int(customCount + channelModelCount), nil
}

func (s *AIModelService) checkParamMappings(ctx context.Context, report *ModelPreflightReport) error {
	if report.SupplierCode == "" {
		return nil
	}
	var activeParamCount int64
	if err := s.db.WithContext(ctx).Model(&model.PlatformParam{}).
		Where("is_active = ?", true).
		Count(&activeParamCount).Error; err != nil {
		return fmt.Errorf("failed to count platform params: %w", err)
	}
	if activeParamCount == 0 {
		return nil
	}
	var mappingCount int64
	if err := s.db.WithContext(ctx).Model(&model.SupplierParamMapping{}).
		Where("supplier_code = ?", report.SupplierCode).
		Count(&mappingCount).Error; err != nil {
		return fmt.Errorf("failed to count supplier param mappings: %w", err)
	}
	if mappingCount == 0 {
		report.addIssue(PreflightSeverityWarning, "missing_supplier_param_mappings", "supplier_param_mappings",
			"supplier has no parameter mappings; standard platform params may be passed through unchanged")
	}
	return nil
}

func isEndpointImplementedForModelType(modelType string) bool {
	switch strings.ToLower(strings.TrimSpace(modelType)) {
	case "", "llm", "vlm", "vision", "reasoning":
		return true
	case "imagegeneration", "videogeneration", "embedding", "tts", "asr", "speechsynthesis", "speechrecognition":
		return true
	default:
		return false
	}
}

func needsClientSecret(supplierCode string) bool {
	code := strings.ToLower(strings.TrimSpace(supplierCode))
	return strings.Contains(code, "wenxin")
}

func jsonHasString(raw model.JSON, key string) bool {
	if len(raw) == 0 {
		return false
	}
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return false
	}
	v, ok := data[key]
	if !ok {
		return false
	}
	return strings.TrimSpace(fmt.Sprint(v)) != ""
}
