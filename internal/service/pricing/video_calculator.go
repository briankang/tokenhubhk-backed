package pricing

import (
	"context"
	"encoding/json"
	"math"

	"tokenhub-server/internal/model"
)

// VideoUsageInput 视频生成专用用量输入
type VideoUsageInput struct {
	InputVideoSeconds  float64 // 输入视频时长（秒）
	OutputVideoSeconds float64 // 输出视频时长（秒）
	VideoWidth         int     // 输出视频宽度
	VideoHeight        int     // 输出视频高度
	VideoFPS           int     // 输出视频帧率
	IsDraft            bool    // 是否 Draft 模式（仅 480p）
	HasAudio           bool    // 是否有声（决定 Draft 系数）
	HasInputVideo      bool    // 是否传入视频（决定是否应用最低 token 下限）
}

// CalculateVideoCost 基于 VideoPricingConfig 与 Token 公式计算视频模型费用
//
// 流程：
//  1. tokens = (inputSec + outputSec) × width × height × fps / 1024
//  2. Draft 模式：tokens × DraftCoefSilent/Audio（仅 480p 且 SupportDraft=true）
//  3. 最低下限：有输入视频且 RequireInputVideo=true → tokens = max(tokens, MinTokens)
//  4. 调用 CalculateCost(inputTokens=0, outputTokens=tokens) 走阶梯/折扣链路
//
// 若 aiModel 未配置 VideoPricingConfig 或不是 VideoGeneration 类型，返回 nil, nil
// 由调用方继续走标准路径。
func (c *PricingCalculator) CalculateVideoCost(
	ctx context.Context,
	aiModel *model.AIModel,
	tenantID uint,
	agentLevel int,
	usage VideoUsageInput,
) (*CostResult, error) {
	if aiModel == nil || aiModel.ModelType != model.ModelTypeVideoGeneration {
		return nil, nil
	}
	cfg := parseVideoPricingConfig(aiModel.VideoPricingConfig)

	// 基础 tokens 计算
	base := (usage.InputVideoSeconds + usage.OutputVideoSeconds) *
		float64(usage.VideoWidth) * float64(usage.VideoHeight) * float64(usage.VideoFPS) / 1024.0

	// Draft 折算（仅 480p，高度 ≤ 480）
	if usage.IsDraft && cfg != nil && cfg.SupportDraft && usage.VideoHeight > 0 && usage.VideoHeight <= 480 {
		coef := cfg.DraftCoefSilent
		if usage.HasAudio {
			coef = cfg.DraftCoefAudio
		}
		if coef > 0 {
			base = base * coef
		}
	}

	// 最低 Token 下限（仅在有输入视频时）
	if cfg != nil && usage.HasInputVideo && cfg.RequireInputVideo {
		minTokens := cfg.LookupMinTokens(usage.OutputVideoSeconds)
		if float64(minTokens) > base {
			base = float64(minTokens)
		}
	}

	tokens := int64(math.Ceil(base))
	if tokens < 0 {
		tokens = 0
	}

	// 走 CalculateCost 链路（input=0, output=tokens）→ 自动享受阶梯/折扣
	return c.CalculateCost(ctx, aiModel.ID, tenantID, agentLevel, 0, int(tokens))
}

// parseVideoPricingConfig 从 JSON 解析 VideoPricingConfig
func parseVideoPricingConfig(raw model.JSON) *model.VideoPricingConfig {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var cfg model.VideoPricingConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil
	}
	return &cfg
}
