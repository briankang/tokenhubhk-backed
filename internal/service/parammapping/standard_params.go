package parammapping

import (
	"context"
	"sort"

	"tokenhub-server/internal/model"

	"gorm.io/gorm"
)

type StandardParamDefinition struct {
	ParamName    string `json:"param_name"`
	ParamType    string `json:"param_type"`
	DisplayName  string `json:"display_name"`
	Description  string `json:"description"`
	DefaultValue string `json:"default_value"`
	Category     string `json:"category"`
	SortOrder    int    `json:"sort_order"`
}

type StandardParamChange struct {
	ParamID   uint                    `json:"param_id,omitempty"`
	ParamName string                  `json:"param_name"`
	Action    string                  `json:"action"`
	Param     StandardParamDefinition `json:"param"`
}

type StandardParamEnsureResult struct {
	Overwrite bool                  `json:"overwrite"`
	Applied   bool                  `json:"applied"`
	Created   int                   `json:"created"`
	Updated   int                   `json:"updated"`
	Skipped   int                   `json:"skipped"`
	Changes   []StandardParamChange `json:"changes"`
}

var standardParamDefinitions = []StandardParamDefinition{
	{
		ParamName:    "enable_thinking",
		ParamType:    "bool",
		DisplayName:  "启用深度思考",
		Description:  "开启模型的深度思考/推理模式，适用于复杂逻辑、数学和代码任务。",
		DefaultValue: "false",
		Category:     "thinking",
		SortOrder:    10,
	},
	{
		ParamName:    "thinking_budget",
		ParamType:    "int",
		DisplayName:  "思考 Token 预算",
		Description:  "深度思考模式下允许使用的最大思考 Token 数。",
		DefaultValue: "1024",
		Category:     "thinking",
		SortOrder:    20,
	},
	{
		ParamName:    "reasoning_effort",
		ParamType:    "string",
		DisplayName:  "推理强度",
		Description:  "OpenAI 风格推理努力程度，可选 low / medium / high。",
		DefaultValue: "medium",
		Category:     "thinking",
		SortOrder:    30,
	},
	{
		ParamName:    "enable_search",
		ParamType:    "bool",
		DisplayName:  "启用联网搜索",
		Description:  "允许模型回答时进行联网搜索或调用供应商搜索能力。",
		DefaultValue: "false",
		Category:     "search",
		SortOrder:    40,
	},
	{
		ParamName:    "response_format",
		ParamType:    "json",
		DisplayName:  "响应格式",
		Description:  `指定输出格式，例如 {"type":"json_object"} 强制 JSON 输出。`,
		DefaultValue: "",
		Category:     "format",
		SortOrder:    50,
	},
	{
		ParamName:    "temperature",
		ParamType:    "float",
		DisplayName:  "温度",
		Description:  "控制输出随机性，值越高越发散。",
		DefaultValue: "1",
		Category:     "sampling",
		SortOrder:    60,
	},
	{
		ParamName:    "top_p",
		ParamType:    "float",
		DisplayName:  "Top P",
		Description:  "核采样阈值，控制候选 token 的累计概率范围。",
		DefaultValue: "1",
		Category:     "sampling",
		SortOrder:    70,
	},
	{
		ParamName:    "presence_penalty",
		ParamType:    "float",
		DisplayName:  "存在惩罚",
		Description:  "降低已出现主题继续出现的概率，鼓励引入新内容。",
		DefaultValue: "0",
		Category:     "penalty",
		SortOrder:    80,
	},
	{
		ParamName:    "frequency_penalty",
		ParamType:    "float",
		DisplayName:  "频率惩罚",
		Description:  "降低重复 token 的概率，减少复读。",
		DefaultValue: "0",
		Category:     "penalty",
		SortOrder:    90,
	},
	{
		ParamName:    "top_k",
		ParamType:    "int",
		DisplayName:  "Top K",
		Description:  "限制采样时参与候选的 token 数，常见于 Gemini、Claude 和部分国产模型。",
		DefaultValue: "",
		Category:     "sampling",
		SortOrder:    100,
	},
	{
		ParamName:    "seed",
		ParamType:    "int",
		DisplayName:  "随机种子",
		Description:  "用于尽量获得可复现输出，供应商支持程度不同。",
		DefaultValue: "",
		Category:     "sampling",
		SortOrder:    110,
	},
	{
		ParamName:    "stop",
		ParamType:    "json",
		DisplayName:  "停止序列",
		Description:  "模型生成遇到指定字符串或字符串数组时停止。",
		DefaultValue: "",
		Category:     "format",
		SortOrder:    120,
	},
}

func (s *ParamMappingService) PreviewStandardParamDefinitions(ctx context.Context, overwrite bool) (*StandardParamEnsureResult, error) {
	return s.buildStandardParamEnsureResult(ctx, overwrite)
}

func (s *ParamMappingService) EnsureStandardParamDefinitions(ctx context.Context, overwrite bool) (*StandardParamEnsureResult, error) {
	result, err := s.buildStandardParamEnsureResult(ctx, overwrite)
	if err != nil {
		return nil, err
	}
	if len(result.Changes) == 0 {
		result.Applied = true
		return result, nil
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, change := range result.Changes {
			param := model.PlatformParam{
				ParamName:    change.Param.ParamName,
				ParamType:    change.Param.ParamType,
				DisplayName:  change.Param.DisplayName,
				Description:  change.Param.Description,
				DefaultValue: change.Param.DefaultValue,
				Category:     change.Param.Category,
				SortOrder:    change.Param.SortOrder,
				IsActive:     true,
			}
			if change.Action == "create" {
				if err := tx.Create(&param).Error; err != nil {
					return err
				}
				continue
			}
			if change.Action == "update" {
				if err := tx.Model(&model.PlatformParam{}).
					Where("id = ?", change.ParamID).
					Updates(map[string]interface{}{
						"param_type":    param.ParamType,
						"display_name":  param.DisplayName,
						"description":   param.Description,
						"default_value": param.DefaultValue,
						"category":      param.Category,
						"sort_order":    param.SortOrder,
						"is_active":     true,
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

func (s *ParamMappingService) buildStandardParamEnsureResult(ctx context.Context, overwrite bool) (*StandardParamEnsureResult, error) {
	result := &StandardParamEnsureResult{Overwrite: overwrite, Changes: make([]StandardParamChange, 0)}
	var existing []model.PlatformParam
	if err := s.db.WithContext(ctx).Find(&existing).Error; err != nil {
		return nil, err
	}
	existingByName := make(map[string]model.PlatformParam, len(existing))
	for _, p := range existing {
		existingByName[p.ParamName] = p
	}
	for _, def := range standardParamDefinitions {
		if p, ok := existingByName[def.ParamName]; ok {
			if !overwrite {
				result.Skipped++
				continue
			}
			result.Updated++
			result.Changes = append(result.Changes, StandardParamChange{ParamID: p.ID, ParamName: def.ParamName, Action: "update", Param: def})
			continue
		}
		result.Created++
		result.Changes = append(result.Changes, StandardParamChange{ParamName: def.ParamName, Action: "create", Param: def})
	}
	sort.SliceStable(result.Changes, func(i, j int) bool {
		return result.Changes[i].Param.SortOrder < result.Changes[j].Param.SortOrder
	})
	return result, nil
}
