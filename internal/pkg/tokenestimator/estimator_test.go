package tokenestimator

import (
	"strings"
	"testing"
)

func TestEstimateRawText_PureEnglish(t *testing.T) {
	// "Hello, world!" 13 ASCII 字节 → 13/4 = 3 token，加 20% 安全裕量 → 4
	got := EstimateRawText("Hello, world!")
	if got < 3 || got > 6 {
		t.Errorf("English estimate out of expected range [3,6], got %d", got)
	}
}

func TestEstimateRawText_PureChinese(t *testing.T) {
	// "你好世界" 4 个汉字 → 4 token，加 20% 安全裕量 → 5
	got := EstimateRawText("你好世界")
	if got < 4 || got > 6 {
		t.Errorf("Chinese estimate out of expected range [4,6], got %d", got)
	}
}

func TestEstimateRawText_MixedZhEn(t *testing.T) {
	// 50 ASCII 字节 + 10 个汉字 → 50/4 + 10 = 22 token，加 20% → ~26
	text := strings.Repeat("a", 50) + "你好世界你好世界你好"
	got := EstimateRawText(text)
	if got < 22 || got > 32 {
		t.Errorf("Mixed estimate out of expected range [22,32], got %d", got)
	}
}

func TestEstimateRawText_Empty(t *testing.T) {
	if got := EstimateRawText(""); got != 0 {
		t.Errorf("Empty string should yield 0 tokens, got %d", got)
	}
}

func TestEstimateRawText_VeryShort(t *testing.T) {
	// "a" 1 ASCII 字节 → 0/4 = 0 但兜底 1，加 20% → 1
	if got := EstimateRawText("a"); got < 1 {
		t.Errorf("Single char should yield at least 1 token, got %d", got)
	}
}

func TestEstimatePromptTokens_MultipleMessages(t *testing.T) {
	messages := []Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "你好"},
		{Role: "assistant", Content: "Hello!"},
	}
	got := EstimatePromptTokens(messages)
	// 至少应该 > 12（每条 4 framing + 内容估算）
	if got < 12 {
		t.Errorf("Multi-message prompt should yield ≥12 tokens, got %d", got)
	}
}

func TestEstimatePromptTokens_Empty(t *testing.T) {
	if got := EstimatePromptTokens(nil); got != 0 {
		t.Errorf("Nil messages should yield 0, got %d", got)
	}
	if got := EstimatePromptTokens([]Message{}); got != 0 {
		t.Errorf("Empty messages should yield 0, got %d", got)
	}
}

func TestEstimateCompletionTokens(t *testing.T) {
	// 100 ASCII 字符 → 25 token，加 20% → 30
	text := strings.Repeat("a", 100)
	got := EstimateCompletionTokens(text)
	if got < 25 || got > 40 {
		t.Errorf("100-char completion expected 25-40 tokens, got %d", got)
	}
}

func TestEstimateCompletionTokens_Empty(t *testing.T) {
	if got := EstimateCompletionTokens(""); got != 0 {
		t.Errorf("Empty completion should yield 0, got %d", got)
	}
}

func TestSafetyMarginApplied(t *testing.T) {
	// SafetyMargin = 1.2，估算结果应高于"原始"
	rawText := strings.Repeat("a", 1000) // 250 raw token
	got := EstimateRawText(rawText)
	expectedMin := 250 // 不能少收
	if got < expectedMin {
		t.Errorf("Safety margin not applied: got %d expected ≥%d", got, expectedMin)
	}
}
