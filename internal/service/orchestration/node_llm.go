package orchestration

import (
	"context"
	"fmt"
	"io"

	"tokenhub-server/internal/pkg/safego"
	"tokenhub-server/internal/provider"
	channelsvc "tokenhub-server/internal/service/channel"
)

// LLMNode LLM节点，通过模型提供商执行对话完成
type LLMNode struct {
	step     OrchestrationStep
	registry *provider.Registry
	router   *channelsvc.ChannelRouter
}

// NewLLMNode 创建LLM工作流节点实例
func NewLLMNode(step OrchestrationStep, registry *provider.Registry, router *channelsvc.ChannelRouter) *LLMNode {
	return &LLMNode{
		step:     step,
		registry: registry,
		router:   router,
	}
}

// Validate 校验LLM节点配置，确保模型名称和提供商注册表已配置
func (n *LLMNode) Validate() error {
	if n.step.Model == "" {
		return fmt.Errorf("llm node %q: model is required", n.step.Name)
	}
	if n.registry == nil {
		return fmt.Errorf("llm node %q: provider registry is nil", n.step.Name)
	}
	return nil
}

// Execute 执行非流式对话完成，返回完整响应内容和Token用量
func (n *LLMNode) Execute(ctx context.Context, input *NodeInput) (*NodeOutput, error) {
	if err := n.Validate(); err != nil {
		return nil, err
	}

	messages := n.buildMessages(input)

	p, err := n.registry.GetByModel(n.step.Model)
	if err != nil {
		return nil, fmt.Errorf("llm node %q: %w", n.step.Name, err)
	}

	req := &provider.ChatRequest{
		Model:    n.step.Model,
		Messages: messages,
		Stream:   false,
	}
	n.applyParams(req)

	resp, err := p.Chat(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("llm node %q chat failed: %w", n.step.Name, err)
	}

	content := ""
	if len(resp.Choices) > 0 {
		content = provider.TextContent(resp.Choices[0].Message.Content)
	}

	return &NodeOutput{
		Content: content,
		Usage:   resp.Usage,
		Metadata: map[string]interface{}{
			"model":    resp.Model,
			"node":     n.step.Name,
			"response": resp.ID,
		},
	}, nil
}

// Stream 执行流式对话完成，通过channel返回实时事件
func (n *LLMNode) Stream(ctx context.Context, input *NodeInput) (<-chan StreamEvent, error) {
	if err := n.Validate(); err != nil {
		return nil, err
	}

	messages := n.buildMessages(input)

	p, err := n.registry.GetByModel(n.step.Model)
	if err != nil {
		return nil, fmt.Errorf("llm node %q: %w", n.step.Name, err)
	}

	req := &provider.ChatRequest{
		Model:    n.step.Model,
		Messages: messages,
		Stream:   true,
	}
	n.applyParams(req)

	reader, err := p.StreamChat(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("llm node %q stream failed: %w", n.step.Name, err)
	}

	ch := make(chan StreamEvent, 32)
	safego.Go("orchestration-llm-stream", func() {
		defer close(ch)
		defer reader.Close()

		var totalUsage provider.Usage
		for {
			select {
			case <-ctx.Done():
				ch <- StreamEvent{
					NodeName: n.step.Name,
					Status:   "error",
					Error:    ctx.Err(),
				}
				return
			default:
			}

			chunk, err := reader.Read()
			if err != nil {
				if err == io.EOF {
					ch <- StreamEvent{
						NodeName: n.step.Name,
						Status:   "done",
						Usage:    &totalUsage,
					}
					return
				}
				ch <- StreamEvent{
					NodeName: n.step.Name,
					Status:   "error",
					Error:    fmt.Errorf("stream read: %w", err),
				}
				return
			}

			if chunk == nil {
				continue
			}

			if chunk.Usage != nil {
				totalUsage = *chunk.Usage
			}

			content := ""
			if len(chunk.Choices) > 0 {
				content = chunk.Choices[0].Delta.Content
			}

			if content != "" {
				ch <- StreamEvent{
					NodeName: n.step.Name,
					Content:  content,
					Status:   "running",
				}
			}
		}
	})

	return ch, nil
}

// buildMessages 构建消息列表，如果步骤配置了system prompt则注入到列表开头
func (n *LLMNode) buildMessages(input *NodeInput) []provider.Message {
	msgs := make([]provider.Message, 0, len(input.Messages)+1)

	// 如果步骤配置了system prompt，注入到消息列表开头
	if n.step.Prompt != "" {
		msgs = append(msgs, provider.Message{
			Role:    "system",
			Content: n.step.Prompt,
		})
	}

	msgs = append(msgs, input.Messages...)
	return msgs
}

// applyParams 将步骤配置中的额外参数（max_tokens/temperature/top_p）应用到请求
func (n *LLMNode) applyParams(req *provider.ChatRequest) {
	if n.step.Params == nil {
		return
	}
	if v, ok := n.step.Params["max_tokens"]; ok {
		if mt, ok := v.(float64); ok {
			req.MaxTokens = int(mt)
		}
	}
	if v, ok := n.step.Params["temperature"]; ok {
		if t, ok := v.(float64); ok {
			req.Temperature = &t
		}
	}
	if v, ok := n.step.Params["top_p"]; ok {
		if tp, ok := v.(float64); ok {
			req.TopP = &tp
		}
	}
}
