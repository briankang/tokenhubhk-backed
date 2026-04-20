package model

// VideoPricingConfig 视频生成模型特殊计价配置
// 仅适用于 ModelType=VideoGeneration 的模型（如 Seedance 1.5 Pro / 2.0 / 2.0 Fast）
//
// Token 计算公式：
//   tokens = (input_video_seconds + output_video_seconds) × width × height × fps / 1024
//
// Draft 模式（仅 480p）：tokens × draft_coefficient
//   - Seedance 1.5 Pro: silent=0.7, audio=0.6
//   - 其他模型不支持，传 IsDraft=true 时回退正常公式
//
// 最低 Token 下限（仅 Seedance 2.0 / 2.0 Fast，仅在有输入视频时）：
//   按 OutputDurationSec 查表，若计算结果低于对应门槛则取门槛值。
type VideoPricingConfig struct {
	// ---- Draft 配置（Seedance 1.5 Pro）----
	SupportDraft    bool    `json:"support_draft"`                 // 是否支持 Draft 模式（仅 480p）
	DraftCoefSilent float64 `json:"draft_coef_silent,omitempty"`   // 无声 Draft 折算系数（0=不支持）
	DraftCoefAudio  float64 `json:"draft_coef_audio,omitempty"`    // 有声 Draft 折算系数（0=不支持）

	// ---- 最低 Token 下限（Seedance 2.0 / 2.0 Fast）----
	RequireInputVideo bool                `json:"require_input_video,omitempty"` // true=仅在有输入视频时应用下限
	MinTokensRules    []VideoMinTokenRule `json:"min_tokens_rules,omitempty"`    // 按输出时长查表

	// ---- 多档价格参考（前端展示用，计费走 AIModel.PriceTiers）----
	HasInputVideoPrice  float64 `json:"has_input_video_price,omitempty"` // 有输入视频时的参考价（元/百万token）
	NoInputVideoPrice   float64 `json:"no_input_video_price,omitempty"`  // 无输入视频时的参考价（元/百万token）
}

// VideoMinTokenRule 视频模型最低 Token 用量规则
// 按输出视频时长查表，取第一个 >= OutputDurationSec 的规则
type VideoMinTokenRule struct {
	OutputDurationSec float64 `json:"output_duration_sec"` // 输出时长（秒）
	MinTokens         int64   `json:"min_tokens"`          // 最低 token 数
}

// LookupMinTokens 查找输出时长对应的最低 token 数
// 返回首个 rule.OutputDurationSec >= outputSec 的 MinTokens；未命中返回 0（无下限）
func (c *VideoPricingConfig) LookupMinTokens(outputSec float64) int64 {
	if c == nil {
		return 0
	}
	for _, rule := range c.MinTokensRules {
		if outputSec <= rule.OutputDurationSec {
			return rule.MinTokens
		}
	}
	return 0
}
