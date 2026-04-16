package model

import "time"

// PricingUnit 计费单位常量
// 8 种计费单位覆盖 LLM、图像、视频、语音合成、语音识别、Embedding、Rerank 等 AI 模型类别
const (
	UnitPerMillionTokens     = "per_million_tokens"     // LLM / Embedding / Seedance 视频(按 Token)
	UnitPerImage             = "per_image"              // 图像生成: Seedream / wanx / cogview / dall-e
	UnitPerSecond            = "per_second"             // 视频/音频: wanx-video / cogvideo
	UnitPerMinute            = "per_minute"             // 语音识别: whisper
	UnitPer10kCharacters     = "per_10k_characters"     // TTS: 豆包 TTS 2.0 (¥/万字符)
	UnitPerMillionCharacters = "per_million_characters" // TTS: qwen-tts / openai-tts
	UnitPerCall              = "per_call"               // Rerank / 意图识别
	UnitPerHour              = "per_hour"               // ASR: 豆包/paraformer 流式语音识别

	// 历史遗留别名，等价于 UnitPer10kCharacters
	UnitPerKChars = "per_k_chars"
)

// ModelType 模型类型常量
const (
	ModelTypeLLM             = "LLM"
	ModelTypeVision          = "Vision"
	ModelTypeEmbedding       = "Embedding"
	ModelTypeImageGeneration = "ImageGeneration"
	ModelTypeVideoGeneration = "VideoGeneration"
	ModelTypeTTS             = "TTS"
	ModelTypeASR             = "ASR"
	ModelTypeRerank          = "Rerank"
)

// AIModel AI 模型模型，表示平台支持的某个 AI 模型
// 包含成本价、参数限制、关联供应商和分类
// 采用双轨存储：积分成本(int64) + 人民币成本(float64)
// 模型状态说明：
// - offline: 默认状态，模型未验证或Key未配置
// - online: 已验证，对用户可见可用
// - error: 验证失败或API异常
type AIModel struct {
	BaseModel
	CategoryID          uint    `gorm:"index;not null" json:"category_id"`                          // 分类 ID
	SupplierID          uint    `gorm:"index;not null" json:"supplier_id"`                          // 供应商 ID
	ModelName           string  `gorm:"type:varchar(100);not null;index" json:"model_name"`          // 模型名称（如 gpt-4o）
	DisplayName         string  `gorm:"type:varchar(255)" json:"display_name"`                      // 展示名称
	Description         string  `gorm:"type:text" json:"description,omitempty"`                     // 描述
	IsActive            bool    `gorm:"default:true" json:"is_active"`                              // 是否启用（管理开关）
	Status              string  `gorm:"type:varchar(20);default:'offline';index" json:"status"`     // 状态: offline(默认)/online/error
	MaxTokens           int     `gorm:"default:4096" json:"max_tokens"`                             // 最大输出 Token 数
	ContextWindow       int     `gorm:"default:4096" json:"context_window"`                         // 上下文窗口大小
	InputPricePerToken  int64   `gorm:"type:bigint;default:0" json:"input_price_per_token"`          // 输入成本价 (每百万token积分)
	InputCostRMB        float64 `gorm:"type:decimal(16,4);default:0" json:"input_cost_rmb"`          // 输入成本价 (每百万token人民币)
	OutputPricePerToken int64   `gorm:"type:bigint;default:0" json:"output_price_per_token"`         // 输出成本价 (每百万token积分)
	OutputCostRMB       float64 `gorm:"type:decimal(16,4);default:0" json:"output_cost_rmb"`         // 输出成本价 (每百万token人民币)
	Currency            string  `gorm:"type:varchar(10);default:'CREDIT'" json:"currency"`           // 币种 CREDIT
	Source              string     `gorm:"type:varchar(20);default:'manual'" json:"source"`           // manual=手动创建, auto=自动发现
	LastSyncedAt        *time.Time `json:"last_synced_at,omitempty"`                                  // 上次自动同步时间

	// --- 扩展字段（来源于供应商 API 同步） ---
	ModelType        string `gorm:"type:varchar(50);default:'LLM'" json:"model_type"`           // 模型类型: LLM/VLM/Embedding/ImageGeneration/VideoGeneration 等
	Version          string `gorm:"type:varchar(100)" json:"version,omitempty"`                 // 模型版本号（火山引擎返回）
	Domain           string `gorm:"type:varchar(50)" json:"domain,omitempty"`                   // 模型领域（火山引擎原始 domain 字段）
	TaskTypes        JSON   `gorm:"type:json" json:"task_types,omitempty"`                      // 任务类型列表（如 ["chat","completion"]）
	InputModalities  JSON   `gorm:"type:json" json:"input_modalities,omitempty"`                // 输入模态（如 ["text","image"]）
	OutputModalities JSON   `gorm:"type:json" json:"output_modalities,omitempty"`               // 输出模态（如 ["text"]）
	MaxInputTokens   int    `gorm:"default:0" json:"max_input_tokens"`                         // 最大输入 Token 数（供应商返回）
	MaxOutputTokens  int    `gorm:"default:0" json:"max_output_tokens"`                        // 最大输出 Token 数（供应商返回，区别于手动设置的 MaxTokens）
	Features         JSON   `gorm:"type:json" json:"features,omitempty"`                        // 模型特性（如流式/函数调用/JSON模式等，完整 JSON）
	SupplierStatus   string `gorm:"type:varchar(50);default:'Active'" json:"supplier_status"`   // 供应商侧模型状态（Active/Deprecated 等）
	ApiCreatedAt     int64  `gorm:"default:0" json:"api_created_at"`                            // 供应商 API 返回的模型创建时间（Unix 时间戳）
	PricingUnit      string `gorm:"type:varchar(50);default:'per_million_tokens'" json:"pricing_unit"` // 计费单位: per_million_tokens/per_image/per_second/per_minute/per_10k_characters/per_million_characters/per_call/per_hour
	Variant          string `gorm:"type:varchar(50);default:''" json:"variant,omitempty"`      // 变体/质量档（如 1024x1024、hd、low-latency），同一模型不同 variant 独立定价
	PriceTiers       JSON   `gorm:"type:json" json:"price_tiers,omitempty"`                    // 阶梯价格配置（供应商原始数据，JSON 格式的 PriceTiersData）
	ExtraParams      JSON   `gorm:"type:json" json:"extra_params,omitempty"`                  // 自定义参数（传递给上游供应商的额外参数，如 enable_thinking）

	// --- 缓存定价字段 ---
	// cache_mechanism: 缓存机制类型
	//   auto     - 全自动透明缓存（OpenAI/DeepSeek/Moonshot/智谱/火山引擎）
	//   explicit - 需显式传 cache_control 参数（Anthropic）
	//   both     - 同时支持隐式(auto)和显式(explicit)，如阿里云百炼
	//   none     - 不支持缓存（默认）
	SupportsCache              bool    `gorm:"default:false" json:"supports_cache"`                               // 是否支持缓存定价
	CacheMechanism             string  `gorm:"type:varchar(20);default:'none'" json:"cache_mechanism"`            // 缓存机制: auto/explicit/both/none
	CacheMinTokens             int     `gorm:"default:0" json:"cache_min_tokens"`                                 // 触发缓存的最小Token门槛（用于自动注入 cache_control 时的判断）
	CacheInputPriceRMB         float64 `gorm:"type:decimal(20,6);default:0" json:"cache_input_price_rmb"`         // 缓存命中(隐式/auto)输入价，元/百万Token
	CacheExplicitInputPriceRMB float64 `gorm:"type:decimal(20,6);default:0" json:"cache_explicit_input_price_rmb"` // 显式缓存命中价，元/百万Token（both模式专用，如阿里云显式缓存）
	CacheWritePriceRMB         float64 `gorm:"type:decimal(20,6);default:0" json:"cache_write_price_rmb"`         // 缓存写入溢价，元/百万Token（Anthropic/阿里云显式缓存专用）
	CacheStoragePriceRMB       float64 `gorm:"type:decimal(20,6);default:0" json:"cache_storage_price_rmb"`       // 缓存存储价，元/百万Token/小时（火山引擎/Gemini显式缓存）
	Tags             string `gorm:"type:varchar(500)" json:"tags,omitempty"`                 // 搜索标签（逗号分隔，如 "DeepSeek,深度求索"），支持跨供应商搜索
	CallCount        int64  `gorm:"type:bigint;default:0;index" json:"call_count"`           // 累计调用次数

	Category ModelCategory `gorm:"foreignKey:CategoryID" json:"category,omitempty"` // 关联分类
	Supplier Supplier      `gorm:"foreignKey:SupplierID" json:"supplier,omitempty"` // 关联供应商
	Pricing  *ModelPricing  `gorm:"foreignKey:ModelID" json:"pricing,omitempty"`     // 关联售价
}

// TableName 指定 AI 模型表名
func (AIModel) TableName() string {
	return "ai_models"
}
