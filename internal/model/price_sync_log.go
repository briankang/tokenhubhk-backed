package model

import "time"

// PriceSyncLog 价格同步历史日志
// 记录每次从供应商抓取定价数据的同步结果
type PriceSyncLog struct {
	BaseModel
	SupplierID    uint      `gorm:"index;not null" json:"supplier_id"`                   // 供应商 ID
	SupplierName  string    `gorm:"type:varchar(100)" json:"supplier_name"`              // 供应商名称（冗余存储，便于查询）
	SyncTime      time.Time `gorm:"index;not null" json:"sync_time"`                     // 同步执行时间
	Status        string    `gorm:"type:varchar(20)" json:"status"`                      // 同步状态: success / partial_success / failed
	FetchStatus   string    `gorm:"type:varchar(50)" json:"fetch_status"`                // 抓取状态详情
	ModelsChecked int       `gorm:"default:0" json:"models_checked"`                     // 检查的模型数量
	ModelsUpdated int       `gorm:"default:0" json:"models_updated"`                     // 更新的模型数量
	ModelsSkipped int       `gorm:"default:0" json:"models_skipped"`                     // 跳过的模型数量
	Errors        JSON      `gorm:"type:json" json:"errors,omitempty"`                   // 错误信息列表（JSON 数组）
	Changes       JSON      `gorm:"type:json" json:"changes,omitempty"`                  // 变更详情（JSON 数组，记录每个模型的价格变动）
	CreatedByID   uint      `json:"created_by_id,omitempty"`                             // 触发同步的用户 ID（0 表示系统自动）
}

// TableName 指定价格同步日志表名
func (PriceSyncLog) TableName() string {
	return "price_sync_logs"
}
