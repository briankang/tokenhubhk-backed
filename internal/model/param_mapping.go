package model

// PlatformParam 平台标准参数定义
// TokenHubHK 平台统一的参数标准，参考 OpenAI 参数命名规范
// 用于屏蔽不同供应商之间的参数差异
type PlatformParam struct {
	BaseModel
	ParamName    string `gorm:"type:varchar(100);uniqueIndex;not null" json:"param_name"`    // 平台标准参数名（如 enable_thinking）
	ParamType    string `gorm:"type:varchar(20);not null" json:"param_type"`                 // 参数类型：bool, int, float, string, json
	DisplayName  string `gorm:"type:varchar(100)" json:"display_name"`                       // 中文展示名称
	Description  string `gorm:"type:varchar(500)" json:"description"`                        // 参数说明
	DefaultValue string `gorm:"type:varchar(500)" json:"default_value"`                      // 默认值（JSON 编码）
	Category     string `gorm:"type:varchar(50);index" json:"category"`                      // 分类：thinking, search, format, penalty, safety
	SortOrder    int    `gorm:"default:0" json:"sort_order"`                                 // 排序
	IsActive     bool   `gorm:"default:true" json:"is_active"`                               // 是否启用

	// 关联的供应商映射
	Mappings []SupplierParamMapping `gorm:"foreignKey:PlatformParamID" json:"mappings,omitempty"`
}

// SupplierParamMapping 供应商参数映射
// 定义平台标准参数到各供应商实际参数的映射关系
type SupplierParamMapping struct {
	BaseModel
	PlatformParamID uint   `gorm:"not null;uniqueIndex:uidx_param_supplier" json:"platform_param_id"`               // 关联平台参数
	SupplierCode    string `gorm:"type:varchar(50);not null;uniqueIndex:uidx_param_supplier" json:"supplier_code"` // 供应商代码（openai, anthropic 等）
	VendorParamName string `gorm:"type:varchar(200);not null" json:"vendor_param_name"`                               // 供应商端实际参数名/路径

	// TransformType 转换类型：
	//   direct  - 直接透传（参数名相同或仅改名）
	//   rename  - 仅参数名不同，值直接传递
	//   nested  - 需要构造嵌套结构（如 Anthropic 的 thinking 对象）
	//   mapping - 值映射（如 true/false → "enabled"/"disabled"）
	//   none    - 该供应商不支持此参数
	TransformType string `gorm:"type:varchar(20);not null;default:'direct'" json:"transform_type"`

	// TransformRule JSON 格式的转换规则
	// nested 示例：{"path": "thinking", "structure": {"type": "enabled", "budget_tokens": "$value"}}
	// mapping 示例：{"true": "enabled", "false": "disabled"}
	TransformRule string `gorm:"type:text" json:"transform_rule"`

	Supported bool   `gorm:"default:true" json:"supported"` // 该供应商是否支持此参数
	Notes     string `gorm:"type:varchar(500)" json:"notes"` // 备注说明
}

// TableName 指定表名
func (PlatformParam) TableName() string { return "platform_params" }

// TableName 指定表名
func (SupplierParamMapping) TableName() string { return "supplier_param_mappings" }
