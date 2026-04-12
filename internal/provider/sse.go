// SSE流式转发核心模块
// 处理从上游提供商到客户端的逐块无缓冲转发
package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"tokenhub-server/internal/pkg/logger"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// SSEWriter 从 StreamReader 读取分片并以SSE事件格式转发给客户端
// 完成时返回汇总的Token用量统计
// includeUsage 参数控制是否在最后一个 chunk 中包含 usage 信息（OpenAI stream_options.include_usage）
func SSEWriter(c *gin.Context, reader StreamReader, includeUsage bool) (*Usage, error) {
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

	var finalUsage *Usage
	ctx := c.Request.Context()

	for {
		select {
		case <-ctx.Done():
			logger.L.Debug("sse: client disconnected")
			return finalUsage, ctx.Err()
		default:
		}

		chunk, err := reader.Read()
		if err != nil {
			if err == io.EOF {
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
				return finalUsage, nil
			}
			logger.L.Error("sse: read chunk error", zap.Error(err))
			return finalUsage, fmt.Errorf("sse: read chunk: %w", err)
		}

		if chunk == nil {
			continue
		}

		// 从最后一个分片捕获用量统计
		if chunk.Usage != nil {
			finalUsage = chunk.Usage
		}

		data, err := json.Marshal(chunk)
		if err != nil {
			logger.L.Error("sse: marshal chunk error", zap.Error(err))
			continue
		}

		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
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
func (m *StreamManager) HandleStream(c *gin.Context, reader StreamReader, includeUsage bool) (*Usage, error) {
	if !m.Acquire() {
		return nil, fmt.Errorf("sse: max concurrent streams reached (%d)", m.maxConn)
	}
	defer m.Release()

	return SSEWriter(c, reader, includeUsage)
}
