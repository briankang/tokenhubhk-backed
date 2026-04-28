package admin

// 价格计算器场景预设（B3 任务）。
//
// 设计目标：让运营在「模型抽屉 → 价格 Tab → 价格计算器试算」面板顶部，
// 按模型类型一键切换常用负载场景（如 LLM 短问答 / 缓存命中 / 阶梯越界），
// 配合 calculate-preview 端点快速核对计费正确性。
//
// 维护原则：
//   1. 仅维护「id + 适用 model_types + 预设 params」，显示文本由前端 i18n 翻译
//      （key: priceCalculator.scenarios.{id}.{name|description}），后端默认中文作为 fallback
//   2. 新增场景时仅改本文件 + 三份 i18n；前端按钮组自动渲染
//   3. params 字段名与 calculate-preview 入参对齐，确保 UI 直接复用试算面板字段渲染逻辑

import (
	"strings"

	"github.com/gin-gonic/gin"
	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/response"
)

// ScenarioPreset 单个场景预设
type ScenarioPreset struct {
	ID             string                 `json:"id"`
	Name           string                 `json:"name"`            // 默认中文显示名（前端 i18n 优先）
	Description    string                 `json:"description"`     // 默认中文说明
	ModelTypes     []string               `json:"model_types"`     // 适用模型类型
	PricingUnits   []string               `json:"pricing_units"`   // 适用计费单位（用于跨类型筛选）
	Params         map[string]interface{} `json:"params"`          // 预设入参（与 calculate-preview 对齐）
	EnableThinking *bool                  `json:"enable_thinking,omitempty"`
}

// scenarioCatalog 全部内置场景，按 model_type 维度组织
func scenarioCatalog() []ScenarioPreset {
	boolPtr := func(b bool) *bool { return &b }

	return []ScenarioPreset{
		// ==================== LLM ====================
		{
			ID:           "llm_short_qa",
			Name:         "短问答",
			Description:  "客服 / FAQ 类对话，input ≈ 200 tokens，output ≈ 300 tokens",
			ModelTypes:   []string{model.ModelTypeLLM, model.ModelTypeVision},
			PricingUnits: []string{model.UnitPerMillionTokens},
			Params: map[string]interface{}{
				"input_tokens":  200,
				"output_tokens": 300,
			},
		},
		{
			ID:           "llm_long_context",
			Name:         "长上下文对话",
			Description:  "文档问答 / 代码分析，input ≈ 8000 tokens，output ≈ 1000 tokens",
			ModelTypes:   []string{model.ModelTypeLLM, model.ModelTypeVision},
			PricingUnits: []string{model.UnitPerMillionTokens},
			Params: map[string]interface{}{
				"input_tokens":  8000,
				"output_tokens": 1000,
			},
		},
		{
			ID:           "llm_cache_hit_80",
			Name:         "缓存命中 80%",
			Description:  "RAG / 重复 system prompt 场景，cache_read 占输入 80%",
			ModelTypes:   []string{model.ModelTypeLLM, model.ModelTypeVision},
			PricingUnits: []string{model.UnitPerMillionTokens},
			Params: map[string]interface{}{
				"input_tokens":      8000,
				"cache_read_tokens": 6400,
				"output_tokens":     500,
			},
		},
		{
			ID:           "llm_cache_first_write",
			Name:         "首次缓存写入",
			Description:  "RAG 首次建立索引，整段输入按 cache_write 计费",
			ModelTypes:   []string{model.ModelTypeLLM, model.ModelTypeVision},
			PricingUnits: []string{model.UnitPerMillionTokens},
			Params: map[string]interface{}{
				"input_tokens":       8000,
				"cache_write_tokens": 8000,
				"output_tokens":      500,
			},
		},
		{
			ID:           "llm_tier_boundary",
			Name:         "阶梯越界",
			Description:  "input ≥ 200K，触发第二阶梯（适用阶梯定价模型）",
			ModelTypes:   []string{model.ModelTypeLLM, model.ModelTypeVision},
			PricingUnits: []string{model.UnitPerMillionTokens},
			Params: map[string]interface{}{
				"input_tokens":  200000,
				"output_tokens": 2000,
			},
		},

		// ==================== Reasoning LLM ====================
		// 通过 features.supports_thinking=true 的模型自动适配；前端在 model_type=LLM 时
		// 也会展示这两个场景，由 UI 根据 features 决定是否显示
		{
			ID:           "reasoning_thinking_normal",
			Name:         "推理（开启思考）",
			Description:  "o1 / DeepSeek-R1 / QwQ 默认思考模式",
			ModelTypes:   []string{model.ModelTypeLLM},
			PricingUnits: []string{model.UnitPerMillionTokens},
			Params: map[string]interface{}{
				"input_tokens":    500,
				"output_tokens":   500,
				"thinking_tokens": 2000,
			},
			EnableThinking: boolPtr(true),
		},
		{
			ID:           "reasoning_thinking_off",
			Name:         "推理（关闭思考）",
			Description:  "qwen3 系列关闭思考，验证非思考路径计费",
			ModelTypes:   []string{model.ModelTypeLLM},
			PricingUnits: []string{model.UnitPerMillionTokens},
			Params: map[string]interface{}{
				"input_tokens":  500,
				"output_tokens": 500,
			},
			EnableThinking: boolPtr(false),
		},

		// ==================== Vision ====================
		{
			ID:           "vision_single_image",
			Name:         "单图理解",
			Description:  "OCR / 图像问答，1 张图 + 1000 input tokens",
			ModelTypes:   []string{model.ModelTypeVision},
			PricingUnits: []string{model.UnitPerImage, model.UnitPerMillionTokens},
			Params: map[string]interface{}{
				"image_count":   1,
				"resolution":    "high",
				"input_tokens":  1000,
				"output_tokens": 200,
			},
		},

		// ==================== Embedding ====================
		{
			ID:           "embedding_single_doc",
			Name:         "单文档向量化",
			Description:  "知识库入库，input ≈ 512 tokens",
			ModelTypes:   []string{model.ModelTypeEmbedding},
			PricingUnits: []string{model.UnitPerMillionTokens},
			Params: map[string]interface{}{
				"input_tokens":  512,
				"output_tokens": 0,
			},
		},
		{
			ID:           "embedding_batch",
			Name:         "批量向量化",
			Description:  "批处理 100 条文档 ≈ 50K tokens",
			ModelTypes:   []string{model.ModelTypeEmbedding},
			PricingUnits: []string{model.UnitPerMillionTokens},
			Params: map[string]interface{}{
				"input_tokens":  50000,
				"output_tokens": 0,
			},
		},

		// ==================== ImageGeneration ====================
		{
			ID:           "image_standard_1024",
			Name:         "标准图（1024×1024）",
			Description:  "文生图，标准质量 1 张",
			ModelTypes:   []string{model.ModelTypeImageGeneration},
			PricingUnits: []string{model.UnitPerImage},
			Params: map[string]interface{}{
				"image_count": 1,
				"resolution":  "1024x1024",
				"quality":     "standard",
				"mode":        "generation",
			},
		},
		{
			ID:           "image_hd_2048_quad",
			Name:         "高清图（2048×2048×4 张）",
			Description:  "高清四宫格生成",
			ModelTypes:   []string{model.ModelTypeImageGeneration},
			PricingUnits: []string{model.UnitPerImage},
			Params: map[string]interface{}{
				"image_count": 4,
				"resolution":  "2048x2048",
				"quality":     "hd",
				"mode":        "generation",
			},
		},

		// ==================== VideoGeneration ====================
		{
			ID:           "video_5s_720p",
			Name:         "短视频 5s / 720p",
			Description:  "Seedance 标准短视频生成",
			ModelTypes:   []string{model.ModelTypeVideoGeneration},
			PricingUnits: []string{model.UnitPerMillionTokens, model.UnitPerSecond},
			Params: map[string]interface{}{
				"output_seconds": 5,
				"resolution":     "720p",
				"fps":            24,
			},
		},
		{
			ID:           "video_10s_1080p",
			Name:         "长视频 10s / 1080p",
			Description:  "高清长视频生成",
			ModelTypes:   []string{model.ModelTypeVideoGeneration},
			PricingUnits: []string{model.UnitPerMillionTokens, model.UnitPerSecond},
			Params: map[string]interface{}{
				"output_seconds": 10,
				"resolution":     "1080p",
				"fps":            30,
			},
		},

		// ==================== TTS / SpeechSynthesis ====================
		{
			ID:           "tts_short_zh_100",
			Name:         "中文短句（100 字）",
			Description:  "语音通知 / 客服话术",
			ModelTypes:   []string{model.ModelTypeTTS, "SpeechSynthesis"},
			PricingUnits: []string{model.UnitPer10kCharacters, model.UnitPerMillionCharacters, model.UnitPerKChars},
			Params: map[string]interface{}{
				"char_count": 100,
			},
		},
		{
			ID:           "tts_long_zh_100k",
			Name:         "长文（10 万字）",
			Description:  "有声书 / 大段播报",
			ModelTypes:   []string{model.ModelTypeTTS, "SpeechSynthesis"},
			PricingUnits: []string{model.UnitPer10kCharacters, model.UnitPerMillionCharacters, model.UnitPerKChars},
			Params: map[string]interface{}{
				"char_count": 100000,
			},
		},

		// ==================== ASR / SpeechRecognition ====================
		{
			ID:           "asr_call_1min",
			Name:         "1 分钟通话",
			Description:  "短通话识别",
			ModelTypes:   []string{model.ModelTypeASR, "SpeechRecognition"},
			PricingUnits: []string{model.UnitPerSecond, model.UnitPerMinute, model.UnitPerHour},
			Params: map[string]interface{}{
				"duration_seconds": 60,
			},
		},
		{
			ID:           "asr_recording_1h",
			Name:         "1 小时录音",
			Description:  "长录音转写",
			ModelTypes:   []string{model.ModelTypeASR, "SpeechRecognition"},
			PricingUnits: []string{model.UnitPerSecond, model.UnitPerMinute, model.UnitPerHour},
			Params: map[string]interface{}{
				"duration_seconds": 3600,
			},
		},

		// ==================== Rerank ====================
		{
			ID:           "rerank_single_batch",
			Name:         "单批 10 文档",
			Description:  "Rerank 一组文档（10 个候选）",
			ModelTypes:   []string{model.ModelTypeRerank},
			PricingUnits: []string{model.UnitPerCall, model.UnitPerMillionTokens},
			Params: map[string]interface{}{
				"call_count":   1,
				"input_tokens": 2000,
			},
		},
	}
}

// ListScenarios GET /admin/model-ops/scenarios?model_type=LLM&pricing_unit=per_million_tokens
//
// 查询参数：
//   - model_type 可选，按模型类型过滤；不传则返回全部
//   - pricing_unit 可选，进一步按计费单位过滤（用于同一 model_type 下细分场景，如 ImageGen 既有 per_image 也可能 per_call）
//
// 响应：{ code:0, data: { items: ScenarioPreset[], total: int } }
func (h *ModelOpsHandler) ListScenarios(c *gin.Context) {
	modelType := strings.TrimSpace(c.Query("model_type"))
	pricingUnit := strings.TrimSpace(c.Query("pricing_unit"))

	all := scenarioCatalog()
	filtered := make([]ScenarioPreset, 0, len(all))
	for _, s := range all {
		if modelType != "" && !containsString(s.ModelTypes, modelType) {
			continue
		}
		if pricingUnit != "" && len(s.PricingUnits) > 0 && !containsString(s.PricingUnits, pricingUnit) {
			continue
		}
		filtered = append(filtered, s)
	}

	response.Success(c, gin.H{
		"items": filtered,
		"total": len(filtered),
	})
}

