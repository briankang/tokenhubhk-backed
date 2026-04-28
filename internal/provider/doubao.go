// 豆包（字节跳动火山引擎）适配器
//
// Supported models:
//   - doubao-pro-32k
//   - doubao-lite-32k
//   - doubao-pro-128k
//
// API reference: https://www.volcengine.com/docs/82379/1263482
// 特殊处理: 火山引擎API，使用endpoint_id路由，OpenAI兼容格式
package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

var _ Provider = (*DoubaoProvider)(nil)

const doubaoDefaultBaseURL = "https://ark.cn-beijing.volces.com/api/v3"

var doubaoModels = []string{
	"doubao-pro-32k",
	"doubao-lite-32k",
	"doubao-pro-128k",
}

// DoubaoProvider 实现Provider接口的字节豆包适配器
type DoubaoProvider struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewDoubaoProvider 创建豆包提供商实例
func NewDoubaoProvider(cfg ProviderConfig) *DoubaoProvider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = doubaoDefaultBaseURL
	}
	return &DoubaoProvider{
		apiKey:  cfg.APIKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  newHTTPClient(cfg.TimeoutDuration()),
	}
}

func (p *DoubaoProvider) Name() string        { return "doubao" }
func (p *DoubaoProvider) ModelList() []string { return doubaoModels }

func (p *DoubaoProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider doubao: %w", err)
	}

	oaiReq := convertToOpenAIFormat(req, false)
	body, err := MarshalWithExtra(oaiReq, req.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider doubao: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider doubao: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider doubao: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider doubao: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var oaiResp openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&oaiResp); err != nil {
		return nil, fmt.Errorf("provider doubao: decode response: %w", err)
	}

	return convertFromOpenAIResponse(&oaiResp), nil
}

func (p *DoubaoProvider) StreamChat(ctx context.Context, req *ChatRequest) (StreamReader, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider doubao: %w", err)
	}

	oaiReq := convertToOpenAIFormat(req, true)
	body, err := MarshalWithExtra(oaiReq, req.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider doubao: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider doubao: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider doubao: do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("provider doubao: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return &openAIStreamReader{
		reader: bufio.NewReader(resp.Body),
		body:   resp.Body,
	}, nil
}

func (p *DoubaoProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
}

// 编译期接口检查：DoubaoProvider 实现 ImageGenerator
var _ ImageGenerator = (*DoubaoProvider)(nil)

// volcImageRequest 火山引擎图像生成请求（OpenAI 兼容格式）
// 参考: https://www.volcengine.com/docs/82379/1541523
type volcImageRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              int    `json:"n,omitempty"`
	Size           string `json:"size,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"`
	Seed           int64  `json:"seed,omitempty"`
}

// volcImageResponse 火山引擎图像响应
type volcImageResponse struct {
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Data    []struct {
		URL     string `json:"url,omitempty"`
		B64JSON string `json:"b64_json,omitempty"`
	} `json:"data"`
}

// GenerateImage 调用火山引擎图像生成接口（同步）
// API: POST /api/v3/images/generations （OpenAI 兼容）
func (p *DoubaoProvider) GenerateImage(ctx context.Context, req *ImageRequest) (*ImageResponse, error) {
	if req.Model == "" {
		return nil, fmt.Errorf("provider doubao: model is required")
	}
	if req.Prompt == "" {
		return nil, fmt.Errorf("provider doubao: prompt is required")
	}

	reqBody := &volcImageRequest{
		Model:          req.Model,
		Prompt:         req.Prompt,
		N:              req.N,
		Size:           req.Size,
		ResponseFormat: req.ResponseFormat,
		Seed:           req.Seed,
	}
	if reqBody.N <= 0 {
		reqBody.N = 1
	}

	body, err := MarshalWithExtra(reqBody, req.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider doubao: marshal image request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/images/generations", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider doubao: create image request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider doubao: do image request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider doubao: image API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var volcResp volcImageResponse
	if err := json.NewDecoder(resp.Body).Decode(&volcResp); err != nil {
		return nil, fmt.Errorf("provider doubao: decode image response: %w", err)
	}

	data := make([]ImageData, len(volcResp.Data))
	for i, d := range volcResp.Data {
		data[i] = ImageData{URL: d.URL, B64JSON: d.B64JSON}
	}
	return &ImageResponse{
		Created: volcResp.Created,
		Model:   volcResp.Model,
		Data:    data,
	}, nil
}

// ============ 视频生成（VideoGenerator）============
var _ VideoGenerator = (*DoubaoProvider)(nil)

const (
	volcVideoPollInterval = 3 * time.Second
	volcVideoMaxWait      = 5 * time.Minute // 视频生成典型 30s-3min
)

// volcVideoSubmitRequest 火山引擎视频生成提交请求
// 参考: https://www.volcengine.com/docs/82379/1521309
type volcVideoSubmitRequest struct {
	Model           string                 `json:"model"`
	Content         []volcVideoContentItem `json:"content"`
	GenerateAudio   *bool                  `json:"generate_audio,omitempty"`
	Draft           *bool                  `json:"draft,omitempty"`
	Resolution      string                 `json:"resolution,omitempty"`
	Ratio           string                 `json:"ratio,omitempty"`
	Duration        int                    `json:"duration,omitempty"`
	FramesPerSecond int                    `json:"framespersecond,omitempty"`
	ServiceTier     string                 `json:"service_tier,omitempty"`
}

type volcVideoContentItem struct {
	Type     string                 `json:"type"` // text / image_url / video_url
	Text     string                 `json:"text,omitempty"`
	ImageURL map[string]interface{} `json:"image_url,omitempty"` // {"url":"..."}
	VideoURL string                 `json:"video_url,omitempty"`
}

type volcVideoSubmitResponse struct {
	ID string `json:"id"` // task_id
}

type volcVideoTaskResponse struct {
	ID      string `json:"id"`
	Status  string `json:"status"` // queued/running/succeeded/failed/cancelled
	Content struct {
		VideoURL string `json:"video_url"`
	} `json:"content"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// GenerateVideo 火山引擎视频生成（异步任务 + 轮询）
// 1. POST /api/v3/contents/generations/tasks → task_id
// 2. 轮询 GET /api/v3/contents/generations/tasks/{id} 直到 status=succeeded/failed
func (p *DoubaoProvider) GenerateVideo(ctx context.Context, req *VideoRequest) (*VideoResponse, error) {
	if req.Model == "" {
		return nil, fmt.Errorf("provider doubao: model is required")
	}
	if req.Prompt == "" {
		return nil, fmt.Errorf("provider doubao: prompt is required")
	}

	promptText := req.Prompt

	content := []volcVideoContentItem{{Type: "text", Text: promptText}}
	if req.ImageURL != "" {
		content = append(content, volcVideoContentItem{
			Type:     "image_url",
			ImageURL: map[string]interface{}{"url": req.ImageURL},
		})
	}
	if req.VideoURL != "" {
		content = append(content, volcVideoContentItem{
			Type:     "video_url",
			VideoURL: req.VideoURL,
		})
	}

	submitReq := &volcVideoSubmitRequest{
		Model:           req.Model,
		Content:         content,
		GenerateAudio:   req.GenerateAudio,
		Draft:           req.Draft,
		Resolution:      strings.ToLower(req.Resolution),
		Ratio:           req.AspectRatio,
		Duration:        req.Duration,
		FramesPerSecond: req.FPS,
		ServiceTier:     req.ServiceTier,
	}
	body, err := MarshalWithExtra(submitReq, req.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider doubao: marshal video request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/contents/generations/tasks", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider doubao: create video submit: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider doubao: submit video task: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider doubao: video submit %d: %s", resp.StatusCode, string(respBody))
	}

	var submitResp volcVideoSubmitResponse
	if err := json.NewDecoder(resp.Body).Decode(&submitResp); err != nil {
		return nil, fmt.Errorf("provider doubao: decode video submit: %w", err)
	}
	if submitResp.ID == "" {
		return nil, fmt.Errorf("provider doubao: video submit missing task id")
	}

	return p.pollVideoTask(ctx, submitResp.ID)
}

// pollVideoTask 轮询火山引擎视频生成任务
func (p *DoubaoProvider) pollVideoTask(ctx context.Context, taskID string) (*VideoResponse, error) {
	pollCtx, cancel := context.WithTimeout(ctx, volcVideoMaxWait)
	defer cancel()
	ticker := time.NewTicker(volcVideoPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-pollCtx.Done():
			return nil, fmt.Errorf("provider doubao: video task %s timed out: %w", taskID, pollCtx.Err())
		case <-ticker.C:
			qReq, err := http.NewRequestWithContext(pollCtx, http.MethodGet, p.baseURL+"/contents/generations/tasks/"+taskID, nil)
			if err != nil {
				return nil, fmt.Errorf("provider doubao: create task query: %w", err)
			}
			p.setHeaders(qReq)

			qResp, err := p.client.Do(qReq)
			if err != nil {
				return nil, fmt.Errorf("provider doubao: query video task: %w", err)
			}
			respBody, _ := io.ReadAll(qResp.Body)
			qResp.Body.Close()
			if qResp.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("provider doubao: task query %d: %s", qResp.StatusCode, string(respBody))
			}

			var tq volcVideoTaskResponse
			if err := json.Unmarshal(respBody, &tq); err != nil {
				return nil, fmt.Errorf("provider doubao: decode task: %w", err)
			}

			switch tq.Status {
			case "succeeded":
				if tq.Content.VideoURL == "" {
					return nil, fmt.Errorf("provider doubao: video succeeded but no url")
				}
				return &VideoResponse{
					Created: time.Now().Unix(),
					Data:    []VideoData{{URL: tq.Content.VideoURL}},
					TaskID:  taskID,
				}, nil
			case "failed", "cancelled":
				msg := tq.Error.Message
				if msg == "" {
					msg = tq.Status
				}
				return nil, fmt.Errorf("provider doubao: video task failed: %s", msg)
			default:
				continue
			}
		}
	}
}

// ============ 语音合成（SpeechSynthesizer）============
var _ SpeechSynthesizer = (*DoubaoProvider)(nil)

type openaiTTSRequest struct {
	Model          string  `json:"model"`
	Input          string  `json:"input"`
	Voice          string  `json:"voice,omitempty"`
	ResponseFormat string  `json:"response_format,omitempty"`
	Speed          float64 `json:"speed,omitempty"`
}

// SynthesizeSpeech 调用 OpenAI 兼容 TTS 端点（火山引擎适用）
// API: POST /api/v3/audio/speech
func (p *DoubaoProvider) SynthesizeSpeech(ctx context.Context, req *TTSRequest) (*TTSResponse, error) {
	if req.Input == "" {
		return nil, fmt.Errorf("provider doubao: tts input is required")
	}
	reqBody := &openaiTTSRequest{
		Model:          req.Model,
		Input:          req.Input,
		Voice:          req.Voice,
		ResponseFormat: req.ResponseFormat,
		Speed:          req.Speed,
	}
	body, err := MarshalWithExtra(reqBody, req.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider doubao: marshal tts: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/audio/speech", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider doubao: create tts: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider doubao: do tts: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider doubao: tts %d: %s", resp.StatusCode, string(errBody))
	}
	audio, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("provider doubao: read tts audio: %w", err)
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "audio/mpeg"
	}
	return &TTSResponse{Audio: audio, ContentType: ct}, nil
}

// ============ 语音识别（SpeechTranscriber）============
var _ SpeechTranscriber = (*DoubaoProvider)(nil)

// TranscribeSpeech 调用 OpenAI 兼容 Whisper 端点（火山引擎适用）
// API: POST /api/v3/audio/transcriptions（multipart/form-data）
func (p *DoubaoProvider) TranscribeSpeech(ctx context.Context, req *ASRRequest) (*ASRResponse, error) {
	if len(req.Audio) == 0 {
		return nil, fmt.Errorf("provider doubao: asr audio is required")
	}
	filename := req.Filename
	if filename == "" {
		filename = "audio.mp3"
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("model", req.Model); err != nil {
		return nil, err
	}
	if req.Language != "" {
		mw.WriteField("language", req.Language)
	}
	if req.Prompt != "" {
		mw.WriteField("prompt", req.Prompt)
	}
	if req.ResponseFormat != "" {
		mw.WriteField("response_format", req.ResponseFormat)
	}
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return nil, fmt.Errorf("provider doubao: asr create form file: %w", err)
	}
	if _, err := fw.Write(req.Audio); err != nil {
		return nil, fmt.Errorf("provider doubao: asr write audio: %w", err)
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/audio/transcriptions", &buf)
	if err != nil {
		return nil, fmt.Errorf("provider doubao: create asr: %w", err)
	}
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider doubao: do asr: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("provider doubao: asr %d: %s", resp.StatusCode, string(respBody))
	}

	// 兼容两种响应：text 或 json
	if req.ResponseFormat == "text" || req.ResponseFormat == "srt" || req.ResponseFormat == "vtt" {
		return &ASRResponse{Text: string(respBody)}, nil
	}
	var parsed struct {
		Text     string  `json:"text"`
		Language string  `json:"language"`
		Duration float64 `json:"duration"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("provider doubao: decode asr: %w", err)
	}
	return &ASRResponse{
		Text:     parsed.Text,
		Language: parsed.Language,
		Duration: parsed.Duration,
	}, nil
}
