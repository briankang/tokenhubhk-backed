// Package provider 语音合成 / 语音识别能力接口
//
// OpenAI 定义的两个端点：
//   - TTS（语音合成）: POST /v1/audio/speech，返回二进制音频
//   - ASR（语音识别）: POST /v1/audio/transcriptions，multipart/form-data 上传音频
//
// 设计同 ImageGenerator：能力接口 + 类型断言。
package provider

import "context"

// TTSRequest 语音合成请求
type TTSRequest struct {
	Model          string                 `json:"model"`
	Input          string                 `json:"input"`                     // 待合成文本
	Voice          string                 `json:"voice,omitempty"`           // 音色（OpenAI: alloy/echo/fable/onyx/nova/shimmer）
	ResponseFormat string                 `json:"response_format,omitempty"` // mp3/opus/aac/flac/wav/pcm（默认 mp3）
	Speed          float64                `json:"speed,omitempty"`           // 语速 0.25-4.0
	Extra          map[string]interface{} `json:"extra,omitempty"`
}

// TTSResponse 语音合成响应
type TTSResponse struct {
	Audio       []byte `json:"-"`                    // 原始音频字节（调用方自行转 base64 或保存）
	ContentType string `json:"content_type"`         // audio/mpeg / audio/wav 等
	RequestID   string `json:"request_id,omitempty"` // 上游请求 ID
}

// SpeechSynthesizer 语音合成（TTS）能力接口
type SpeechSynthesizer interface {
	SynthesizeSpeech(ctx context.Context, req *TTSRequest) (*TTSResponse, error)
}

// ASRRequest 语音识别请求
type ASRRequest struct {
	Model          string                 `json:"model"`
	Audio          []byte                 `json:"-"`                         // 原始音频字节
	Filename       string                 `json:"filename,omitempty"`        // 原始文件名（含扩展名，决定 MIME）
	Language       string                 `json:"language,omitempty"`        // ISO-639-1，如 "zh"、"en"
	Prompt         string                 `json:"prompt,omitempty"`          // 提示词（帮助识别专有名词）
	ResponseFormat string                 `json:"response_format,omitempty"` // json/text/srt/vtt/verbose_json（默认 json）
	Temperature    float64                `json:"temperature,omitempty"`
	Extra          map[string]interface{} `json:"extra,omitempty"`
}

// ASRResponse 语音识别响应
type ASRResponse struct {
	Text     string   `json:"text"`               // 识别文本
	Language string   `json:"language,omitempty"` // 检测到的语言
	Duration float64  `json:"duration,omitempty"` // 音频时长（秒）
	Segments []string `json:"segments,omitempty"` // 分段文本（可选）
}

// SpeechTranscriber 语音识别（ASR）能力接口
type SpeechTranscriber interface {
	TranscribeSpeech(ctx context.Context, req *ASRRequest) (*ASRResponse, error)
}
