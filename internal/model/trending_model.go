package model

import "gorm.io/gorm"

// TrendingModel 全球热门模型参考库
type TrendingModel struct {
	gorm.Model
	ModelName       string `gorm:"uniqueIndex;not null;size:200" json:"model_name"`
	DisplayName     string `gorm:"not null;size:200" json:"display_name"`
	SupplierName    string `gorm:"index;not null;size:100" json:"supplier_name"`
	LaunchYearMonth string `gorm:"not null;size:10" json:"launch_year_month"` // "2025-04"
	PopularityStars int    `gorm:"not null;default:3" json:"popularity_stars"` // 1-5
	ModelType       string `gorm:"size:50;default:'LLM'" json:"model_type"`
	Description     string `gorm:"type:text" json:"description"`
	SourceURL       string `gorm:"not null;size:500" json:"source_url"`
	IsActive        bool   `gorm:"default:true" json:"is_active"`
}

func (TrendingModel) TableName() string { return "trending_models" }
