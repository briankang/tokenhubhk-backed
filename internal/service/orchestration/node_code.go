package orchestration

import (
	"context"
	"fmt"
	"strings"

	"tokenhub-server/internal/pkg/safego"
	"tokenhub-server/internal/provider"
)

// CodeNode 代码节点，执行简单的内容变换逻辑
// 支持模板渲染、前后缀添加、代码块提取等操作，无需外部运行时
type CodeNode struct {
	step OrchestrationStep
}

// NewCodeNode 创建代码工作流节点实例
func NewCodeNode(step OrchestrationStep) *CodeNode {
	return &CodeNode{step: step}
}

// Validate 校验代码节点配置，名称必填
func (n *CodeNode) Validate() error {
	if n.step.Name == "" {
		return fmt.Errorf("code node: name is required")
	}
	return nil
}

// Execute 同步执行内容变换，代码节点不消耗Token
func (n *CodeNode) Execute(ctx context.Context, input *NodeInput) (*NodeOutput, error) {
	if err := n.Validate(); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	content := n.transform(input)

	return &NodeOutput{
		Content: content,
		Usage:   provider.Usage{}, // code nodes don't consume tokens
		Metadata: map[string]interface{}{
			"node":      n.step.Name,
			"node_type": "code",
		},
	}, nil
}

// Stream 将Execute包装为单事件流（代码节点是同步的）
func (n *CodeNode) Stream(ctx context.Context, input *NodeInput) (<-chan StreamEvent, error) {
	ch := make(chan StreamEvent, 2)
	safego.Go("orchestration-code-stream", func() {
		defer close(ch)

		output, err := n.Execute(ctx, input)
		if err != nil {
			ch <- StreamEvent{
				NodeName: n.step.Name,
				Status:   "error",
				Error:    err,
			}
			return
		}

		ch <- StreamEvent{
			NodeName: n.step.Name,
			Content:  output.Content,
			Status:   "running",
		}
		ch <- StreamEvent{
			NodeName: n.step.Name,
			Status:   "done",
			Usage:    &output.Usage,
		}
	})
	return ch, nil
}

// transform 根据配置的action参数对输入内容进行变换
func (n *CodeNode) transform(input *NodeInput) string {
	// 获取最后一条消息作为基础输入
	baseContent := ""
	if len(input.Messages) > 0 {
		baseContent = provider.TextContent(input.Messages[len(input.Messages)-1].Content)
	}

	// 检查是否指定了上游节点输出作为数据源
	if n.step.Params != nil {
		if src, ok := n.step.Params["source"]; ok {
			if srcName, ok := src.(string); ok {
				if val, exists := input.Upstream[srcName]; exists {
					if s, ok := val.(string); ok {
						baseContent = s
					}
				}
			}
		}
	}

	action := "passthrough"
	if n.step.Params != nil {
		if a, ok := n.step.Params["action"]; ok {
			action, _ = a.(string)
		}
	}

	switch action {
	case "template":
		return n.applyTemplate(baseContent, input)
	case "prefix":
		prefix := n.paramString("prefix")
		return prefix + baseContent
	case "suffix":
		suffix := n.paramString("suffix")
		return baseContent + suffix
	case "wrap":
		prefix := n.paramString("prefix")
		suffix := n.paramString("suffix")
		return prefix + baseContent + suffix
	case "extract_code":
		return extractCodeBlocks(baseContent)
	case "truncate":
		maxLen := n.paramInt("max_length", 4000)
		if len(baseContent) > maxLen {
			return baseContent[:maxLen]
		}
		return baseContent
	case "join_upstream":
		return n.joinUpstream(input, "\n\n")
	default:
		// passthrough
		return baseContent
	}
}

// applyTemplate 替换{{variable}}占位符为实际值（支持input/变量/上游引用）
func (n *CodeNode) applyTemplate(content string, input *NodeInput) string {
	template := n.paramString("template")
	if template == "" {
		return content
	}

	result := template
	// 替换{{input}}为基础内容
	result = strings.ReplaceAll(result, "{{input}}", content)

	// 替换变量占位符
	for k, v := range input.Variables {
		result = strings.ReplaceAll(result, "{{"+k+"}}", v)
	}

	// 替换上游节点输出引用
	for k, v := range input.Upstream {
		if s, ok := v.(string); ok {
			result = strings.ReplaceAll(result, "{{upstream."+k+"}}", s)
		}
	}

	return result
}

// joinUpstream 拼接所有上游节点的输出内容
func (n *CodeNode) joinUpstream(input *NodeInput, sep string) string {
	if s := n.paramString("separator"); s != "" {
		sep = s
	}
	var parts []string
	for _, v := range input.Upstream {
		if s, ok := v.(string); ok && s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, sep)
}

// extractCodeBlocks 从Markdown代码块中提取代码内容
func extractCodeBlocks(content string) string {
	var blocks []string
	lines := strings.Split(content, "\n")
	inBlock := false
	var current []string

	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if inBlock {
				blocks = append(blocks, strings.Join(current, "\n"))
				current = nil
				inBlock = false
			} else {
				inBlock = true
			}
			continue
		}
		if inBlock {
			current = append(current, line)
		}
	}

	if len(blocks) == 0 {
		return content // return original if no code blocks found
	}
	return strings.Join(blocks, "\n\n")
}

// paramString 从步骤配置中获取字符串参数
func (n *CodeNode) paramString(key string) string {
	if n.step.Params == nil {
		return ""
	}
	if v, ok := n.step.Params[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// paramInt 从步骤配置中获取整数参数，不存在则返回默认值
func (n *CodeNode) paramInt(key string, def int) int {
	if n.step.Params == nil {
		return def
	}
	if v, ok := n.step.Params[key]; ok {
		if f, ok := v.(float64); ok {
			return int(f)
		}
	}
	return def
}
