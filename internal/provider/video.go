// Package provider 瑙嗛鐢熸垚鑳藉姏鎺ュ彛
//
// 瑙嗛鐢熸垚涓庡浘鍍忕浉浼间絾鑰楁椂鏇撮暱锛堝嚑鍗佺鍒板嚑鍒嗛挓锛夛紝涓绘祦渚涘簲鍟嗗潎涓哄紓姝ヤ换鍔℃ā寮忥細
//   - 闃块噷浜?DashScope: POST /api/v1/services/aigc/video-generation/video-synthesis 鈫?task_id
//   - 鐏北寮曟搸: POST /api/v3/contents/generations/tasks 鈫?task_id
//
// 璁捐鍚?ImageGenerator锛氳兘鍔涙帴鍙?+ 绫诲瀷鏂█锛屼笉鎵╁ぇ Provider 涓绘帴鍙ｃ€?
package provider

import "context"

// VideoRequest 缁熶竴瑙嗛鐢熸垚璇锋眰鏍煎紡
type VideoRequest struct {
	Model          string                 `json:"model"`
	Prompt         string                 `json:"prompt"`
	ImageURL       string                 `json:"image_url,omitempty"`    // 鍥剧敓瑙嗛锛坕2v锛夊弬鑰冨浘
	Duration       int                    `json:"duration,omitempty"`     // 绉掓暟锛岄€氬父 4/5/10
	Resolution     string                 `json:"resolution,omitempty"`   // "720P"/"1080P"/"480P"
	AspectRatio    string                 `json:"aspect_ratio,omitempty"` // "16:9"/"9:16"/"1:1"
	FPS            int                    `json:"fps,omitempty"`          // 甯х巼锛堝彲閫夛紝榛樿渚涘簲鍟嗛粯璁ゅ€硷級
	Seed           int64                  `json:"seed,omitempty"`
	NegativePrompt string                 `json:"negative_prompt,omitempty"`
	Extra          map[string]interface{} `json:"extra,omitempty"`
}

// VideoData 鍗曚釜鐢熸垚鐨勮棰戞暟鎹?
type VideoData struct {
	URL           string `json:"url,omitempty"`            // 瑙嗛 URL锛堜緵搴斿晢涓€鑸繑鍥炴湁鏃舵晥鐨勭鍚?URL锛?
	CoverURL      string `json:"cover_url,omitempty"`      // 灏侀潰鍥?URL
	DurationSec   int    `json:"duration_sec,omitempty"`   // 瀹為檯鏃堕暱
	RevisedPrompt string `json:"revised_prompt,omitempty"` // 瀹為檯浣跨敤鐨?prompt
}

// VideoResponse 缁熶竴瑙嗛鐢熸垚鍝嶅簲鏍煎紡
type VideoResponse struct {
	Created int64       `json:"created"`
	Model   string      `json:"model,omitempty"`
	Data    []VideoData `json:"data"`
	TaskID  string      `json:"task_id,omitempty"` // async task ID
	Status  string      `json:"status,omitempty"`
}

// VideoGenerator 瑙嗛鐢熸垚鑳藉姏鎺ュ彛
type VideoGenerator interface {
	// GenerateVideo 鎵ц瑙嗛鐢熸垚锛屽唴閮ㄥ鐞嗗紓姝ヤ换鍔℃彁浜ゅ拰杞
	GenerateVideo(ctx context.Context, req *VideoRequest) (*VideoResponse, error)
}

// VideoTaskQuerier is implemented by providers that can query an async video task.
type VideoTaskQuerier interface {
	QueryVideoTask(ctx context.Context, taskID string) (*VideoResponse, error)
}
