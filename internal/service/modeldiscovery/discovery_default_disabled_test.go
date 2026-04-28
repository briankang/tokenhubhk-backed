package modeldiscovery

import (
	"strings"
	"testing"

	internalmodel "tokenhub-server/internal/model"
)

// TestSyncStandardModels_NewModelDefaultDisabled 验证：
// 通过 syncStandardModels 自动入库的新模型，默认 IsActive=false，
// 且 tags 中包含 NeedsReview 标签。
func TestSyncStandardModels_NewModelDefaultDisabled(t *testing.T) {
	svc, db := setupDiscoveryDB(t)

	// 额外迁移 ChannelModel 表（syncStandardModels 会写入）
	if err := db.AutoMigrate(&internalmodel.ChannelModel{}); err != nil {
		t.Fatalf("auto migrate ChannelModel: %v", err)
	}

	supID := seedDiscoverySupplier(t, db, "test-default-disabled")
	_ = seedDiscoveryCategory(t, db, supID, "cat-default-disabled")

	// 构造一个测试 channel（不需写 DB，syncStandardModels 直接接 channel 引用）
	channel := internalmodel.Channel{
		BaseModel:  internalmodel.BaseModel{ID: 99},
		Name:       "test-channel",
		SupplierID: supID,
		Endpoint:   "http://test",
	}
	channel.Supplier = internalmodel.Supplier{
		BaseModel: internalmodel.BaseModel{ID: supID},
		Code:      "test-default-disabled",
	}

	// 模拟上游返回两个新模型
	upstreamModels := []openAIModelID{
		{ID: "test-new-model-llm"},
		{ID: "test-new-model-vlm-vision"},
	}

	result := &SyncResult{}
	if err := svc.syncStandardModels(channel, upstreamModels, result); err != nil {
		t.Fatalf("syncStandardModels failed: %v", err)
	}

	// 验证两个模型都被写入 + IsActive=false + tags 含 NeedsReview
	for _, modelName := range []string{"test-new-model-llm", "test-new-model-vlm-vision"} {
		var m internalmodel.AIModel
		if err := db.Where("model_name = ?", modelName).First(&m).Error; err != nil {
			t.Errorf("模型 %s 未被创建: %v", modelName, err)
			continue
		}
		if m.IsActive {
			t.Errorf("模型 %s 应该默认 IsActive=false，但得到 true", modelName)
		}
		if m.Source != "auto" {
			t.Errorf("模型 %s 应该 Source=auto，但得到 %q", modelName, m.Source)
		}
		if !strings.Contains(m.Tags, "NeedsReview") {
			t.Errorf("模型 %s 的 tags 应含 NeedsReview，实际 tags=%q", modelName, m.Tags)
		}
	}
}
