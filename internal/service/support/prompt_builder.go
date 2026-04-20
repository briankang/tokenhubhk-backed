package support

import (
	"context"
	"fmt"
	"strings"

	"tokenhub-server/internal/model"
)

// PromptBuilder 构造 AI 客服的 system prompt
type PromptBuilder struct {
	resolver *DynamicValueResolver
}

func NewPromptBuilder(resolver *DynamicValueResolver) *PromptBuilder {
	return &PromptBuilder{resolver: resolver}
}

// BuildInput Prompt 渲染所需输入
type BuildInput struct {
	Locale        string   // 用户 locale（zh/en/ja/...），指示 AI 用此语言回复
	UserLevel     string   // 用户等级（V0-V4，可选）
	Chunks        []ScoredChunk
	ProviderUrls  []model.ProviderDocReference
	Memories      []model.UserSupportMemory
	HistoryTurns  int
}

// Build 返回完整 system prompt（中文，但 AI 会按 Locale 输出）
func (pb *PromptBuilder) Build(ctx context.Context, in BuildInput) string {
	var sb strings.Builder

	sb.WriteString(`你是 tokenhubhk（AI 模型聚合平台）的官方客服助手，专业解答 API 使用、计费、渠道、账户相关问题。

【严格规则】
1. 只能基于下方「知识库」和「供应商文档链接」回答。知识库未覆盖的技术细节（字段名、参数、URL、价格）禁止编造。
2. 涉及账户私有数据（余额、订单号、充值状态、API Key）、投诉、退款、故障申报、企业合作：末尾输出 <need_human/> 并建议用户「提交工单，人工客服 24 小时内回复」。
3. 如果涉及供应商原生 API 参数细节、错误码、SDK，在相关位置引用「供应商文档链接」，格式：` + "`参考 [供应商名-文档标题](URL)`" + `。
4. 不回答与平台无关的问题（写代码、闲聊）。
5. **回答必须使用用户提问的语言（目标语言代码：` + in.Locale + `）。** 用户用中文问就用中文答，用英文就用英文答。代码块、URL、API 参数名保持原始英文。
6. Markdown 格式，分点，简洁，≤300 字。

`)

	// 用户背景
	sb.WriteString("【用户背景】\n")
	if in.UserLevel == "" {
		in.UserLevel = "V0"
	}
	sb.WriteString(fmt.Sprintf("会员等级: %s | 会话语言: %s\n\n", in.UserLevel, in.Locale))

	// 个人记忆
	if len(in.Memories) > 0 {
		sb.WriteString("【个人记忆】（不共享，仅当前用户）\n")
		for _, m := range in.Memories {
			sb.WriteString(fmt.Sprintf("- [%s] %s\n", m.MemoryType, strings.TrimSpace(m.Content)))
		}
		sb.WriteString("\n")
	}

	// 知识库
	sb.WriteString("【知识库检索结果】（按相关度降序）\n")
	if len(in.Chunks) == 0 {
		sb.WriteString("（本次未检索到高相关度片段。若问题超出平台知识，请末尾输出 <need_human/>）\n\n")
	} else {
		// 先做占位符替换（仅在 chunks 内容上）
		contents := make([]string, len(in.Chunks))
		for i, c := range in.Chunks {
			contents[i] = c.Chunk.Content
		}
		resolved := contents
		if pb.resolver != nil {
			resolved = pb.resolver.ResolveMany(ctx, contents)
		}
		for i, c := range in.Chunks {
			title := c.Chunk.Title
			if title == "" {
				title = "（无标题）"
			}
			sb.WriteString(fmt.Sprintf("### %s (score=%.2f)\n%s\n\n", title, c.Raw, truncateText(resolved[i], 800)))
		}
	}

	// 供应商文档链接
	if len(in.ProviderUrls) > 0 {
		sb.WriteString("【供应商官方文档链接】（可引用）\n")
		for _, u := range in.ProviderUrls {
			sb.WriteString(fmt.Sprintf("- [%s](%s) — %s\n", u.Title, u.URL, u.Description))
		}
		sb.WriteString("\n")
	}

	if in.HistoryTurns > 0 {
		sb.WriteString(fmt.Sprintf("【历史对话】 已为你注入最近 %d 轮对话。\n\n", in.HistoryTurns))
	}

	return sb.String()
}

// BuildEmergencyReply 预算熔断时的固定话术
func (pb *PromptBuilder) BuildEmergencyReply(locale string) string {
	if strings.HasPrefix(locale, "zh") || locale == "" {
		return "抱歉，AI 客服本月额度已达上限。已为你生成工单草稿，人工客服将在 24 小时内回复。请点击「提交工单」按钮继续。<need_human/>"
	}
	return "Sorry, the AI support budget for this month has been reached. A ticket draft has been prepared for you. Our human team will reply within 24 hours. Please click 'Submit Ticket' to continue. <need_human/>"
}

func truncateText(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
}
