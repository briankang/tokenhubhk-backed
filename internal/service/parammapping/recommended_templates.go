package parammapping

import (
	"context"
	"fmt"
	"sort"

	"tokenhub-server/internal/model"

	"gorm.io/gorm"
)

type RecommendedTemplateItem struct {
	ParamName       string `json:"param_name"`
	VendorParamName string `json:"vendor_param_name"`
	TransformType   string `json:"transform_type"`
	TransformRule   string `json:"transform_rule,omitempty"`
	Supported       bool   `json:"supported"`
	Notes           string `json:"notes,omitempty"`
}

type RecommendedTemplateChange struct {
	SupplierCode    string `json:"supplier_code"`
	ParamID         uint   `json:"param_id"`
	ParamName       string `json:"param_name"`
	Action          string `json:"action"`
	VendorParamName string `json:"vendor_param_name"`
	TransformType   string `json:"transform_type"`
	Supported       bool   `json:"supported"`
	Notes           string `json:"notes,omitempty"`
}

type RecommendedTemplateResult struct {
	SupplierCode  string                      `json:"supplier_code,omitempty"`
	Overwrite     bool                        `json:"overwrite"`
	Applied       bool                        `json:"applied"`
	Created       int                         `json:"created"`
	Updated       int                         `json:"updated"`
	Skipped       int                         `json:"skipped"`
	MissingParams []string                    `json:"missing_params"`
	Changes       []RecommendedTemplateChange `json:"changes"`
}

var recommendedTemplateAliases = map[string]string{
	"azure_openai":    "openai",
	"google_gemini":   "gemini",
	"baidu_qianfan":   "baidu_wenxin",
	"wenxin":          "baidu_wenxin",
	"qwen":            "aliyun_dashscope",
	"zhipu_glm":       "zhipu",
	"moonshot_kimi":   "moonshot",
	"tencent_hunyuan": "hunyuan",
}

var commonOpenAICompatibleTemplate = []RecommendedTemplateItem{
	direct("temperature", "temperature", "OpenAI-compatible sampling temperature."),
	direct("top_p", "top_p", "OpenAI-compatible nucleus sampling."),
	direct("presence_penalty", "presence_penalty", "OpenAI-compatible presence penalty."),
	direct("frequency_penalty", "frequency_penalty", "OpenAI-compatible frequency penalty."),
	direct("response_format", "response_format", "OpenAI-compatible structured output."),
	unsupported("enable_search", "Provider does not expose a stable OpenAI-compatible search flag."),
}

var recommendedParamTemplates = map[string][]RecommendedTemplateItem{
	"openai": append([]RecommendedTemplateItem{
		direct("reasoning_effort", "reasoning_effort", "OpenAI reasoning models use reasoning_effort."),
		unsupported("enable_thinking", "OpenAI uses reasoning_effort instead of enable_thinking."),
		unsupported("thinking_budget", "OpenAI does not expose thinking_budget in Chat Completions."),
	}, commonOpenAICompatibleTemplate...),
	"anthropic": {
		nested("enable_thinking", "thinking", `{"when_true":{"type":"enabled","budget_tokens":1024},"when_false":null}`, "Anthropic uses a thinking object when extended thinking is enabled."),
		nested("thinking_budget", "thinking", `{"path":"thinking","field":"budget_tokens"}`, "Anthropic thinking budget is thinking.budget_tokens."),
		direct("temperature", "temperature", "Anthropic messages temperature."),
		direct("top_p", "top_p", "Anthropic messages top_p."),
		unsupported("reasoning_effort", "Anthropic uses thinking budget rather than reasoning_effort."),
		unsupported("response_format", "Anthropic Messages API has no response_format parameter compatible with OpenAI JSON mode."),
		unsupported("presence_penalty", "Anthropic Messages API does not support presence_penalty."),
		unsupported("frequency_penalty", "Anthropic Messages API does not support frequency_penalty."),
		unsupported("enable_search", "Anthropic web search is configured through tools, not a flat enable_search parameter."),
	},
	"gemini": {
		nested("thinking_budget", "generationConfig", `{"path":"generationConfig","field":"thinkingConfig"}`, "Gemini native APIs use generationConfig.thinkingConfig; review manually if using OpenAI-compatible endpoint."),
		direct("temperature", "temperature", "Gemini OpenAI-compatible sampling temperature."),
		direct("top_p", "top_p", "Gemini OpenAI-compatible nucleus sampling."),
		direct("response_format", "response_format", "Gemini OpenAI-compatible structured output."),
		unsupported("enable_thinking", "Gemini thinking is budget/config driven, not a stable enable_thinking boolean across endpoints."),
		unsupported("reasoning_effort", "Gemini does not expose OpenAI reasoning_effort."),
		unsupported("presence_penalty", "Gemini compatibility varies; keep disabled unless verified."),
		unsupported("frequency_penalty", "Gemini compatibility varies; keep disabled unless verified."),
		unsupported("enable_search", "Gemini grounding/search requires tools configuration."),
	},
	"aliyun_dashscope": {
		direct("enable_thinking", "enable_thinking", "Qwen compatible mode supports enable_thinking for thinking models."),
		direct("thinking_budget", "thinking_budget", "Qwen compatible mode supports thinking_budget on supported thinking models."),
		direct("enable_search", "enable_search", "DashScope compatible mode supports enable_search on supported models."),
		direct("temperature", "temperature", "DashScope OpenAI-compatible sampling temperature."),
		direct("top_p", "top_p", "DashScope OpenAI-compatible nucleus sampling."),
		direct("presence_penalty", "presence_penalty", "DashScope OpenAI-compatible presence penalty where supported."),
		direct("frequency_penalty", "frequency_penalty", "DashScope OpenAI-compatible frequency penalty where supported."),
		direct("response_format", "response_format", "DashScope OpenAI-compatible structured output."),
		unsupported("reasoning_effort", "Qwen uses enable_thinking/thinking_budget rather than OpenAI reasoning_effort."),
	},
	"deepseek": append([]RecommendedTemplateItem{
		unsupported("enable_thinking", "DeepSeek reasoning is model-selected rather than controlled with enable_thinking."),
		unsupported("thinking_budget", "DeepSeek API does not expose thinking_budget."),
		unsupported("reasoning_effort", "DeepSeek API does not expose OpenAI reasoning_effort."),
	}, commonOpenAICompatibleTemplate...),
	"baidu_wenxin": append([]RecommendedTemplateItem{
		unsupported("enable_thinking", "Qianfan thinking controls differ by model and endpoint; keep disabled until verified."),
		unsupported("thinking_budget", "Qianfan does not expose a stable thinking_budget field across models."),
		unsupported("reasoning_effort", "Qianfan does not expose OpenAI reasoning_effort."),
	}, commonOpenAICompatibleTemplate...),
	"volcengine": append([]RecommendedTemplateItem{
		unsupported("enable_thinking", "Volcengine Ark thinking controls vary by endpoint/model."),
		unsupported("thinking_budget", "Volcengine Ark does not expose a stable thinking_budget field across models."),
		unsupported("reasoning_effort", "Volcengine Ark does not expose OpenAI reasoning_effort for all models."),
	}, commonOpenAICompatibleTemplate...),
	"zhipu": append([]RecommendedTemplateItem{
		unsupported("enable_thinking", "Zhipu reasoning/search controls are model-specific."),
		unsupported("thinking_budget", "Zhipu does not expose a stable thinking_budget field."),
		unsupported("reasoning_effort", "Zhipu does not expose OpenAI reasoning_effort."),
	}, commonOpenAICompatibleTemplate...),
	"moonshot": append([]RecommendedTemplateItem{
		unsupported("enable_thinking", "Moonshot does not expose enable_thinking."),
		unsupported("thinking_budget", "Moonshot does not expose thinking_budget."),
		unsupported("reasoning_effort", "Moonshot does not expose OpenAI reasoning_effort."),
	}, commonOpenAICompatibleTemplate...),
	"hunyuan": append([]RecommendedTemplateItem{
		unsupported("enable_thinking", "Hunyuan thinking controls are model-specific."),
		unsupported("thinking_budget", "Hunyuan does not expose a stable thinking_budget field."),
		unsupported("reasoning_effort", "Hunyuan does not expose OpenAI reasoning_effort."),
	}, commonOpenAICompatibleTemplate...),
}

func direct(paramName, vendorName, notes string) RecommendedTemplateItem {
	return RecommendedTemplateItem{ParamName: paramName, VendorParamName: vendorName, TransformType: "direct", Supported: true, Notes: notes}
}

func nested(paramName, vendorName, rule, notes string) RecommendedTemplateItem {
	return RecommendedTemplateItem{ParamName: paramName, VendorParamName: vendorName, TransformType: "nested", TransformRule: rule, Supported: true, Notes: notes}
}

func unsupported(paramName, notes string) RecommendedTemplateItem {
	return RecommendedTemplateItem{ParamName: paramName, VendorParamName: paramName, TransformType: "none", Supported: false, Notes: notes}
}

func canonicalTemplateCode(supplierCode string) string {
	if v, ok := recommendedTemplateAliases[supplierCode]; ok {
		return v
	}
	return supplierCode
}

func RecommendedTemplateSupplierCodes() []string {
	codes := make([]string, 0, len(recommendedParamTemplates))
	for code := range recommendedParamTemplates {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}

func (s *ParamMappingService) PreviewRecommendedTemplate(ctx context.Context, supplierCode string, overwrite bool) (*RecommendedTemplateResult, error) {
	return s.buildRecommendedTemplateResult(ctx, supplierCode, overwrite)
}

func (s *ParamMappingService) ApplyRecommendedTemplate(ctx context.Context, supplierCode string, overwrite bool) (*RecommendedTemplateResult, error) {
	result, err := s.buildRecommendedTemplateResult(ctx, supplierCode, overwrite)
	if err != nil {
		return nil, err
	}
	if len(result.Changes) == 0 {
		result.Applied = true
		return result, nil
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, change := range result.Changes {
			mapping := model.SupplierParamMapping{
				PlatformParamID: change.ParamID,
				SupplierCode:    supplierCode,
				VendorParamName: change.VendorParamName,
				TransformType:   change.TransformType,
				Supported:       change.Supported,
				Notes:           change.Notes,
			}
			for _, tpl := range recommendedParamTemplates[canonicalTemplateCode(supplierCode)] {
				if tpl.ParamName == change.ParamName {
					mapping.TransformRule = tpl.TransformRule
					break
				}
			}
			if change.Action == "create" {
				supported := mapping.Supported
				if err := tx.Create(&mapping).Error; err != nil {
					return err
				}
				if !supported {
					if err := tx.Model(&model.SupplierParamMapping{}).Where("id = ?", mapping.ID).Update("supported", false).Error; err != nil {
						return err
					}
				}
				continue
			}
			if change.Action == "update" {
				if err := tx.Model(&model.SupplierParamMapping{}).
					Where("platform_param_id = ? AND supplier_code = ?", change.ParamID, supplierCode).
					Updates(map[string]interface{}{
						"vendor_param_name": mapping.VendorParamName,
						"transform_type":    mapping.TransformType,
						"transform_rule":    mapping.TransformRule,
						"supported":         mapping.Supported,
						"notes":             mapping.Notes,
					}).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.invalidateCache()
	result.Applied = true
	return result, nil
}

func (s *ParamMappingService) buildRecommendedTemplateResult(ctx context.Context, supplierCode string, overwrite bool) (*RecommendedTemplateResult, error) {
	templateCode := canonicalTemplateCode(supplierCode)
	template, ok := recommendedParamTemplates[templateCode]
	if !ok {
		return nil, fmt.Errorf("no recommended mapping template for supplier %s", supplierCode)
	}
	result := &RecommendedTemplateResult{SupplierCode: supplierCode, Overwrite: overwrite, MissingParams: make([]string, 0), Changes: make([]RecommendedTemplateChange, 0)}

	var params []model.PlatformParam
	if err := s.db.WithContext(ctx).Where("is_active = ?", true).Find(&params).Error; err != nil {
		return nil, err
	}
	paramByName := make(map[string]model.PlatformParam, len(params))
	for _, p := range params {
		paramByName[p.ParamName] = p
	}

	var existing []model.SupplierParamMapping
	if err := s.db.WithContext(ctx).Where("supplier_code = ?", supplierCode).Find(&existing).Error; err != nil {
		return nil, err
	}
	existingByParamID := make(map[uint]model.SupplierParamMapping, len(existing))
	for _, m := range existing {
		existingByParamID[m.PlatformParamID] = m
	}

	for _, item := range template {
		p, ok := paramByName[item.ParamName]
		if !ok {
			result.Skipped++
			result.MissingParams = append(result.MissingParams, item.ParamName)
			continue
		}
		action := "create"
		if _, ok := existingByParamID[p.ID]; ok {
			if !overwrite {
				result.Skipped++
				continue
			}
			action = "update"
		}
		if action == "create" {
			result.Created++
		} else {
			result.Updated++
		}
		result.Changes = append(result.Changes, RecommendedTemplateChange{
			SupplierCode:    supplierCode,
			ParamID:         p.ID,
			ParamName:       p.ParamName,
			Action:          action,
			VendorParamName: item.VendorParamName,
			TransformType:   item.TransformType,
			Supported:       item.Supported,
			Notes:           item.Notes,
		})
	}
	sort.Strings(result.MissingParams)
	return result, nil
}
