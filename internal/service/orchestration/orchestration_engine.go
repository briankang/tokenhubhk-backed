package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
	"tokenhub-server/internal/pkg/safego"
	"tokenhub-server/internal/provider"
	channelsvc "tokenhub-server/internal/service/channel"
)

const (
	defaultStepTimeout  = 60  // seconds
	defaultTotalTimeout = 300 // seconds
)

// WorkflowNode 工作流节点接口，所有编排节点必须实现此接口
type WorkflowNode interface {
	Execute(ctx context.Context, input *NodeInput) (*NodeOutput, error)
	Stream(ctx context.Context, input *NodeInput) (<-chan StreamEvent, error)
	Validate() error
}

// NodeInput 工作流节点的输入数据
type NodeInput struct {
	Messages  []provider.Message          // 原始或累积的消息列表
	Upstream  map[string]interface{}       // 上游节点的输出结果
	GlobalCtx map[string]interface{}       // 全局编排上下文
	Variables map[string]string            // 变量替换映射表
}

// NodeOutput 工作流节点的输出数据
type NodeOutput struct {
	Content  string                 // 节点输出内容
	Usage    provider.Usage         // Token用量统计
	Metadata map[string]interface{} // 附加元数据
}

// StreamEvent 流式编排节点的事件输出
type StreamEvent struct {
	NodeName string          // 当前执行的节点名称
	Content  string          // 流式输出内容片段
	Status   string          // 状态: "running" / "done" / "error"
	Usage    *provider.Usage // Token用量（"done"时可用）
	Error    error           // 错误详情（"error"时可用）
}

// OrchestrationResult 编排执行的最终结果
type OrchestrationResult struct {
	Content    string                 `json:"content"`
	TotalUsage provider.Usage         `json:"total_usage"`
	Steps      []StepResult           `json:"steps"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
}

// StepResult 单个编排步骤的执行结果
type StepResult struct {
	Name    string         `json:"name"`
	Content string         `json:"content"`
	Usage   provider.Usage `json:"usage"`
	Latency int64          `json:"latency_ms"`
}

// OrchestrationEngine 编排执行引擎，负责解析并执行工作流
type OrchestrationEngine struct {
	registry *provider.Registry
	router   *channelsvc.ChannelRouter
	logger   *zap.Logger
	svc      *OrchestrationService
}

// NewOrchestrationEngine 创建编排执行引擎实例
func NewOrchestrationEngine(
	registry *provider.Registry,
	router *channelsvc.ChannelRouter,
	svc *OrchestrationService,
) *OrchestrationEngine {
	return &OrchestrationEngine{
		registry: registry,
		router:   router,
		logger:   logger.L,
		svc:      svc,
	}
}

// Execute 执行编排工作流（非流式），根据模式分发到对应的执行策略
func (e *OrchestrationEngine) Execute(ctx context.Context, orchID uint, messages []provider.Message) (*OrchestrationResult, error) {
	orch, err := e.svc.GetByID(ctx, orchID)
	if err != nil {
		return nil, fmt.Errorf("load orchestration: %w", err)
	}
	if !orch.IsActive {
		return nil, fmt.Errorf("orchestration %d is not active", orchID)
	}

	steps, err := e.parseSteps(orch)
	if err != nil {
		return nil, err
	}

	// 设置整体超时时间
	totalTimeout := time.Duration(defaultTotalTimeout) * time.Second
	ctx, cancel := context.WithTimeout(ctx, totalTimeout)
	defer cancel()

	// 根据编排模式分发到不同的执行策略
	switch orch.Mode {
	case "PIPELINE":
		return e.executePipeline(ctx, steps, messages)
	case "ROUTER":
		return e.executeRouter(ctx, steps, messages)
	case "FALLBACK":
		return e.executeFallback(ctx, steps, messages)
	default:
		return nil, fmt.Errorf("unsupported orchestration mode: %s", orch.Mode)
	}
}

// Stream 以流式方式执行编排工作流，通过channel返回实时事件
func (e *OrchestrationEngine) Stream(ctx context.Context, orchID uint, messages []provider.Message) (<-chan StreamEvent, error) {
	orch, err := e.svc.GetByID(ctx, orchID)
	if err != nil {
		return nil, fmt.Errorf("load orchestration: %w", err)
	}
	if !orch.IsActive {
		return nil, fmt.Errorf("orchestration %d is not active", orchID)
	}

	steps, err := e.parseSteps(orch)
	if err != nil {
		return nil, err
	}

	totalTimeout := time.Duration(defaultTotalTimeout) * time.Second
	ctx, cancel := context.WithTimeout(ctx, totalTimeout)

	ch := make(chan StreamEvent, 64)
	safego.Go("orchestration-engine-stream", func() {
		defer cancel()
		defer close(ch)

		switch orch.Mode {
		case "PIPELINE":
			e.streamPipeline(ctx, steps, messages, ch)
		case "ROUTER":
			e.streamRouter(ctx, steps, messages, ch)
		case "FALLBACK":
			e.streamFallback(ctx, steps, messages, ch)
		default:
			ch <- StreamEvent{Status: "error", Error: fmt.Errorf("unsupported mode: %s", orch.Mode)}
		}
	})

	return ch, nil
}

// parseSteps 解析编排模型中的步骤JSON配置
func (e *OrchestrationEngine) parseSteps(orch *model.Orchestration) ([]OrchestrationStep, error) {
	if len(orch.Steps) == 0 {
		return nil, fmt.Errorf("orchestration has no steps defined")
	}
	var steps []OrchestrationStep
	if err := parseJSON(orch.Steps, &steps); err != nil {
		return nil, fmt.Errorf("parse orchestration steps: %w", err)
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("orchestration must have at least one step")
	}
	return steps, nil
}

// executePipeline 顺序执行所有步骤，每步输出作为assistant消息传递给下一步
func (e *OrchestrationEngine) executePipeline(ctx context.Context, steps []OrchestrationStep, messages []provider.Message) (*OrchestrationResult, error) {
	result := &OrchestrationResult{
		Steps:    make([]StepResult, 0, len(steps)),
		Metadata: map[string]interface{}{"mode": "PIPELINE"},
	}
	currentMsgs := make([]provider.Message, len(messages))
	copy(currentMsgs, messages)
	upstream := make(map[string]interface{})

	for _, step := range steps {
		node := e.buildNode(step)
		if err := node.Validate(); err != nil {
			return nil, fmt.Errorf("step %q validation failed: %w", step.Name, err)
		}

		stepCtx, stepCancel := e.stepContext(ctx, step)
		start := time.Now()

		input := &NodeInput{
			Messages:  currentMsgs,
			Upstream:  upstream,
			GlobalCtx: result.Metadata,
			Variables: map[string]string{},
		}

		output, err := node.Execute(stepCtx, input)
		stepCancel()

		latency := time.Since(start).Milliseconds()
		if err != nil {
			e.logger.Error("pipeline step failed", zap.String("step", step.Name), zap.Error(err))
			return nil, fmt.Errorf("step %q failed: %w", step.Name, err)
		}

		stepResult := StepResult{
			Name:    step.Name,
			Content: output.Content,
			Usage:   output.Usage,
			Latency: latency,
		}
		result.Steps = append(result.Steps, stepResult)
		result.TotalUsage = AddUsage(result.TotalUsage, output.Usage)

		// 将当前步骤输出作为assistant消息追加，传递给下一步
		currentMsgs = append(currentMsgs, provider.Message{
			Role:    "assistant",
			Content: output.Content,
		})
		upstream[step.Name] = output.Content
	}

	// 最终输出为最后一步的结果
	if len(result.Steps) > 0 {
		result.Content = result.Steps[len(result.Steps)-1].Content
	}
	return result, nil
}

// executeRouter 评估条件并路由到匹配的步骤执行
func (e *OrchestrationEngine) executeRouter(ctx context.Context, steps []OrchestrationStep, messages []provider.Message) (*OrchestrationResult, error) {
	result := &OrchestrationResult{
		Steps:    make([]StepResult, 0, 1),
		Metadata: map[string]interface{}{"mode": "ROUTER"},
	}

	selected := e.selectRouterStep(steps, messages)
	if selected == nil {
		// 无匹配条件时默认使用第一个步骤
		selected = &steps[0]
	}

	node := e.buildNode(*selected)
	if err := node.Validate(); err != nil {
		return nil, fmt.Errorf("router step %q validation: %w", selected.Name, err)
	}

	stepCtx, stepCancel := e.stepContext(ctx, *selected)
	start := time.Now()

	input := &NodeInput{
		Messages:  messages,
		Upstream:  map[string]interface{}{},
		GlobalCtx: result.Metadata,
		Variables: map[string]string{},
	}

	output, err := node.Execute(stepCtx, input)
	stepCancel()

	if err != nil {
		return nil, fmt.Errorf("router step %q failed: %w", selected.Name, err)
	}

	result.Steps = append(result.Steps, StepResult{
		Name:    selected.Name,
		Content: output.Content,
		Usage:   output.Usage,
		Latency: time.Since(start).Milliseconds(),
	})
	result.Content = output.Content
	result.TotalUsage = output.Usage
	return result, nil
}

// executeFallback 依次尝试每个步骤，返回第一个成功的结果（容错降级策略）
func (e *OrchestrationEngine) executeFallback(ctx context.Context, steps []OrchestrationStep, messages []provider.Message) (*OrchestrationResult, error) {
	result := &OrchestrationResult{
		Steps:    make([]StepResult, 0, len(steps)),
		Metadata: map[string]interface{}{"mode": "FALLBACK"},
	}

	var lastErr error
	for _, step := range steps {
		node := e.buildNode(step)
		if err := node.Validate(); err != nil {
			e.logger.Warn("fallback step validation failed, skipping", zap.String("step", step.Name), zap.Error(err))
			continue
		}

		stepCtx, stepCancel := e.stepContext(ctx, step)
		start := time.Now()

		input := &NodeInput{
			Messages:  messages,
			Upstream:  map[string]interface{}{},
			GlobalCtx: result.Metadata,
			Variables: map[string]string{},
		}

		output, err := node.Execute(stepCtx, input)
		stepCancel()

		latency := time.Since(start).Milliseconds()
		if err != nil {
			e.logger.Warn("fallback step failed, trying next",
				zap.String("step", step.Name), zap.Error(err), zap.Int64("latency_ms", latency))
			lastErr = err
			result.Steps = append(result.Steps, StepResult{
				Name:    step.Name,
				Latency: latency,
			})
			continue
		}

		// 步骤执行成功，立即返回结果
		result.Steps = append(result.Steps, StepResult{
			Name:    step.Name,
			Content: output.Content,
			Usage:   output.Usage,
			Latency: latency,
		})
		result.Content = output.Content
		result.TotalUsage = output.Usage
		return result, nil
	}

	return nil, fmt.Errorf("all fallback steps failed, last error: %w", lastErr)
}

// streamPipeline 流式执行管道模式
func (e *OrchestrationEngine) streamPipeline(ctx context.Context, steps []OrchestrationStep, messages []provider.Message, out chan<- StreamEvent) {
	currentMsgs := make([]provider.Message, len(messages))
	copy(currentMsgs, messages)
	upstream := make(map[string]interface{})

	for _, step := range steps {
		node := e.buildNode(step)
		if err := node.Validate(); err != nil {
			out <- StreamEvent{NodeName: step.Name, Status: "error", Error: err}
			return
		}

		stepCtx, stepCancel := e.stepContext(ctx, step)
		input := &NodeInput{
			Messages:  currentMsgs,
			Upstream:  upstream,
			GlobalCtx: map[string]interface{}{},
			Variables: map[string]string{},
		}

		events, err := node.Stream(stepCtx, input)
		if err != nil {
			stepCancel()
			out <- StreamEvent{NodeName: step.Name, Status: "error", Error: err}
			return
		}

		var content string
		var usage *provider.Usage
		for evt := range events {
			evt.NodeName = step.Name
			out <- evt
			if evt.Status == "running" {
				content += evt.Content
			}
			if evt.Usage != nil {
				usage = evt.Usage
			}
			if evt.Status == "error" {
				stepCancel()
				return
			}
		}
		stepCancel()

		// 将当前步骤输出传递给下一步
		currentMsgs = append(currentMsgs, provider.Message{
			Role:    "assistant",
			Content: content,
		})
		upstream[step.Name] = content
		_ = usage
	}
}

// streamRouter 流式执行路由模式
func (e *OrchestrationEngine) streamRouter(ctx context.Context, steps []OrchestrationStep, messages []provider.Message, out chan<- StreamEvent) {
	selected := e.selectRouterStep(steps, messages)
	if selected == nil {
		selected = &steps[0]
	}

	node := e.buildNode(*selected)
	if err := node.Validate(); err != nil {
		out <- StreamEvent{NodeName: selected.Name, Status: "error", Error: err}
		return
	}

	stepCtx, stepCancel := e.stepContext(ctx, *selected)
	defer stepCancel()

	input := &NodeInput{
		Messages:  messages,
		Upstream:  map[string]interface{}{},
		GlobalCtx: map[string]interface{}{},
		Variables: map[string]string{},
	}

	events, err := node.Stream(stepCtx, input)
	if err != nil {
		out <- StreamEvent{NodeName: selected.Name, Status: "error", Error: err}
		return
	}

	for evt := range events {
		evt.NodeName = selected.Name
		out <- evt
	}
}

// streamFallback 流式执行容错降级模式
func (e *OrchestrationEngine) streamFallback(ctx context.Context, steps []OrchestrationStep, messages []provider.Message, out chan<- StreamEvent) {
	for i, step := range steps {
		node := e.buildNode(step)
		if err := node.Validate(); err != nil {
			continue
		}

		stepCtx, stepCancel := e.stepContext(ctx, step)
		input := &NodeInput{
			Messages:  messages,
			Upstream:  map[string]interface{}{},
			GlobalCtx: map[string]interface{}{},
			Variables: map[string]string{},
		}

		events, err := node.Stream(stepCtx, input)
		if err != nil {
			stepCancel()
			if i == len(steps)-1 {
				out <- StreamEvent{NodeName: step.Name, Status: "error", Error: err}
			}
			continue
		}

		// 尝试收集流式输出，如果出错则降级到下一个步骤
		success := true
		for evt := range events {
			evt.NodeName = step.Name
			if evt.Status == "error" {
				success = false
				break
			}
			out <- evt
		}
		stepCancel()

		if success {
			return
		}
		// 尝试下一个步骤
	}

	out <- StreamEvent{Status: "error", Error: fmt.Errorf("all fallback steps failed")}
}

// buildNode 根据步骤配置创建对应类型的工作流节点（llm/code/condition）
func (e *OrchestrationEngine) buildNode(step OrchestrationStep) WorkflowNode {
	switch step.NodeType {
	case "llm":
		return NewLLMNode(step, e.registry, e.router)
	case "code":
		return NewCodeNode(step)
	case "condition":
		return NewConditionNode(step)
	default:
		// 默认使用LLM节点
		return NewLLMNode(step, e.registry, e.router)
	}
}

// stepContext 为单个步骤创建带超时的context
func (e *OrchestrationEngine) stepContext(parent context.Context, step OrchestrationStep) (context.Context, context.CancelFunc) {
	timeout := step.Timeout
	if timeout <= 0 {
		timeout = defaultStepTimeout
	}
	return context.WithTimeout(parent, time.Duration(timeout)*time.Second)
}

// selectRouterStep 评估条件表达式，选择匹配的路由步骤
func (e *OrchestrationEngine) selectRouterStep(steps []OrchestrationStep, messages []provider.Message) *OrchestrationStep {
	if len(messages) == 0 {
		return nil
	}
	lastMsg := provider.TextContent(messages[len(messages)-1].Content)
	msgLen := len(lastMsg)

	for i := range steps {
		if steps[i].Condition == "" {
			continue
		}
		cond := NewConditionNode(steps[i])
		if cond.Evaluate(msgLen, lastMsg) {
			return &steps[i]
		}
	}
	return nil
}

// AddUsage 将两个Usage结构的Token用量累加
func AddUsage(a, b provider.Usage) provider.Usage {
	return provider.Usage{
		PromptTokens:     a.PromptTokens + b.PromptTokens,
		CompletionTokens: a.CompletionTokens + b.CompletionTokens,
		TotalTokens:      a.TotalTokens + b.TotalTokens,
	}
}

// parseJSON 解析JSON字节数组到目标结构体的辅助函数
func parseJSON(data []byte, target interface{}) error {
	if len(data) == 0 {
		return fmt.Errorf("empty JSON data")
	}
	return json.Unmarshal(data, target)
}
