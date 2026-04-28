package model

// ModelDimensionConfig 模型级业务维度声明（可扩展性核心，2026-04-28）
//
// 背景：
//
//	不同 ModelType 的"业务维度"差异巨大：
//	  VideoGeneration: resolution × input_has_video × inference_mode × audio_mode × is_draft
//	  ImageGeneration: size × quality × style
//	  LLM:             context_tier × thinking_mode × cache_hit_kind
//	  TTS:             voice_kind × emotion (预留)
//	  ASR:             grade × stream_mode (预留)
//
//	旧设计：每种 model_type 一个独立 PricingForm 组件，硬编码维度。
//	新设计：模型级声明 DimensionConfig，前后端 schema-driven 渲染矩阵。
//
// 用法：
//
//	1. AIModel.DimensionConfig 字段（JSON）声明该模型支持的维度
//	2. AIModel.PriceTiers 中每个 tier 的 DimValues 必须使用 DimensionConfig 中的 key
//	3. 请求侧 UsageInput.Dimensions 用 key→value map 传维度
//	4. 计费匹配（pricing/tier_calculator.go selectPriceForTokens）按 dims 优先命中
//	5. 前端按 DimensionConfig 自动渲染 N 维矩阵编辑器（不再需要 7 个独立 form）
//
// 兼容性：
//
//	DimensionConfig 为空 → 模型走旧路径（按 token 区间 + variant 字段），无影响。
//	DimensionConfig 非空 → tier 必须用 DimValues 表达；缺失 dim 的请求落到 fallback 档。
type ModelDimensionConfig struct {
	// SchemaVersion 用于未来 schema 演进（v1 起步）
	SchemaVersion int `json:"schema_version,omitempty"`

	// Dimensions 维度列表，按显示顺序排列
	Dimensions []DimensionDefinition `json:"dimensions,omitempty"`

	// RequiredKeys 必填维度键列表
	//   - 请求侧 dims 中必须包含 RequiredKeys 中所有的键，否则记为 unsupported（兜底走最大档）
	//   - 默认所有 Dimensions 中的键都是必填，可在此处显式收窄
	RequiredKeys []string `json:"required_keys,omitempty"`
}

// DimensionDefinition 单个维度的声明
type DimensionDefinition struct {
	// Key 维度键（snake_case），与 PriceTier.DimValues 的键、UsageInput.Dimensions 的键对齐
	Key string `json:"key"`

	// Label 展示名（中文）
	Label string `json:"label,omitempty"`

	// Help tooltip 帮助文案
	Help string `json:"help,omitempty"`

	// Type 维度类型：select / boolean
	//   - select: Values 列出可选项（字符串）
	//   - boolean: 隐含 Values=["true","false"]
	Type string `json:"type"`

	// Values 可选值列表（type=select 时必填，type=boolean 时省略）
	Values []string `json:"values,omitempty"`

	// Default 默认值（前端下拉默认显示，请求未传时用此值，nil=无默认）
	Default *string `json:"default,omitempty"`
}

// DimensionType 常量
const (
	DimensionTypeSelect  = "select"
	DimensionTypeBoolean = "boolean"
)

// 常用维度键常量（便于 handler/scraper 引用，避免拼写错误）
const (
	DimKeyResolution     = "resolution"      // 分辨率：480p/720p/1080p/2k/4k
	DimKeyInputHasVideo  = "input_has_video" // 是否含输入视频：true/false
	DimKeyInferenceMode  = "inference_mode"  // 推理模式：online/offline (flex)
	DimKeyAudioMode      = "audio_mode"      // 是否生成音频：true/false
	DimKeyDraftMode      = "draft_mode"      // 是否 Draft 样片：true/false
	DimKeyContextTier    = "context_tier"    // 上下文档：0-32k/32k-128k/128k-256k/256k-1M
	DimKeyThinkingMode   = "thinking_mode"   // 思考模式：true/false
	DimKeyCacheHitKind   = "cache_hit_kind"  // 缓存命中类型：miss/read/write_5min/write_1h
	DimKeyImageSize      = "image_size"      // 图片尺寸：512x512/1024x1024/2048x2048
	DimKeyImageQuality   = "image_quality"   // 图片质量：standard/hd
	DimKeyVoiceKind      = "voice_kind"      // TTS 音色：standard/clone/expert (预留)
	DimKeyAsrGrade       = "asr_grade"       // ASR 等级：standard/hd
)

// VideoGenerationDefaultDimensions 视频生成模型默认维度模板（可被覆盖）
//
// 包含所有 Seedance 系列已知维度：分辨率 / 是否含输入视频 / 在线-离线 / 有声-无声 / Draft
// 具体模型（如 1.0-pro 只有 inference_mode）只需声明它实际支持的子集。
func VideoGenerationDefaultDimensions() []DimensionDefinition {
	return []DimensionDefinition{
		{
			Key: DimKeyResolution, Label: "分辨率", Type: DimensionTypeSelect,
			Values: []string{"480p", "720p", "1080p"},
			Default: stringPtr("720p"),
			Help:    "Seedance 2.0 / wan2.7-t2v 等支持多档分辨率定价；价格随分辨率递增",
		},
		{
			Key: DimKeyInputHasVideo, Label: "输入是否含视频", Type: DimensionTypeBoolean,
			Default: stringPtr("false"),
			Help:    "Seedance 2.0：含视频输入时单价更低（输入视频已含部分上下文，计算资源减少）",
		},
		{
			Key: DimKeyInferenceMode, Label: "推理模式", Type: DimensionTypeSelect,
			Values: []string{"online", "offline"},
			Default: stringPtr("online"),
			Help:    "Seedance 1.0/1.5 系列支持离线推理（service_tier=flex），价格通常半价",
		},
		{
			Key: DimKeyAudioMode, Label: "是否生成音频", Type: DimensionTypeBoolean,
			Default: stringPtr("true"),
			Help:    "Seedance 1.5 Pro：含音频时按全价计费；无音频时半价",
		},
		{
			Key: DimKeyDraftMode, Label: "Draft 样片模式", Type: DimensionTypeBoolean,
			Default: stringPtr("false"),
			Help:    "Seedance 1.5 Pro 仅 480p 支持，token 折算 0.6 (有声) / 0.7 (无声)",
		},
	}
}

// LLMDefaultDimensions LLM 模型默认维度模板（上下文 + 思考 + 缓存）
func LLMDefaultDimensions() []DimensionDefinition {
	return []DimensionDefinition{
		{
			Key: DimKeyContextTier, Label: "上下文档位", Type: DimensionTypeSelect,
			Values: []string{"0-32k", "32k-128k", "128k-256k", "256k-1M"},
			Default: stringPtr("0-32k"),
			Help:    "按输入 tokens 区间分阶梯定价（doubao-pro/qwen3-max 等）",
		},
		{
			Key: DimKeyThinkingMode, Label: "思考模式", Type: DimensionTypeBoolean,
			Default: stringPtr("false"),
			Help:    "qwen3.5-flash / qwq / qvq 等推理模型；部分模型思考价显著高于非思考价",
		},
		{
			Key: DimKeyCacheHitKind, Label: "缓存命中类型", Type: DimensionTypeSelect,
			Values: []string{"miss", "read", "write_5min", "write_1h"},
			Default: stringPtr("miss"),
			Help:    "Anthropic 显式缓存 / 阿里云双模式缓存 / OpenAI 隐式缓存",
		},
	}
}

// ImageGenerationDefaultDimensions 图像生成默认维度（预留，当前所有模型按张计费）
func ImageGenerationDefaultDimensions() []DimensionDefinition {
	return []DimensionDefinition{
		{
			Key: DimKeyImageSize, Label: "图片尺寸", Type: DimensionTypeSelect,
			Values: []string{"512x512", "1024x1024", "2048x2048"},
			Default: stringPtr("1024x1024"),
			Help:    "OpenAI DALL-E / 通义万相高清版按尺寸分档定价（预留扩展点）",
		},
		{
			Key: DimKeyImageQuality, Label: "图片质量", Type: DimensionTypeSelect,
			Values: []string{"standard", "hd"},
			Default: stringPtr("standard"),
			Help:    "DALL-E 3 hd 质量价格 ≈ standard 的 2 倍",
		},
	}
}

// stringPtr 返回字符串指针（避免到处写 &"x"）
func stringPtr(s string) *string {
	return &s
}

// HasDimension 检查 config 中是否声明了某维度
func (c *ModelDimensionConfig) HasDimension(key string) bool {
	if c == nil {
		return false
	}
	for _, d := range c.Dimensions {
		if d.Key == key {
			return true
		}
	}
	return false
}

// GetDimension 按键查找维度定义；未找到返回 nil
func (c *ModelDimensionConfig) GetDimension(key string) *DimensionDefinition {
	if c == nil {
		return nil
	}
	for i := range c.Dimensions {
		if c.Dimensions[i].Key == key {
			return &c.Dimensions[i]
		}
	}
	return nil
}

// ApplyDefaults 用 DimensionConfig 中各维度的 Default 值补齐 dims 中缺失的键
//
// 用途：handler 层从请求中提取 dims 后调用，把可选维度填上默认值。
// 例如 Seedance 1.0 用户只传 resolution，audio_mode/inference_mode 走默认。
func (c *ModelDimensionConfig) ApplyDefaults(dims map[string]string) map[string]string {
	if c == nil {
		return dims
	}
	if dims == nil {
		dims = make(map[string]string, len(c.Dimensions))
	}
	for _, d := range c.Dimensions {
		if _, has := dims[d.Key]; has {
			continue
		}
		if d.Default != nil {
			dims[d.Key] = *d.Default
		}
	}
	return dims
}
