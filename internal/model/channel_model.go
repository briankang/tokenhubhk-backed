package model

// ChannelModel 渠道-模型映射，解决不同供应商模型ID差异问题
// 如: 火山引擎用接入点ID(ep-xxx)调用, 阿里云用标准模型名调用
// 唯一约束: (channel_id, vendor_model_id) 防止同一渠道重复映射
type ChannelModel struct {
	BaseModel
	ChannelID       uint   `gorm:"not null;uniqueIndex:uidx_ch_vendor_model" json:"channel_id"`
	StandardModelID string `gorm:"type:varchar(100);not null;index" json:"standard_model_id"`
	// 标准模型名(用户看到的), 如 deepseek-r1
	VendorModelID string `gorm:"type:varchar(200);not null;uniqueIndex:uidx_ch_vendor_model" json:"vendor_model_id"`
	// 供应商特定ID(实际调API传的), 如 ep-20250305001
	IsActive bool   `gorm:"default:true" json:"is_active"`
	Source   string `gorm:"type:varchar(20);default:'auto'" json:"source"`
	// auto=自动发现, manual=手动配置

	Channel Channel `gorm:"foreignKey:ChannelID" json:"channel,omitempty"`
}

// TableName 指定渠道-模型映射表名
func (ChannelModel) TableName() string {
	return "channel_models"
}
