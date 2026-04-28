package pricing

import (
	"context"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"tokenhub-server/internal/model"
)

// TestAutoEnableIfNeedsSellPrice_SkipsAutoSourceModels 验证：
// source='auto' 的模型即便带 NeedsSellPrice 标签 + is_active=false,
// 配置售价时 autoEnableIfNeedsSellPrice 也不会自动启用。
// 该路径用于阻止自动同步入库的模型在管理员补价后绕过人工审核直接对外公开。
func TestAutoEnableIfNeedsSellPrice_SkipsAutoSourceModels(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.AIModel{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}

	// 准备两个模型：source=auto 和 source=manual，其它字段一致（含 NeedsSellPrice 标签 + is_active=false）
	autoModel := model.AIModel{
		ModelName: "auto-discovered-model",
		IsActive:  false,
		Source:    "auto",
		Tags:      "Hunyuan,NeedsSellPrice",
	}
	manualModel := model.AIModel{
		ModelName: "manual-created-model",
		IsActive:  false,
		Source:    "manual",
		Tags:      "Custom,NeedsSellPrice",
	}
	if err := db.Create(&autoModel).Error; err != nil {
		t.Fatalf("create auto model: %v", err)
	}
	if err := db.Create(&manualModel).Error; err != nil {
		t.Fatalf("create manual model: %v", err)
	}
	// AIModel.IsActive 字段有 GORM `default:true` tag，Create 时 false 零值会被跳过 → 数据库默认 true
	// 强制 UPDATE 让两个模型 is_active=false 进入测试初始状态
	if err := db.Table("ai_models").Where("id IN ?", []uint{autoModel.ID, manualModel.ID}).
		Update("is_active", false).Error; err != nil {
		t.Fatalf("force is_active=false: %v", err)
	}

	svc := NewPricingService(db, NewPricingCalculator(db))

	// 调用 autoEnableIfNeedsSellPrice 两次
	svc.autoEnableIfNeedsSellPrice(context.Background(), autoModel.ID)
	svc.autoEnableIfNeedsSellPrice(context.Background(), manualModel.ID)

	// 验证 source=auto 的模型保持 is_active=false 且 NeedsSellPrice 标签未被移除
	var autoAfter model.AIModel
	if err := db.First(&autoAfter, autoModel.ID).Error; err != nil {
		t.Fatalf("reload auto model: %v", err)
	}
	if autoAfter.IsActive {
		t.Errorf("source=auto 的模型不应被自动启用，但 IsActive=true")
	}
	if !strings.Contains(autoAfter.Tags, "NeedsSellPrice") {
		t.Errorf("source=auto 的模型 NeedsSellPrice 标签应保留，实际 tags=%q", autoAfter.Tags)
	}

	// 验证 source=manual 的模型按既有逻辑被自动启用 + 移除 NeedsSellPrice 标签
	var manualAfter model.AIModel
	if err := db.First(&manualAfter, manualModel.ID).Error; err != nil {
		t.Fatalf("reload manual model: %v", err)
	}
	if !manualAfter.IsActive {
		t.Errorf("source=manual 的模型应被自动启用，但 IsActive=false")
	}
	if strings.Contains(manualAfter.Tags, "NeedsSellPrice") {
		t.Errorf("source=manual 的模型 NeedsSellPrice 标签应被移除，实际 tags=%q", manualAfter.Tags)
	}
}
