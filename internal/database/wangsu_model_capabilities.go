package database

// WangsuModelCapability 网宿网关代理模型的官网权威元数据
//
// **价格来源原则（2026-04-22 重写）**：
//
//	只采用三家官方发布的 USD 价，不使用任何第三方或推测价。
//	- OpenAI:    https://openai.com/api/pricing/
//	- Anthropic: https://www.anthropic.com/pricing#api
//	- Google:    https://ai.google.dev/pricing
//
// 网宿命名差异（Wangsu-only 命名如 gpt-5.4 / claude-opus-4-7 / gemini-3-pro-preview
// 官方未发布或未命名的版本）：我们将其显式映射到最接近的官方已发布同层级（primary_tier），
// 并在 MappedFrom 字段注明映射来源。这样价格仍然严格等于某个真实官网价，不引入推测。
//
// 成本价公式：
//
//	cost_RMB/M = official_USD/M × USDCNYSnapshot × discount
//	对于阶梯模型：上述计算应用到每个阶梯档位
//
// 折扣（Wangsu 合同含税系数）：
//   - GPT     → 0.795
//   - Claude  → 0.848
//   - Gemini  → 0.795（文本/图像），0.848（视频，本期未用到）
type WangsuModelCapability struct {
	ModelName       string // 网宿请求体中使用的真 model 值（带前缀如 "anthropic.xxx"）
	DisplayName     string
	Family          string // "gpt" / "claude" / "gemini"
	ModelType       string // LLM / VLM / Reasoning
	ContextWindow   int
	MaxOutputTokens int
	Discount        float64

	// MappedFrom 对于 Wangsu-only 命名的模型，标注对应的官方模型（用于价格透明度和审计）
	MappedFrom string

	// 平台默认（非阶梯）价格。阶梯模型应保留这两项为阶梯一价格以便兼容默认查询
	InputUSDPerM             float64
	OutputUSDPerM            float64
	CacheReadUSDPerM         float64
	CacheExplicitReadUSDPerM float64
	CacheWriteUSDPerM        float64
	CacheStorageUSDPerMHour  float64

	// 阶梯价（nil 表示无阶梯）。按 MaxInputTokens 升序，最后一档 MaxInputTokens=0 表示无上限
	PriceTiersUSD []PriceTierUSD

	SupportsVision       bool
	SupportsFunctionCall bool
	SupportsJSONMode     bool
	SupportsThinking     bool
	SupportsWebSearch    bool
	SupportsCache        bool
	CacheMechanism       string // auto / explicit / none
	CacheMinTokens       int
	RequiresStream       bool
	InputModalities      []string
	OutputModalities     []string

	Tags string
}

// PriceTierUSD 阶梯价档位（USD/百万 tokens）
type PriceTierUSD struct {
	Label             string // 展示名，如 "<=200K" / ">200K"
	MaxInputTokens    int    // 此档位适用的 prompt 长度上限；0 表示无上限
	InputUSDPerM      float64
	OutputUSDPerM     float64
	CacheReadUSDPerM  float64
	CacheWriteUSDPerM float64
}

// USDCNYSnapshot 汇率快照（2026-04-22）
// 规避每日波动对历史扣费的影响；由管理员在 /admin → 汇率配置 可进一步覆盖为动态查询
const USDCNYSnapshot = 7.10

// wangsuModels 网宿代理模型清单
//
// 列表更新记录：
//
//	2026-04-22 初版（基于测试期推测）
//	2026-04-22 权威化改版：全量替换为 OpenAI/Anthropic/Google 官网价，补充阶梯定价
var wangsuModels = []WangsuModelCapability{

	// ════════════════════ OpenAI GPT 家族 ════════════════════
	// 官网：https://openai.com/api/pricing/
	// 2025 年 OpenAI 官方 API 价格表（截至 2025-10 最新）

	// --- gpt-4o-mini (official) ---
	{
		ModelName: "gpt-4o-mini", DisplayName: "GPT-4o mini", Family: "gpt", ModelType: "LLM",
		ContextWindow: 128000, MaxOutputTokens: 16384, Discount: 0.795,
		InputUSDPerM: 0.15, OutputUSDPerM: 0.60, CacheReadUSDPerM: 0.075,
		SupportsVision: true, SupportsFunctionCall: true, SupportsJSONMode: true,
		SupportsCache: true, CacheMechanism: "auto", CacheMinTokens: 1024,
		Tags: "Wangsu,OpenAI",
	},
	// --- gpt-4 legacy (official) ---
	{
		ModelName: "gpt-4", DisplayName: "GPT-4", Family: "gpt", ModelType: "LLM",
		ContextWindow: 8192, MaxOutputTokens: 8192, Discount: 0.795,
		InputUSDPerM: 30.0, OutputUSDPerM: 60.0,
		SupportsFunctionCall: true, SupportsJSONMode: true,
		Tags: "Wangsu,OpenAI,Legacy",
	},
	// --- gpt-4.1 (official) ---
	{
		ModelName: "gpt-4.1", DisplayName: "GPT-4.1", Family: "gpt", ModelType: "LLM",
		ContextWindow: 1047576, MaxOutputTokens: 32768, Discount: 0.795,
		InputUSDPerM: 2.00, OutputUSDPerM: 8.00, CacheReadUSDPerM: 0.50,
		SupportsVision: true, SupportsFunctionCall: true, SupportsJSONMode: true,
		SupportsCache: true, CacheMechanism: "auto", CacheMinTokens: 1024,
		Tags: "Wangsu,OpenAI",
	},
	// --- gpt-4.1-mini (official) ---
	{
		ModelName: "gpt-4.1-mini", DisplayName: "GPT-4.1 mini", Family: "gpt", ModelType: "LLM",
		ContextWindow: 1047576, MaxOutputTokens: 32768, Discount: 0.795,
		InputUSDPerM: 0.40, OutputUSDPerM: 1.60, CacheReadUSDPerM: 0.10,
		SupportsVision: true, SupportsFunctionCall: true, SupportsJSONMode: true,
		SupportsCache: true, CacheMechanism: "auto", CacheMinTokens: 1024,
		Tags: "Wangsu,OpenAI",
	},
	// --- gpt-5 (official, 2025-08) ---
	{
		ModelName: "gpt-5", DisplayName: "GPT-5", Family: "gpt", ModelType: "Reasoning",
		ContextWindow: 272000, MaxOutputTokens: 32768, Discount: 0.795,
		InputUSDPerM: 1.25, OutputUSDPerM: 10.00, CacheReadUSDPerM: 0.125,
		SupportsVision: true, SupportsFunctionCall: true, SupportsJSONMode: true,
		SupportsThinking: true, SupportsCache: true, CacheMechanism: "auto", CacheMinTokens: 1024,
		Tags: "Wangsu,OpenAI,Reasoning",
	},
	// --- gpt-5.1-chat-latest → 映射 gpt-5 chat 价格（OpenAI 将 5.x chat-latest 纳入 gpt-5 定价档） ---
	{
		ModelName: "gpt-5.1-chat-latest", DisplayName: "GPT-5.1 Chat Latest", Family: "gpt", ModelType: "LLM",
		MappedFrom:    "gpt-5 (official chat tier)",
		ContextWindow: 272000, MaxOutputTokens: 32768, Discount: 0.795,
		InputUSDPerM: 1.25, OutputUSDPerM: 10.00, CacheReadUSDPerM: 0.125,
		SupportsVision: true, SupportsFunctionCall: true, SupportsJSONMode: true,
		SupportsCache: true, CacheMechanism: "auto", CacheMinTokens: 1024,
		Tags: "Wangsu,OpenAI",
	},
	// --- gpt-5.2 → gpt-5 官方价 ---
	{
		ModelName: "gpt-5.2", DisplayName: "GPT-5.2", Family: "gpt", ModelType: "Reasoning",
		MappedFrom:    "gpt-5 (official, Wangsu 自有命名)",
		ContextWindow: 272000, MaxOutputTokens: 32768, Discount: 0.795,
		InputUSDPerM: 1.25, OutputUSDPerM: 10.00, CacheReadUSDPerM: 0.125,
		SupportsVision: true, SupportsFunctionCall: true, SupportsJSONMode: true,
		SupportsThinking: true, SupportsCache: true, CacheMechanism: "auto", CacheMinTokens: 1024,
		Tags: "Wangsu,OpenAI,Reasoning",
	},
	// --- gpt-5.3-codex → OpenAI codex 系列与 gpt-5 同档 ---
	{
		ModelName: "gpt-5.3-codex", DisplayName: "GPT-5.3 Codex", Family: "gpt", ModelType: "Reasoning",
		MappedFrom:    "gpt-5 (Codex 同档，Wangsu 自有命名)",
		ContextWindow: 272000, MaxOutputTokens: 32768, Discount: 0.795,
		InputUSDPerM: 1.25, OutputUSDPerM: 10.00, CacheReadUSDPerM: 0.125,
		SupportsFunctionCall: true, SupportsJSONMode: true, SupportsThinking: true,
		SupportsCache: true, CacheMechanism: "auto", CacheMinTokens: 1024, RequiresStream: true,
		Tags: "Wangsu,OpenAI,Reasoning,Coding",
	},
	// --- gpt-5.4 → gpt-5 官方价（Wangsu-only 版本号） ---
	{
		ModelName: "gpt-5.4", DisplayName: "GPT-5.4", Family: "gpt", ModelType: "Reasoning",
		MappedFrom:    "gpt-5 (official, Wangsu 自有命名)",
		ContextWindow: 272000, MaxOutputTokens: 32768, Discount: 0.795,
		InputUSDPerM: 1.25, OutputUSDPerM: 10.00, CacheReadUSDPerM: 0.125,
		SupportsVision: true, SupportsFunctionCall: true, SupportsJSONMode: true,
		SupportsThinking: true, SupportsCache: true, CacheMechanism: "auto", CacheMinTokens: 1024,
		Tags: "Wangsu,OpenAI,Reasoning",
	},
	// --- gpt-5.4-mini → gpt-5-mini (official $0.25/$2, cache $0.025) ---
	{
		ModelName: "gpt-5.4-mini", DisplayName: "GPT-5.4 mini", Family: "gpt", ModelType: "Reasoning",
		MappedFrom:    "gpt-5-mini (official, Wangsu 自有命名)",
		ContextWindow: 272000, MaxOutputTokens: 32768, Discount: 0.795,
		InputUSDPerM: 0.25, OutputUSDPerM: 2.00, CacheReadUSDPerM: 0.025,
		SupportsVision: true, SupportsFunctionCall: true, SupportsJSONMode: true,
		SupportsThinking: true, SupportsCache: true, CacheMechanism: "auto", CacheMinTokens: 1024,
		Tags: "Wangsu,OpenAI,Reasoning",
	},

	// ════════════════════ Anthropic Claude 家族 ════════════════════
	// 官网：https://platform.claude.com/docs/en/about-claude/pricing
	// 截至 2026-04，Claude 4.5+ 官方 API 价格：
	//   - Haiku 4.5:  $1 / $5，cache read $0.10，cache write 5m $1.25，1h $2.00
	//   - Sonnet 4.5/4.6:  $3 / $15，cache read $0.30，cache write 5m $3.75，1h $6.00
	//   - Opus 4.5/4.6/4.7: $5 / $25，cache read $0.50，cache write 5m $6.25，1h $10.00
	// 1M context 在 Sonnet 4.6、Opus 4.6、Opus 4.7 为标准价格；Sonnet 4.5 默认仍按 200K 管理。

	// --- claude-haiku-4-5 (official) ---
	{
		ModelName: "anthropic.claude-haiku-4-5", DisplayName: "Claude Haiku 4.5", Family: "claude", ModelType: "LLM",
		ContextWindow: 200000, MaxOutputTokens: 64000, Discount: 0.848,
		InputUSDPerM: 1.00, OutputUSDPerM: 5.00,
		CacheReadUSDPerM: 0.10, CacheWriteUSDPerM: 1.25,
		SupportsVision: true, SupportsFunctionCall: true,
		SupportsCache: true, CacheMechanism: "explicit", CacheMinTokens: 4096,
		Tags: "Wangsu,Anthropic",
	},
	// --- claude-sonnet-4-5 (official) ---
	{
		ModelName: "anthropic.claude-sonnet-4-5", DisplayName: "Claude Sonnet 4.5", Family: "claude", ModelType: "LLM",
		ContextWindow: 200000, MaxOutputTokens: 64000, Discount: 0.848,
		InputUSDPerM: 3.00, OutputUSDPerM: 15.00,
		CacheReadUSDPerM: 0.30, CacheWriteUSDPerM: 3.75,
		SupportsVision: true, SupportsFunctionCall: true, SupportsThinking: true,
		SupportsCache: true, CacheMechanism: "explicit", CacheMinTokens: 1024,
		Tags: "Wangsu,Anthropic",
	},
	// --- claude-sonnet-4-6 (official) ---
	{
		ModelName: "anthropic.claude-sonnet-4-6", DisplayName: "Claude Sonnet 4.6", Family: "claude", ModelType: "LLM",
		MappedFrom:    "claude-sonnet-4-6 (official)",
		ContextWindow: 1000000, MaxOutputTokens: 64000, Discount: 0.848,
		InputUSDPerM: 3.00, OutputUSDPerM: 15.00,
		CacheReadUSDPerM: 0.30, CacheWriteUSDPerM: 3.75,
		SupportsVision: true, SupportsFunctionCall: true, SupportsThinking: true, SupportsWebSearch: true,
		SupportsCache: true, CacheMechanism: "explicit", CacheMinTokens: 2048,
		Tags: "Wangsu,Anthropic",
	},
	// --- claude-opus-4-5 (official) ---
	{
		ModelName: "anthropic.claude-opus-4-5", DisplayName: "Claude Opus 4.5", Family: "claude", ModelType: "LLM",
		ContextWindow: 200000, MaxOutputTokens: 32000, Discount: 0.848,
		InputUSDPerM: 5.00, OutputUSDPerM: 25.00,
		CacheReadUSDPerM: 0.50, CacheWriteUSDPerM: 6.25,
		SupportsVision: true, SupportsFunctionCall: true, SupportsThinking: true,
		SupportsCache: true, CacheMechanism: "explicit", CacheMinTokens: 4096,
		Tags: "Wangsu,Anthropic",
	},
	// --- claude-opus-4-6 (official) ---
	{
		ModelName: "anthropic.claude-opus-4-6", DisplayName: "Claude Opus 4.6", Family: "claude", ModelType: "LLM",
		MappedFrom:    "claude-opus-4-6 (official)",
		ContextWindow: 1000000, MaxOutputTokens: 32000, Discount: 0.848,
		InputUSDPerM: 5.00, OutputUSDPerM: 25.00,
		CacheReadUSDPerM: 0.50, CacheWriteUSDPerM: 6.25,
		SupportsVision: true, SupportsFunctionCall: true, SupportsThinking: true, SupportsWebSearch: true,
		SupportsCache: true, CacheMechanism: "explicit", CacheMinTokens: 4096,
		Tags: "Wangsu,Anthropic",
	},
	// --- claude-opus-4-7 (official) ---
	{
		ModelName: "anthropic.claude-opus-4-7", DisplayName: "Claude Opus 4.7", Family: "claude", ModelType: "LLM",
		MappedFrom:    "claude-opus-4-7 (official)",
		ContextWindow: 1000000, MaxOutputTokens: 32000, Discount: 0.848,
		InputUSDPerM: 5.00, OutputUSDPerM: 25.00,
		CacheReadUSDPerM: 0.50, CacheWriteUSDPerM: 6.25,
		SupportsVision: true, SupportsFunctionCall: true, SupportsThinking: true, SupportsWebSearch: true,
		SupportsCache: true, CacheMechanism: "explicit", CacheMinTokens: 4096,
		Tags: "Wangsu,Anthropic",
	},

	// ════════════════════ Google Gemini 家族 ════════════════════
	// 官网：https://ai.google.dev/pricing
	// 截至 2025，Gemini 2.5 系列官方价格：
	//   - 2.5 Flash:       $0.30 / $2.50   cache $0.075（文/图输入 $0.30，音频 $1.00；输出含思考）
	//   - 2.5 Flash Lite:  $0.10 / $0.40   cache $0.025
	//   - 2.5 Pro:         阶梯 — ≤200K: $1.25 / $10, cache $0.3125
	//                              >200K:  $2.50 / $15, cache $0.625
	// Gemini 3 系列官方尚未公开定价，按同功能档对应到 2.5 基准（官方价），在 MappedFrom 注明。

	// --- gemini-3-flash-preview → 2.5 Flash ---
	{
		ModelName: "gemini.gemini-3-flash-preview", DisplayName: "Gemini 3 Flash (Preview)", Family: "gemini", ModelType: "VLM",
		MappedFrom:    "gemini-2.5-flash (official, Wangsu 预览命名)",
		ContextWindow: 1048576, MaxOutputTokens: 65536, Discount: 0.795,
		InputUSDPerM: 0.30, OutputUSDPerM: 2.50, CacheReadUSDPerM: 0.075,
		SupportsVision: true, SupportsFunctionCall: true, SupportsJSONMode: true, SupportsThinking: true,
		SupportsCache: true, CacheMechanism: "auto",
		Tags: "Wangsu,Google",
	},
	// --- gemini-3.1-flash-lite-preview → 2.5 Flash Lite ---
	{
		ModelName: "gemini.gemini-3.1-flash-lite-preview", DisplayName: "Gemini 3.1 Flash Lite (Preview)", Family: "gemini", ModelType: "VLM",
		MappedFrom:    "gemini-2.5-flash-lite (official, Wangsu 预览命名)",
		ContextWindow: 1048576, MaxOutputTokens: 65536, Discount: 0.795,
		InputUSDPerM: 0.10, OutputUSDPerM: 0.40, CacheReadUSDPerM: 0.025,
		SupportsVision: true, SupportsFunctionCall: true, SupportsJSONMode: true,
		SupportsCache: true, CacheMechanism: "auto",
		Tags: "Wangsu,Google",
	},
	// --- gemini-3.1-flash-image-preview → 2.5 Flash（含图像） ---
	{
		ModelName: "gemini.gemini-3.1-flash-image-preview", DisplayName: "Gemini 3.1 Flash Image (Preview)", Family: "gemini", ModelType: "VLM",
		MappedFrom:    "gemini-2.5-flash (official, 图像同档)",
		ContextWindow: 1048576, MaxOutputTokens: 32768, Discount: 0.795,
		InputUSDPerM: 0.30, OutputUSDPerM: 2.50, CacheReadUSDPerM: 0.075,
		SupportsVision: true, SupportsFunctionCall: true, SupportsJSONMode: true,
		SupportsCache: true, CacheMechanism: "auto",
		Tags: "Wangsu,Google,Image",
	},
	// --- gemini-3-pro-preview → 2.5 Pro 阶梯 ---
	{
		ModelName: "gemini.gemini-3-pro-preview", DisplayName: "Gemini 3 Pro (Preview)", Family: "gemini", ModelType: "VLM",
		MappedFrom:    "gemini-3-pro-preview (official tiered, shut down by Google on 2026-03-09; keep only for Wangsu compatibility)",
		ContextWindow: 1048576, MaxOutputTokens: 65536, Discount: 0.795,
		InputUSDPerM: 2.00, OutputUSDPerM: 12.00, CacheReadUSDPerM: 0.20,
		CacheExplicitReadUSDPerM: 0.20, CacheStorageUSDPerMHour: 4.50,
		PriceTiersUSD: []PriceTierUSD{
			{Label: "<=200K", MaxInputTokens: 200000, InputUSDPerM: 2.00, OutputUSDPerM: 12.00, CacheReadUSDPerM: 0.20},
			{Label: ">200K", MaxInputTokens: 0, InputUSDPerM: 4.00, OutputUSDPerM: 18.00, CacheReadUSDPerM: 0.40},
		},
		SupportsVision: true, SupportsFunctionCall: true, SupportsJSONMode: true, SupportsThinking: true,
		SupportsCache: true, CacheMechanism: "both", CacheMinTokens: 4096,
		InputModalities:  []string{"text", "image", "video", "audio", "pdf"},
		OutputModalities: []string{"text"},
		Tags:             "Wangsu,Google,Reasoning,Deprecated",
	},
	// --- gemini-3.1-pro-preview → 2.5 Pro 阶梯 ---
	{
		ModelName: "gemini.gemini-3.1-pro-preview", DisplayName: "Gemini 3.1 Pro (Preview)", Family: "gemini", ModelType: "VLM",
		MappedFrom:    "gemini-3.1-pro-preview (official tiered)",
		ContextWindow: 1048576, MaxOutputTokens: 65536, Discount: 0.795,
		InputUSDPerM: 2.00, OutputUSDPerM: 12.00, CacheReadUSDPerM: 0.20,
		CacheExplicitReadUSDPerM: 0.20, CacheStorageUSDPerMHour: 4.50,
		PriceTiersUSD: []PriceTierUSD{
			{Label: "<=200K", MaxInputTokens: 200000, InputUSDPerM: 2.00, OutputUSDPerM: 12.00, CacheReadUSDPerM: 0.20},
			{Label: ">200K", MaxInputTokens: 0, InputUSDPerM: 4.00, OutputUSDPerM: 18.00, CacheReadUSDPerM: 0.40},
		},
		SupportsVision: true, SupportsFunctionCall: true, SupportsJSONMode: true, SupportsThinking: true, SupportsWebSearch: true,
		SupportsCache: true, CacheMechanism: "both", CacheMinTokens: 4096,
		InputModalities:  []string{"text", "image", "video", "audio", "pdf"},
		OutputModalities: []string{"text"},
		Tags:             "Wangsu,Google,Reasoning",
	},
}
