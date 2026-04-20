package modeldiscovery

import "strings"

// streamOnlyPrefixes 列出强制要求流式接口的模型前缀。
//
// 这些模型当以非流式方式请求时，上游会直接返回 400（如阿里云 qwq/qvq 的
// `only support stream mode`）。发现阶段应主动标记 features.requires_stream=true，
// 由 v1/chat/completions handler 的 auto-upgrade 逻辑将非流式请求转为流式。
//
// 维护说明：
//   - 新增供应商要求流式的模型时，在此追加前缀即可
//   - 前缀匹配不区分大小写
var streamOnlyPrefixes = []string{
	"qwq-",  // 阿里云通义千问 QwQ 推理系列（qwq-plus, qwq-32b-preview 等）
	"qvq-",  // 阿里云通义千问 QvQ 视觉推理系列
	"qwen3-coder-plus",   // 同属阿里云，典型强制流式
}

// MatchStreamOnly 判断模型名称是否属于强制流式名单
func MatchStreamOnly(modelName string) bool {
	lower := strings.ToLower(strings.TrimSpace(modelName))
	if lower == "" {
		return false
	}
	for _, prefix := range streamOnlyPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}
