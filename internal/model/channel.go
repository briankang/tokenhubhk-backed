package model

// Channel 供应商渠道模型，代表一个 AI 提供商的接入端点
// 每个渠道对应一个供应商的 API Key 和 Endpoint
// 渠道状态说明：
// - unverified: 默认状态，新建渠道尚未验证Key
// - active: 已验证通过，可正常使用
// - disabled: 已禁用
// - error: 验证失败或API异常
type Channel struct {
	BaseModel
	Name           string       `gorm:"type:varchar(100);not null" json:"name"`                                  // 渠道名称
	SupplierID     uint         `gorm:"index;not null" json:"supplier_id"`                                       // 关联供应商 ID
	Type           string       `gorm:"type:varchar(30);not null" json:"type"`                                   // 类型: openai / azure / anthropic / ...
	ChannelType    string       `gorm:"type:varchar(20);default:'CHAT';index" json:"channel_type"`               // 渠道用途: CHAT(对话) / CODING(代码补全) / MIXED(混合)
	Endpoint       string       `gorm:"type:varchar(500);not null" json:"endpoint"`                               // API 端点 URL
	APIKey         string       `gorm:"type:varchar(500);not null" json:"-"`                                     // API Key（不输出）
	Models         JSON         `gorm:"type:json" json:"models,omitempty"`                                       // 支持的模型列表 (JSON)
	Weight         int          `gorm:"default:1" json:"weight"`                                                 // 路由权重
	Priority       int          `gorm:"default:0" json:"priority"`                                               // 路由优先级
	Status         string       `gorm:"type:varchar(20);default:'unverified';index" json:"status"`                // 状态: unverified(默认) / active / disabled / error
	Verified       bool         `gorm:"default:false" json:"verified"`                                           // Key是否已验证通过
	MaxConcurrency int          `gorm:"default:100" json:"max_concurrency"`                                      // 最大并发数
	QPM            int          `gorm:"default:60" json:"qpm"`                                                   // 每分钟请求数限制
	PreferenceTag  string       `gorm:"type:varchar(30);default:''" json:"preference_tag"`                        // 偏好标签: availability/cost/speed 或空
	Tags           []ChannelTag `gorm:"many2many:channel_tags_relation" json:"tags,omitempty"`                    // 标签（多对多）

	// --- API协议与鉴权配置 ---
	ApiProtocol string `gorm:"type:varchar(30);default:'openai_chat'" json:"api_protocol"`
	// openai_chat: /chat/completions (默认), openai_responses: /responses, anthropic: /v1/messages, custom: 自定义
	ApiPath string `gorm:"type:varchar(200)" json:"api_path,omitempty"`
	// API请求路径，协议切换时自动填充默认值，可手动覆盖
	AuthMethod string `gorm:"type:varchar(30);default:'bearer'" json:"auth_method"`
	// bearer: Authorization: Bearer key (默认), x-api-key: x-api-key: key, custom: 自定义Header
	AuthHeader string `gorm:"type:varchar(100)" json:"auth_header,omitempty"`
	// 自定义鉴权Header名，auth_method=custom时生效
	CustomParams JSON `gorm:"type:json" json:"custom_params,omitempty"`
	// 供应商特定参数(JSON)，合并到每次API请求中
	ContextLength int `gorm:"default:0" json:"context_length"`
	// 模型上下文长度(tokens)

	Supplier Supplier `gorm:"foreignKey:SupplierID" json:"supplier,omitempty"` // 关联供应商
}

// TableName 指定渠道表名
func (Channel) TableName() string {
	return "channels"
}
