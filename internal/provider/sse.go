// SSE流式转发核心模块
// 处理从上游提供商到客户端的逐块无缓冲转发
package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"tokenhub-server/internal/pkg/logger"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// sseHeartbeatInterval SSE 心跳间隔
// 上游模型（如思考型）出第一字符前可能静默 30+s，中间链路（防火墙/NAT/代理）若 idle 超时即会切断 TCP，
// 表现为前端"network error"。每 15s 写一行 SSE 注释（: keep-alive）保活，客户端会自动忽略。
const sseHeartbeatInterval = 15 * time.Second

// SSEResult 流结束后的结构化结果，用于上层 handler 写日志和回填 ChannelLog。
type SSEResult struct {
	Usage *Usage
	// ThinkingOnly 表示流正常 EOF 结束，但仅收到 reasoning 内容、没有正文 content。
	// 典型触发：思考型模型（DeepSeek-R1 / Claude Extended Thinking 等）的 reasoning 阶段被上游截断。
	// 前端会据此触发一次自动重试；后端据此写入 ChannelLog.ErrorMessage 便于监控统计。
	ThinkingOnly bool
	// FinishReason 最后一个 chunk 中的 finish_reason（若有），用于日志诊断。
	FinishReason string
}

// SSEWriter 从 StreamReader 读取分片并以SSE事件格式转发给客户端
// 完成时返回结构化结果（用量统计、thinking-only 标记、finish_reason）
// includeUsage 参数控制是否在最后一个 chunk 中包含 usage 信息（OpenAI stream_options.include_usage）
func SSEWriter(c *gin.Context, reader StreamReader, includeUsage bool) (*SSEResult, error) {
	if reader == nil {
		return nil, fmt.Errorf("sse: stream reader is nil")
	}
	defer reader.Close()

	// 设置SSE响应头
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no") // disable nginx buffering

	w := c.Writer
	flusher, ok := w.(interface{ Flush() })
	if !ok {
		return nil, fmt.Errorf("sse: response writer does not support flushing")
	}

	// writeMu 保护 w 的并发写入：心跳 goroutine 与主循环都会写
	var writeMu sync.Mutex

	var finalUsage *Usage
	// 统计 reasoning vs content 字符数，用于 EOF 时判定是否"仅思考无正文"
	var reasoningChars int
	var contentChars int
	var lastFinishReason string
	ctx := c.Request.Context()

	// 启动心跳 goroutine，主循环退出时通过 done channel 通知其结束
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(sseHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				writeMu.Lock()
				// SSE 注释行（以冒号开头），客户端按规范忽略；仅用于刷新中间链路的 idle 计时器
				if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err == nil {
					flusher.Flush()
				}
				writeMu.Unlock()
			}
		}
	}()

	buildResult := func() *SSEResult {
		thinkingOnly := reasoningChars > 0 && contentChars == 0
		return &SSEResult{
			Usage:        finalUsage,
			ThinkingOnly: thinkingOnly,
			FinishReason: lastFinishReason,
		}
	}

	for {
		select {
		case <-ctx.Done():
			logger.L.Debug("sse: client disconnected")
			return buildResult(), ctx.Err()
		default:
		}

		chunk, err := reader.Read()
		if err != nil {
			if err == io.EOF {
				writeMu.Lock()
				// 如果 includeUsage=true 且有 usage 信息，在 [DONE] 之前发送一个 usage chunk
				if includeUsage && finalUsage != nil {
					usageChunk := &StreamChunk{
						ID:      "",
						Model:   "",
						Choices: []StreamChoice{},
						Usage:   finalUsage,
					}
					usageData, marshalErr := json.Marshal(usageChunk)
					if marshalErr == nil {
						fmt.Fprintf(w, "data: %s\n\n", usageData)
						flusher.Flush()
					}
				}
				// 发送[DONE]结束标记
				fmt.Fprintf(w, "data: [DONE]\n\n")
				flusher.Flush()
				writeMu.Unlock()

				// 正常结束后对"仅思考无正文"情况输出 Warn 日志，便于监控聚合
				if reasoningChars > 0 && contentChars == 0 {
					reqIDStr := c.GetString("X-Request-ID")
					if reqIDStr == "" {
						reqIDStr = c.GetHeader("X-Request-ID")
					}
					fields := []zap.Field{
						zap.String("request_id", reqIDStr),
						zap.Int("reasoning_chars", reasoningChars),
						zap.Int("content_chars", contentChars),
						zap.String("finish_reason", lastFinishReason),
					}
					if finalUsage != nil {
						fields = append(fields,
							zap.Int("prompt_tokens", finalUsage.PromptTokens),
							zap.Int("completion_tokens", finalUsage.CompletionTokens),
						)
					}
					logger.L.Warn("sse: thinking-only response (reasoning without content)", fields...)
				}

				return buildResult(), nil
			}
			logger.L.Error("sse: read chunk error", zap.Error(err))
			return buildResult(), fmt.Errorf("sse: read chunk: %w", err)
		}

		if chunk == nil {
			continue
		}

		// 从最后一个分片捕获用量统计
		if chunk.Usage != nil {
			finalUsage = chunk.Usage
		}
		// 累计 reasoning/content 字符数 & 捕获最后的 finish_reason
		for _, choice := range chunk.Choices {
			reasoningChars += len(choice.Delta.ReasoningContent)
			contentChars += len(choice.Delta.Content)
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				lastFinishReason = *choice.FinishReason
			}
		}

		data, err := json.Marshal(chunk)
		if err != nil {
			logger.L.Error("sse: marshal chunk error", zap.Error(err))
			continue
		}

		writeMu.Lock()
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
		writeMu.Unlock()
	}
}

// StreamManager 并发流式请求管理器，支持优雅关闭
type StreamManager struct {
	wg      sync.WaitGroup
	active  atomic.Int64
	maxConn int64
}

// NewStreamManager 创建新的流式请求管理器，指定最大并发流数
func NewStreamManager(maxConcurrent int64) *StreamManager {
	if maxConcurrent <= 0 {
		maxConcurrent = 1000
	}
	return &StreamManager{
		maxConn: maxConcurrent,
	}
}

// Acquire 尝试获取一个流槽位，达到最大并发数时返回false
func (m *StreamManager) Acquire() bool {
	current := m.active.Load()
	if current >= m.maxConn {
		return false
	}
	m.active.Add(1)
	m.wg.Add(1)
	return true
}

// Release 释放一个流槽位
func (m *StreamManager) Release() {
	m.active.Add(-1)
	m.wg.Done()
}

// ActiveCount 返回当前活跃流数量
func (m *StreamManager) ActiveCount() int64 {
	return m.active.Load()
}

// Wait 阻塞直到所有活跃流完成
func (m *StreamManager) Wait() {
	m.wg.Wait()
}

// HandleStream 便捷方法，管理流式请求的完整生命周期
// 获取槽位、运行SSE写入器、完成时释放槽位
func (m *StreamManager) HandleStream(c *gin.Context, reader StreamReader, includeUsage bool) (*SSEResult, error) {
	if !m.Acquire() {
		return nil, fmt.Errorf("sse: max concurrent streams reached (%d)", m.maxConn)
	}
	defer m.Release()

	return SSEWriter(c, reader, includeUsage)
}
