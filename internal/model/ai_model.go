package model

import "time"

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
	PriceTiers       JSON   `gorm:"type:json" json:"price_tiers,omitempty"`                    // 阶梯价格配置（供应商原始数据，JSON 格式的 PriceTiersData）

	Category ModelCategory `gorm:"foreignKey:CategoryID" json:"category,omitempty"` // 关联分类
	Supplier Supplier      `gorm:"foreignKey:SupplierID" json:"supplier,omitempty"` // 关联供应商
}

// TableName 指定 AI 模型表名
func (AIModel) TableName() string {
	return "ai_models"
}
