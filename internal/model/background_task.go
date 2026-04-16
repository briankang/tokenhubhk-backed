package model

import (
	"time"

	"gorm.io/gorm"
)

// BackgroundTask 后台异步任务
// 记录模型同步、模型检测、价格抓取等耗时任务的执行状态与结果
type BackgroundTask struct {
	ID              uint           `json:"id" gorm:"primaryKey"`
	TaskType        string         `json:"task_type" gorm:"type:varchar(50);not null;index"`         // model_sync, model_check, price_scrape
	Status          string         `json:"status" gorm:"type:varchar(20);not null;default:'pending'"` // pending, running, completed, failed
	Params          string         `json:"params" gorm:"type:text"`                                   // JSON 格式的任务参数
	Result          string         `json:"result" gorm:"type:longtext"`                               // JSON 格式的任务结果
	Progress        int            `json:"progress" gorm:"default:0"`                                 // 进度百分比 0-100
	ProgressMessage string         `json:"progress_message" gorm:"type:varchar(500)"`                 // 当前进度描述
	ErrorMessage    string         `json:"error_message" gorm:"type:text"`                            // 错误信息
	OperatorID      uint           `json:"operator_id" gorm:"index"`                                  // 操作者用户ID
	StartedAt       *time.Time     `json:"started_at"`                                                // 开始执行时间
	CompletedAt     *time.Time     `json:"completed_at"`                                              // 完成时间
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"-" gorm:"index"`
}

// TaskType 常量
const (
	TaskTypeModelSync   = "model_sync"
	TaskTypeModelCheck  = "model_check"
	TaskTypePriceScrape = "price_scrape"
)

// TaskStatus 常量
const (
	TaskStatusPending   = "pending"
	TaskStatusRunning   = "running"
	TaskStatusCompleted = "completed"
	TaskStatusFailed    = "failed"
)

// TaskTypeLabel 任务类型中文标签
func TaskTypeLabel(taskType string) string {
	switch taskType {
	case TaskTypeModelSync:
		return "模型同步"
	case TaskTypeModelCheck:
		return "模型检测"
	case TaskTypePriceScrape:
		return "价格抓取"
	default:
		return taskType
	}
}
