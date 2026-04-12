package model

// ChannelTag 渠道标签模型，用于给渠道打标签分类
type ChannelTag struct {
	BaseModel
	Name      string `gorm:"type:varchar(50);uniqueIndex;not null" json:"name"` // 标签名称（唯一）
	Color     string `gorm:"type:varchar(20);default:'#3B82F6'" json:"color"`  // 颜色值
	SortOrder int    `gorm:"default:0" json:"sort_order"`                     // 排序
}

// TableName 指定渠道标签表名
func (ChannelTag) TableName() string {
	return "channel_tags"
}
