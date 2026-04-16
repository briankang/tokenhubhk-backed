// Package provider 视频生成能力接口
//
// 视频生成与图像相似但耗时更长（几十秒到几分钟），主流供应商均为异步任务模式：
//   - 阿里云 DashScope: POST /api/v1/services/aigc/video-generation/video-synthesis → task_id
//   - 火山引擎: POST /api/v3/contents/generations/tasks → task_id
//
// 设计同 ImageGenerator：能力接口 + 类型断言，不扩大 Provider 主接口。
package provider

import "context"

// VideoRequest 统一视频生成请求格式
type VideoRequest struct {
	Model          string                 `json:"model"`
	Prompt         string                 `json:"prompt"`
	ImageURL       string                 `json:"image_url,omitempty"`        // 图生视频（i2v）参考图
	Duration       int                    `json:"duration,omitempty"`         // 秒数，通常 4/5/10
	Resolution     string                 `json:"resolution,omitempty"`       // "720P"/"1080P"/"480P"
	AspectRatio    string                 `json:"aspect_ratio,omitempty"`     // "16:9"/"9:16"/"1:1"
	FPS            int                    `json:"fps,omitempty"`              // 帧率（可选，默认供应商默认值）
	Seed           int64                  `json:"seed,omitempty"`
	NegativePrompt string                 `json:"negative_prompt,omitempty"`
	Extra          map[string]interface{} `json:"extra,omitempty"`
}

// VideoData 单个生成的视频数据
type VideoData struct {
	URL           string `json:"url,omitempty"`            // 视频 URL（供应商一般返回有时效的签名 URL）
	CoverURL      string `json:"cover_url,omitempty"`      // 封面图 URL
	DurationSec   int    `json:"duration_sec,omitempty"`   // 实际时长
	RevisedPrompt string `json:"revised_prompt,omitempty"` // 实际使用的 prompt
}

// VideoResponse 统一视频生成响应格式
type VideoResponse struct {
	Created int64       `json:"created"`
	Model   string      `json:"model,omitempty"`
	Data    []VideoData `json:"data"`
	TaskID  string      `json:"task_id,omitempty"` // 异步任务 ID（用于前端展示）
}

// VideoGenerator 视频生成能力接口
type VideoGenerator interface {
	// GenerateVideo 执行视频生成，内部处理异步任务提交和轮询
	GenerateVideo(ctx context.Context, req *VideoRequest) (*VideoResponse, error)
}
