package database

import (
	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// RunSeedCapabilityCases 初始化默认能力测试用例
//
// 行为：仅在首次安装时（表中完全无记录）写入种子数据，避免每次重启覆盖管理员的修改。
// 如需重新应用默认用例，请调用 ResetSeedCapabilityCases（管理后台"重置默认"按钮）。
func RunSeedCapabilityCases(db *gorm.DB) {
	// 检查是否首次安装（包含软删除记录也算已安装，避免误覆盖）
	var count int64
	if err := db.Unscoped().Model(&model.CapabilityTestCase{}).Count(&count).Error; err != nil {
		logger.L.Warn("能力测试用例种子：查表失败，跳过种子写入", zap.Error(err))
		return
	}
	if count > 0 {
		logger.L.Debug("能力测试用例种子：表非空，跳过（首次安装已完成）", zap.Int64("existing", count))
		return
	}

	// 首次安装：批量写入
	cases := buildDefaultCapabilityCases()
	inserted := 0
	for _, c := range cases {
		if err := db.Create(&c).Error; err != nil {
			logger.L.Warn("能力测试用例种子写入失败",
				zap.String("name", c.Name), zap.Error(err))
			continue
		}
		inserted++
	}
	if inserted > 0 {
		logger.L.Info("能力测试用例种子已写入（首次安装）", zap.Int("count", inserted))
	}
}

// ResetSeedCapabilityCases 重置为默认用例（删除所有种子用例后重新插入）
// 用于管理后台"重置默认"按钮
func ResetSeedCapabilityCases(db *gorm.DB) error {
	cases := buildDefaultCapabilityCases()
	names := make([]string, 0, len(cases))
	for _, c := range cases {
		names = append(names, c.Name)
	}
	// 用 Unscoped 硬删除，避免软删除后 uniqueIndex 冲突
	if err := db.Unscoped().Where("name IN ?", names).Delete(&model.CapabilityTestCase{}).Error; err != nil {
		return err
	}
	for _, c := range cases {
		if err := db.Create(&c).Error; err != nil {
			return err
		}
	}
	return nil
}

// buildDefaultCapabilityCases 返回 42 条默认测试用例（全部中文化）
func buildDefaultCapabilityCases() []model.CapabilityTestCase {
	return []model.CapabilityTestCase{
		// ===== Chat 基础 (10 条) =====
		{
			Name: "chat_basic", DisplayName: "基础对话", Category: "baseline", ModelType: "chat",
			Capability: "", Priority: 10, Enabled: true, CostEstimateCredits: 1,
			RequestTemplate: `{"path":"/chat/completions","body":{"model":"{{.ModelName}}","messages":[{"role":"user","content":"你好"}],"enable_thinking":false,"max_tokens":8}}`,
			Assertions:      `[{"type":"status_eq","value":200},{"type":"jsonpath_exists","path":"$.choices[0].message.content"}]`,
			Notes:           "验证模型基本对话能力，断言响应包含 choices[0].message.content；enable_thinking=false 兼容 qwen3",
		},
		{
			Name: "chat_thinking", DisplayName: "深度思考", Category: "thinking", ModelType: "chat",
			Capability: "supports_thinking", Priority: 20, Enabled: true, CostEstimateCredits: 2,
			ProviderFilter:  "qwen,deepseek,hunyuan,doubao,glm,kimi,moonshot",
			RequestTemplate: `{"path":"/chat/completions","body":{"model":"{{.ModelName}}","messages":[{"role":"user","content":"1加1等于几？"}],"max_tokens":32,"stream":true,"enable_thinking":true}}`,
			Assertions:      `[{"type":"status_eq","value":200},{"type":"streaming_min_chunks","min_chunks":1}]`,
			Notes:           "验证模型支持深度思考/推理参数，使用流式模式（qwen3 要求 stream=true），断言返回流式帧；覆盖 qwen/deepseek/hunyuan/doubao/glm/kimi",
		},
		{
			Name: "chat_thinking_off", DisplayName: "深度思考关闭验证", Category: "thinking", ModelType: "chat",
			Capability: "", Priority: 21, Enabled: true, CostEstimateCredits: 2,
			ProviderFilter:  "qwen,deepseek,hunyuan,doubao,glm,kimi,moonshot",
			RequestTemplate: `{"path":"/chat/completions","body":{"model":"{{.ModelName}}","messages":[{"role":"user","content":"你好，请用一句话介绍你自己"}],"max_tokens":100,"enable_thinking":false}}`,
			Assertions:      `[{"type":"status_eq","value":200},{"type":"field_nonempty","path":"$.choices[0].message.content"}]`,
			Notes:           "验证 enable_thinking=false 参数：支持思考的模型应关闭思考直接输出文本；不支持思考的模型应忽略此参数并正常响应；覆盖所有主流中文供应商",
		},
		{
			Name: "chat_search_web", DisplayName: "联网搜索", Category: "web_search", ModelType: "chat",
			Capability: "supports_web_search", Priority: 20, Enabled: true, CostEstimateCredits: 3,
			ProviderFilter:  "qwen,hunyuan,glm,doubao,kimi,moonshot",
			RequestTemplate: `{"path":"/chat/completions","body":{"model":"{{.ModelName}}","messages":[{"role":"user","content":"今天天气怎么样？"}],"enable_search":true,"enable_thinking":false,"max_tokens":32}}`,
			Assertions:      `[{"type":"status_eq","value":200},{"type":"no_error_code"}]`,
			Notes:           "验证模型支持联网搜索参数（enable_search）；覆盖 qwen/hunyuan/glm/doubao/kimi/moonshot；enable_thinking=false 兼容 qwen3",
		},
		{
			Name: "chat_prompt_cache", DisplayName: "提示词缓存", Category: "cache", ModelType: "chat",
			Capability: "supports_cache", Priority: 30, Enabled: true, CostEstimateCredits: 1,
			ProviderFilter:  "claude,deepseek,qwen,doubao,kimi",
			RequestTemplate: `{"path":"/chat/completions","body":{"model":"{{.ModelName}}","messages":[{"role":"system","content":"你是一个有用的助手。","cache_control":{"type":"ephemeral"}},{"role":"user","content":"你好"}],"enable_thinking":false,"max_tokens":8}}`,
			Assertions:      `[{"type":"status_eq","value":200}]`,
			Notes:           "验证供应商支持缓存计费参数（cache_control），适用于 claude/deepseek/qwen；enable_thinking=false 兼容 qwen3",
		},
		{
			Name: "chat_json_mode", DisplayName: "JSON 输出模式", Category: "json_mode", ModelType: "chat",
			Capability: "supports_json_mode", Priority: 30, Enabled: true, CostEstimateCredits: 1,
			RequestTemplate: `{"path":"/chat/completions","body":{"model":"{{.ModelName}}","messages":[{"role":"user","content":"以 JSON 格式返回：{\"name\":\"张三\",\"age\":25}，只返回 JSON 不要有其他文字"}],"response_format":{"type":"json_object"},"enable_thinking":false,"max_tokens":64}}`,
			Assertions:      `[{"type":"status_eq","value":200},{"type":"response_is_valid_json","path":"$.choices[0].message.content"}]`,
			Notes:           "验证模型在 response_format=json_object 时返回合法 JSON；enable_thinking=false 兼容 qwen3",
		},
		{
			Name: "chat_function_call", DisplayName: "函数调用", Category: "function_call", ModelType: "chat",
			Capability: "supports_function_call", Priority: 40, Enabled: true, CostEstimateCredits: 2,
			RequestTemplate: `{"path":"/chat/completions","body":{"model":"{{.ModelName}}","messages":[{"role":"user","content":"北京今天天气怎么样？"}],"tools":[{"type":"function","function":{"name":"get_weather","description":"获取指定城市天气","parameters":{"type":"object","properties":{"city":{"type":"string","description":"城市名称"}},"required":["city"]}}}],"enable_thinking":false,"max_tokens":64}}`,
			Assertions:      `[{"type":"status_eq","value":200},{"type":"no_error_code"}]`,
			Notes:           "验证模型支持 tools 参数并在合适时机调用自定义函数；enable_thinking=false 兼容 qwen3",
		},
		{
			Name: "chat_stream_basic", DisplayName: "流式输出（SSE）", Category: "baseline", ModelType: "chat",
			Capability: "supports_stream", Priority: 20, Enabled: true, CostEstimateCredits: 1,
			RequestTemplate: `{"path":"/chat/completions","body":{"model":"{{.ModelName}}","messages":[{"role":"user","content":"从1数到3"}],"stream":true,"max_tokens":32}}`,
			Assertions:      `[{"type":"status_eq","value":200},{"type":"content_type_matches","pattern":"text/event-stream"},{"type":"streaming_min_chunks","min_chunks":3}]`,
			Notes:           "验证 SSE 流式响应，断言 Content-Type 含 text/event-stream 且至少返回 3 帧；category=baseline 确保流式专用模型（qwq）也能被正确识别为 online",
		},
		{
			Name: "chat_multi_turn", DisplayName: "多轮对话", Category: "multi_turn", ModelType: "chat",
			Capability: "", Priority: 30, Enabled: true, CostEstimateCredits: 1,
			RequestTemplate: `{"path":"/chat/completions","body":{"model":"{{.ModelName}}","messages":[{"role":"user","content":"将以下文字翻译成英文：春节快乐"},{"role":"assistant","content":"Happy Spring Festival"},{"role":"user","content":"请再翻译一句：万事如意"}],"enable_thinking":false,"max_tokens":16}}`,
			Assertions:      `[{"type":"status_eq","value":200},{"type":"field_nonempty","path":"$.choices[0].message.content"}]`,
			Notes:           "验证模型对历史消息的上下文记忆能力（多轮翻译对话）；enable_thinking=false 兼容 qwen3",
		},
		{
			Name: "chat_system_prompt", DisplayName: "系统提示词", Category: "baseline", ModelType: "chat",
			Capability: "", Priority: 30, Enabled: true, CostEstimateCredits: 1,
			RequestTemplate: `{"path":"/chat/completions","body":{"model":"{{.ModelName}}","messages":[{"role":"system","content":"只能说你好"},{"role":"user","content":"hi"}],"enable_thinking":false,"max_tokens":8}}`,
			Assertions:      `[{"type":"status_eq","value":200},{"type":"field_nonempty","path":"$.choices[0].message.content"}]`,
			Notes:           "验证 system role 消息能有效影响回答风格和内容；enable_thinking=false 兼容 qwen3",
		},
		{
			Name: "chat_stop_sequences", DisplayName: "停止序列", Category: "advanced_params", ModelType: "chat",
			Capability: "param:stop", Priority: 40, Enabled: true, CostEstimateCredits: 1,
			RequestTemplate: `{"path":"/chat/completions","body":{"model":"{{.ModelName}}","messages":[{"role":"user","content":"数数：一,二,三,四,五"}],"stop":["三"],"enable_thinking":false,"max_tokens":32}}`,
			Assertions:      `[{"type":"status_eq","value":200}]`,
			Notes:           "验证 stop 参数：指定停止词时模型在该词前截断输出；enable_thinking=false 兼容 qwen3",
		},

		// ===== Chat 边界 (4 条) =====
		{
			Name: "chat_boundary_max_tokens_over", DisplayName: "最大 Token 越界", Category: "boundary", ModelType: "chat",
			Subcategory: "boundary", Capability: "", Priority: 60, Enabled: true, CostEstimateCredits: 1,
			RequestTemplate: `{"path":"/chat/completions","body":{"model":"{{.ModelName}}","messages":[{"role":"user","content":"你好"}],"max_tokens":999999}}`,
			Assertions:      `[{"type":"status_in","value":[200,400,422]}]`,
			Notes:           "边界测试：max_tokens 超出模型上限，期望返回明确错误（400/422）而非 500",
		},
		{
			Name: "chat_boundary_temperature_oor", DisplayName: "Temperature 参数越界", Category: "boundary", ModelType: "chat",
			Subcategory: "boundary", Priority: 60, Enabled: true, CostEstimateCredits: 1,
			RequestTemplate: `{"path":"/chat/completions","body":{"model":"{{.ModelName}}","messages":[{"role":"user","content":"你好"}],"temperature":5.0,"max_tokens":8}}`,
			Assertions:      `[{"type":"status_in","value":[200,400,422]}]`,
			Notes:           "边界测试：temperature=5.0 超出 [0,2] 合法范围，期望返回参数错误",
		},
		{
			Name: "chat_boundary_context_overflow", DisplayName: "上下文长度溢出（禁用）", Category: "boundary", ModelType: "chat",
			Subcategory: "boundary", Priority: 70, Enabled: false, CostEstimateCredits: 5,
			RequestTemplate: `{"path":"/chat/completions","body":{"model":"{{.ModelName}}","messages":[{"role":"user","content":"A"}],"max_tokens":8}}`,
			Assertions:      `[{"type":"expect_error_category","category":"context_length_exceeded"}]`,
			Notes:           "边界测试：发送超长文本触发 context_length_exceeded 错误分类（需管理员配置 200K token 输入）",
		},
		{
			Name: "chat_boundary_empty_messages", DisplayName: "空消息数组", Category: "boundary", ModelType: "chat",
			Subcategory: "boundary", Priority: 60, Enabled: true, CostEstimateCredits: 1,
			RequestTemplate: `{"path":"/chat/completions","body":{"model":"{{.ModelName}}","messages":[]}}`,
			Assertions:      `[{"type":"status_in","value":[400,422]}]`,
			Notes:           "边界测试：messages=[] 空数组，期望返回参数校验错误（400/422）而非服务器崩溃",
		},

		// ===== VLM / OCR (4 条) =====
		{
			Name: "vlm_image_url", DisplayName: "图像 URL 识别", Category: "baseline", ModelType: "vlm",
			Capability: "supports_vision", Priority: 30, Enabled: true, CostEstimateCredits: 2,
			RequestTemplate: `{"path":"/chat/completions","body":{"model":"{{.ModelName}}","messages":[{"role":"user","content":[{"type":"text","text":"请描述这张图片的内容"},{"type":"image_url","image_url":{"url":"{{.SampleImageURL}}"}}]}],"max_tokens":64}}`,
			Assertions:      `[{"type":"status_eq","value":200},{"type":"field_nonempty","path":"$.choices[0].message.content"}]`,
			Notes:           "验证视觉模型能解析 URL 图像，断言响应中包含对图像的描述",
		},
		{
			Name: "vlm_base64_image", DisplayName: "Base64 图像识别", Category: "baseline", ModelType: "vlm",
			Capability: "supports_vision", Priority: 30, Enabled: true, CostEstimateCredits: 2,
			RequestTemplate: `{"path":"/chat/completions","body":{"model":"{{.ModelName}}","messages":[{"role":"user","content":[{"type":"text","text":"请描述图片内容"},{"type":"image_url","image_url":{"url":"data:image/png;base64,{{.SampleImageBase64}}"}}]}],"max_tokens":64}}`,
			Assertions:      `[{"type":"status_eq","value":200},{"type":"field_nonempty","path":"$.choices[0].message.content"}]`,
			Notes:           "验证视觉模型接受 Base64 编码图像，使用内置样本图片",
		},
		{
			Name: "vlm_multi_image", DisplayName: "多图像识别", Category: "baseline", ModelType: "vlm",
			Capability: "supports_vision_multi", Priority: 50, Enabled: true, CostEstimateCredits: 3,
			RequestTemplate: `{"path":"/chat/completions","body":{"model":"{{.ModelName}}","messages":[{"role":"user","content":[{"type":"text","text":"对比这两张图片的异同"},{"type":"image_url","image_url":{"url":"data:image/png;base64,{{.SampleImageBase64}}"}},{"type":"image_url","image_url":{"url":"data:image/png;base64,{{.SampleImageBase64}}"}}]}],"max_tokens":64}}`,
			Assertions:      `[{"type":"status_eq","value":200},{"type":"field_nonempty","path":"$.choices[0].message.content"}]`,
			Notes:           "验证模型同时处理多张图片，断言对每张图均有描述",
		},
		{
			Name: "ocr_printed_text", DisplayName: "印刷文字 OCR", Category: "baseline", ModelType: "ocr",
			Capability: "supports_ocr", Priority: 30, Enabled: true, CostEstimateCredits: 2,
			RequestTemplate: `{"path":"/chat/completions","body":{"model":"{{.ModelName}}","messages":[{"role":"user","content":[{"type":"text","text":"请识别图片中的所有文字"},{"type":"image_url","image_url":{"url":"data:image/png;base64,{{.SampleOCRBase64}}"}}]}],"max_tokens":128}}`,
			Assertions:      `[{"type":"status_eq","value":200},{"type":"field_nonempty","path":"$.choices[0].message.content"}]`,
			Notes:           `验证 OCR 能识别印刷体文字，使用内置含 "Hello 2026" 的样本图`,
		},

		// ===== Embedding (4 条) =====
		{
			Name: "embedding_basic", DisplayName: "基础向量嵌入", Category: "baseline", ModelType: "embedding",
			EndpointOverride: "/embeddings", Priority: 10, Enabled: true, CostEstimateCredits: 1,
			RequestTemplate: `{"path":"/embeddings","body":{"model":"{{.ModelName}}","input":"人工智能技术的应用前景"}}`,
			Assertions:      `[{"type":"status_eq","value":200},{"type":"field_nonempty","path":"$.data[0].embedding"}]`,
			Notes:           "验证 Embedding 端点返回向量数组，断言 data[0].embedding 非空",
		},
		{
			Name: "embedding_batch32", DisplayName: "批量嵌入（32条）", Category: "baseline", ModelType: "embedding",
			EndpointOverride: "/embeddings", Priority: 20, Enabled: true, CostEstimateCredits: 2,
			RequestTemplate: `{"path":"/embeddings","body":{"model":"{{.ModelName}}","input":["苹果","香蕉","橙子","葡萄","西瓜","草莓","蓝莓","芒果","菠萝","樱桃","梨","桃","李","杏","柚","橘","柠檬","椰子","荔枝","龙眼","山竹","榴莲","火龙果","百香果","木瓜","番石榴","无花果","杨梅","桑椹","枇杷","金桔","枣"]}}`,
			Assertions:      `[{"type":"status_eq","value":200}]`,
			Notes:           "验证批量嵌入能力，输入 32 个中文词汇，断言响应正常",
		},
		{
			Name: "embedding_dim_check", DisplayName: "向量维度参数", Category: "advanced_params", ModelType: "embedding",
			EndpointOverride: "/embeddings", Capability: "param:dimensions", Priority: 30, Enabled: true, CostEstimateCredits: 1,
			RequestTemplate: `{"path":"/embeddings","body":{"model":"{{.ModelName}}","input":"机器学习","dimensions":256}}`,
			Assertions:      `[{"type":"status_in","value":[200,400]}]`,
			Notes:           "验证 dimensions 参数可控制输出向量长度，不支持时返回 400",
		},
		{
			Name: "embedding_long_input_8k", DisplayName: "长文本嵌入（8K，禁用）", Category: "boundary", ModelType: "embedding",
			Subcategory: "boundary", EndpointOverride: "/embeddings", Priority: 60, Enabled: false, CostEstimateCredits: 3,
			RequestTemplate: `{"path":"/embeddings","body":{"model":"{{.ModelName}}","input":"长文本占位符，管理员需手动替换为真实 8000 token 文本"}}`,
			Assertions:      `[{"type":"status_in","value":[200,400,413]}]`,
			Notes:           "验证 8000 token 长文本嵌入不截断，成本较高默认禁用，需管理员填入实际长文本",
		},

		// ===== Rerank (2 条) =====
		{
			Name: "rerank_basic", DisplayName: "基础重排序", Category: "baseline", ModelType: "rerank",
			EndpointOverride: "/rerank", Priority: 10, Enabled: true, CostEstimateCredits: 1,
			RequestTemplate: `{"path":"/rerank","body":{"model":"{{.ModelName}}","query":"人工智能的应用场景","documents":["机器学习用于图像识别","烹饪美食的技巧","深度学习改变了自然语言处理","股票市场分析","神经网络在医疗诊断中的应用"]}}`,
			Assertions:      `[{"type":"status_eq","value":200},{"type":"field_nonempty","path":"$.results"}]`,
			Notes:           "验证 Rerank 端点按相关性对文档列表重新排序，断言返回 results 列表",
		},
		{
			Name: "rerank_empty_docs", DisplayName: "空文档列表", Category: "boundary", ModelType: "rerank",
			Subcategory: "boundary", EndpointOverride: "/rerank", Priority: 60, Enabled: true, CostEstimateCredits: 1,
			RequestTemplate: `{"path":"/rerank","body":{"model":"{{.ModelName}}","query":"测试","documents":[]}}`,
			Assertions:      `[{"type":"status_in","value":[200,400]}]`,
			Notes:           "边界测试：documents=[] 时期望返回明确错误而非服务器崩溃",
		},

		// ===== TTS (2 条) =====
		{
			Name: "tts_basic_zh", DisplayName: "中文语音合成", Category: "baseline", ModelType: "tts",
			EndpointOverride: "/audio/speech", Priority: 20, Enabled: true, CostEstimateCredits: 2,
			RequestTemplate: `{"path":"/audio/speech","body":{"model":"{{.ModelName}}","input":"你好，欢迎使用人工智能语音合成服务。","voice":"alloy","response_format":"mp3"}}`,
			Assertions:      `[{"type":"status_eq","value":200},{"type":"content_type_matches","pattern":"audio/"},{"type":"response_size_gt_bytes","min_bytes":500}]`,
			Notes:           "验证 TTS 能将中文文本合成语音，断言响应为音频二进制且大小 > 500 字节",
		},
		{
			Name: "tts_voice_selection", DisplayName: "语音参数选择", Category: "advanced_params", ModelType: "tts",
			EndpointOverride: "/audio/speech", Capability: "param:voice", Priority: 40, Enabled: true, CostEstimateCredits: 2,
			RequestTemplate: `{"path":"/audio/speech","body":{"model":"{{.ModelName}}","input":"测试","voice":"nova","response_format":"mp3"}}`,
			Assertions:      `[{"type":"status_in","value":[200,400]}]`,
			Notes:           "验证 voice 参数有效，切换不同音色（nova）时响应正常，不支持时返回 400",
		},

		// ===== ASR (2 条) =====
		{
			Name: "asr_wav_basic", DisplayName: "WAV 音频转录（禁用）", Category: "baseline", ModelType: "asr",
			EndpointOverride: "/audio/transcriptions", Priority: 30, Enabled: false, CostEstimateCredits: 3,
			RequestTemplate: `{"path":"/audio/transcriptions","body":{"model":"{{.ModelName}}","file":"{{.SampleAudioBase64}}"}}`,
			Assertions:      `[{"type":"status_eq","value":200},{"type":"field_nonempty","path":"$.text"}]`,
			Notes:           "验证 ASR 能转录 WAV 格式语音为文字（默认禁用，需 multipart 适配器支持）",
		},
		{
			Name: "asr_long_audio", DisplayName: "长音频处理（禁用）", Category: "boundary", ModelType: "asr",
			Subcategory: "boundary", EndpointOverride: "/audio/transcriptions", Priority: 70, Enabled: false, CostEstimateCredits: 5,
			RequestTemplate: `{"path":"/audio/transcriptions","body":{"model":"{{.ModelName}}","file":""}}`,
			Assertions:      `[{"type":"status_in","value":[200,400,413]}]`,
			Notes:           "验证 ASR 对长音频的处理上限，成本较高默认禁用，需管理员配置真实音频文件",
		},

		// ===== Image Generation (3 条，默认 enabled=false) =====
		{
			Name: "image_basic", DisplayName: "基础图像生成（禁用）", Category: "baseline", ModelType: "image",
			EndpointOverride: "/images/generations", Priority: 30, Enabled: false, CostEstimateCredits: 10,
			RequestTemplate: `{"path":"/images/generations","body":{"model":"{{.ModelName}}","prompt":"一只可爱的橘猫坐在窗台上","n":1,"size":"1024x1024"}}`,
			Assertions:      `[{"type":"status_eq","value":200},{"type":"field_nonempty","path":"$.data[0].url"}]`,
			Notes:           "验证图像生成端点返回 URL 或 Base64，使用 1024x1024（兼容性最好），高成本默认禁用",
		},
		{
			Name: "image_size_1024", DisplayName: "1024×1024 图像（禁用）", Category: "advanced_params", ModelType: "image",
			EndpointOverride: "/images/generations", Capability: "param:size", Priority: 40, Enabled: false, CostEstimateCredits: 15,
			RequestTemplate: `{"path":"/images/generations","body":{"model":"{{.ModelName}}","prompt":"山水风景画","size":"1024x1024"}}`,
			Assertions:      `[{"type":"status_in","value":[200,400]}]`,
			Notes:           "验证尺寸参数支持（1024×1024），不支持时返回 400，高成本默认禁用",
		},
		{
			Name: "image_n2", DisplayName: "批量生成 2 张（禁用）", Category: "advanced_params", ModelType: "image",
			EndpointOverride: "/images/generations", Capability: "param:n", Priority: 40, Enabled: false, CostEstimateCredits: 20,
			RequestTemplate: `{"path":"/images/generations","body":{"model":"{{.ModelName}}","prompt":"卡通狗","n":2}}`,
			Assertions:      `[{"type":"status_in","value":[200,400]}]`,
			Notes:           "验证 n=2 时返回两张图像，断言 data.length=2，高成本默认禁用",
		},

		// ===== Video Generation (3 条，默认 enabled=false) =====
		{
			Name: "video_submit_basic", DisplayName: "视频任务提交（禁用）", Category: "async", ModelType: "video",
			Subcategory: "async", EndpointOverride: "/video/generations", Priority: 30, Enabled: false, CostEstimateCredits: 50,
			RequestTemplate: `{"path":"/video/generations","body":{"model":"{{.ModelName}}","prompt":"夕阳下的山脉全景","duration":5}}`,
			Assertions:      `[{"type":"status_in","value":[200,202]},{"type":"jsonpath_exists","path":"$.id"}]`,
			Notes:           "异步视频：提交生成任务，断言返回 task_id 和初始 pending 状态，高成本默认禁用",
		},
		{
			Name: "video_poll_until_done", DisplayName: "视频异步轮询完成（禁用）", Category: "async", ModelType: "video",
			Subcategory: "async", EndpointOverride: "/video/generations", Priority: 40, Enabled: false, CostEstimateCredits: 100,
			RequestTemplate: `{"path":"/video/generations","body":{"model":"{{.ModelName}}","prompt":"海浪拍打礁石","duration":3}}`,
			Assertions:      `[{"type":"status_in","value":[200,202]},{"type":"async_poll","taskIdPath":"$.id","pollEndpoint":"/video/tasks/{taskId}","statusPath":"$.status","resultPath":"$.result.video_url","successValues":["succeeded","completed","success"],"failValues":["failed","error"],"pollIntervalSec":10,"timeoutSec":300},{"type":"field_nonempty","path":"$.result.video_url"}]`,
			Notes:           "异步视频：轮询任务状态直至 succeeded，断言 video_url 不为空（超时 5 分钟），高成本默认禁用",
		},
		{
			Name: "video_poll_timeout_graceful", DisplayName: "视频轮询超时降级（禁用）", Category: "async", ModelType: "video",
			Subcategory: "async", EndpointOverride: "/video/generations", Priority: 50, Enabled: false, CostEstimateCredits: 80,
			RequestTemplate: `{"path":"/video/generations","body":{"model":"{{.ModelName}}","prompt":"测试短片","duration":3}}`,
			Assertions:      `[{"type":"async_poll","taskIdPath":"$.id","pollEndpoint":"/video/tasks/{taskId}","statusPath":"$.status","successValues":["succeeded"],"failValues":["failed"],"pollIntervalSec":5,"timeoutSec":30}]`,
			Notes:           "异步视频：故意设置 30 秒短超时，验证超时时返回友好错误而非 panic，高成本默认禁用",
		},

		// ===== Translation (2 条) =====
		{
			Name: "translation_zh_en", DisplayName: "中译英", Category: "baseline", ModelType: "translation",
			Priority: 30, Enabled: true, CostEstimateCredits: 1,
			RequestTemplate: `{"path":"/chat/completions","body":{"model":"{{.ModelName}}","messages":[{"role":"user","content":"将以下文字翻译成英文：春节快乐，万事如意！"}],"max_tokens":64}}`,
			Assertions:      `[{"type":"status_eq","value":200},{"type":"field_nonempty","path":"$.choices[0].message.content"}]`,
			Notes:           "验证翻译端点将中文翻译为英文，断言返回内容非空",
		},
		{
			Name: "translation_en_ja", DisplayName: "英译日", Category: "baseline", ModelType: "translation",
			Priority: 30, Enabled: true, CostEstimateCredits: 1,
			RequestTemplate: `{"path":"/chat/completions","body":{"model":"{{.ModelName}}","messages":[{"role":"user","content":"请将以下英文翻译成日文：Hello, welcome to our service."}],"max_tokens":64}}`,
			Assertions:      `[{"type":"status_eq","value":200},{"type":"field_nonempty","path":"$.choices[0].message.content"}]`,
			Notes:           "验证翻译端点将英文翻译为日文，断言返回内容非空",
		},

		// ===== UX Flow (5 条) =====
		{
			Name: "ux_playground_quick_chat", DisplayName: "Playground 快速对话流程", Category: "ux_flow", ModelType: "ux_flow",
			Priority: 30, Enabled: true, CostEstimateCredits: 2,
			RequestTemplate: `{"path":"/chat/completions","body":{"model":"{{.ModelName}}","messages":[{"role":"user","content":"你好"}],"max_tokens":8}}`,
			Assertions:      `[]`,
			FlowSteps:       `[{"name":"获取模型列表","method":"GET","endpoint":"/public/models?page_size=5","assertions":[{"type":"status_eq","value":200}]},{"name":"发送对话消息","method":"POST","endpoint":"/chat/completions","body_template":"{\"model\":\"{{.ModelName}}\",\"messages\":[{\"role\":\"user\",\"content\":\"你好，请做个自我介绍\"}],\"max_tokens\":32}","assertions":[{"type":"status_eq","value":200},{"type":"field_nonempty","path":"$.choices[0].message.content"}]}]`,
			Notes:           "UX 流程：获取模型列表→选模型→发送消息→断言流式响应正常",
		},
		{
			Name: "ux_streaming_interruption", DisplayName: "流式中断处理", Category: "ux_flow", ModelType: "ux_flow",
			Subcategory: "streaming", Priority: 50, Enabled: true, CostEstimateCredits: 2,
			RequestTemplate: `{"path":"/chat/completions","body":{"model":"{{.ModelName}}","messages":[{"role":"user","content":"请从1数到100，每个数字单独一行"}],"stream":true,"max_tokens":500}}`,
			Assertions:      `[]`,
			FlowSteps:       `[{"name":"启动流式并中途中断","method":"POST","endpoint":"/chat/completions","body_template":"{\"model\":\"{{.ModelName}}\",\"messages\":[{\"role\":\"user\",\"content\":\"请从1数到100\"}],\"stream\":true,\"max_tokens\":500}","abort_after_ms":500,"assertions":[{"type":"streaming_min_chunks","min_chunks":1}]}]`,
			Notes:           "UX 流程：中途中断 SSE 流，断言后端连接正常关闭无资源泄漏",
		},
		{
			Name: "ux_parallel_requests_rate_limit", DisplayName: "并发请求限流验证", Category: "ux_flow", ModelType: "ux_flow",
			Priority: 60, Enabled: true, CostEstimateCredits: 5,
			RequestTemplate: `{}`, Assertions: `[]`,
			FlowSteps: `[{"name":"并发发送10个请求","parallel":10,"method":"POST","endpoint":"/chat/completions","body_template":"{\"model\":\"gpt-4o-mini\",\"messages\":[{\"role\":\"user\",\"content\":\"你好\"}],\"max_tokens\":4}","assertions":[{"type":"status_in","value":[200,429]}]}]`,
			Notes:     "UX 流程：同一 Key 并发发送 10 个请求，断言至少 3 个返回 429 限流响应",
		},
		{
			Name: "ux_api_key_lifecycle", DisplayName: "API Key 生命周期（禁用）", Category: "ux_flow", ModelType: "ux_flow",
			Priority: 40, Enabled: false, CostEstimateCredits: 3,
			RequestTemplate: `{}`, Assertions: `[]`,
			FlowSteps: `[{"name":"创建API密钥","method":"POST","endpoint":"/user/api-keys","body_template":"{\"name\":\"自动测试临时密钥\"}","extract_vars":{"key_id":"$.data.id","key":"$.data.key"},"assertions":[{"type":"status_eq","value":200}]},{"name":"使用密钥调用接口","method":"POST","endpoint":"/chat/completions","headers":{"Authorization":"Bearer {{.vars.key}}"},"body_template":"{\"model\":\"gpt-4o-mini\",\"messages\":[{\"role\":\"user\",\"content\":\"你好\"}],\"max_tokens\":4}","assertions":[{"type":"status_eq","value":200}]},{"name":"撤销密钥","method":"DELETE","endpoint":"/user/api-keys/{{.vars.key_id}}","assertions":[{"type":"status_eq","value":200}]},{"name":"撤销后调用应失败","method":"POST","endpoint":"/chat/completions","headers":{"Authorization":"Bearer {{.vars.key}}"},"body_template":"{\"model\":\"gpt-4o-mini\",\"messages\":[{\"role\":\"user\",\"content\":\"你好\"}]}","assertions":[{"type":"status_eq","value":401}]}]`,
			Notes:     "UX 流程：创建 Key→调用成功→撤销 Key→再次调用返回 401（默认禁用，需管理员配置 JWT 鉴权环境）",
		},
		{
			Name: "ux_error_message_quality", DisplayName: "错误提示质量", Category: "ux_flow", ModelType: "ux_flow",
			Priority: 50, Enabled: true, CostEstimateCredits: 1,
			RequestTemplate: `{}`, Assertions: `[]`,
			FlowSteps: `[{"name":"发送无效模型请求","method":"POST","endpoint":"/chat/completions","body_template":"{\"model\":\"不存在的模型xyz123\",\"messages\":[{\"role\":\"user\",\"content\":\"你好\"}]}","assertions":[{"type":"status_in","value":[400,404]},{"type":"field_nonempty","path":"$.message"}]}]`,
			Notes:     "UX 流程：发送无效请求，断言错误响应中 message 字段不为空（中文可读描述）",
		},

		// ===== Performance (1 条) =====
		{
			Name: "chat_latency_baseline", DisplayName: "延迟基线检测", Category: "performance", ModelType: "chat",
			Subcategory: "performance", Priority: 20, Enabled: true, CostEstimateCredits: 1,
			RequestTemplate: `{"path":"/chat/completions","body":{"model":"{{.ModelName}}","messages":[{"role":"user","content":"你好"}],"enable_thinking":false,"max_tokens":4}}`,
			Assertions:      `[{"type":"status_eq","value":200},{"type":"latency_lt_ms","threshold":5000}]`,
			Notes:           "性能测试：简单对话延迟应低于 5000ms（P50 基线），超出时触发回归告警；enable_thinking=false 兼容 qwen3",
		},
	}
}
