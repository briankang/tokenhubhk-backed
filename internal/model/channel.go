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
	// SupportedCapabilities 渠道支持的能力列表，逗号分隔
	// 可选值: chat, image, video, tts, asr, embedding
	// 空值或 "chat" 兼容旧数据（老渠道默认仅支持对话）
	SupportedCapabilities string       `gorm:"type:varchar(255);default:'chat'" json:"supported_capabilities"`
	Endpoint       string       `gorm:"type:varchar(500);not null" json:"endpoint"`                               // API 端点 URL
	APIKey         string       `gorm:"type:varchar(500);not null" json:"-"`                                     // API Key（不输出）
	Models         JSON         `gorm:"type:json" json:"models,omitempty"`                                       // Deprecated: 使用 CustomChannelRoute + ChannelModel 替代。保留兼容旧数据
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

// 渠道能力常量
const (
	CapabilityChat      = "chat"
	CapabilityImage     = "image"
	CapabilityVideo     = "video"
	CapabilityTTS       = "tts"
	CapabilityASR       = "asr"
	CapabilityEmbedding = "embedding"
)

// ModelTypeToCapability 将 ai_models.model_type 映射为渠道能力标签
// 空或未知类型返回 "chat"（兼容旧 LLM 模型）
func ModelTypeToCapability(modelType string) string {
	switch modelType {
	case "ImageGeneration":
		return CapabilityImage
	case "VideoGeneration":
		return CapabilityVideo
	case "SpeechSynthesis":
		return CapabilityTTS
	case "SpeechRecognition":
		return CapabilityASR
	case "Embedding":
		return CapabilityEmbedding
	case "LLM", "VLM", "":
		return CapabilityChat
	default:
		return CapabilityChat
	}
}

// HasCapability 判断渠道是否声明支持指定能力
// 空 SupportedCapabilities 兼容旧数据（视为支持 chat）
func (c *Channel) HasCapability(cap string) bool {
	if c.SupportedCapabilities == "" {
		return cap == CapabilityChat
	}
	for _, part := range splitAndTrim(c.SupportedCapabilities, ",") {
		if part == cap {
			return true
		}
	}
	return false
}

// splitAndTrim 简易切分 + 去空白
func splitAndTrim(s, sep string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || string(s[i]) == sep {
			if i > start {
				seg := s[start:i]
				// 去首尾空白
				for len(seg) > 0 && (seg[0] == ' ' || seg[0] == '\t') {
					seg = seg[1:]
				}
				for len(seg) > 0 && (seg[len(seg)-1] == ' ' || seg[len(seg)-1] == '\t') {
					seg = seg[:len(seg)-1]
				}
				if seg != "" {
					out = append(out, seg)
				}
			}
			start = i + 1
		}
	}
	return out
}
