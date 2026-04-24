package support

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// ChatOrchestrator AI 客服对话主流程
type ChatOrchestrator struct {
	db           *gorm.DB
	redis        *goredis.Client
	retriever    *KnowledgeRetriever
	resolver     *DynamicValueResolver
	translator   *Translator
	selector     *ModelSelector
	budget       *BudgetGuard
	prompt       *PromptBuilder
	embed        *EmbeddingClient
	internalBase string // http://gateway:8080
	internalKey  string // 系统 API Key
	httpCli      *http.Client
	providerSvc  *ProviderDocService
	sessionSvc   *SessionService
	msgSvc       *MessageService
	memorySvc    *MemoryService
}

// Dependencies orchestrator 依赖注入包
type Dependencies struct {
	DB           *gorm.DB
	Redis        *goredis.Client
	Retriever    *KnowledgeRetriever
	Resolver     *DynamicValueResolver
	Translator   *Translator
	Selector     *ModelSelector
	Budget       *BudgetGuard
	Prompt       *PromptBuilder
	Embed        *EmbeddingClient
	ProviderSvc  *ProviderDocService
	SessionSvc   *SessionService
	MessageSvc   *MessageService
	MemorySvc    *MemoryService
	InternalBase string
	InternalKey  string
}

func NewChatOrchestrator(d Dependencies) *ChatOrchestrator {
	return &ChatOrchestrator{
		db:           d.DB,
		redis:        d.Redis,
		retriever:    d.Retriever,
		resolver:     d.Resolver,
		translator:   d.Translator,
		selector:     d.Selector,
		budget:       d.Budget,
		prompt:       d.Prompt,
		embed:        d.Embed,
		providerSvc:  d.ProviderSvc,
		sessionSvc:   d.SessionSvc,
		msgSvc:       d.MessageSvc,
		memorySvc:    d.MemorySvc,
		internalBase: strings.TrimRight(d.InternalBase, "/"),
		internalKey:  d.InternalKey,
		httpCli:      &http.Client{Timeout: 120 * time.Second},
	}
}

// ChatRequest 对话请求
type ChatRequest struct {
	UserID    uint
	SessionID uint   // 0 表示新建会话
	Message   string // 用户当前输入
	Locale    string // zh / en / ja / ...
	UserLevel string // V0-V4（可选，由上层注入）
}

// StreamEvent SSE 事件
type StreamEvent struct {
	Type      string `json:"type"`                  // delta / done / error / stage
	Delta     string `json:"delta,omitempty"`       // 流式增量文本
	SessionID uint   `json:"session_id,omitempty"`
	MessageID uint   `json:"message_id,omitempty"`
	DocRefs   []uint `json:"doc_refs,omitempty"`
	NeedHuman bool   `json:"need_human,omitempty"`
	Error     string `json:"error,omitempty"`
	// Phase B2: 阶段提示（type=stage）
	// Stage 可选值：session_ready / detecting_lang / translating / retrieving_kb / kb_found / thinking / answering
	Stage string `json:"stage,omitempty"`
	Count int    `json:"count,omitempty"` // stage=kb_found 时携带命中条数
}

// Chat 主对话入口，通过 channel 流式推送事件
func (o *ChatOrchestrator) Chat(ctx context.Context, req ChatRequest, out chan<- StreamEvent) {
	defer close(out)
	send := func(e StreamEvent) {
		select {
		case <-ctx.Done():
		case out <- e:
		}
	}

	if strings.TrimSpace(req.Message) == "" {
		send(StreamEvent{Type: "error", Error: "empty message"})
		return
	}
	if req.Locale == "" {
		req.Locale = "zh"
	}

	// 1. Session 准备
	session, err := o.sessionSvc.GetOrCreate(ctx, req.UserID, req.SessionID, req.Locale)
	if err != nil {
		send(StreamEvent{Type: "error", Error: "session error"})
		return
	}
	send(StreamEvent{Type: "delta", Delta: "", SessionID: session.ID}) // 发送 session_id 给前端
	send(StreamEvent{Type: "stage", Stage: "session_ready", SessionID: session.ID})

	// 2. 预算守护
	level := o.budget.Check(ctx)
	if level == BudgetEmergency {
		reply := o.prompt.BuildEmergencyReply(req.Locale)
		// 保存消息并发送
		_ = o.msgSvc.SaveUser(ctx, session.ID, req.Message, "")
		msgID, _ := o.msgSvc.SaveAssistant(ctx, session.ID, reply, "", nil, nil, true)
		send(StreamEvent{Type: "delta", Delta: reply})
		send(StreamEvent{Type: "done", SessionID: session.ID, MessageID: msgID, NeedHuman: true})
		return
	}

	// 3. 语言检测（CJK 纯中文 fast-path 在 translator.ToZh 内部短路，不再额外调用 LLM）
	origLang := o.translator.Detect(req.Message)
	queryForRetrieval := req.Message
	if origLang != "zh" {
		send(StreamEvent{Type: "stage", Stage: "translating"})
		// 翻译（用 qwen-turbo，传入回调）
		translated := o.translator.ToZh(ctx, req.Message, func(ctx context.Context, sysPrompt, userText string) (string, error) {
			return o.oneShotChat(ctx, "qwen-turbo", sysPrompt, userText, 0.1, 1024, false)
		})
		queryForRetrieval = translated
	}

	// ========== Phase B1：并行检索步骤 4-7 ==========
	// 4. RAG 检索（最重，耗时 ~200-500ms）
	// 5. 供应商 URL 匹配（全表 active 扫描 + 内存 filter，~10-50ms）
	// 6. 拉记忆（DB 查询 + limit 10，~10ms）
	// 7. 历史对话（DB 查询 + limit N，~5-20ms）
	// 这四步彼此无依赖，错开独立 goroutine 并发执行；用 errgroup 聚合等待
	send(StreamEvent{Type: "stage", Stage: "retrieving_kb"})
	var (
		chunks       []ScoredChunk
		providerUrls []model.ProviderDocReference
		memories     []model.UserSupportMemory
		history      []map[string]string
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		c, _ := o.retriever.Retrieve(gctx, queryForRetrieval, RetrieveOptions{TopK: 5, Threshold: 0.5, MultiSource: true})
		chunks = c
		return nil
	})
	g.Go(func() error {
		providerUrls = o.providerSvc.MatchByQuery(gctx, queryForRetrieval, 3)
		return nil
	})
	g.Go(func() error {
		memories = o.memorySvc.Top(gctx, req.UserID, 10)
		return nil
	})
	g.Go(func() error {
		history = o.msgSvc.RecentForPrompt(gctx, session.ID, 8)
		return nil
	})
	// errgroup 中的任务全部是 best-effort，不会返回 error，可忽略
	_ = g.Wait()
	send(StreamEvent{Type: "stage", Stage: "kb_found", Count: len(chunks)})

	// 8. 构造 system prompt
	sysPrompt := o.prompt.Build(ctx, BuildInput{
		Locale:       req.Locale,
		UserLevel:    req.UserLevel,
		Chunks:       chunks,
		ProviderUrls: providerUrls,
		Memories:     memories,
		HistoryTurns: len(history),
	})

	// 9. 挑模型候选列表（支持 Fallback 链）
	candidates := o.selector.Candidates(ctx, level)
	if len(candidates) == 0 {
		reply := o.prompt.BuildEmergencyReply(req.Locale)
		_ = o.msgSvc.SaveUser(ctx, session.ID, req.Message, queryForRetrieval)
		msgID, _ := o.msgSvc.SaveAssistant(ctx, session.ID, reply, "", nil, nil, true)
		send(StreamEvent{Type: "delta", Delta: reply})
		send(StreamEvent{Type: "done", SessionID: session.ID, MessageID: msgID, NeedHuman: true})
		return
	}

	// 10. 保存用户消息（提前写入便于历史恢复）
	_ = o.msgSvc.SaveUser(ctx, session.ID, req.Message, queryForRetrieval)

	// 11. 调 LLM 流式（Fallback 链：依次尝试候选模型，首个成功的为准）
	send(StreamEvent{Type: "stage", Stage: "thinking"})
	var (
		fullReply          string
		tokensIn, tokensOut int
		usedCandidate      *model.SupportModelProfile
		lastErr            error
	)
	for i := range candidates {
		c := &candidates[i]
		if i > 0 {
			logger.L.Warn("support: fallback to next model",
				zap.String("failed_model", candidates[i-1].ModelKey),
				zap.String("next_model", c.ModelKey),
				zap.Error(lastErr))
		}
		fullReply, tokensIn, tokensOut, lastErr = o.streamLLM(ctx, c, sysPrompt, history, req.Message, out)
		if lastErr == nil {
			usedCandidate = c
			break
		}
	}
	if lastErr != nil {
		logger.L.Error("support: all model candidates failed", zap.Error(lastErr))
		send(StreamEvent{Type: "error", Error: "service_unavailable"})
		return
	}

	needHuman := strings.Contains(fullReply, "<need_human/>")
	cleanReply := strings.ReplaceAll(fullReply, "<need_human/>", "")

	// 12. 保存 AI 消息
	chunkIDs := make([]uint, 0, len(chunks))
	for _, c := range chunks {
		chunkIDs = append(chunkIDs, c.Chunk.ID)
	}
	urlStrings := make([]string, 0, len(providerUrls))
	for _, u := range providerUrls {
		urlStrings = append(urlStrings, u.URL)
	}
	msgID, _ := o.msgSvc.SaveAssistant(ctx, session.ID, cleanReply, usedCandidate.ModelKey, chunkIDs, urlStrings, needHuman)
	o.msgSvc.UpdateTokens(ctx, msgID, tokensIn, tokensOut)

	// 13. 扣预算（用户消耗的 tokens × 模型成本近似）
	// 简化：以 output tokens × 价格，仅估算（实际精确计费已在 /v1/chat/completions 内部处理）
	go o.trackBudget(ctx, usedCandidate.ModelKey, tokensIn, tokensOut)

	// 14. 异步提取记忆（每 5 条触发）
	if session.MsgCount+2 > 0 && (session.MsgCount+2)%5 == 0 {
		o.sessionSvc.ScheduleMemoryExtract(ctx, session.ID)
	}

	send(StreamEvent{
		Type:      "done",
		SessionID: session.ID,
		MessageID: msgID,
		DocRefs:   chunkIDs,
		NeedHuman: needHuman,
	})
}

// streamLLM 调 /v1/chat/completions 流式响应，转发 delta 到 out
// 返回 (完整回复, tokens_in, tokens_out, err)
func (o *ChatOrchestrator) streamLLM(ctx context.Context, profile *model.SupportModelProfile, systemPrompt string, history []map[string]string, userMsg string, out chan<- StreamEvent) (string, int, int, error) {
	// 构造 messages
	msgs := []map[string]string{{"role": "system", "content": systemPrompt}}
	msgs = append(msgs, history...)
	msgs = append(msgs, map[string]string{"role": "user", "content": userMsg})

	body := map[string]any{
		"model":       profile.ModelKey,
		"messages":    msgs,
		"max_tokens":  profile.MaxTokens,
		"temperature": profile.Temperature,
		"stream":      true,
	}
	if profile.EnableSearch {
		body["enable_search"] = true // SupplierParamMapping 会映射到各供应商原生格式
	}
	raw, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, "POST", o.internalBase+"/v1/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return "", 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.internalKey)

	resp, err := o.httpCli.Do(req)
	if err != nil {
		return "", 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(resp.Body)
		return "", 0, 0, fmt.Errorf("upstream %d: %s", resp.StatusCode, truncate(string(data), 200))
	}

	reader := bufio.NewReader(resp.Body)
	var full strings.Builder
	var tokensIn, tokensOut int
	firstDeltaSent := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			logger.L.Warn("sse read error", zap.Error(err))
			break
		}
		line = strings.TrimRight(line, "\r\n")
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) > 0 {
			delta := chunk.Choices[0].Delta.Content
			if delta != "" {
				// 首个 delta 到达前发送 answering 阶段事件，让前端切换到"正在回答"UI
				if !firstDeltaSent {
					firstDeltaSent = true
					select {
					case <-ctx.Done():
						return full.String(), tokensIn, tokensOut, ctx.Err()
					case out <- StreamEvent{Type: "stage", Stage: "answering"}:
					}
				}
				full.WriteString(delta)
				select {
				case <-ctx.Done():
					return full.String(), tokensIn, tokensOut, ctx.Err()
				case out <- StreamEvent{Type: "delta", Delta: delta}:
				}
			}
		}
		if chunk.Usage != nil {
			tokensIn = chunk.Usage.PromptTokens
			tokensOut = chunk.Usage.CompletionTokens
		}
	}
	return full.String(), tokensIn, tokensOut, nil
}

// oneShotChat 非流式单次对话（仅用于 translator 辅助调用）
func (o *ChatOrchestrator) oneShotChat(ctx context.Context, modelKey, systemPrompt, userMsg string, temperature float32, maxTokens int, enableSearch bool) (string, error) {
	body := map[string]any{
		"model":       modelKey,
		"messages":    []map[string]string{{"role": "system", "content": systemPrompt}, {"role": "user", "content": userMsg}},
		"max_tokens":  maxTokens,
		"temperature": temperature,
		"stream":      false,
	}
	if enableSearch {
		body["enable_search"] = true
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", o.internalBase+"/v1/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.internalKey)
	resp, err := o.httpCli.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("upstream %d: %s", resp.StatusCode, truncate(string(data), 200))
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", err
	}
	if len(parsed.Choices) == 0 {
		return "", nil
	}
	return parsed.Choices[0].Message.Content, nil
}

// trackBudget 估算本次对话消耗的积分并扣减
// 仅近似（精确计费在 completions_handler 内部处理，此处仅为守护节流）
func (o *ChatOrchestrator) trackBudget(ctx context.Context, modelKey string, tokensIn, tokensOut int) {
	inCost, outCost := queryModelPricing(o.db, ctx, modelKey)
	if inCost < 0 {
		inCost = 5
	}
	if outCost < 0 {
		outCost = 5
	}
	// 成本价估算（¥/百万 tokens 转积分）
	// 1 RMB = 10000 积分
	credits := int64(float64(tokensIn)*inCost/1000000.0*10000.0 + float64(tokensOut)*outCost/1000000.0*10000.0)
	if credits <= 0 {
		credits = 1
	}
	_ = o.budget.Deduct(ctx, credits)
}
