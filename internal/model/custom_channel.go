package model

// CustomChannel 自定义渠道 -- 统一替代 MixedChannel + ModelAlias + PreferenceTag
// 每个自定义渠道包含一组路由规则和访问控制列表
// 路由策略支持: weighted(加权轮询) / priority(优先级) / round_robin(轮询) / least_load(最小负载) / cost_first(成本优先)
// 可见性: all=所有会员可见, specific=仅指定会员
type CustomChannel struct {
	BaseModel
	Name        string                `gorm:"type:varchar(100);not null;uniqueIndex" json:"name"`
	Description string                `gorm:"type:text" json:"description,omitempty"`
	Strategy    string                `gorm:"type:varchar(30);default:'weighted'" json:"strategy"`
	// 路由策略: weighted/priority/round_robin/least_load/cost_first
	IsDefault bool `gorm:"default:false;index" json:"is_default"`
	AutoRoute bool `gorm:"default:true" json:"auto_route"`
	// true=对没有显式路由的模型自动从供应商接入点按成本优先发现
	Visibility string `gorm:"type:varchar(20);default:'all'" json:"visibility"`
	// all=所有会员可见, specific=仅指定会员
	IsActive   bool                  `gorm:"default:true" json:"is_active"`
	Routes     []CustomChannelRoute  `gorm:"foreignKey:CustomChannelID" json:"routes,omitempty"`
	AccessList []CustomChannelAccess `gorm:"foreignKey:CustomChannelID" json:"access_list,omitempty"`
}

// TableName 指定自定义渠道表名
func (CustomChannel) TableName() string {
	return "custom_channels"
}

// CustomChannelRoute 自定义渠道路由规则
// 定义模型别名到实际供应商渠道+模型ID的映射关系
// 唯一约束: (custom_channel_id, alias_model, channel_id) 防止重复路由
type CustomChannelRoute struct {
	BaseModel
	CustomChannelID uint   `gorm:"not null;index;uniqueIndex:uidx_cc_model_ch" json:"custom_channel_id"`
	AliasModel      string `gorm:"type:varchar(100);not null;uniqueIndex:uidx_cc_model_ch" json:"alias_model"`
	// 用户请求的标准模型名 (如 deepseek-r1)
	ChannelID   uint   `gorm:"not null;uniqueIndex:uidx_cc_model_ch" json:"channel_id"`
	ActualModel string `gorm:"type:varchar(100);not null" json:"actual_model"`
	// 实际发送给供应商的模型ID (标准=同名, 火山引擎=ep-xxx)
	Weight   int  `gorm:"default:100" json:"weight"`
	Priority int  `gorm:"default:0" json:"priority"`
	IsActive bool `gorm:"default:true" json:"is_active"`

	Channel Channel `gorm:"foreignKey:ChannelID" json:"channel,omitempty"`
}

// TableName 指定自定义渠道路由表名
func (CustomChannelRoute) TableName() string {
	return "custom_channel_routes"
}

// CustomChannelAccess 自定义渠道访问控制
// 当 CustomChannel.Visibility = "specific" 时，仅允许此表中列出的用户访问
// 唯一约束: (custom_channel_id, user_id) 防止重复授权
type CustomChannelAccess struct {
	BaseModel
	CustomChannelID uint `gorm:"not null;uniqueIndex:uidx_cc_user" json:"custom_channel_id"`
	UserID          uint `gorm:"not null;uniqueIndex:uidx_cc_user" json:"user_id"`
}

// TableName 指定自定义渠道访问控制表名
func (CustomChannelAccess) TableName() string {
	return "custom_channel_accesses"
}
