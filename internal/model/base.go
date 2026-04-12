package model

import (
	"time"

	"gorm.io/gorm"
)

// BaseModel 所有 GORM 模型的公共基础结构
// 包含主键 ID、创建时间、更新时间、软删除时间
type BaseModel struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"deleted_at,omitempty"`
}

// JSON MySQL JSON 列的字节切片类型别名
type JSON = []byte
