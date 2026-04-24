package modeldiscovery

import (
	"encoding/json"
	"strings"

	"tokenhub-server/internal/model"
)

var capabilityKeys = []string{
	"supports_thinking",
	"supports_web_search",
	"supports_vision",
	"supports_function_call",
	"supports_json_mode",
}

// InferFeaturesFromName 根据模型名称推断其具备的能力（features）。
// 兼容旧调用：没有供应商和模型类型上下文时，只做保守的模型名推断。
func InferFeaturesFromName(modelName string, features map[string]interface{}) {
	InferFeaturesForModel("", modelName, "", nil, nil, features)
}

// InferFeaturesForModel 根据供应商、模型名、模型类型和上游元数据推断模型能力。
//
// 优先级：
//  1. 非 LLM/VLM 的生成或语音模型先清空对话能力，避免继承供应商默认能力。
//  2. 模型名/类型/模态命中明确规则后写入 true。
//  3. 未命中的核心能力写入 false，修正历史“供应商全选”造成的过度标记。
//  4. 其他非能力字段（param:stop、supports_cache、requires_stream 等）原样保留。
func InferFeaturesForModel(supplierCode, modelName, modelType string, inputModalities, taskTypes model.JSON, features map[string]interface{}) {
	if features == nil {
		return
	}

	name := strings.ToLower(strings.TrimSpace(modelName))
	supplier := strings.ToLower(strings.TrimSpace(supplierCode))
	mt := strings.ToUpper(strings.TrimSpace(modelType))
	inputs := parseStringJSON(inputModalities)
	tasks := parseStringJSON(taskTypes)

	if mt != "" && mt != "LLM" && mt != "VLM" && mt != "VISION" {
		clearCoreCapabilities(features)
		return
	}

	inferred := map[string]bool{
		"supports_thinking":      inferThinking(supplier, name),
		"supports_web_search":    inferWebSearch(supplier, name),
		"supports_vision":        inferVision(name, mt, inputs),
		"supports_function_call": inferFunctionCall(supplier, name, mt, tasks),
		"supports_json_mode":     inferJSONMode(supplier, name, mt, tasks),
	}

	for _, key := range capabilityKeys {
		features[key] = inferred[key]
	}

	if inferred["supports_thinking"] && requiresStreamForReasoning(name) {
		features["requires_stream"] = true
	}
}

func clearCoreCapabilities(features map[string]interface{}) {
	for _, key := range capabilityKeys {
		features[key] = false
	}
}

func parseStringJSON(raw model.JSON) []string {
	if len(raw) == 0 {
		return nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return lowerList(list)
	}
	var anyList []interface{}
	if err := json.Unmarshal(raw, &anyList); err != nil {
		return nil
	}
	out := make([]string, 0, len(anyList))
	for _, v := range anyList {
		if s, ok := v.(string); ok {
			out = append(out, strings.ToLower(strings.TrimSpace(s)))
		}
	}
	return out
}

func lowerList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, strings.ToLower(strings.TrimSpace(s)))
	}
	return out
}

func inferThinking(supplier, name string) bool {
	if featureNameContainsAny(name, "thinking", "reasoning", "reasoner", "deepseek-r1", "qwq", "qvq", "ernie-x1", "hunyuan-t1", "t1-vision") {
		return true
	}
	if strings.Contains(name, "qwen3") && !strings.Contains(name, "instruct") {
		return true
	}
	if strings.HasPrefix(name, "o1") || strings.HasPrefix(name, "o3") || strings.HasPrefix(name, "o4") {
		return true
	}
	if strings.HasPrefix(name, "gpt-5") {
		return true
	}
	if strings.Contains(name, "claude-sonnet-4") || strings.Contains(name, "claude-opus-4") {
		return true
	}
	if supplier == "tencent_hunyuan" && strings.Contains(name, "2.0-thinking") {
		return true
	}
	return false
}

func inferWebSearch(supplier, name string) bool {
	if featureNameContainsAny(name, "web-search", "web_search", "deep-search", "deepsearch", "deep-research", "search-planning") {
		return true
	}
	if supplier == "tencent_hunyuan" && (strings.Contains(name, "hunyuan-2.0-thinking") || strings.Contains(name, "hunyuan-2.0-instruct")) {
		return true
	}
	// 网宿中只有少数 Claude 网关版本实测配置了搜索能力，按显式版本名开启。
	if supplier == "wangsu_aigw" && featureNameContainsAny(name, "claude-sonnet-4-6", "claude-opus-4-6", "claude-opus-4-7") {
		return true
	}
	return false
}

func inferVision(name, modelType string, inputs []string) bool {
	if modelType == "VLM" || modelType == "VISION" {
		return true
	}
	if featureNameContainsAny(name, "-vl", "vl-", "vision", "omni", "qvq", "gpt-4o", "gemini", "image-to-text", "t1-vision") {
		return true
	}
	for _, in := range inputs {
		if in == "image" || in == "video" {
			return true
		}
	}
	return false
}

func inferFunctionCall(supplier, name, modelType string, tasks []string) bool {
	if modelType != "" && modelType != "LLM" && modelType != "VLM" && modelType != "VISION" {
		return false
	}
	for _, task := range tasks {
		if task == "tool" || task == "tools" || task == "function_call" || task == "function-calling" {
			return true
		}
	}
	if featureNameContainsAny(name, "functioncall", "function-call", "function_call") {
		return true
	}
	switch supplier {
	case "aliyun_dashscope":
		return isModernQwenText(name) || isQwenMultimodalToolModel(name) || featureNameContainsAny(name, "deepseek-v3", "kimi-k2", "glm-", "minimax-m")
	case "volcengine":
		return strings.Contains(name, "doubao") || strings.Contains(name, "deepseek-v3")
	case "baidu_qianfan":
		return strings.Contains(name, "ernie-4.") || strings.Contains(name, "ernie-5.") || strings.Contains(name, "ernie-x1")
	case "tencent_hunyuan":
		return strings.Contains(name, "hunyuan-2.0") || strings.Contains(name, "hunyuan-functioncall") || strings.Contains(name, "deepseek-v3")
	case "wangsu_aigw":
		return featureNameContainsAny(name, "gpt-4", "gpt-5", "claude-", "gemini-")
	case "talkingdata":
		return strings.Contains(name, "doubao-seed-2.0") || strings.Contains(name, "doubao-seed-1.6")
	default:
		return false
	}
}

func inferJSONMode(supplier, name, modelType string, tasks []string) bool {
	if modelType != "" && modelType != "LLM" && modelType != "VLM" && modelType != "VISION" {
		return false
	}
	for _, task := range tasks {
		if task == "json" || task == "json_mode" || task == "structured_output" {
			return true
		}
	}
	if featureNameContainsAny(name, "json", "structured") {
		return true
	}
	switch supplier {
	case "aliyun_dashscope":
		return isModernQwenText(name) || featureNameContainsAny(name, "deepseek-v3", "kimi-k2", "glm-", "minimax-m")
	case "volcengine":
		return strings.Contains(name, "doubao") || strings.Contains(name, "deepseek-v3")
	case "baidu_qianfan":
		return strings.Contains(name, "ernie-4.") || strings.Contains(name, "ernie-5.") || strings.Contains(name, "ernie-x1")
	case "tencent_hunyuan":
		return strings.Contains(name, "hunyuan-2.0") || strings.Contains(name, "deepseek-v3")
	case "wangsu_aigw":
		return featureNameContainsAny(name, "gpt-4", "gpt-5", "gemini-")
	case "talkingdata":
		return strings.Contains(name, "doubao-seed-2.0") || strings.Contains(name, "doubao-seed-1.6")
	default:
		return false
	}
}

func isModernQwenText(name string) bool {
	if strings.Contains(name, "qwen-image") || strings.Contains(name, "qwen-tts") || strings.Contains(name, "qwen-asr") {
		return false
	}
	return featureNameContainsAny(name,
		"qwen-plus", "qwen-turbo", "qwen-max", "qwen-flash", "qwen-long",
		"qwen3", "qwen-coder", "qwen-math", "qwen-mt",
	)
}

func isQwenMultimodalToolModel(name string) bool {
	if strings.Contains(name, "qwen-image") || strings.Contains(name, "qwen-tts") || strings.Contains(name, "qwen-asr") {
		return false
	}
	return featureNameContainsAny(name, "qwen-vl", "qwen3-vl", "qwen-omni", "qwen3-omni")
}

func requiresStreamForReasoning(name string) bool {
	return featureNameContainsAny(name, "qwq", "qvq", "thinking", "reasoner", "deepseek-r1")
}

func featureNameContainsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}
