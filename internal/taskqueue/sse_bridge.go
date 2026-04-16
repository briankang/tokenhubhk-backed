package taskqueue

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

// SSEBridge 将异步任务进度通过 SSE 推送给客户端。
// Backend Handler 调用 PublishAndStream() 发布任务到 Worker，
// 然后订阅 Redis Pub/Sub 进度频道，将进度事件转发为 SSE data 帧。
type SSEBridge struct {
	publisher *Publisher
}

// NewSSEBridge 创建 SSE 桥接器
func NewSSEBridge(publisher *Publisher) *SSEBridge {
	return &SSEBridge{publisher: publisher}
}

// PublishAndStream 发布任务并以 SSE 流式返回进度。
// 这是 Backend admin handler 委派重操作的核心方法。
//
// SSE 事件格式（与原 BatchCheck SSE 兼容）:
//   data: {"type":"progress","task_id":"...","progress":50,"message":"..."}
//   data: {"type":"done","task_id":"...","data":{...}}
//   data: {"type":"error","task_id":"...","message":"..."}
func (b *SSEBridge) PublishAndStream(c *gin.Context, taskType string, payload interface{}) {
	// 设置 SSE 头
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 50001, "message": "streaming not supported"})
		return
	}

	ctx := c.Request.Context()

	// 1. 发布任务
	taskID, err := b.publisher.Publish(ctx, taskType, payload)
	if err != nil {
		fmt.Fprintf(c.Writer, "data: {\"type\":\"error\",\"message\":\"任务发布失败: %s\"}\n\n", err.Error())
		flusher.Flush()
		return
	}

	// 发送任务 ID
	fmt.Fprintf(c.Writer, "data: {\"type\":\"started\",\"task_id\":\"%s\"}\n\n", taskID)
	flusher.Flush()

	// 2. 订阅进度
	progressCh, err := b.publisher.SubscribeProgress(ctx, taskID)
	if err != nil {
		fmt.Fprintf(c.Writer, "data: {\"type\":\"error\",\"task_id\":\"%s\",\"message\":\"订阅进度失败: %s\"}\n\n", taskID, err.Error())
		flusher.Flush()
		return
	}

	// 3. 转发进度为 SSE 事件
	for progress := range progressCh {
		switch progress.Status {
		case StatusCompleted:
			fmt.Fprintf(c.Writer, "data: {\"type\":\"done\",\"task_id\":\"%s\",\"message\":\"%s\",\"data\":%s}\n\n",
				taskID, progress.Message, defaultJSON(progress.Data))
		case StatusFailed:
			fmt.Fprintf(c.Writer, "data: {\"type\":\"error\",\"task_id\":\"%s\",\"message\":\"%s\"}\n\n",
				taskID, progress.Message)
		default:
			fmt.Fprintf(c.Writer, "data: {\"type\":\"progress\",\"task_id\":\"%s\",\"progress\":%d,\"message\":\"%s\"}\n\n",
				taskID, progress.Progress, progress.Message)
		}
		flusher.Flush()
	}
}

// PublishAndWait 发布任务并等待完成（非 SSE，同步等待结果）。
// 用于同步版本的 API（如 batch-check-sync）。
func (b *SSEBridge) PublishAndWait(ctx context.Context, taskType string, payload interface{}) (*TaskProgress, error) {
	taskID, err := b.publisher.Publish(ctx, taskType, payload)
	if err != nil {
		return nil, fmt.Errorf("发布任务失败: %w", err)
	}

	progressCh, err := b.publisher.SubscribeProgress(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("订阅进度失败: %w", err)
	}

	// 等待最终结果
	var last TaskProgress
	for p := range progressCh {
		last = p
	}

	if last.Status == StatusFailed {
		return &last, fmt.Errorf("任务执行失败: %s", last.Message)
	}
	return &last, nil
}

func defaultJSON(s string) string {
	if s == "" || s == "null" {
		return "null"
	}
	// 验证是合法 JSON
	if json.Valid([]byte(s)) {
		return s
	}
	return "null"
}
