package model

// ChannelGroup 渠道组模型，将多个渠道组合并配置路由策略
type ChannelGroup struct {
	BaseModel
	Name       string `gorm:"type:varchar(100);not null" json:"name"`                                   // 组名称
	Code       string `gorm:"type:varchar(50);uniqueIndex;not null" json:"code"`                         // 唯一编码
	Strategy   string `gorm:"type:varchar(30);not null;default:'Priority'" json:"strategy"`               // 路由策略: Priority/Weight/RoundRobin/LeastLoad/CostFirst
	ChannelIDs JSON   `gorm:"type:json" json:"channel_ids,omitempty"`                                   // 包含的渠道 ID 列表
	MixMode    string `gorm:"type:varchar(30);default:'SINGLE'" json:"mix_mode"`                         // 混合模式: SINGLE/FALLBACK_CHAIN/SPLIT_BY_MODEL/TAG_BASED
	MixConfig  JSON   `gorm:"type:json" json:"mix_config,omitempty"`                                    // 混合配置 (JSON)
	TagFilter  JSON   `gorm:"type:json" json:"tag_filter,omitempty"`                                    // 标签过滤 (JSON)
	IsActive   bool   `gorm:"default:true" json:"is_active"`                                             // 是否启用
}

// TableName 指定渠道组表名
func (ChannelGroup) TableName() string {
	return "channel_groups"
}
