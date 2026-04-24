package database

import (
	"encoding/json"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

func TestRunPruneUnusedSuppliersMigration(t *testing.T) {
	db := newPruneSuppliersTestDB(t)

	keep := model.Supplier{Name: "阿里云百炼", Code: "aliyun_dashscope", AccessType: "api", IsActive: true}
	stale := model.Supplier{Name: "updated-supplier_1", Code: "sp_1", AccessType: "api", IsActive: true}
	if err := db.Create(&keep).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&stale).Error; err != nil {
		t.Fatal(err)
	}

	keepCat := model.ModelCategory{Name: "通用对话", Code: "qwen_chat", SupplierID: keep.ID}
	staleCat := model.ModelCategory{Name: "测试分类", Code: "junk_chat", SupplierID: stale.ID}
	if err := db.Create(&keepCat).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&staleCat).Error; err != nil {
		t.Fatal(err)
	}

	keepModel := model.AIModel{SupplierID: keep.ID, CategoryID: keepCat.ID, ModelName: "qwen-plus", IsActive: true}
	staleModel := model.AIModel{SupplierID: stale.ID, CategoryID: staleCat.ID, ModelName: "junk-model", IsActive: true}
	if err := db.Create(&keepModel).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&staleModel).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ModelPricing{ModelID: staleModel.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ModelLabel{ModelID: staleModel.ID, LabelKey: "tag", LabelValue: "热卖"}).Error; err != nil {
		t.Fatal(err)
	}

	keepChannel := model.Channel{Name: "aliyun", SupplierID: keep.ID, Type: "openai", Endpoint: "https://dashscope.aliyuncs.com", APIKey: "x"}
	staleChannel := model.Channel{Name: "junk", SupplierID: stale.ID, Type: "openai", Endpoint: "https://example.com", APIKey: "x"}
	if err := db.Create(&keepChannel).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&staleChannel).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ChannelModel{ChannelID: staleChannel.ID, StandardModelID: "junk-model", VendorModelID: "junk-model"}).Error; err != nil {
		t.Fatal(err)
	}

	groupIDs, _ := json.Marshal([]uint{keepChannel.ID, staleChannel.ID})
	group := model.ChannelGroup{Name: "混合组", Code: "mixed", Strategy: "Priority", ChannelIDs: groupIDs, IsActive: true}
	if err := db.Create(&group).Error; err != nil {
		t.Fatal(err)
	}

	if err := RunPruneUnusedSuppliersMigration(db); err != nil {
		t.Fatal(err)
	}

	var suppliers []model.Supplier
	if err := db.Find(&suppliers).Error; err != nil {
		t.Fatal(err)
	}
	if len(suppliers) != 1 || suppliers[0].Code != "aliyun_dashscope" {
		t.Fatalf("active suppliers=%v, want only aliyun_dashscope", suppliers)
	}

	var staleModelCount int64
	db.Model(&model.AIModel{}).Where("model_name = ?", "junk-model").Count(&staleModelCount)
	if staleModelCount != 0 {
		t.Fatalf("stale model still visible")
	}
	var stalePricingCount int64
	db.Model(&model.ModelPricing{}).Where("model_id = ?", staleModel.ID).Count(&stalePricingCount)
	if stalePricingCount != 0 {
		t.Fatalf("stale pricing still visible")
	}

	var updatedGroup model.ChannelGroup
	if err := db.First(&updatedGroup, group.ID).Error; err != nil {
		t.Fatal(err)
	}
	var remaining []uint
	if err := json.Unmarshal(updatedGroup.ChannelIDs, &remaining); err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0] != keepChannel.ID {
		t.Fatalf("group channel ids=%v, want [%d]", remaining, keepChannel.ID)
	}
}

func newPruneSuppliersTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.Supplier{},
		&model.ModelCategory{},
		&model.AIModel{},
		&model.Channel{},
		&model.ChannelGroup{},
		&model.BackupRule{},
		&model.ModelPricing{},
		&model.ModelLabel{},
		&model.AgentPricing{},
		&model.UserModelDiscount{},
		&model.ChannelModel{},
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE IF NOT EXISTS channel_tags_relation (channel_id integer, channel_tag_id integer)").Error; err != nil {
		t.Fatal(err)
	}
	return db
}
