package database

import (
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// RunSeedLabelDictionary 首次启动写入标签字典种子（幂等：表非空则跳过）
//
// 包含 3 个分类：
//   - user     用户可见标签（热卖/优惠/新品/免费/推荐/测试/即将下线）
//   - pricing  价格相关内部标识（待定价/待设售价）
//   - brand    供应商品牌（通义千问/豆包/ERNIE/混元/DeepSeek/Claude/OpenAI 等）
//
// 管理员可通过 Admin API 增删改字典项，首次 seed 后不会覆盖管理员修改。
func RunSeedLabelDictionary(db *gorm.DB) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}
	start := time.Now()

	var count int64
	if err := db.Model(&model.LabelDictionary{}).Count(&count).Error; err != nil {
		log.Warn("seed_label_dictionary: 查询字典数量失败", zap.Error(err))
		return
	}
	if count > 0 {
		log.Info("seed_label_dictionary: 已存在数据，跳过",
			zap.Int64("existing", count),
			zap.Duration("duration", time.Since(start)))
		return
	}

	labels := []model.LabelDictionary{
		// ============ 用户可见标签（category=user）============
		{Key: "hot", NameZhCN: "热卖", NameZhTW: "熱賣", NameEn: "Hot",
			NameJa: "人気", NameKo: "인기", NameEs: "Popular", NameFr: "Populaire",
			NameDe: "Beliebt", NameRu: "Популярное", NameAr: "شائع", NamePt: "Popular",
			NameVi: "Bán chạy", NameTh: "ยอดนิยม", NameId: "Populer", NameHi: "लोकप्रिय",
			NameIt: "Popolare", NameNl: "Populair", NamePl: "Popularne", NameTr: "Popüler",
			NameMs: "Popular", NameFil: "Popular", NameHe: "פופולרי", NameFa: "محبوب",
			Color: "red", Icon: "flame", Category: "user", Priority: 100, IsActive: true},

		{Key: "promo", NameZhCN: "优惠", NameZhTW: "優惠", NameEn: "Promo",
			NameJa: "セール", NameKo: "할인", NameEs: "Oferta", NameFr: "Promo",
			NameDe: "Angebot", NameRu: "Акция", NameAr: "عرض", NamePt: "Promo",
			NameVi: "Ưu đãi", NameTh: "โปรโมชั่น", NameId: "Promo", NameHi: "प्रोमो",
			NameIt: "Promo", NameNl: "Promo", NamePl: "Promocja", NameTr: "Promosyon",
			NameMs: "Promo", NameFil: "Promo", NameHe: "מבצע", NameFa: "تخفیف",
			Color: "amber", Icon: "tag", Category: "user", Priority: 80, IsActive: true},

		{Key: "new", NameZhCN: "新品", NameZhTW: "新品", NameEn: "New",
			NameJa: "新着", NameKo: "신규", NameEs: "Nuevo", NameFr: "Nouveau",
			NameDe: "Neu", NameRu: "Новый", NameAr: "جديد", NamePt: "Novo",
			NameVi: "Mới", NameTh: "ใหม่", NameId: "Baru", NameHi: "नया",
			NameIt: "Nuovo", NameNl: "Nieuw", NamePl: "Nowy", NameTr: "Yeni",
			NameMs: "Baru", NameFil: "Bago", NameHe: "חדש", NameFa: "جدید",
			Color: "emerald", Icon: "sparkles", Category: "user", Priority: 70, IsActive: true},

		{Key: "free", NameZhCN: "免费", NameZhTW: "免費", NameEn: "Free",
			NameJa: "無料", NameKo: "무료", NameEs: "Gratis", NameFr: "Gratuit",
			NameDe: "Kostenlos", NameRu: "Бесплатно", NameAr: "مجاني", NamePt: "Grátis",
			NameVi: "Miễn phí", NameTh: "ฟรี", NameId: "Gratis", NameHi: "मुफ्त",
			NameIt: "Gratis", NameNl: "Gratis", NamePl: "Darmowy", NameTr: "Ücretsiz",
			NameMs: "Percuma", NameFil: "Libre", NameHe: "חינם", NameFa: "رایگان",
			Color: "emerald", Icon: "gift", Category: "user", Priority: 60, IsActive: true},

		{Key: "featured", NameZhCN: "推荐", NameZhTW: "推薦", NameEn: "Featured",
			NameJa: "おすすめ", NameKo: "추천", NameEs: "Destacado", NameFr: "Recommandé",
			NameDe: "Empfohlen", NameRu: "Рекомендуемое", NameAr: "مميز", NamePt: "Destaque",
			NameVi: "Nổi bật", NameTh: "แนะนำ", NameId: "Unggulan", NameHi: "फीचर्ड",
			NameIt: "In evidenza", NameNl: "Uitgelicht", NamePl: "Polecane", NameTr: "Öne Çıkan",
			NameMs: "Unggulan", NameFil: "Tampok", NameHe: "מומלץ", NameFa: "پیشنهاد ویژه",
			Color: "blue", Icon: "star", Category: "user", Priority: 50, IsActive: true},

		{Key: "beta", NameZhCN: "测试版", NameZhTW: "測試版", NameEn: "Beta",
			NameJa: "ベータ", NameKo: "베타", NameEs: "Beta", NameFr: "Bêta",
			NameDe: "Beta", NameRu: "Бета", NameAr: "تجريبي", NamePt: "Beta",
			Color: "purple", Icon: "flask-conical", Category: "user", Priority: 30, IsActive: true},

		{Key: "deprecated", NameZhCN: "即将下线", NameZhTW: "即將下線", NameEn: "Deprecated",
			NameJa: "廃止予定", NameKo: "지원 종료 예정", NameEs: "Obsoleto", NameFr: "Obsolète",
			NameDe: "Veraltet", NameRu: "Устарело", NameAr: "مهمل", NamePt: "Obsoleto",
			Color: "gray", Icon: "archive", Category: "user", Priority: 10, IsActive: true},

		// ============ 价格相关系统标识（category=pricing）============
		{Key: "needs_pricing", NameZhCN: "待定价", NameZhTW: "待定價", NameEn: "Needs Pricing",
			NameJa: "価格未設定", NameKo: "가격 미정", NameEs: "Precio pendiente", NameFr: "Prix à définir",
			NameDe: "Preis offen", NameRu: "Нужна цена", NamePt: "Preço pendente",
			Color: "amber", Icon: "alert-circle", Category: "pricing", Priority: 20, IsActive: true},

		{Key: "needs_sell_price", NameZhCN: "待设售价", NameZhTW: "待設售價", NameEn: "Needs Sell Price",
			NameJa: "販売価格未設定", NameKo: "판매가 미정", NameEs: "Precio venta pendiente",
			NameFr: "Prix de vente à définir", NameDe: "Verkaufspreis offen", NameRu: "Нужна цена продажи",
			Color: "amber", Icon: "alert-circle", Category: "pricing", Priority: 20, IsActive: true},

		// ============ 系统内部审核标识（category=system）============
		{Key: "needs_review", NameZhCN: "待审核", NameZhTW: "待審核", NameEn: "Needs Review",
			NameJa: "審査待ち", NameKo: "검토 필요", NameEs: "Pendiente revisión",
			NameFr: "À examiner", NameDe: "Prüfung erforderlich", NameRu: "Требуется проверка",
			NameAr: "بحاجة إلى مراجعة", NamePt: "Aguarda revisão", NameVi: "Cần xét duyệt",
			NameTh: "รอตรวจสอบ", NameId: "Perlu ditinjau", NameHi: "समीक्षा आवश्यक",
			Color: "amber", Icon: "alert-circle", Category: "system", Priority: 25, IsActive: true},

		// ============ 供应商品牌（category=brand）============
		{Key: "qwen", NameZhCN: "通义千问", NameZhTW: "通義千問", NameEn: "Qwen",
			Color: "blue", Category: "brand", IsActive: true},
		{Key: "doubao", NameZhCN: "豆包", NameZhTW: "豆包", NameEn: "Doubao",
			Color: "blue", Category: "brand", IsActive: true},
		{Key: "ernie", NameZhCN: "文心", NameZhTW: "文心", NameEn: "ERNIE",
			Color: "blue", Category: "brand", IsActive: true},
		{Key: "hunyuan", NameZhCN: "混元", NameZhTW: "混元", NameEn: "Hunyuan",
			Color: "blue", Category: "brand", IsActive: true},
		{Key: "deepseek", NameZhCN: "深度求索", NameZhTW: "深度求索", NameEn: "DeepSeek",
			Color: "blue", Category: "brand", IsActive: true},
		{Key: "claude", NameZhCN: "Claude", NameZhTW: "Claude", NameEn: "Claude",
			Color: "blue", Category: "brand", IsActive: true},
		{Key: "openai", NameZhCN: "OpenAI", NameZhTW: "OpenAI", NameEn: "OpenAI",
			Color: "blue", Category: "brand", IsActive: true},
		{Key: "gemini", NameZhCN: "Gemini", NameZhTW: "Gemini", NameEn: "Gemini",
			Color: "blue", Category: "brand", IsActive: true},
		{Key: "moonshot", NameZhCN: "月之暗面", NameZhTW: "月之暗面", NameEn: "Moonshot",
			Color: "blue", Category: "brand", IsActive: true},
		{Key: "glm", NameZhCN: "智谱", NameZhTW: "智譜", NameEn: "ChatGLM",
			Color: "blue", Category: "brand", IsActive: true},
		{Key: "minimax", NameZhCN: "MiniMax", NameZhTW: "MiniMax", NameEn: "MiniMax",
			Color: "blue", Category: "brand", IsActive: true},
	}

	if err := db.Create(&labels).Error; err != nil {
		log.Warn("seed_label_dictionary: 批量写入失败", zap.Error(err))
		return
	}

	log.Info("seed_label_dictionary: 完成",
		zap.Int("inserted", len(labels)),
		zap.Duration("duration", time.Since(start)))
}
