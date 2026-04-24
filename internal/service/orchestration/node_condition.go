package orchestration

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"tokenhub-server/internal/pkg/safego"
	"tokenhub-server/internal/provider"
)

// ConditionNode 条件节点，评估路由条件决定走哪个分支
// 支持的条件表达式: "length > 500", "contains:code", "complexity:high"等
type ConditionNode struct {
	step OrchestrationStep
}

// NewConditionNode 创建条件工作流节点实例
func NewConditionNode(step OrchestrationStep) *ConditionNode {
	return &ConditionNode{step: step}
}

// Validate 校验条件节点配置，名称和条件表达式必填
func (n *ConditionNode) Validate() error {
	if n.step.Name == "" {
		return fmt.Errorf("condition node: name is required")
	}
	if n.step.Condition == "" {
		return fmt.Errorf("condition node %q: condition expression is required", n.step.Name)
	}
	return nil
}

// Execute 评估条件表达式，返回"true"或"false"作为输出内容
func (n *ConditionNode) Execute(ctx context.Context, input *NodeInput) (*NodeOutput, error) {
	if err := n.Validate(); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	lastMsg := ""
	if len(input.Messages) > 0 {
		lastMsg = provider.TextContent(input.Messages[len(input.Messages)-1].Content)
	}

	result := n.Evaluate(len(lastMsg), lastMsg)
	content := "false"
	if result {
		content = "true"
	}

	return &NodeOutput{
		Content: content,
		Usage:   provider.Usage{},
		Metadata: map[string]interface{}{
			"node":      n.step.Name,
			"node_type": "condition",
			"condition": n.step.Condition,
			"result":    result,
		},
	}, nil
}

// Stream 将Execute包装为单事件流（条件节点是同步的）
func (n *ConditionNode) Stream(ctx context.Context, input *NodeInput) (<-chan StreamEvent, error) {
	ch := make(chan StreamEvent, 2)
	safego.Go("orchestration-condition-stream", func() {
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

// Evaluate 评估条件表达式，支持以下类型:
//   - "length > N"    : 消息长度大于N
//   - "length < N"    : 消息长度小于N
//   - "length >= N"   : 消息长度大于等于N
//   - "length <= N"   : 消息长度小于等于N
//   - "contains:word" : 消息包含指定关键词
//   - "default"       : 始终匹配（兆底条件）
//   - "true"          : 始终匹配
func (n *ConditionNode) Evaluate(msgLen int, content string) bool {
	cond := strings.TrimSpace(n.step.Condition)

	if cond == "default" || cond == "true" || cond == "*" {
		return true
	}

	// 基于消息长度的条件判断
	if strings.HasPrefix(cond, "length") {
		return n.evalLengthCondition(cond, msgLen)
	}

	// 关键词包含检查（不区分大小写）
	if strings.HasPrefix(cond, "contains:") {
		keyword := strings.TrimPrefix(cond, "contains:")
		return strings.Contains(strings.ToLower(content), strings.ToLower(strings.TrimSpace(keyword)))
	}

	// 复杂度启发式评估
	if strings.HasPrefix(cond, "complexity:") {
		level := strings.TrimPrefix(cond, "complexity:")
		return n.evalComplexity(level, content)
	}

	return false
}

// evalLengthCondition 解析并评估"length OP N"格式的条件表达式
func (n *ConditionNode) evalLengthCondition(cond string, msgLen int) bool {
	// Parse: "length >= 500", "length > 100", etc.
	parts := strings.Fields(cond)
	if len(parts) != 3 {
		return false
	}

	op := parts[1]
	threshold, err := strconv.Atoi(parts[2])
	if err != nil {
		return false
	}

	switch op {
	case ">":
		return msgLen > threshold
	case "<":
		return msgLen < threshold
	case ">=":
		return msgLen >= threshold
	case "<=":
		return msgLen <= threshold
	case "==":
		return msgLen == threshold
	case "!=":
		return msgLen != threshold
	default:
		return false
	}
}

// evalComplexity 基于简单启发式规则评估消息复杂度（low/medium/high）
func (n *ConditionNode) evalComplexity(level string, content string) bool {
	// Simple heuristic: count words, code markers, and special chars
	wordCount := len(strings.Fields(content))
	hasCode := strings.Contains(content, "```") || strings.Contains(content, "code") ||
		strings.Contains(content, "function") || strings.Contains(content, "class")
	longMsg := wordCount > 100

	switch strings.TrimSpace(level) {
	case "low":
		return !hasCode && !longMsg && wordCount < 30
	case "medium":
		return (!hasCode && wordCount >= 30) || (hasCode && wordCount < 100)
	case "high":
		return hasCode || longMsg
	default:
		return false
	}
}
