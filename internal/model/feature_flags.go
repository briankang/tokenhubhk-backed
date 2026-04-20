// Package model 提供能力标记键白名单（用于区分 ExtraParams 中的脏数据与合法参数）
package model

// BogusFlagKeys 列出所有被视为"能力标记"的键名
//
// 背景：历史上 UI 误把模型能力标记（如"是否支持 stop"）当作 extra_params 自定义参数写入
// ai_models.extra_params，形成 {"stop": true, "voice": true} 这类 {key: bool} 形式的脏数据。
// 当 v1/chat/completions 把这些参数合并进请求体时，上游 (如百度千帆) 会报
// `malformed_json: cannot unmarshal bool into Go struct field ... of type []string`。
//
// 判定规则（与 migrate_extra_params_feature_flags.go + completions_handler.mergeExtraParams 共用）：
//
//	if BogusFlagKeys[key] && typeof(value) == bool { 视为脏数据，清除或跳过 }
//
// 注意：此列表仅包含"值为 bool 时一定非法"的键名；合法自定义参数（即使 key 名相同
// 但值类型合法，如 stop=["END"]）不受影响，不会被误清。
var BogusFlagKeys = map[string]bool{
	// OpenAI 协议原生字段（bool 绝对非法）
	"stop":            true, // 应为 []string
	"stream":          true, // 应为 bool，但由 handler 显式控制，不允许 extra_params 覆盖
	"tools":           true, // 应为 []object
	"tool_choice":     true, // 应为 string 或 object
	"logit_bias":      true, // 应为 object
	"logprobs":        true, // 应为 bool（但作为 flag 标记时通常被误写）
	"top_logprobs":    true, // 应为 int
	"response_format": true, // 应为 object
	"seed":            true, // 应为 int
	"n":               true, // 应为 int
	"user":            true, // 应为 string

	// 供应商能力标记（常被误当 flag 写入）
	"voice":            true, // TTS：应为 string
	"dimensions":       true, // Embedding：应为 int
	"size":             true, // Image：应为 string (如 "1024x1024")
	"quality":          true, // Image：应为 string
	"style":            true, // Image：应为 string
	"reasoning":        true, // 应为 object 或 string
	"reasoning_effort": true, // 应为 string
	"web_search":       true, // 应为 object
	"enable_search":    true, // 应为 bool（合法），但作为能力标记常被误写；由 param_mapping 统一处理
	"enable_thinking":  true, // 同上
	"thinking":         true, // 应为 object
	"prefix":           true, // 应为 string
	"suffix":           true, // 应为 string
}

// IsBogusFlagKey 判断给定键名是否在能力标记白名单中
//
// 用于 completions_handler.mergeExtraParams 的防御性过滤：
// 若 key 在白名单且 value 是 bool，跳过该键防止脏数据污染请求体。
func IsBogusFlagKey(key string) bool {
	return BogusFlagKeys[key]
}
