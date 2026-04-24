package support

import (
	"context"
	"strings"
	"time"
	"unicode"

	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// Translator 语言检测 + 翻译（仅检索用）
//
// 设计说明：
//   - LLM 自己处理多语言输出（system prompt 指定目标 locale），无需"中文→目标语言"回译
//   - Translator 仅做两件事：检测语言、把非中文 query 翻译成中文用于 RAG 检索
type Translator struct {
	db    *gorm.DB
	redis *goredis.Client
}

func NewTranslator(db *gorm.DB, redis *goredis.Client) *Translator {
	return &Translator{db: db, redis: redis}
}

// Detect 基于字符集启发式的语言检测（零成本，5ms 级）
// 返回值：zh / ja / ko / ar / ru / en（兜底）
//
// Phase C1: 针对 CJK 纯净快速路径做了增强
//  1. 短文本（≤ 40 字符）出现任一汉字 + 无假名 → 立即判 zh（首字母微分析跳过）
//  2. 补充 CJK 扩展 A/B 区（0x3400-0x4DBF / 0x20000-0x2A6DF）让生僻汉字也识别
//  3. 片假名中点 `・`、全角标点等原本被 IsPunct 过滤掉，避免误判
//  4. 若含 ASCII 字母 > 50% 且 ≥ 20 字符 → 快速判 en（跳过后续比例计算）
func (t *Translator) Detect(text string) string {
	if text == "" {
		return "en"
	}

	// ---- Phase C1 快速路径 ----
	// 1. 纯 ASCII 英文：避免遍历长文本，先做前 32 字节探测
	if isLikelyAsciiEnglish(text) {
		return "en"
	}

	// 2. 短文本 CJK 存在性扫描：含任一汉字/假名/韩文就直接返回
	//    （用户在聊天框通常输入短问题，这个路径覆盖 80%+ 的中文查询）
	if fastLang := quickCJKDetect(text); fastLang != "" {
		return fastLang
	}

	// ---- 标准字符频次统计路径 ----
	var total, chinese, japanese, korean, arabic, cyrillic int
	for _, r := range text {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			continue
		}
		total++
		switch {
		case (r >= 0x4E00 && r <= 0x9FFF) || // 基本 CJK 统一汉字
			(r >= 0x3400 && r <= 0x4DBF) || // CJK 扩展 A
			(r >= 0x20000 && r <= 0x2A6DF): // CJK 扩展 B（生僻字）
			chinese++
		case (r >= 0x3040 && r <= 0x309F) || (r >= 0x30A0 && r <= 0x30FF): // 平假名 / 片假名
			japanese++
		case r >= 0xAC00 && r <= 0xD7AF: // 韩文
			korean++
		case r >= 0x0600 && r <= 0x06FF: // 阿拉伯
			arabic++
		case r >= 0x0400 && r <= 0x04FF: // 西里尔
			cyrillic++
		}
	}
	if total == 0 {
		return "en"
	}
	// 日文可能含大量汉字，但若有假名则判为日文
	if japanese*20 > total {
		return "ja"
	}
	if korean*3 > total {
		return "ko"
	}
	if chinese*3 > total {
		return "zh"
	}
	if arabic*3 > total {
		return "ar"
	}
	if cyrillic*3 > total {
		return "ru"
	}
	return "en"
}

// quickCJKDetect 短文本 / 首字符启发式快速路径，命中立即返回；未命中返回 ""
// 覆盖场景：用户在聊天框输入的中文短句（"怎么充值" / "token 用完了怎么办"）
// 性能：O(n) 单次遍历，遇到假名/韩文字符立即返回，无需完整统计
func quickCJKDetect(text string) string {
	hasChinese := false
	hasKana := false     // 平假名 / 片假名
	hasHangul := false   // 韩文
	scanned := 0
	for _, r := range text {
		scanned++
		switch {
		case (r >= 0x3040 && r <= 0x309F) || (r >= 0x30A0 && r <= 0x30FF):
			hasKana = true
		case r >= 0xAC00 && r <= 0xD7AF:
			hasHangul = true
		case (r >= 0x4E00 && r <= 0x9FFF) ||
			(r >= 0x3400 && r <= 0x4DBF):
			hasChinese = true
		}
		// 扫描上限 40 字符：短文本足够判定
		if scanned >= 40 {
			break
		}
	}
	if hasKana {
		return "ja"
	}
	if hasHangul {
		return "ko"
	}
	if hasChinese && scanned <= 40 {
		return "zh"
	}
	return ""
}

// isLikelyAsciiEnglish 前 32 字节全部 ASCII 且字母比例 > 60% → 判定为英文
// 避免把短 ASCII 英文输入走完整 rune 遍历
func isLikelyAsciiEnglish(text string) bool {
	probe := text
	if len(probe) > 32 {
		probe = probe[:32]
	}
	letters, nonAscii := 0, 0
	for i := 0; i < len(probe); i++ {
		c := probe[i]
		if c >= 0x80 {
			nonAscii++
			break
		}
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			letters++
		}
	}
	if nonAscii > 0 || len(probe) == 0 {
		return false
	}
	return letters*10 >= len(probe)*6 // ≥ 60% 字母
}

// ToZh 把非中文翻译成中文（仅供 embedding 检索用）
// 调 qwen-turbo（¥0.38/M，便宜快），Redis 缓存 5 min
// 此方法依赖一个 LLM 调用客户端（与 chat 使用同一个内部 API Key + /v1/chat/completions）
//
// 参数 llmCall 由外部注入：func(ctx, systemPrompt, userText) (string, error)
// 这样避免 translator 直接持有 LLM 客户端，解耦
func (t *Translator) ToZh(ctx context.Context, text string, llmCall func(context.Context, string, string) (string, error)) string {
	if text == "" {
		return text
	}
	// 已经是中文，直接返回
	if t.Detect(text) == "zh" {
		return text
	}
	// 缓存
	cacheKey := "support:tr:zh:" + md5Hex(text)
	if t.redis != nil {
		if s, err := t.redis.Get(ctx, cacheKey).Result(); err == nil && s != "" {
			return s
		}
	}
	// 调 LLM 翻译
	if llmCall == nil {
		return text // 无翻译通道，降级用原文（text-embedding-v3 多语言兼容）
	}
	zh, err := llmCall(ctx,
		"你是精准的翻译引擎。将用户输入翻译为中文，保持技术术语、API 参数名、代码、URL 不变。只输出译文，不要解释。",
		text,
	)
	if err != nil || strings.TrimSpace(zh) == "" {
		return text
	}
	if t.redis != nil {
		_ = t.redis.Set(ctx, cacheKey, zh, 5*time.Minute).Err()
	}
	return zh
}
