package database

import (
	"tokenhub-server/internal/model"
)

// RunSeedTrendingModels 棣栨鍚姩鍐欏叆鐑棬妯″瀷鍙傝€冩暟鎹紝琛ㄩ潪绌哄垯璺宠繃
// 鏁版嵁鏉ユ簮锛歄penRouter 鍟嗕笟璋冪敤閲忔帓鍚嶃€佸悇渚涘簲鍟嗗畼鏂瑰彂甯冧細銆佹柊鍗庣綉/鏂版氮璐㈢粡/IT涔嬪绛夋潈濞佸獟浣?// 鍏虫敞鐐癸細2025H2 鑷?2026 骞村彂甯冪殑鏈€鏂板晢涓氬寲妯″瀷
func RunSeedTrendingModels() error {
	return seedTrendingModels()
}

func seedTrendingModels() error {
	var count int64
	if err := DB.Model(&model.TrendingModel{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	models := []model.TrendingModel{

		// 鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲
		// DeepSeek锛堟繁搴︽眰绱級
		// 鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲
		{ModelName: "deepseek-v3.2", DisplayName: "DeepSeek V3.2", SupplierName: "DeepSeek", LaunchYearMonth: "2025-09", PopularityStars: 5, ModelType: "LLM",
			Description: "绋€鐤忔敞鎰忓姏鏋舵瀯锛屾帹鐞嗘晥鐜囧ぇ骞呮彁鍗囷紝OpenRouter 鍟嗕笟璋冪敤閲忔寔缁墠浜?",
			SourceURL:   "https://openrouter.ai/deepseek/deepseek-v3.2"},
		{ModelName: "deepseek-v3.1-terminus", DisplayName: "DeepSeek V3.1 Terminus", SupplierName: "DeepSeek", LaunchYearMonth: "2025-09", PopularityStars: 4, ModelType: "LLM",
			Description: "V3.1 缁堟瀬鐗堬紝164K 涓婁笅鏂囷紝缂栫▼涓庢帹鐞嗚兘鍔涙寔缁紭鍖?",
			SourceURL:   "https://api-docs.deepseek.com/quick_start/pricing"},
		{ModelName: "deepseek-r1-0528", DisplayName: "DeepSeek R1-0528", SupplierName: "DeepSeek", LaunchYearMonth: "2025-05", PopularityStars: 5, ModelType: "Reasoning",
			Description: "R1 閲嶅ぇ鏇存柊锛屽够瑙夊ぇ骞呴檷浣庯紝缂栫▼鑳藉姏鎺ヨ繎 Claude 3.5 Sonnet",
			SourceURL:   "https://api-docs.deepseek.com/news/news0528"},
		{ModelName: "deepseek-chat", DisplayName: "DeepSeek V3锛堝璇濓級", SupplierName: "DeepSeek", LaunchYearMonth: "2024-12", PopularityStars: 5, ModelType: "LLM",
			Description: "瀹樻柟 API 鍏ュ彛锛屽簳灞傝嚜鍔ㄥ崌绾ц嚦鏈€鏂扮増鏈紝鍏ㄧ悆鍟嗕笟璋冪敤閲忛鍏?",
			SourceURL:   "https://api-docs.deepseek.com/quick_start/pricing"},
		{ModelName: "deepseek-reasoner", DisplayName: "DeepSeek R1锛堟帹鐞嗭級", SupplierName: "DeepSeek", LaunchYearMonth: "2025-01", PopularityStars: 5, ModelType: "Reasoning",
			Description: "瀹樻柟鎺ㄧ悊 API 鍏ュ彛锛屾繁搴︽€濊€冨紩鍙戝叏鐞?AI 鎴愭湰闈╁懡",
			SourceURL:   "https://api-docs.deepseek.com/news/news0120"},

		// 鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲
		// 闃块噷浜戯紙閫氫箟鍗冮棶 Qwen锛?		// 鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲
		{ModelName: "qwen-3.6-plus", DisplayName: "閫氫箟鍗冮棶 Qwen 3.6 Plus", SupplierName: "闃块噷浜?", LaunchYearMonth: "2026-04", PopularityStars: 5, ModelType: "LLM",
			Description: "鐧句竾 Token 涓婁笅鏂囷紝缂栫▼鑳藉姏瓒?Claude Opus 4.5锛?2B/18B 婵€娲?MoE锛孫penRouter 鏃ユ绗竴",
			SourceURL:   "https://finance.sina.com.cn/jjxw/2026-04-02/doc-inhtavkf9494501.shtml"},
		{ModelName: "qwen-3.6-flash", DisplayName: "閫氫箟鍗冮棶 Qwen 3.6 Flash", SupplierName: "闃块噷浜?", LaunchYearMonth: "2026-04", PopularityStars: 4, ModelType: "LLM",
			Description: "Qwen 3.6 鏋侀€熺増锛岃秴蹇帹鐞嗭紝鎬т环姣旀渶浼橈紝閫傚悎楂樺苟鍙戝晢鐢?",
			SourceURL:   "https://help.aliyun.com/zh/model-studio/getting-started/models"},
		{ModelName: "qwen3-max", DisplayName: "閫氫箟鍗冮棶 Qwen3-Max", SupplierName: "闃块噷浜?", LaunchYearMonth: "2025-04", PopularityStars: 5, ModelType: "Reasoning",
			Description: "閫氫箟鍗冮棶 3 浠ｅ晢涓氭棗鑸帮紝娣峰悎鎺ㄧ悊妯″紡锛屽叏鐞冨椤瑰熀鍑?SOTA",
			SourceURL:   "https://qwenlm.github.io/blog/qwen3/"},
		{ModelName: "qwen3-235b-a22b", DisplayName: "Qwen3-235B-A22B", SupplierName: "闃块噷浜?", LaunchYearMonth: "2025-04", PopularityStars: 5, ModelType: "Reasoning",
			Description: "Qwen3 鏈€澶?MoE 寮€婧愭棗鑸帮紝235B/22B 婵€娲伙紝鏀寔娣峰悎鎬濊€?闈炴€濊€冨弻妯″紡",
			SourceURL:   "https://qwenlm.github.io/blog/qwen3/"},
		{ModelName: "qwen3-plus", DisplayName: "閫氫箟鍗冮棶 Qwen3-Plus", SupplierName: "闃块噷浜?", LaunchYearMonth: "2025-04", PopularityStars: 4, ModelType: "Reasoning",
			Description: "Qwen3 鍟嗙敤鍧囪　鐗堬紝鎬ц兘涓庢垚鏈渶浣冲钩琛★紝浼佷笟绾ч儴缃查閫?",
			SourceURL:   "https://help.aliyun.com/zh/model-studio/getting-started/models"},
		{ModelName: "qwq-plus", DisplayName: "QwQ-Plus", SupplierName: "闃块噷浜?", LaunchYearMonth: "2025-03", PopularityStars: 4, ModelType: "Reasoning",
			Description: "閫氫箟鍗冮棶鎺ㄧ悊澧炲己鐗堬紝鏁板/浠ｇ爜/绉戝鎺ㄧ悊鍟嗕笟鍖栨棗鑸?",
			SourceURL:   "https://help.aliyun.com/zh/model-studio/getting-started/models"},
		{ModelName: "qvq-max", DisplayName: "QvQ-Max", SupplierName: "闃块噷浜?", LaunchYearMonth: "2025-03", PopularityStars: 4, ModelType: "VLM",
			Description: "瑙嗚鎺ㄧ悊鏃楄埌锛屽浘鍍?瑙嗛+娣卞害鎬濊€冿紝璺ㄦā鎬佸鏉傛帹鐞嗚兘鍔?",
			SourceURL:   "https://help.aliyun.com/zh/model-studio/getting-started/models"},
		{ModelName: "qwen2.5-vl-72b-instruct", DisplayName: "Qwen2.5-VL-72B", SupplierName: "闃块噷浜?", LaunchYearMonth: "2025-01", PopularityStars: 4, ModelType: "VLM",
			Description: "瑙嗚鐞嗚В寮€婧愭棗鑸帮紝鏂囨。/鍥捐〃/瑙嗛娣卞害鐞嗚В锛屾敮鎸佷换鎰忓垎杈ㄧ巼",
			SourceURL:   "https://qwenlm.github.io/blog/qwen2.5-vl/"},
		{ModelName: "qwen-long", DisplayName: "Qwen-Long锛堝崈涓囧瓧闀挎枃锛?", SupplierName: "闃块噷浜?", LaunchYearMonth: "2024-09", PopularityStars: 3, ModelType: "LLM",
			Description: "瓒呴暱涓婁笅鏂囦笓鐢ㄦā鍨嬶紝鏀寔 1000 涓?Token锛岄暱鏂囨。澶勭悊棣栭€?",
			SourceURL:   "https://help.aliyun.com/zh/model-studio/getting-started/models"},

		// 鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲
		// 瀛楄妭璺冲姩 / 鐏北寮曟搸锛堣眴鍖?Doubao + Seedance + Seedream锛?		// 鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲
		{ModelName: "doubao-seed-2.0-pro", DisplayName: "璞嗗寘 Seed 2.0 Pro", SupplierName: "鐏北寮曟搸", LaunchYearMonth: "2026-02", PopularityStars: 5, ModelType: "Reasoning",
			Description: "璞嗗寘鏈€寮哄晢涓氭棗鑸帮紝MoE 鏋舵瀯鎺ㄧ悊瀵规爣 GPT-5.2 / Gemini 3 Pro锛孫penRouter 鍓嶄簲",
			SourceURL:   "https://www.guancha.cn/economy/2026_02_14_807208.shtml"},
		{ModelName: "doubao-seed-2.0-lite", DisplayName: "璞嗗寘 Seed 2.0 Lite", SupplierName: "鐏北寮曟搸", LaunchYearMonth: "2026-02", PopularityStars: 4, ModelType: "LLM",
			Description: "Seed 2.0 鎬т环姣旂増鏈紝杈撳叆浠?0.6 鍏?鐧句竾 Token锛屽ぇ瑙勬ā鍟嗙敤浼橀€?",
			SourceURL:   "http://www.news.cn/tech/20260211/1ed7ca6ab28143928fb62d2a65b88228/c.html"},
		{ModelName: "doubao-seed-2.0-mini", DisplayName: "璞嗗寘 Seed 2.0 Mini", SupplierName: "鐏北寮曟搸", LaunchYearMonth: "2026-02", PopularityStars: 3, ModelType: "LLM",
			Description: "Seed 2.0 瓒呰交閲忕増锛屾瀬閫熷搷搴旓紝楂樺苟鍙戝璇濆満鏅閫?",
			SourceURL:   "http://www.news.cn/tech/20260211/1ed7ca6ab28143928fb62d2a65b88228/c.html"},
		{ModelName: "doubao-seed-2.0-code", DisplayName: "璞嗗寘 Seed 2.0 Code", SupplierName: "鐏北寮曟搸", LaunchYearMonth: "2026-02", PopularityStars: 4, ModelType: "LLM",
			Description: "Seed 2.0 浠ｇ爜涓撻」鐗堬紝澶嶆潅宸ョ▼浠ｇ爜鐢熸垚涓庤皟璇曡兘鍔涚獊鍑?",
			SourceURL:   "https://news.qq.com/rain/a/20260215A04N5J00"},
		{ModelName: "seedance-2.0", DisplayName: "Seedance 2.0锛堣棰戠敓鎴愶級", SupplierName: "鐏北寮曟搸", LaunchYearMonth: "2026-02", PopularityStars: 5, ModelType: "VideoGeneration",
			Description: "鐢靛奖绾у妯℃€佽棰戠敓鎴愶紝60 绉?2K 瑙嗛锛孌B-DiT 鏋舵瀯闊崇敾鍚屾锛孍lo 1269 鍏ㄧ悆绗竴",
			SourceURL:   "https://finance.sina.com.cn/china/2026-02-12/doc-inhmpqkp4075366.shtml"},
		{ModelName: "seedream-5.0-lite", DisplayName: "Seedream 5.0 Lite锛堝浘鍍忕敓鎴愶級", SupplierName: "鐏北寮曟搸", LaunchYearMonth: "2026-02", PopularityStars: 5, ModelType: "ImageGeneration",
			Description: "鏅鸿兘鎺ㄧ悊鍥惧儚鐢熸垚锛屽疄鏃舵悳绱?绮剧‘缂栬緫锛屽妯℃€佽瀺鍚堬紝鎴愭湰杈?5.0 闄嶄綆 22%",
			SourceURL:   "https://finance.sina.com.cn/roll/2026-02-12/doc-inhmpqkh3100490.shtml"},
		{ModelName: "seedream-4.0", DisplayName: "Seedream 4.0锛堝浘鍍忕敓鎴愶級", SupplierName: "鐏北寮曟搸", LaunchYearMonth: "2025-09", PopularityStars: 5, ModelType: "ImageGeneration",
			Description: "4K 瓒呴珮娓呭浘鍍忕敓鎴愶紝鎺ㄧ悊閫熷害鎻愬崌 10 鍊嶏紝Artificial Analysis 鏂囩敓鍥惧叏鐞冪涓€",
			SourceURL:   "https://developer.volcengine.com/articles/7553203414411247643"},
		{ModelName: "doubao-1-5-thinking-pro-250415", DisplayName: "璞嗗寘 1.5 Thinking Pro", SupplierName: "鐏北寮曟搸", LaunchYearMonth: "2025-04", PopularityStars: 5, ModelType: "Reasoning",
			Description: "璞嗗寘娣卞害鎬濊€冩棗鑸帮紝鏁板绔炶禌/浠ｇ爜鎺ㄧ悊瓒呰秺鍥藉唴涓绘祦鎺ㄧ悊妯″瀷",
			SourceURL:   "https://www.volcengine.com/docs/82379/1801935"},
		{ModelName: "doubao-seed-1-5-vl-250428", DisplayName: "Seed1.5-VL锛堣瑙夋帹鐞嗭級", SupplierName: "鐏北寮曟搸", LaunchYearMonth: "2025-04", PopularityStars: 5, ModelType: "VLM",
			Description: "瀛楄妭 Seed 瑙嗚鎺ㄧ悊鏃楄埌锛孫penCompass 澶氭ā鎬佹鍗?SOTA",
			SourceURL:   "https://www.volcengine.com/docs/82379/1801935"},
		{ModelName: "doubao-1-5-pro-256k-250115", DisplayName: "璞嗗寘 1.5 Pro 256K", SupplierName: "鐏北寮曟搸", LaunchYearMonth: "2025-01", PopularityStars: 4, ModelType: "LLM",
			Description: "璞嗗寘 1.5 瓒呴暱涓婁笅鏂囩増锛?56K锛岄暱鏂囨。/浠ｇ爜搴撳垎鏋愰閫?",
			SourceURL:   "https://www.volcengine.com/docs/82379/1330310"},

		// 鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲
		// 鑵捐娣峰厓锛圚unyuan锛?		// 鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲
		{ModelName: "hunyuan-hy-2.0-think", DisplayName: "娣峰厓 HY 2.0 Think", SupplierName: "鑵捐", LaunchYearMonth: "2025-12", PopularityStars: 5, ModelType: "Reasoning",
			Description: "鑵捐娣峰厓鍟嗕笟鎺ㄧ悊鏃楄埌锛?06B/32B 婵€娲?MoE锛?56K 涓婁笅鏂囷紝鍥藉唴鎺ㄧ悊绗竴姊槦",
			SourceURL:   "https://www.ithome.com/0/902/856.htm"},
		{ModelName: "hunyuan-hy-2.0-instruct", DisplayName: "娣峰厓 HY 2.0 Instruct", SupplierName: "鑵捐", LaunchYearMonth: "2025-12", PopularityStars: 5, ModelType: "LLM",
			Description: "娣峰厓 HY 2.0 鎸囦护璺熼殢鐗堬紝鏂囨湰鍒涗綔浼樺娍绐佸嚭锛?56K 涓婁笅鏂?",
			SourceURL:   "https://news.qq.com/rain/a/20251206A02VMU00"},
		{ModelName: "hunyuan-turbos-20250416", DisplayName: "娣峰厓 Turbo S", SupplierName: "鑵捐", LaunchYearMonth: "2025-04", PopularityStars: 4, ModelType: "LLM",
			Description: "娣峰厓鏋侀€熺増锛屼綆寤惰繜楂樺苟鍙戯紝鍟嗕笟鍖栧満鏅閫?",
			SourceURL:   "https://cloud.tencent.com/document/product/1729/104753"},
		{ModelName: "hunyuan-t1-20250321", DisplayName: "娣峰厓 T1", SupplierName: "鑵捐", LaunchYearMonth: "2025-03", PopularityStars: 4, ModelType: "Reasoning",
			Description: "鑵捐棣栨鎺ㄧ悊妯″瀷锛屾參鎬濊€冩満鍒讹紝鏁板/閫昏緫/浠ｇ爜鑳藉姏鏄捐憲鎻愬崌",
			SourceURL:   "https://cloud.tencent.com/document/product/1729/104753"},
		{ModelName: "hunyuan-vision-1.5", DisplayName: "娣峰厓 Vision 1.5", SupplierName: "鑵捐", LaunchYearMonth: "2025-06", PopularityStars: 4, ModelType: "VLM",
			Description: "鑵捐澶氭ā鎬佸崌绾х増锛屽浘鏂囪棰戠悊瑙ｈ兘鍔涘ぇ骞呮彁鍗?",
			SourceURL:   "https://cloud.tencent.com/document/product/1729/97731"},
		{ModelName: "hunyuan-large", DisplayName: "娣峰厓 Large锛堝紑婧愶級", SupplierName: "鑵捐", LaunchYearMonth: "2024-11", PopularityStars: 4, ModelType: "LLM",
			Description: "鑵捐寮€婧愭棗鑸帮紝389B/52B 婵€娲?MoE锛屼腑鏂囩悊瑙ｄ笌闀挎枃鏈帹鐞嗛鍏?",
			SourceURL:   "https://cloud.tencent.com/document/product/1729/97731"},

		// 鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲
		// 鐧惧害锛堟枃蹇?ERNIE锛?		// 鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲
		{ModelName: "ernie-5.0-thinking-latest", DisplayName: "鏂囧績 ERNIE 5.0 Thinking", SupplierName: "鐧惧害", LaunchYearMonth: "2026-01", PopularityStars: 5, ModelType: "Reasoning",
			Description: "鏂囧績 5.0 鎺ㄧ悊鐗堬紝2.4 涓囦嚎鍙傛暟锛屽師鐢熷叏妯℃€侊紝LMArena 鍥藉唴绗竴",
			SourceURL:   "https://technews.tw/2026/01/22/ernie-5/"},
		{ModelName: "ernie-5.0", DisplayName: "鏂囧績 ERNIE 5.0", SupplierName: "鐧惧害", LaunchYearMonth: "2026-01", PopularityStars: 5, ModelType: "VLM",
			Description: "鐧惧害鏂囧績绗簲浠ｆ棗鑸帮紝鍘熺敓鍏ㄦā鎬侊紙鏂囨湰/鍥惧儚/闊抽/瑙嗛锛夛紝103 绉嶈瑷€",
			SourceURL:   "https://technews.tw/2026/01/22/ernie-5/"},
		{ModelName: "ernie-4.5-turbo-128k", DisplayName: "鏂囧績 ERNIE 4.5 Turbo", SupplierName: "鐧惧害", LaunchYearMonth: "2025-04", PopularityStars: 4, ModelType: "LLM",
			Description: "鏂囧績 4.5 鏋侀€熺増锛屽畾浠烽檷鑷冲墠浠?20%锛?28K 涓婁笅鏂囧晢涓氬寲钀藉湴",
			SourceURL:   "https://cloud.baidu.com/doc/WENXINWORKSHOP/s/Nlks5zkzu"},
		{ModelName: "ernie-x1-turbo-32k", DisplayName: "鏂囧績 ERNIE X1 Turbo", SupplierName: "鐧惧害", LaunchYearMonth: "2025-04", PopularityStars: 4, ModelType: "Reasoning",
			Description: "鏂囧績 X1 鏋侀€熸帹鐞嗙増锛屽伐鍏疯皟鐢?娣卞害鎼滅储锛屾垚鏈ぇ骞呴檷浣?",
			SourceURL:   "https://cloud.baidu.com/doc/WENXINWORKSHOP/s/Nlks5zkzu"},
		{ModelName: "ernie-4.5-8k", DisplayName: "鏂囧績 ERNIE 4.5", SupplierName: "鐧惧害", LaunchYearMonth: "2025-03", PopularityStars: 4, ModelType: "VLM",
			Description: "鏂囧績 4.5 鍘熺敓澶氭ā鎬佹棗鑸帮紝鍥炬枃鐞嗚В鍏ㄩ潰鍗囩骇锛屾敮鎸佸鍥捐緭鍏?",
			SourceURL:   "https://cloud.baidu.com/doc/WENXINWORKSHOP/s/Nlks5zkzu"},
		{ModelName: "ernie-x1-32k", DisplayName: "鏂囧績 ERNIE X1", SupplierName: "鐧惧害", LaunchYearMonth: "2025-03", PopularityStars: 4, ModelType: "Reasoning",
			Description: "鐧惧害棣栨鎺ㄧ悊妯″瀷锛屽伐鍏疯皟鐢ㄤ笌娣卞害鎼滅储锛屽妯℃€佹帹鐞?",
			SourceURL:   "https://cloud.baidu.com/doc/WENXINWORKSHOP/s/Nlks5zkzu"},

		// 鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲
		// Moonshot AI锛圞imi锛?		// 鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲
		{ModelName: "kimi-k2.5", DisplayName: "Kimi K2.5", SupplierName: "Moonshot", LaunchYearMonth: "2026-01", PopularityStars: 5, ModelType: "VLM",
			Description: "鍘熺敓澶氭ā鎬侊紝1T MoE锛?56K 涓婁笅鏂囷紝Agent Swarm 闆嗙兢鏅鸿兘锛孫penRouter 2鏈堢浜岋紙4.02涓囦嚎Token锛?",
			SourceURL:   "https://www.infoq.cn/article/kadDBSqBrtFfUbipxQSs"},
		{ModelName: "kimi-k2-thinking", DisplayName: "Kimi K2 Thinking", SupplierName: "Moonshot", LaunchYearMonth: "2025-11", PopularityStars: 5, ModelType: "Reasoning",
			Description: "Kimi K2 娣卞害鎬濊€冪増锛?T 鍙傛暟 MoE锛屽鏉傛帹鐞嗕笌 Agent 鑳藉姏",
			SourceURL:   "https://platform.moonshot.cn/docs/introduction"},
		{ModelName: "kimi-k2-0711-preview", DisplayName: "Kimi K2", SupplierName: "Moonshot", LaunchYearMonth: "2025-07", PopularityStars: 5, ModelType: "LLM",
			Description: "1 涓囦嚎鍙傛暟 MoE 寮€婧愭棗鑸帮紝Tool Use 鍏ㄧ悆绗竴锛孉gentic 鑳藉姏绐佸嚭",
			SourceURL:   "https://platform.moonshot.cn/docs/introduction"},
		{ModelName: "kimi-k1.5", DisplayName: "Kimi K1.5", SupplierName: "Moonshot", LaunchYearMonth: "2025-01", PopularityStars: 4, ModelType: "Reasoning",
			Description: "Kimi 鎺ㄧ悊妯″瀷锛岄暱閾炬€濊€冿紝澶氭ā鎬佹敮鎸侊紝MATH-500 鍙戝竷鏃跺叏鐞冪浜?",
			SourceURL:   "https://platform.moonshot.cn/docs/introduction"},
		{ModelName: "moonshot-v1-128k", DisplayName: "Moonshot V1 128K", SupplierName: "Moonshot", LaunchYearMonth: "2024-03", PopularityStars: 4, ModelType: "LLM",
			Description: "Kimi 缁忓吀闀挎枃鏈棗鑸帮紝128K 涓婁笅鏂囷紝鍥藉唴鏈€鏃╁晢涓氬寲闀夸笂涓嬫枃妯″瀷",
			SourceURL:   "https://platform.moonshot.cn/docs/introduction"},

		// 鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲
		// 鏅鸿氨锛圙LM锛?		// 鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲
		{ModelName: "glm-5", DisplayName: "GLM-5", SupplierName: "鏅鸿氨", LaunchYearMonth: "2026-02", PopularityStars: 5, ModelType: "LLM",
			Description: "鏅鸿氨绗簲浠ｅ紑婧愭棗鑸帮紝缂栫▼瀵规爣 Claude Opus 4.5锛孫penRouter 2鏈堝墠浜旓紝寮€婧愮涓€",
			SourceURL:   "https://36kr.com/p/3679611307617928"},
		{ModelName: "glm-5-plus", DisplayName: "GLM-5 Plus", SupplierName: "鏅鸿氨", LaunchYearMonth: "2026-02", PopularityStars: 4, ModelType: "LLM",
			Description: "GLM-5 杞婚噺鍟嗕笟鐗堬紝绾枃鏈紝浠锋牸杈?GLM-5 闄嶄綆杩?50%",
			SourceURL:   "https://docs.bigmodel.cn/cn/guide/start/model-overview"},
		{ModelName: "glm-4.6", DisplayName: "GLM-4.6", SupplierName: "鏅鸿氨", LaunchYearMonth: "2025-09", PopularityStars: 4, ModelType: "LLM",
			Description: "GLM 寮€婧愮増锛屼唬鐮佺敓鎴?SOTA锛岃瑙夋帹鐞嗚兘鍔涘己锛?00K 涓婁笅鏂?",
			SourceURL:   "https://docs.bigmodel.cn/cn/guide/start/model-overview"},
		{ModelName: "glm-4.5", DisplayName: "GLM-4.5", SupplierName: "鏅鸿氨", LaunchYearMonth: "2025-07", PopularityStars: 4, ModelType: "LLM",
			Description: "Agent 鍩哄骇妯″瀷锛屾帹鐞?缂栫爜/宸ュ叿璋冪敤鍏ㄩ潰铻嶅悎",
			SourceURL:   "https://docs.bigmodel.cn/cn/guide/start/model-overview"},
		{ModelName: "glm-z1-plus", DisplayName: "GLM-Z1 Plus", SupplierName: "鏅鸿氨", LaunchYearMonth: "2025-04", PopularityStars: 4, ModelType: "Reasoning",
			Description: "鏅鸿氨鎺ㄧ悊鏃楄埌锛岃仈缃戞悳绱?娣卞害鎬濊€冿紝鍟嗕笟绾у鏉備换鍔″鐞?",
			SourceURL:   "https://open.bigmodel.cn/dev/api"},
		{ModelName: "glm-4-plus", DisplayName: "GLM-4 Plus", SupplierName: "鏅鸿氨", LaunchYearMonth: "2024-09", PopularityStars: 3, ModelType: "LLM",
			Description: "鏅鸿氨绋冲畾鍟嗕笟鏃楄埌锛?28K 涓婁笅鏂囷紝鍑芥暟璋冪敤鑳藉姏浼樼",
			SourceURL:   "https://open.bigmodel.cn/pricing"},
		{ModelName: "glm-4-flash", DisplayName: "GLM-4 Flash锛堝厤璐癸級", SupplierName: "鏅鸿氨", LaunchYearMonth: "2024-09", PopularityStars: 4, ModelType: "LLM",
			Description: "鏅鸿氨鍏嶈垂鏋侀€熸ā鍨嬶紝128K 涓婁笅鏂囷紝楂樺苟鍙戝満鏅閫?",
			SourceURL:   "https://open.bigmodel.cn/pricing"},
		{ModelName: "glm-image", DisplayName: "GLM Image锛堝浘鍍忕敓鎴愶級", SupplierName: "鏅鸿氨", LaunchYearMonth: "2025-08", PopularityStars: 4, ModelType: "ImageGeneration",
			Description: "鏅鸿氨鍥惧儚鐢熸垚锛屾枃鏈覆鏌撳紑婧?SOTA锛屼腑鏂囨捣鎶ヨ璁″己",
			SourceURL:   "https://docs.bigmodel.cn/cn/guide/start/model-overview"},
		{ModelName: "cogvideox-3", DisplayName: "CogVideoX-3锛堣棰戠敓鎴愶級", SupplierName: "鏅鸿氨", LaunchYearMonth: "2025-06", PopularityStars: 4, ModelType: "VideoGeneration",
			Description: "澶氬垎杈ㄧ巼瑙嗛鐢熸垚锛屾敮鎸佸浘鐢熻棰?鏂囩敓瑙嗛/瑙嗛寤跺睍",
			SourceURL:   "https://docs.bigmodel.cn/cn/guide/start/model-overview"},

		// 鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲
		// MiniMax
		// 鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲
		{ModelName: "minimax-music-2.6", DisplayName: "MiniMax Music 2.6", SupplierName: "MiniMax", LaunchYearMonth: "2026-04", PopularityStars: 4, ModelType: "MusicGeneration",
			Description: "AI 闊充箰鐢熸垚鏃楄埌锛屼汉澹颁箰鍣ㄦ帶鍒讹紝姝岃瘝浼樺寲锛屽叏鐞?Beta 鍙戝竷",
			SourceURL:   "https://www.minimaxi.com/news/music-26"},
		{ModelName: "minimax-m2.7", DisplayName: "MiniMax M2.7", SupplierName: "MiniMax", LaunchYearMonth: "2026-03", PopularityStars: 5, ModelType: "LLM",
			Description: "閫掑綊鑷敼杩涳紝SWE-Pro 56.22%锛孍LO 1495 寮€婧愭渶楂橈紝100TPS 楂橀€熺増",
			SourceURL:   "https://www.minimaxi.com/news/minimax-m27-en"},
		{ModelName: "minimax-m2.5", DisplayName: "MiniMax M2.5", SupplierName: "MiniMax", LaunchYearMonth: "2026-02", PopularityStars: 5, ModelType: "LLM",
			Description: "OpenRouter 2鏈堣皟鐢ㄩ噺绗竴锛?.55涓囦嚎Token锛夛紝鏈堟敹鍏ョ牬1.5浜跨編鍏?",
			SourceURL:   "https://www.pingwest.com/a/311573"},
		{ModelName: "minimax-m2", DisplayName: "MiniMax M2", SupplierName: "MiniMax", LaunchYearMonth: "2025-10", PopularityStars: 4, ModelType: "LLM",
			Description: "鑷垜杩唬妯″瀷绗簩浠ｏ紝缂栫▼涓?Agent 鑳藉姏鎸佺画杩涘寲",
			SourceURL:   "https://platform.minimaxi.com/document/models"},
		{ModelName: "minimax-m1", DisplayName: "MiniMax M1", SupplierName: "MiniMax", LaunchYearMonth: "2025-05", PopularityStars: 5, ModelType: "Reasoning",
			Description: "棣栨壒鐧句竾 Token 涓婁笅鏂囧紑婧愭帹鐞嗘ā鍨嬶紝80K 鎬濈淮閾撅紝寮哄寲瀛︿範璁粌",
			SourceURL:   "https://platform.minimaxi.com/document/models"},
		{ModelName: "minimax-hailuo-2.3", DisplayName: "娴疯灪 Hailuo 2.3锛堣棰戯級", SupplierName: "MiniMax", LaunchYearMonth: "2025-10", PopularityStars: 4, ModelType: "VideoGeneration",
			Description: "娴疯灪瑙嗛鐢熸垚鏃楄埌锛岀墿鐞嗚繍鍔ㄧ簿鍑嗭紝720P 楂樻竻杈撳嚭",
			SourceURL:   "https://platform.minimaxi.com/document/models"},
		{ModelName: "minimax-speech-2.6", DisplayName: "MiniMax Speech 2.6", SupplierName: "MiniMax", LaunchYearMonth: "2025-10", PopularityStars: 3, ModelType: "TTS",
			Description: "楂樹繚鐪熻闊冲悎鎴愶紝鎯呮劅涓庨煶鑹叉帶鍒剁簿鍑嗭紝鏀寔澶氳瑷€",
			SourceURL:   "https://platform.minimaxi.com/document/models"},

		// 鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲
		// 璁鏄熺伀锛圫park锛?		// 鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲
		{ModelName: "spark-x1.5", DisplayName: "鏄熺伀 X1.5", SupplierName: "璁", LaunchYearMonth: "2025-11", PopularityStars: 5, ModelType: "Reasoning",
			Description: "293B/30B 婵€娲?MoE 鎺ㄧ悊妯″瀷锛屾帹鐞嗘晥鐜囨彁鍗?100%锛屾敮鎸?130+ 璇█",
			SourceURL:   "https://finance.sina.com.cn/jjxw/2025-11-11/doc-infwzezw1996297.shtml"},
		{ModelName: "spark4-ultra", DisplayName: "鏄熺伀 4.0 Ultra", SupplierName: "璁", LaunchYearMonth: "2024-08", PopularityStars: 4, ModelType: "LLM",
			Description: "璁鍟嗕笟鏃楄埌锛屽鏍?GPT-4 Turbo锛岀敓鎴愰€熷害鎻愬崌 70%",
			SourceURL:   "https://xinghuo.xfyun.cn/sparkapi"},
		{ModelName: "spark-max", DisplayName: "鏄熺伀 Max", SupplierName: "璁", LaunchYearMonth: "2024-06", PopularityStars: 3, ModelType: "LLM",
			Description: "璁涓撲笟鐗堬紝澶嶆潅鎺ㄧ悊涓庤涓氱煡璇嗛棶绛旓紝浼佷笟绾ф枃妗ｇ悊瑙?",
			SourceURL:   "https://xinghuo.xfyun.cn/sparkapi"},

		// 鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲
		// 闃惰穬鏄熻景锛圫tepFun锛?		// 鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲
		{ModelName: "step-3.5-flash", DisplayName: "Step 3.5 Flash", SupplierName: "闃惰穬鏄熻景", LaunchYearMonth: "2026-02", PopularityStars: 5, ModelType: "LLM",
			Description: "1960B/110B 婵€娲?MoE锛屽疄鏃?Agent 宸ョ▼浼樺寲锛?50 tokens/s锛孫penRouter Trending 绗竴",
			SourceURL:   "https://www.qbitai.com/2026/02/375351.html"},
		{ModelName: "step-3", DisplayName: "Step-3", SupplierName: "闃惰穬鏄熻景", LaunchYearMonth: "2025-07", PopularityStars: 4, ModelType: "LLM",
			Description: "寮€婧愭棗鑸帮紝鍥戒骇鑺帹鐞嗘晥鐜囪揪 DeepSeek R1 鐨?3 鍊嶏紝澶氭ā鎬佹敮鎸?",
			SourceURL:   "https://www.infoq.cn/article/9ishp2ykyqs7auwsd9if"},
		{ModelName: "step-2-16k", DisplayName: "Step-2", SupplierName: "闃惰穬鏄熻景", LaunchYearMonth: "2024-07", PopularityStars: 4, ModelType: "LLM",
			Description: "涓囦嚎鍙傛暟 MoE锛孋-Eval/CMMLU 鍥藉唴鏉冨▉姒滃崟绗竴鍚?",
			SourceURL:   "https://platform.stepfun.com/docs/llm/text"},
		{ModelName: "step-2-mini", DisplayName: "Step-2 Mini", SupplierName: "闃惰穬鏄熻景", LaunchYearMonth: "2024-12", PopularityStars: 3, ModelType: "LLM",
			Description: "Step-2 杞婚噺鐗堬紝MFA 娉ㄦ剰鍔涳紝浣庢垚鏈揩閫熸帹鐞?",
			SourceURL:   "https://platform.stepfun.com/docs/llm/text"},
		{ModelName: "step-1v-8k", DisplayName: "Step-1V", SupplierName: "闃惰穬鏄熻景", LaunchYearMonth: "2024-04", PopularityStars: 3, ModelType: "VLM",
			Description: "闃惰穬鏄熻景澶氭ā鎬佹ā鍨嬶紝鍥炬枃鐞嗚В锛屽鏉傚浘鍍忓垎鏋愪笌闂瓟",
			SourceURL:   "https://platform.stepfun.com/docs/llm/text"},

		// 鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲
		// 闆朵竴涓囩墿锛圷i / 01.AI锛?		// 鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲鈺愨晲
		{ModelName: "yi-lightning", DisplayName: "Yi-Lightning", SupplierName: "闆朵竴涓囩墿", LaunchYearMonth: "2024-09", PopularityStars: 4, ModelType: "LLM",
			Description: "闆朵竴涓囩墿鏋侀€熸棗鑸帮紝鍥藉唴棣栨瓒呰秺 GPT-4o锛屾帹鐞嗛€熷害鎻愬崌 50%",
			SourceURL:   "https://platform.lingyiwanwu.com/docs"},
		{ModelName: "yi-vision", DisplayName: "Yi-Vision", SupplierName: "闆朵竴涓囩墿", LaunchYearMonth: "2024-07", PopularityStars: 3, ModelType: "VLM",
			Description: "闆朵竴涓囩墿瑙嗚妯″瀷锛屽浘鍍忕悊瑙ｄ笌瑙嗚闂瓟锛屼腑鏂囧満鏅紭鍖?",
			SourceURL:   "https://platform.lingyiwanwu.com/docs"},
		{ModelName: "yi-large", DisplayName: "Yi-Large", SupplierName: "闆朵竴涓囩墿", LaunchYearMonth: "2024-05", PopularityStars: 3, ModelType: "LLM",
			Description: "闆朵竴涓囩墿澶ц妯″晢涓氭ā鍨嬶紝200K 涓婁笅鏂囷紝涓嫳鍙岃鑳藉姏鍧囪　",
			SourceURL:   "https://platform.lingyiwanwu.com/docs"},
	}

	return DB.Create(&models).Error
}
