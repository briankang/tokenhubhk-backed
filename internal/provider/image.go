// Package provider 图像生成能力接口
//
// 图像生成不同于 Chat/StreamChat 文本补全：
//   - 部分供应商同步返回（火山引擎等 OpenAI 兼容）
//   - 部分供应商异步任务模式（阿里云 DashScope，需轮询 task_id）
//
// 为避免扩大 Provider 主接口、影响现有 10 个适配器，改用「可选能力接口 + 类型断言」。
// Provider 实现类按需嵌入 ImageGenerator，调用方通过 `p.(ImageGenerator)` 动态检查。
//
// 未来视频、3D、音频等多模态生成能力可复用同一模式：定义独立的 VideoGenerator 接口等。
package provider

import "context"

// ImageRequest 统一图像生成请求格式（OpenAI 兼容）
type ImageRequest struct {
	Model          string                 `json:"model"`
	Prompt         string                 `json:"prompt"`
	N              int                    `json:"n,omitempty"`               // 生成数量（默认 1）
	Size           string                 `json:"size,omitempty"`            // 如 "1024x1024"
	Quality        string                 `json:"quality,omitempty"`         // standard/hd
	Style          string                 `json:"style,omitempty"`           // vivid/natural
	ResponseFormat string                 `json:"response_format,omitempty"` // url / b64_json（默认 url）
	NegativePrompt string                 `json:"negative_prompt,omitempty"` // 反向提示词（阿里云等）
	Seed           int64                  `json:"seed,omitempty"`
	Extra          map[string]interface{} `json:"extra,omitempty"` // 透传给上游的自定义参数
}

// ImageData 单张生成的图片数据
type ImageData struct {
	URL           string `json:"url,omitempty"`
	B64JSON       string `json:"b64_json,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

// ImageResponse 统一图像生成响应格式
type ImageResponse struct {
	Created int64       `json:"created"`
	Model   string      `json:"model,omitempty"`
	Data    []ImageData `json:"data"`
}

// ImageGenerator 图像生成能力接口
// Provider 实现此接口以支持图像生成。调用方使用类型断言检查能力：
//
//	if ig, ok := p.(ImageGenerator); ok {
//	    resp, err := ig.GenerateImage(ctx, req)
//	}
type ImageGenerator interface {
	// GenerateImage 执行图像生成请求，内部处理同步/异步细节（如轮询异步任务）
	// ctx 用于控制超时；返回 URL 或 base64 格式的图片数据
	GenerateImage(ctx context.Context, req *ImageRequest) (*ImageResponse, error)
}
