package database

import (
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// RunMigrateLabelDictNeedsReview 为已部署环境补充 needs_review 字典项。
//
// 背景：seed_label_dictionary.go 是「表非空跳过整个 seed」的幂等模式，
// 因此对已部署环境新增的字典 key 不会自动写入。本迁移仅插入缺失的 needs_review 一行，
// 不影响管理员对其他字典项的自定义修改。
//
// 用途：与 NeedsReview 模型标签配合，让管理后台在自动入库的待审核模型上展示彩色徽标。
func RunMigrateLabelDictNeedsReview(db *gorm.DB) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}
	start := time.Now()

	target := model.LabelDictionary{
		Key:      "needs_review",
		NameZhCN: "待审核",
		NameZhTW: "待審核",
		NameEn:   "Needs Review",
		NameJa:   "審査待ち",
		NameKo:   "검토 필요",
		NameEs:   "Pendiente revisión",
		NameFr:   "À examiner",
		NameDe:   "Prüfung erforderlich",
		NameRu:   "Требуется проверка",
		NameAr:   "بحاجة إلى مراجعة",
		NamePt:   "Aguarda revisão",
		NameVi:   "Cần xét duyệt",
		NameTh:   "รอตรวจสอบ",
		NameId:   "Perlu ditinjau",
		NameHi:   "समीक्षा आवश्यक",
		Color:    "amber",
		Icon:     "alert-circle",
		Category: "system",
		Priority: 25,
		IsActive: true,
	}

	// FirstOrCreate 按 key 查找，存在则跳过、不覆盖管理员的本地修改
	var existing model.LabelDictionary
	res := db.Where("`key` = ?", target.Key).
		Attrs(target).
		FirstOrCreate(&existing)
	if res.Error != nil {
		log.Warn("migrate_label_dict_needs_review: 写入失败",
			zap.Error(res.Error))
		return
	}

	action := "skipped (already exists)"
	if res.RowsAffected > 0 {
		action = "inserted"
	}
	log.Info("migrate_label_dict_needs_review: 完成",
		zap.String("action", action),
		zap.Duration("duration", time.Since(start)))
}
