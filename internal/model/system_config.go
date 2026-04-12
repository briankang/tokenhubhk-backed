package model

import "time"

// SystemConfig 系统配置键值对模型
// 用于存储系统级别的配置项，如初始化标志、站点名称等
type SystemConfig struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Key       string    `gorm:"type:varchar(100);uniqueIndex;not null" json:"key"`   // 配置键
	Value     string    `gorm:"type:text" json:"value"`                              // 配置值
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName 指定表名
func (SystemConfig) TableName() string {
	return "system_configs"
}
