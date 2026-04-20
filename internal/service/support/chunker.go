package support

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// ChunkOptions 切片选项
type ChunkOptions struct {
	SoftMax  int // 软上限字符数（默认 800）
	HardMax  int // 硬上限字符数（超过强制切，默认 1200）
	MinChunk int // 最小片段字符数（默认 100，短于此的合并）
}

func defaultOpts() ChunkOptions {
	return ChunkOptions{SoftMax: 800, HardMax: 1200, MinChunk: 100}
}

// Chunk 表示切片结果
type Chunk struct {
	Title      string // 最近的上级标题（"章 > 节 > 小节"）
	Content    string // 正文
	ChunkIndex int    // 在原文档中的顺序
}

// SplitMarkdown 按 Markdown 结构切片
// 策略：
//  1. 按 H2/H3 标题拆段，每段保留上级标题上下文
//  2. 若段落超 SoftMax 字符，按双换行/句号再切
//  3. 极短段落（< MinChunk）合并到下一段
func SplitMarkdown(title, md string, opts ...ChunkOptions) []Chunk {
	opt := defaultOpts()
	if len(opts) > 0 {
		opt = opts[0]
	}
	if strings.TrimSpace(md) == "" {
		return nil
	}

	// 按标题扫描
	lines := strings.Split(md, "\n")
	type section struct {
		title   string
		content []string
	}
	var sections []section
	var curTitle = title
	var curContent []string

	// 跟踪 H2 / H3 path（用于 title 拼接 "章 > 节"）
	var h2 string
	for _, line := range lines {
		h2Match := regexp.MustCompile(`^##\s+(.+)$`).FindStringSubmatch(line)
		h3Match := regexp.MustCompile(`^###\s+(.+)$`).FindStringSubmatch(line)
		if len(h2Match) > 1 {
			// flush
			if len(curContent) > 0 {
				sections = append(sections, section{curTitle, curContent})
			}
			h2 = strings.TrimSpace(h2Match[1])
			curTitle = title + " > " + h2
			curContent = nil
			continue
		}
		if len(h3Match) > 1 {
			if len(curContent) > 0 {
				sections = append(sections, section{curTitle, curContent})
			}
			h3 := strings.TrimSpace(h3Match[1])
			if h2 != "" {
				curTitle = title + " > " + h2 + " > " + h3
			} else {
				curTitle = title + " > " + h3
			}
			curContent = nil
			continue
		}
		curContent = append(curContent, line)
	}
	if len(curContent) > 0 {
		sections = append(sections, section{curTitle, curContent})
	}

	// 每个 section 按字数进一步切
	var chunks []Chunk
	for _, sec := range sections {
		text := strings.TrimSpace(strings.Join(sec.content, "\n"))
		if text == "" {
			continue
		}
		for _, piece := range softSplit(text, opt.SoftMax, opt.HardMax) {
			chunks = append(chunks, Chunk{Title: sec.title, Content: piece})
		}
	}

	// 合并极短段
	chunks = mergeSmall(chunks, opt.MinChunk)

	// 填序号
	for i := range chunks {
		chunks[i].ChunkIndex = i
	}
	return chunks
}

// softSplit 把一段文本按软上限切分
func softSplit(text string, softMax, hardMax int) []string {
	runeLen := utf8.RuneCountInString(text)
	if runeLen <= softMax {
		return []string{text}
	}

	// 优先按双换行切
	parts := strings.Split(text, "\n\n")
	var out []string
	var buf strings.Builder
	bufRunes := 0
	flush := func() {
		s := strings.TrimSpace(buf.String())
		if s != "" {
			out = append(out, s)
		}
		buf.Reset()
		bufRunes = 0
	}
	for _, p := range parts {
		pRunes := utf8.RuneCountInString(p)
		if bufRunes+pRunes+2 > softMax && bufRunes > 0 {
			flush()
		}
		if pRunes > hardMax {
			// 单段过长：按句号再切
			for _, s := range splitBySentence(p, hardMax) {
				out = append(out, s)
			}
			continue
		}
		if buf.Len() > 0 {
			buf.WriteString("\n\n")
			bufRunes += 2
		}
		buf.WriteString(p)
		bufRunes += pRunes
	}
	flush()
	return out
}

func splitBySentence(text string, hardMax int) []string {
	// 中英文句号
	sentences := regexp.MustCompile(`[。！？.!?]+`).Split(text, -1)
	var out []string
	var buf strings.Builder
	bufRunes := 0
	for _, s := range sentences {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		sRunes := utf8.RuneCountInString(s)
		if bufRunes+sRunes > hardMax && bufRunes > 0 {
			out = append(out, strings.TrimSpace(buf.String()))
			buf.Reset()
			bufRunes = 0
		}
		buf.WriteString(s)
		buf.WriteString("。")
		bufRunes += sRunes + 1
	}
	if buf.Len() > 0 {
		out = append(out, strings.TrimSpace(buf.String()))
	}
	return out
}

func mergeSmall(chunks []Chunk, minChunk int) []Chunk {
	if len(chunks) < 2 {
		return chunks
	}
	var out []Chunk
	for _, c := range chunks {
		if utf8.RuneCountInString(c.Content) < minChunk && len(out) > 0 {
			out[len(out)-1].Content += "\n\n" + c.Content
			continue
		}
		out = append(out, c)
	}
	return out
}

// EstimateTokens 粗略估算 token 数（中文 ~1.5 字符/token，英文 ~4 字符/token，混合取均值）
func EstimateTokens(text string) int {
	runes := utf8.RuneCountInString(text)
	chinese := 0
	for _, r := range text {
		if r >= 0x4E00 && r <= 0x9FFF {
			chinese++
		}
	}
	if runes == 0 {
		return 0
	}
	chinesePct := float64(chinese) / float64(runes)
	// 插值
	cpt := 1.5*chinesePct + 4.0*(1.0-chinesePct)
	return int(float64(runes) / cpt)
}
