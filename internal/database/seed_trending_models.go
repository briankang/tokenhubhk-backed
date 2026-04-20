package database

import (
	"tokenhub-server/internal/model"
)

// RunSeedTrendingModels 首次启动写入热门模型参考数据，表非空则跳过
// 数据来源：OpenRouter 商业调用量排名、各供应商官方发布会、新华网/新浪财经/IT之家等权威媒体
// 关注点：2025H2 至 2026 年发布的最新商业化模型
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

		// ════════════════════════════════════════════════════════════
		// DeepSeek（深度求索）
		// ════════════════════════════════════════════════════════════
		{ModelName: "deepseek-v3.2", DisplayName: "DeepSeek V3.2", SupplierName: "DeepSeek", LaunchYearMonth: "2025-09", PopularityStars: 5, ModelType: "LLM",
			Description: "稀疏注意力架构，推理效率大幅提升，OpenRouter 商业调用量持续前五",
			SourceURL:   "https://openrouter.ai/deepseek/deepseek-v3.2"},
		{ModelName: "deepseek-v3.1-terminus", DisplayName: "DeepSeek V3.1 Terminus", SupplierName: "DeepSeek", LaunchYearMonth: "2025-09", PopularityStars: 4, ModelType: "LLM",
			Description: "V3.1 终极版，164K 上下文，编程与推理能力持续优化",
			SourceURL:   "https://api-docs.deepseek.com/quick_start/pricing"},
		{ModelName: "deepseek-r1-0528", DisplayName: "DeepSeek R1-0528", SupplierName: "DeepSeek", LaunchYearMonth: "2025-05", PopularityStars: 5, ModelType: "Reasoning",
			Description: "R1 重大更新，幻觉大幅降低，编程能力接近 Claude 3.5 Sonnet",
			SourceURL:   "https://api-docs.deepseek.com/news/news0528"},
		{ModelName: "deepseek-chat", DisplayName: "DeepSeek V3（对话）", SupplierName: "DeepSeek", LaunchYearMonth: "2024-12", PopularityStars: 5, ModelType: "LLM",
			Description: "官方 API 入口，底层自动升级至最新版本，全球商业调用量领先",
			SourceURL:   "https://api-docs.deepseek.com/quick_start/pricing"},
		{ModelName: "deepseek-reasoner", DisplayName: "DeepSeek R1（推理）", SupplierName: "DeepSeek", LaunchYearMonth: "2025-01", PopularityStars: 5, ModelType: "Reasoning",
			Description: "官方推理 API 入口，深度思考引发全球 AI 成本革命",
			SourceURL:   "https://api-docs.deepseek.com/news/news0120"},

		// ════════════════════════════════════════════════════════════
		// 阿里云（通义千问 Qwen）
		// ════════════════════════════════════════════════════════════
		{ModelName: "qwen-3.6-plus", DisplayName: "通义千问 Qwen 3.6 Plus", SupplierName: "阿里云", LaunchYearMonth: "2026-04", PopularityStars: 5, ModelType: "LLM",
			Description: "百万 Token 上下文，编程能力超 Claude Opus 4.5，72B/18B 激活 MoE，OpenRouter 日榜第一",
			SourceURL:   "https://finance.sina.com.cn/jjxw/2026-04-02/doc-inhtavkf9494501.shtml"},
		{ModelName: "qwen-3.6-flash", DisplayName: "通义千问 Qwen 3.6 Flash", SupplierName: "阿里云", LaunchYearMonth: "2026-04", PopularityStars: 4, ModelType: "LLM",
			Description: "Qwen 3.6 极速版，超快推理，性价比最优，适合高并发商用",
			SourceURL:   "https://help.aliyun.com/zh/model-studio/getting-started/models"},
		{ModelName: "qwen3-max", DisplayName: "通义千问 Qwen3-Max", SupplierName: "阿里云", LaunchYearMonth: "2025-04", PopularityStars: 5, ModelType: "Reasoning",
			Description: "通义千问 3 代商业旗舰，混合推理模式，全球多项基准 SOTA",
			SourceURL:   "https://qwenlm.github.io/blog/qwen3/"},
		{ModelName: "qwen3-235b-a22b", DisplayName: "Qwen3-235B-A22B", SupplierName: "阿里云", LaunchYearMonth: "2025-04", PopularityStars: 5, ModelType: "Reasoning",
			Description: "Qwen3 最大 MoE 开源旗舰，235B/22B 激活，支持混合思考/非思考双模式",
			SourceURL:   "https://qwenlm.github.io/blog/qwen3/"},
		{ModelName: "qwen3-plus", DisplayName: "通义千问 Qwen3-Plus", SupplierName: "阿里云", LaunchYearMonth: "2025-04", PopularityStars: 4, ModelType: "Reasoning",
			Description: "Qwen3 商用均衡版，性能与成本最佳平衡，企业级部署首选",
			SourceURL:   "https://help.aliyun.com/zh/model-studio/getting-started/models"},
		{ModelName: "qwq-plus", DisplayName: "QwQ-Plus", SupplierName: "阿里云", LaunchYearMonth: "2025-03", PopularityStars: 4, ModelType: "Reasoning",
			Description: "通义千问推理增强版，数学/代码/科学推理商业化旗舰",
			SourceURL:   "https://help.aliyun.com/zh/model-studio/getting-started/models"},
		{ModelName: "qvq-max", DisplayName: "QvQ-Max", SupplierName: "阿里云", LaunchYearMonth: "2025-03", PopularityStars: 4, ModelType: "VLM",
			Description: "视觉推理旗舰，图像/视频+深度思考，跨模态复杂推理能力",
			SourceURL:   "https://help.aliyun.com/zh/model-studio/getting-started/models"},
		{ModelName: "qwen2.5-vl-72b-instruct", DisplayName: "Qwen2.5-VL-72B", SupplierName: "阿里云", LaunchYearMonth: "2025-01", PopularityStars: 4, ModelType: "VLM",
			Description: "视觉理解开源旗舰，文档/图表/视频深度理解，支持任意分辨率",
			SourceURL:   "https://qwenlm.github.io/blog/qwen2.5-vl/"},
		{ModelName: "qwen-long", DisplayName: "Qwen-Long（千万字长文）", SupplierName: "阿里云", LaunchYearMonth: "2024-09", PopularityStars: 3, ModelType: "LLM",
			Description: "超长上下文专用模型，支持 1000 万 Token，长文档处理首选",
			SourceURL:   "https://help.aliyun.com/zh/model-studio/getting-started/models"},

		// ════════════════════════════════════════════════════════════
		// 字节跳动 / 火山引擎（豆包 Doubao + Seedance + Seedream）
		// ════════════════════════════════════════════════════════════
		{ModelName: "doubao-seed-2.0-pro", DisplayName: "豆包 Seed 2.0 Pro", SupplierName: "火山引擎", LaunchYearMonth: "2026-02", PopularityStars: 5, ModelType: "Reasoning",
			Description: "豆包最强商业旗舰，MoE 架构推理对标 GPT-5.2 / Gemini 3 Pro，OpenRouter 前五",
			SourceURL:   "https://www.guancha.cn/economy/2026_02_14_807208.shtml"},
		{ModelName: "doubao-seed-2.0-lite", DisplayName: "豆包 Seed 2.0 Lite", SupplierName: "火山引擎", LaunchYearMonth: "2026-02", PopularityStars: 4, ModelType: "LLM",
			Description: "Seed 2.0 性价比版本，输入仅 0.6 元/百万 Token，大规模商用优选",
			SourceURL:   "http://www.news.cn/tech/20260211/1ed7ca6ab28143928fb62d2a65b88228/c.html"},
		{ModelName: "doubao-seed-2.0-mini", DisplayName: "豆包 Seed 2.0 Mini", SupplierName: "火山引擎", LaunchYearMonth: "2026-02", PopularityStars: 3, ModelType: "LLM",
			Description: "Seed 2.0 超轻量版，极速响应，高并发对话场景首选",
			SourceURL:   "http://www.news.cn/tech/20260211/1ed7ca6ab28143928fb62d2a65b88228/c.html"},
		{ModelName: "doubao-seed-2.0-code", DisplayName: "豆包 Seed 2.0 Code", SupplierName: "火山引擎", LaunchYearMonth: "2026-02", PopularityStars: 4, ModelType: "LLM",
			Description: "Seed 2.0 代码专项版，复杂工程代码生成与调试能力突出",
			SourceURL:   "https://news.qq.com/rain/a/20260215A04N5J00"},
		{ModelName: "seedance-2.0", DisplayName: "Seedance 2.0（视频生成）", SupplierName: "火山引擎", LaunchYearMonth: "2026-02", PopularityStars: 5, ModelType: "VideoGeneration",
			Description: "电影级多模态视频生成，60 秒 2K 视频，DB-DiT 架构音画同步，Elo 1269 全球第一",
			SourceURL:   "https://finance.sina.com.cn/china/2026-02-12/doc-inhmpqkp4075366.shtml"},
		{ModelName: "seedream-5.0-lite", DisplayName: "Seedream 5.0 Lite（图像生成）", SupplierName: "火山引擎", LaunchYearMonth: "2026-02", PopularityStars: 5, ModelType: "ImageGeneration",
			Description: "智能推理图像生成，实时搜索+精确编辑，多模态融合，成本较 5.0 降低 22%",
			SourceURL:   "https://finance.sina.com.cn/roll/2026-02-12/doc-inhmpqkh3100490.shtml"},
		{ModelName: "seedream-4.0", DisplayName: "Seedream 4.0（图像生成）", SupplierName: "火山引擎", LaunchYearMonth: "2025-09", PopularityStars: 5, ModelType: "ImageGeneration",
			Description: "4K 超高清图像生成，推理速度提升 10 倍，Artificial Analysis 文生图全球第一",
			SourceURL:   "https://developer.volcengine.com/articles/7553203414411247643"},
		{ModelName: "doubao-1-5-thinking-pro-250415", DisplayName: "豆包 1.5 Thinking Pro", SupplierName: "火山引擎", LaunchYearMonth: "2025-04", PopularityStars: 5, ModelType: "Reasoning",
			Description: "豆包深度思考旗舰，数学竞赛/代码推理超越国内主流推理模型",
			SourceURL:   "https://www.volcengine.com/docs/82379/1801935"},
		{ModelName: "doubao-seed-1-5-vl-250428", DisplayName: "Seed1.5-VL（视觉推理）", SupplierName: "火山引擎", LaunchYearMonth: "2025-04", PopularityStars: 5, ModelType: "VLM",
			Description: "字节 Seed 视觉推理旗舰，OpenCompass 多模态榜单 SOTA",
			SourceURL:   "https://www.volcengine.com/docs/82379/1801935"},
		{ModelName: "doubao-1-5-pro-256k-250115", DisplayName: "豆包 1.5 Pro 256K", SupplierName: "火山引擎", LaunchYearMonth: "2025-01", PopularityStars: 4, ModelType: "LLM",
			Description: "豆包 1.5 超长上下文版，256K，长文档/代码库分析首选",
			SourceURL:   "https://www.volcengine.com/docs/82379/1330310"},

		// ════════════════════════════════════════════════════════════
		// 腾讯混元（Hunyuan）
		// ════════════════════════════════════════════════════════════
		{ModelName: "hunyuan-hy-2.0-think", DisplayName: "混元 HY 2.0 Think", SupplierName: "腾讯", LaunchYearMonth: "2025-12", PopularityStars: 5, ModelType: "Reasoning",
			Description: "腾讯混元商业推理旗舰，406B/32B 激活 MoE，256K 上下文，国内推理第一梯队",
			SourceURL:   "https://www.ithome.com/0/902/856.htm"},
		{ModelName: "hunyuan-hy-2.0-instruct", DisplayName: "混元 HY 2.0 Instruct", SupplierName: "腾讯", LaunchYearMonth: "2025-12", PopularityStars: 5, ModelType: "LLM",
			Description: "混元 HY 2.0 指令跟随版，文本创作优势突出，256K 上下文",
			SourceURL:   "https://news.qq.com/rain/a/20251206A02VMU00"},
		{ModelName: "hunyuan-turbos-20250416", DisplayName: "混元 Turbo S", SupplierName: "腾讯", LaunchYearMonth: "2025-04", PopularityStars: 4, ModelType: "LLM",
			Description: "混元极速版，低延迟高并发，商业化场景首选",
			SourceURL:   "https://cloud.tencent.com/document/product/1729/104753"},
		{ModelName: "hunyuan-t1-20250321", DisplayName: "混元 T1", SupplierName: "腾讯", LaunchYearMonth: "2025-03", PopularityStars: 4, ModelType: "Reasoning",
			Description: "腾讯首款推理模型，慢思考机制，数学/逻辑/代码能力显著提升",
			SourceURL:   "https://cloud.tencent.com/document/product/1729/104753"},
		{ModelName: "hunyuan-vision-1.5", DisplayName: "混元 Vision 1.5", SupplierName: "腾讯", LaunchYearMonth: "2025-06", PopularityStars: 4, ModelType: "VLM",
			Description: "腾讯多模态升级版，图文视频理解能力大幅提升",
			SourceURL:   "https://cloud.tencent.com/document/product/1729/97731"},
		{ModelName: "hunyuan-large", DisplayName: "混元 Large（开源）", SupplierName: "腾讯", LaunchYearMonth: "2024-11", PopularityStars: 4, ModelType: "LLM",
			Description: "腾讯开源旗舰，389B/52B 激活 MoE，中文理解与长文本推理领先",
			SourceURL:   "https://cloud.tencent.com/document/product/1729/97731"},

		// ════════════════════════════════════════════════════════════
		// 百度（文心 ERNIE）
		// ════════════════════════════════════════════════════════════
		{ModelName: "ernie-5.0-thinking-latest", DisplayName: "文心 ERNIE 5.0 Thinking", SupplierName: "百度", LaunchYearMonth: "2026-01", PopularityStars: 5, ModelType: "Reasoning",
			Description: "文心 5.0 推理版，2.4 万亿参数，原生全模态，LMArena 国内第一",
			SourceURL:   "https://technews.tw/2026/01/22/ernie-5/"},
		{ModelName: "ernie-5.0", DisplayName: "文心 ERNIE 5.0", SupplierName: "百度", LaunchYearMonth: "2026-01", PopularityStars: 5, ModelType: "VLM",
			Description: "百度文心第五代旗舰，原生全模态（文本/图像/音频/视频），103 种语言",
			SourceURL:   "https://technews.tw/2026/01/22/ernie-5/"},
		{ModelName: "ernie-4.5-turbo-128k", DisplayName: "文心 ERNIE 4.5 Turbo", SupplierName: "百度", LaunchYearMonth: "2025-04", PopularityStars: 4, ModelType: "LLM",
			Description: "文心 4.5 极速版，定价降至前代 20%，128K 上下文商业化落地",
			SourceURL:   "https://cloud.baidu.com/doc/WENXINWORKSHOP/s/Nlks5zkzu"},
		{ModelName: "ernie-x1-turbo-32k", DisplayName: "文心 ERNIE X1 Turbo", SupplierName: "百度", LaunchYearMonth: "2025-04", PopularityStars: 4, ModelType: "Reasoning",
			Description: "文心 X1 极速推理版，工具调用+深度搜索，成本大幅降低",
			SourceURL:   "https://cloud.baidu.com/doc/WENXINWORKSHOP/s/Nlks5zkzu"},
		{ModelName: "ernie-4.5-8k", DisplayName: "文心 ERNIE 4.5", SupplierName: "百度", LaunchYearMonth: "2025-03", PopularityStars: 4, ModelType: "VLM",
			Description: "文心 4.5 原生多模态旗舰，图文理解全面升级，支持多图输入",
			SourceURL:   "https://cloud.baidu.com/doc/WENXINWORKSHOP/s/Nlks5zkzu"},
		{ModelName: "ernie-x1-32k", DisplayName: "文心 ERNIE X1", SupplierName: "百度", LaunchYearMonth: "2025-03", PopularityStars: 4, ModelType: "Reasoning",
			Description: "百度首款推理模型，工具调用与深度搜索，多模态推理",
			SourceURL:   "https://cloud.baidu.com/doc/WENXINWORKSHOP/s/Nlks5zkzu"},

		// ════════════════════════════════════════════════════════════
		// Moonshot AI（Kimi）
		// ════════════════════════════════════════════════════════════
		{ModelName: "kimi-k2.5", DisplayName: "Kimi K2.5", SupplierName: "Moonshot", LaunchYearMonth: "2026-01", PopularityStars: 5, ModelType: "VLM",
			Description: "原生多模态，1T MoE，256K 上下文，Agent Swarm 集群智能，OpenRouter 2月第二（4.02万亿Token）",
			SourceURL:   "https://www.infoq.cn/article/kadDBSqBrtFfUbipxQSs"},
		{ModelName: "kimi-k2-thinking", DisplayName: "Kimi K2 Thinking", SupplierName: "Moonshot", LaunchYearMonth: "2025-11", PopularityStars: 5, ModelType: "Reasoning",
			Description: "Kimi K2 深度思考版，1T 参数 MoE，复杂推理与 Agent 能力",
			SourceURL:   "https://platform.moonshot.cn/docs/intro"},
		{ModelName: "kimi-k2-0711-preview", DisplayName: "Kimi K2", SupplierName: "Moonshot", LaunchYearMonth: "2025-07", PopularityStars: 5, ModelType: "LLM",
			Description: "1 万亿参数 MoE 开源旗舰，Tool Use 全球第一，Agentic 能力突出",
			SourceURL:   "https://platform.moonshot.cn/docs/intro"},
		{ModelName: "kimi-k1.5", DisplayName: "Kimi K1.5", SupplierName: "Moonshot", LaunchYearMonth: "2025-01", PopularityStars: 4, ModelType: "Reasoning",
			Description: "Kimi 推理模型，长链思考，多模态支持，MATH-500 发布时全球第二",
			SourceURL:   "https://platform.moonshot.cn/docs/intro"},
		{ModelName: "moonshot-v1-128k", DisplayName: "Moonshot V1 128K", SupplierName: "Moonshot", LaunchYearMonth: "2024-03", PopularityStars: 4, ModelType: "LLM",
			Description: "Kimi 经典长文本旗舰，128K 上下文，国内最早商业化长上下文模型",
			SourceURL:   "https://platform.moonshot.cn/docs/intro"},

		// ════════════════════════════════════════════════════════════
		// 智谱（GLM）
		// ════════════════════════════════════════════════════════════
		{ModelName: "glm-5", DisplayName: "GLM-5", SupplierName: "智谱", LaunchYearMonth: "2026-02", PopularityStars: 5, ModelType: "LLM",
			Description: "智谱第五代开源旗舰，编程对标 Claude Opus 4.5，OpenRouter 2月前五，开源第一",
			SourceURL:   "https://36kr.com/p/3679611307617928"},
		{ModelName: "glm-5-plus", DisplayName: "GLM-5 Plus", SupplierName: "智谱", LaunchYearMonth: "2026-02", PopularityStars: 4, ModelType: "LLM",
			Description: "GLM-5 轻量商业版，纯文本，价格较 GLM-5 降低近 50%",
			SourceURL:   "https://docs.bigmodel.cn/cn/guide/start/model-overview"},
		{ModelName: "glm-4.6", DisplayName: "GLM-4.6", SupplierName: "智谱", LaunchYearMonth: "2025-09", PopularityStars: 4, ModelType: "LLM",
			Description: "GLM 开源版，代码生成 SOTA，视觉推理能力强，200K 上下文",
			SourceURL:   "https://docs.bigmodel.cn/cn/guide/start/model-overview"},
		{ModelName: "glm-4.5", DisplayName: "GLM-4.5", SupplierName: "智谱", LaunchYearMonth: "2025-07", PopularityStars: 4, ModelType: "LLM",
			Description: "Agent 基座模型，推理/编码/工具调用全面融合",
			SourceURL:   "https://docs.bigmodel.cn/cn/guide/start/model-overview"},
		{ModelName: "glm-z1-plus", DisplayName: "GLM-Z1 Plus", SupplierName: "智谱", LaunchYearMonth: "2025-04", PopularityStars: 4, ModelType: "Reasoning",
			Description: "智谱推理旗舰，联网搜索+深度思考，商业级复杂任务处理",
			SourceURL:   "https://open.bigmodel.cn/dev/api"},
		{ModelName: "glm-4-plus", DisplayName: "GLM-4 Plus", SupplierName: "智谱", LaunchYearMonth: "2024-09", PopularityStars: 3, ModelType: "LLM",
			Description: "智谱稳定商业旗舰，128K 上下文，函数调用能力优秀",
			SourceURL:   "https://open.bigmodel.cn/pricing"},
		{ModelName: "glm-4-flash", DisplayName: "GLM-4 Flash（免费）", SupplierName: "智谱", LaunchYearMonth: "2024-09", PopularityStars: 4, ModelType: "LLM",
			Description: "智谱免费极速模型，128K 上下文，高并发场景首选",
			SourceURL:   "https://open.bigmodel.cn/pricing"},
		{ModelName: "glm-image", DisplayName: "GLM Image（图像生成）", SupplierName: "智谱", LaunchYearMonth: "2025-08", PopularityStars: 4, ModelType: "ImageGeneration",
			Description: "智谱图像生成，文本渲染开源 SOTA，中文海报设计强",
			SourceURL:   "https://docs.bigmodel.cn/cn/guide/start/model-overview"},
		{ModelName: "cogvideox-3", DisplayName: "CogVideoX-3（视频生成）", SupplierName: "智谱", LaunchYearMonth: "2025-06", PopularityStars: 4, ModelType: "VideoGeneration",
			Description: "多分辨率视频生成，支持图生视频/文生视频/视频延展",
			SourceURL:   "https://docs.bigmodel.cn/cn/guide/start/model-overview"},

		// ════════════════════════════════════════════════════════════
		// MiniMax
		// ════════════════════════════════════════════════════════════
		{ModelName: "minimax-music-2.6", DisplayName: "MiniMax Music 2.6", SupplierName: "MiniMax", LaunchYearMonth: "2026-04", PopularityStars: 4, ModelType: "MusicGeneration",
			Description: "AI 音乐生成旗舰，人声乐器控制，歌词优化，全球 Beta 发布",
			SourceURL:   "https://www.minimaxi.com/news/music-26"},
		{ModelName: "minimax-m2.7", DisplayName: "MiniMax M2.7", SupplierName: "MiniMax", LaunchYearMonth: "2026-03", PopularityStars: 5, ModelType: "LLM",
			Description: "递归自改进，SWE-Pro 56.22%，ELO 1495 开源最高，100TPS 高速版",
			SourceURL:   "https://www.minimaxi.com/news/minimax-m27-en"},
		{ModelName: "minimax-m2.5", DisplayName: "MiniMax M2.5", SupplierName: "MiniMax", LaunchYearMonth: "2026-02", PopularityStars: 5, ModelType: "LLM",
			Description: "OpenRouter 2月调用量第一（4.55万亿Token），月收入破1.5亿美元",
			SourceURL:   "https://www.pingwest.com/a/311573"},
		{ModelName: "minimax-m2", DisplayName: "MiniMax M2", SupplierName: "MiniMax", LaunchYearMonth: "2025-10", PopularityStars: 4, ModelType: "LLM",
			Description: "自我迭代模型第二代，编程与 Agent 能力持续进化",
			SourceURL:   "https://platform.minimaxi.com/document/models"},
		{ModelName: "minimax-m1", DisplayName: "MiniMax M1", SupplierName: "MiniMax", LaunchYearMonth: "2025-05", PopularityStars: 5, ModelType: "Reasoning",
			Description: "首批百万 Token 上下文开源推理模型，80K 思维链，强化学习训练",
			SourceURL:   "https://platform.minimaxi.com/document/models"},
		{ModelName: "minimax-hailuo-2.3", DisplayName: "海螺 Hailuo 2.3（视频）", SupplierName: "MiniMax", LaunchYearMonth: "2025-10", PopularityStars: 4, ModelType: "VideoGeneration",
			Description: "海螺视频生成旗舰，物理运动精准，720P 高清输出",
			SourceURL:   "https://platform.minimaxi.com/document/models"},
		{ModelName: "minimax-speech-2.6", DisplayName: "MiniMax Speech 2.6", SupplierName: "MiniMax", LaunchYearMonth: "2025-10", PopularityStars: 3, ModelType: "TTS",
			Description: "高保真语音合成，情感与音色控制精准，支持多语言",
			SourceURL:   "https://platform.minimaxi.com/document/models"},

		// ════════════════════════════════════════════════════════════
		// 讯飞星火（Spark）
		// ════════════════════════════════════════════════════════════
		{ModelName: "spark-x1.5", DisplayName: "星火 X1.5", SupplierName: "讯飞", LaunchYearMonth: "2025-11", PopularityStars: 5, ModelType: "Reasoning",
			Description: "293B/30B 激活 MoE 推理模型，推理效率提升 100%，支持 130+ 语言",
			SourceURL:   "https://finance.sina.com.cn/jjxw/2025-11-11/doc-infwzezw1996297.shtml"},
		{ModelName: "spark4-ultra", DisplayName: "星火 4.0 Ultra", SupplierName: "讯飞", LaunchYearMonth: "2024-08", PopularityStars: 4, ModelType: "LLM",
			Description: "讯飞商业旗舰，对标 GPT-4 Turbo，生成速度提升 70%",
			SourceURL:   "https://xinghuo.xfyun.cn/sparkapi"},
		{ModelName: "spark-max", DisplayName: "星火 Max", SupplierName: "讯飞", LaunchYearMonth: "2024-06", PopularityStars: 3, ModelType: "LLM",
			Description: "讯飞专业版，复杂推理与行业知识问答，企业级文档理解",
			SourceURL:   "https://xinghuo.xfyun.cn/sparkapi"},

		// ════════════════════════════════════════════════════════════
		// 阶跃星辰（StepFun）
		// ════════════════════════════════════════════════════════════
		{ModelName: "step-3.5-flash", DisplayName: "Step 3.5 Flash", SupplierName: "阶跃星辰", LaunchYearMonth: "2026-02", PopularityStars: 5, ModelType: "LLM",
			Description: "1960B/110B 激活 MoE，实时 Agent 工程优化，350 tokens/s，OpenRouter Trending 第一",
			SourceURL:   "https://www.qbitai.com/2026/02/375351.html"},
		{ModelName: "step-3", DisplayName: "Step-3", SupplierName: "阶跃星辰", LaunchYearMonth: "2025-07", PopularityStars: 4, ModelType: "LLM",
			Description: "开源旗舰，国产芯推理效率达 DeepSeek R1 的 3 倍，多模态支持",
			SourceURL:   "https://www.infoq.cn/article/9ishp2ykyqs7auwsd9if"},
		{ModelName: "step-2-16k", DisplayName: "Step-2", SupplierName: "阶跃星辰", LaunchYearMonth: "2024-07", PopularityStars: 4, ModelType: "LLM",
			Description: "万亿参数 MoE，C-Eval/CMMLU 国内权威榜单第一名",
			SourceURL:   "https://platform.stepfun.com/docs/llm/text"},
		{ModelName: "step-2-mini", DisplayName: "Step-2 Mini", SupplierName: "阶跃星辰", LaunchYearMonth: "2024-12", PopularityStars: 3, ModelType: "LLM",
			Description: "Step-2 轻量版，MFA 注意力，低成本快速推理",
			SourceURL:   "https://platform.stepfun.com/docs/llm/text"},
		{ModelName: "step-1v-8k", DisplayName: "Step-1V", SupplierName: "阶跃星辰", LaunchYearMonth: "2024-04", PopularityStars: 3, ModelType: "VLM",
			Description: "阶跃星辰多模态模型，图文理解，复杂图像分析与问答",
			SourceURL:   "https://platform.stepfun.com/docs/llm/text"},

		// ════════════════════════════════════════════════════════════
		// 零一万物（Yi / 01.AI）
		// ════════════════════════════════════════════════════════════
		{ModelName: "yi-lightning", DisplayName: "Yi-Lightning", SupplierName: "零一万物", LaunchYearMonth: "2024-09", PopularityStars: 4, ModelType: "LLM",
			Description: "零一万物极速旗舰，国内首次超越 GPT-4o，推理速度提升 50%",
			SourceURL:   "https://platform.lingyiwanwu.com/docs"},
		{ModelName: "yi-vision", DisplayName: "Yi-Vision", SupplierName: "零一万物", LaunchYearMonth: "2024-07", PopularityStars: 3, ModelType: "VLM",
			Description: "零一万物视觉模型，图像理解与视觉问答，中文场景优化",
			SourceURL:   "https://platform.lingyiwanwu.com/docs"},
		{ModelName: "yi-large", DisplayName: "Yi-Large", SupplierName: "零一万物", LaunchYearMonth: "2024-05", PopularityStars: 3, ModelType: "LLM",
			Description: "零一万物大规模商业模型，200K 上下文，中英双语能力均衡",
			SourceURL:   "https://platform.lingyiwanwu.com/docs"},
	}

	return DB.Create(&models).Error
}
