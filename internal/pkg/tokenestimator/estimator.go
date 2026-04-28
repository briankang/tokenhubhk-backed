// Package tokenestimator 提供 LLM token 数量的字符级粗估，
// 仅用于上游 usage 缺失时的兜底计费，不替代 provider 返回的真实 usage。
//
// 估算策略：
//   - 默认按字节数 / 3.2 粗估（混合中英文场景的工程经验值）
//   - 中文字符（utf8 多字节）按 ~1 token / 字符
//   - 英文字符按 ~4 字节 / token
//   - 估算结果**有意偏高 20%**：宁可多收也不漏收，配合
//     usage_estimated=true 标志便于后续退款修正
//
// 注意事项：
//   - 仅适用于 stream usage 缺失等极端场景
//   - 不支持图片/音频/视频等多模态内容
//   - 与 tiktoken 等精确编码器存在 ±30% 偏差是正常的
package tokenestimator

import (
	"unicode/utf8"
)

// SafetyMargin 估算结果上调系数（避免少收）
const SafetyMargin = 1.20

// Message 估算输入消息（与 provider.Message 兼容）
type Message struct {
	Role    string
	Content string
}

// EstimatePromptTokens 粗估 prompt 中的总 token 数
//
// 计算方式：把所有 messages 的 role 与 content 拼接后算字符
// 每条 message 额外加 4 个 framing token（OpenAI 经验值）
func EstimatePromptTokens(messages []Message) int {
	if len(messages) == 0 {
		return 0
	}
	total := 0
	for _, m := range messages {
		// 每条消息固定 framing 开销（role + delimiters）
		total += 4
		total += estimateText(m.Role)
		total += estimateText(m.Content)
	}
	// 收尾 framing token
	total += 2
	return applyMargin(total)
}

// EstimateCompletionTokens 粗估 completion 内容的 token 数
func EstimateCompletionTokens(content string) int {
	if content == "" {
		return 0
	}
	return applyMargin(estimateText(content))
}

// EstimateRawText 粗估纯文本 token 数（不加 framing）
// 用于已知是纯输出文本场景
func EstimateRawText(text string) int {
	return applyMargin(estimateText(text))
}

// estimateText 内部：按 utf8 rune / byte 混合策略估算
//
// 策略：
//   - 多字节 rune（中文/日韩/Emoji）按 1 token/rune
//   - ASCII 单字节按 4 字节/token
//
// 这样混合中英文输入也能得到合理估算。
func estimateText(s string) int {
	if s == "" {
		return 0
	}
	multiByte := 0
	asciiBytes := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if size > 1 {
			multiByte++
		} else if r != utf8.RuneError {
			asciiBytes++
		}
		i += size
	}
	tokens := multiByte + asciiBytes/4
	if tokens == 0 && (multiByte > 0 || asciiBytes > 0) {
		tokens = 1 // 极短文本兜底 1 token
	}
	return tokens
}

func applyMargin(raw int) int {
	if raw <= 0 {
		return 0
	}
	return int(float64(raw)*SafetyMargin + 0.5)
}
