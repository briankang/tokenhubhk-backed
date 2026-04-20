package pricescraper

// =====================================================
// 阿里云百炼（DashScope）官方下线模型清单
// 来源: https://help.aliyun.com/zh/model-studio/model-depreciation
// 用于：将本地数据库中已被百炼官方下线的模型批量标记为 offline
// 命名约定：所有 key 必须为小写规范化的 API 模型 ID
// =====================================================

// GetAliyunDeprecatedModels 返回阿里云百炼已下线 / 即将下线的模型清单
//
// 数据来源: https://help.aliyun.com/zh/model-studio/model-depreciation（截止 2026-04 抓取的官方文档）
// 维护：每次官方更新「模型下线」页面后，手动同步此列表
func GetAliyunDeprecatedModels() map[string]ModelDeprecation {
	return map[string]ModelDeprecation{
		// =========================================================
		// 2026-03-30 已下线
		// =========================================================

		// 千问 Audio 系列
		"qwen-audio-asr":           {RetireDate: "2026-03-30", Reason: "qwen_audio_retired", Replacement: "qwen3-asr-flash"},
		"qwen-audio-asr-latest":    {RetireDate: "2026-03-30", Reason: "qwen_audio_retired", Replacement: "qwen3-asr-flash"},
		"qwen-audio-chat":          {RetireDate: "2026-03-30", Reason: "qwen_audio_retired", Replacement: "qwen3-omni-flash"},
		"qwen2-audio-instruct":     {RetireDate: "2026-03-30", Reason: "qwen_audio_retired", Replacement: "qwen3-omni-flash"},

		// 千问 2 开源版
		"qwen2-57b-a14b-instruct": {RetireDate: "2026-03-30", Reason: "qwen2_opensource_retired", Replacement: "qwen3-235b-a22b"},
		"qwen2-72b-instruct":      {RetireDate: "2026-03-30", Reason: "qwen2_opensource_retired", Replacement: "qwen3-235b-a22b"},
		"qwen2-7b-instruct":       {RetireDate: "2026-03-30", Reason: "qwen2_opensource_retired", Replacement: "qwen3-235b-a22b"},
		"qwen2-1.5b-instruct":     {RetireDate: "2026-03-30", Reason: "qwen2_opensource_retired", Replacement: "qwen3-235b-a22b"},
		"qwen2-0.5b-instruct":     {RetireDate: "2026-03-30", Reason: "qwen2_opensource_retired", Replacement: "qwen3-235b-a22b"},

		// 千问 1.5 开源版
		"qwen1.5-110b-chat": {RetireDate: "2026-03-30", Reason: "qwen1_5_retired", Replacement: "qwen3-235b-a22b"},
		"qwen1.5-72b-chat":  {RetireDate: "2026-03-30", Reason: "qwen1_5_retired", Replacement: "qwen3-235b-a22b"},
		"qwen1.5-32b-chat":  {RetireDate: "2026-03-30", Reason: "qwen1_5_retired", Replacement: "qwen3-235b-a22b"},
		"qwen1.5-14b-chat":  {RetireDate: "2026-03-30", Reason: "qwen1_5_retired", Replacement: "qwen3-235b-a22b"},
		"qwen1.5-7b-chat":   {RetireDate: "2026-03-30", Reason: "qwen1_5_retired", Replacement: "qwen3-235b-a22b"},
		"qwen1.5-1.8b-chat": {RetireDate: "2026-03-30", Reason: "qwen1_5_retired", Replacement: "qwen3-235b-a22b"},
		"qwen1.5-0.5b-chat": {RetireDate: "2026-03-30", Reason: "qwen1_5_retired", Replacement: "qwen3-235b-a22b"},

		// 千问 Math/Coder 小尺寸
		"qwen2.5-math-1.5b-instruct":   {RetireDate: "2026-03-30", Reason: "qwen_math_retired", Replacement: "qwen-math-plus"},
		"qwen2.5-coder-3b-instruct":    {RetireDate: "2026-03-30", Reason: "qwen_coder_retired", Replacement: "qwen-coder-plus"},
		"qwen2.5-coder-1.5b-instruct":  {RetireDate: "2026-03-30", Reason: "qwen_coder_retired", Replacement: "qwen-coder-plus"},
		"qwen2.5-coder-0.5b-instruct":  {RetireDate: "2026-03-30", Reason: "qwen_coder_retired", Replacement: "qwen-coder-plus"},

		// 千问 VL 老版本
		"qwen2-vl-72b-instruct": {RetireDate: "2026-03-30", Reason: "qwen_vl_retired", Replacement: "qwen3.5-flash"},
		"qwen2-vl-7b-instruct":  {RetireDate: "2026-03-30", Reason: "qwen_vl_retired", Replacement: "qwen3.5-flash"},
		"qwen2-vl-2b-instruct":  {RetireDate: "2026-03-30", Reason: "qwen_vl_retired", Replacement: "qwen3.5-flash"},
		"qwen-vl-v1":            {RetireDate: "2026-03-30", Reason: "qwen_vl_retired", Replacement: "qwen3.5-flash"},
		"qwen-vl-chat-v1":       {RetireDate: "2026-03-30", Reason: "qwen_vl_retired", Replacement: "qwen3.5-flash"},

		// 第三方 LLM（Baichuan/ABAB 等）
		"baichuan2-turbo": {RetireDate: "2026-03-30", Reason: "third_party_retired", Replacement: "qwen-flash"},
		"abab6.5s-chat":   {RetireDate: "2026-03-30", Reason: "third_party_retired", Replacement: "qwen-flash"},
		"abab6.5g-chat":   {RetireDate: "2026-03-30", Reason: "third_party_retired", Replacement: "qwen-flash"},
		"abab6.5t-chat":   {RetireDate: "2026-03-30", Reason: "third_party_retired", Replacement: "qwen-flash"},

		// NLU
		"opennlu-v1": {RetireDate: "2026-03-30", Reason: "nlu_retired", Replacement: "qwen3.5-flash"},

		// 图像生成（SD / FLUX）
		"stable-diffusion-v1.5":             {RetireDate: "2026-03-30", Reason: "image_model_retired", Replacement: "qwen-image-plus、wan2.6-t2i"},
		"stable-diffusion-xl":               {RetireDate: "2026-03-30", Reason: "image_model_retired", Replacement: "qwen-image-plus、wan2.6-t2i"},
		"stable-diffusion-3.5-large":        {RetireDate: "2026-03-30", Reason: "image_model_retired", Replacement: "qwen-image-plus、wan2.6-t2i"},
		"stable-diffusion-3.5-large-turbo":  {RetireDate: "2026-03-30", Reason: "image_model_retired", Replacement: "qwen-image-plus、wan2.6-t2i"},
		"flux-dev":                          {RetireDate: "2026-03-30", Reason: "image_model_retired", Replacement: "qwen-image-plus、wan2.6-t2i"},
		"flux-merged":                       {RetireDate: "2026-03-30", Reason: "image_model_retired", Replacement: "qwen-image-plus、wan2.6-t2i"},
		"flux-schnell":                      {RetireDate: "2026-03-30", Reason: "image_model_retired", Replacement: "qwen-image-plus、wan2.6-t2i"},

		// Llama 4
		"llama-4-scout-17b-16e-instruct":     {RetireDate: "2026-03-30", Reason: "llama4_retired", Replacement: "qwen3.5-flash"},
		"llama-4-maverick-17b-128e-instruct": {RetireDate: "2026-03-30", Reason: "llama4_retired", Replacement: "qwen3.5-flash"},

		// =========================================================
		// 2026-01-30 已下线
		// =========================================================

		// 千问 Max 快照
		"qwen-max-2024-04-03": {RetireDate: "2026-01-30", Reason: "qwen_max_snapshot_retired", Replacement: "qwen-max-2025-01-25"},

		// 千问 Plus 快照
		"qwen-plus-2024-11-27": {RetireDate: "2026-01-30", Reason: "qwen_plus_snapshot_retired", Replacement: "qwen-plus-2025-12-01"},
		"qwen-plus-2024-11-25": {RetireDate: "2026-01-30", Reason: "qwen_plus_snapshot_retired", Replacement: "qwen-plus-2025-12-01"},
		"qwen-plus-2024-09-19": {RetireDate: "2026-01-30", Reason: "qwen_plus_snapshot_retired", Replacement: "qwen-plus-2025-12-01"},
		"qwen-plus-2024-08-06": {RetireDate: "2026-01-30", Reason: "qwen_plus_snapshot_retired", Replacement: "qwen-plus-2025-12-01"},
		"qwen-plus-2024-07-23": {RetireDate: "2026-01-30", Reason: "qwen_plus_snapshot_retired", Replacement: "qwen-plus-2025-12-01"},

		// 千问 Turbo 快照
		"qwen-turbo-2024-09-19": {RetireDate: "2026-01-30", Reason: "qwen_turbo_snapshot_retired", Replacement: "qwen-flash-2025-07-28"},
		"qwen-turbo-2024-06-24": {RetireDate: "2026-01-30", Reason: "qwen_turbo_snapshot_retired", Replacement: "qwen-flash-2025-07-28"},

		// 千问 VL 快照
		"qwen-vl-max-2024-10-30":  {RetireDate: "2026-01-30", Reason: "qwen_vl_snapshot_retired", Replacement: "qwen3-vl-plus-2025-12-19"},
		"qwen-vl-max-2024-08-09":  {RetireDate: "2026-01-30", Reason: "qwen_vl_snapshot_retired", Replacement: "qwen3-vl-plus-2025-12-19"},
		"qwen-vl-plus-2024-08-09": {RetireDate: "2026-01-30", Reason: "qwen_vl_snapshot_retired", Replacement: "qwen3-vl-flash-2025-10-15"},

		// 千问 Audio 快照
		"qwen-audio-turbo-2024-12-04": {RetireDate: "2026-01-30", Reason: "qwen_audio_snapshot_retired", Replacement: "qwen3-asr-flash"},
		"qwen-audio-turbo-2024-08-07": {RetireDate: "2026-01-30", Reason: "qwen_audio_snapshot_retired", Replacement: "qwen3-asr-flash"},
		"qwen-audio-asr-2024-12-04":   {RetireDate: "2026-01-30", Reason: "qwen_audio_snapshot_retired", Replacement: "qwen3-asr-flash"},

		// =========================================================
		// 2025-07-30 已下线
		// =========================================================
		"qwen-vl-plus-2023-12-01": {RetireDate: "2025-07-30", Reason: "qwen_vl_snapshot_retired", Replacement: "qwen-vl-plus"},
		"yi-large":                {RetireDate: "2025-07-30", Reason: "yi_model_retired"},
		"yi-medium":               {RetireDate: "2025-07-30", Reason: "yi_model_retired"},
		"yi-large-rag":            {RetireDate: "2025-07-30", Reason: "yi_model_retired"},
		"yi-large-turbo":          {RetireDate: "2025-07-30", Reason: "yi_model_retired"},
		"dolly-12b-v2":            {RetireDate: "2025-07-30", Reason: "dolly_retired"},

		// =========================================================
		// 2025-07-02 已下线
		// =========================================================

		// Llama 仅文本
		"llama3.3-70b-instruct":  {RetireDate: "2025-07-02", Reason: "llama_retired"},
		"llama3.2-3b-instruct":   {RetireDate: "2025-07-02", Reason: "llama_retired"},
		"llama3.2-1b-instruct":   {RetireDate: "2025-07-02", Reason: "llama_retired"},
		"llama3.1-405b-instruct": {RetireDate: "2025-07-02", Reason: "llama_retired"},
		"llama3.1-70b-instruct":  {RetireDate: "2025-07-02", Reason: "llama_retired"},
		"llama3.1-8b-instruct":   {RetireDate: "2025-07-02", Reason: "llama_retired"},
		"llama3-70b-instruct":    {RetireDate: "2025-07-02", Reason: "llama_retired"},
		"llama3-8b-instruct":     {RetireDate: "2025-07-02", Reason: "llama_retired"},
		"llama2-13b-chat-v2":     {RetireDate: "2025-07-02", Reason: "llama_retired"},
		"llama2-7b-chat-v2":      {RetireDate: "2025-07-02", Reason: "llama_retired"},

		// Llama 文本和图像
		"llama3.2-90b-vision-instruct": {RetireDate: "2025-07-02", Reason: "llama_retired"},
		"llama3.2-11b-vision":          {RetireDate: "2025-07-02", Reason: "llama_retired"},

		// 百川 开源版
		"baichuan2-13b-chat-v1": {RetireDate: "2025-07-02", Reason: "baichuan_opensource_retired"},
		"baichuan2-7b-chat-v1":  {RetireDate: "2025-07-02", Reason: "baichuan_opensource_retired"},
		"baichuan-7b-v1":        {RetireDate: "2025-07-02", Reason: "baichuan_opensource_retired"},

		// ChatGLM
		"chatglm3-6b":    {RetireDate: "2025-07-02", Reason: "chatglm_retired"},
		"chatglm-6b-v2":  {RetireDate: "2025-07-02", Reason: "chatglm_retired"},

		// 其他小众模型
		"ziya-llama-13b-v1":       {RetireDate: "2025-07-02", Reason: "legacy_model_retired"},
		"belle-llama-13b-2m-v1":   {RetireDate: "2025-07-02", Reason: "legacy_model_retired"},
		"chatyuan-large-v2":       {RetireDate: "2025-07-02", Reason: "legacy_model_retired"},
		"billa-7b-sft-v1":         {RetireDate: "2025-07-02", Reason: "legacy_model_retired"},

		// Wanx 老版本
		"wanx-style-cosplay-v1":  {RetireDate: "2025-07-02", Reason: "wanx_legacy_retired"},
		"wanx-ast":               {RetireDate: "2025-07-02", Reason: "wanx_legacy_retired"},
		"wordart-surnames":       {RetireDate: "2025-07-02", Reason: "wanx_legacy_retired"},
		"wanx-anytext-v1":        {RetireDate: "2025-07-02", Reason: "wanx_legacy_retired"},

		// =========================================================
		// 2025-05-08 已下线
		// =========================================================
		"qwen-max-2024-01-07":    {RetireDate: "2025-05-08", Reason: "qwen_max_snapshot_retired", Replacement: "qwen-max"},
		"qwen-plus-2024-06-24":   {RetireDate: "2025-05-08", Reason: "qwen_plus_snapshot_retired", Replacement: "qwen-plus"},
		"qwen-plus-2024-02-06":   {RetireDate: "2025-05-08", Reason: "qwen_plus_snapshot_retired", Replacement: "qwen-plus"},
		"qwen-turbo-2024-02-06":  {RetireDate: "2025-05-08", Reason: "qwen_turbo_snapshot_retired", Replacement: "qwen-turbo"},
		"qwen-vl-max-2024-02-01": {RetireDate: "2025-05-08", Reason: "qwen_vl_snapshot_retired", Replacement: "qwen-vl-max"},

		// 千问 开源版老版本
		"qwen-72b-chat":               {RetireDate: "2025-05-08", Reason: "qwen_opensource_retired", Replacement: "qwen2.5-72b-instruct"},
		"qwen-14b-chat":               {RetireDate: "2025-05-08", Reason: "qwen_opensource_retired", Replacement: "qwen2.5-14b-instruct"},
		"qwen-7b-chat":                {RetireDate: "2025-05-08", Reason: "qwen_opensource_retired", Replacement: "qwen2.5-7b-instruct"},
		"qwen-1.8b-chat":              {RetireDate: "2025-05-08", Reason: "qwen_opensource_retired", Replacement: "qwen2.5-1.5b-instruct"},
		"qwen-1.8b-longcontext-chat":  {RetireDate: "2025-05-08", Reason: "qwen_opensource_retired", Replacement: "qwen2.5-1.5b-instruct"},

		// Math 开源版
		"qwen2-math-72b-instruct":  {RetireDate: "2025-05-08", Reason: "qwen_math_opensource_retired", Replacement: "qwen2.5-math-72b-instruct"},
		"qwen2-math-7b-instruct":   {RetireDate: "2025-05-08", Reason: "qwen_math_opensource_retired", Replacement: "qwen2.5-math-7b-instruct"},
		"qwen2-math-1.5b-instruct": {RetireDate: "2025-05-08", Reason: "qwen_math_opensource_retired", Replacement: "qwen2.5-math-1.5b-instruct"},

		// MotionShop 视频
		"motionshop-video-detect": {RetireDate: "2025-05-08", Reason: "motionshop_retired", Replacement: "animate-anyone-gen2"},
		"motionshop-gen3d":        {RetireDate: "2025-05-08", Reason: "motionshop_retired", Replacement: "animate-anyone-gen2"},
		"motionshop-synthesis":    {RetireDate: "2025-05-08", Reason: "motionshop_retired", Replacement: "animate-anyone-gen2"},

		// =========================================================
		// 2024-04-22 已下线
		// =========================================================
		"qwen-max-1201": {RetireDate: "2024-04-22", Reason: "qwen_max_legacy_retired", Replacement: "qwen-max"},
	}
}

// IsAliyunDeprecated 判断模型是否已被阿里云百炼官方下线
func IsAliyunDeprecated(modelName string) (ModelDeprecation, bool) {
	dep, ok := GetAliyunDeprecatedModels()[normalizeModelID(modelName)]
	return dep, ok
}
