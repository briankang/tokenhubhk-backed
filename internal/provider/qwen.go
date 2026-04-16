// 通义千问（Qwen）适配器（OpenAI兼容格式）
//
// Supported models:
//   - qwen-turbo
//   - qwen-plus
//   - qwen-max
//   - qwen-2.5-72b-instruct
//   - qwen-2.5-32b-instruct
//
// API reference: https://help.aliyun.com/zh/model-studio/developer-reference/
// 格式: OpenAI兼容，使用DashScope端点
package provider

import (
	"bufio"
	"bytes"
	"context"
	stdBase64 "encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var _ Provider = (*QwenProvider)(nil)

const qwenDefaultBaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"

var qwenModels = []string{
	"qwen-turbo",
	"qwen-plus",
	"qwen-max",
	"qwen-2.5-72b-instruct",
	"qwen-2.5-32b-instruct",
}

// QwenProvider 实现Provider接口的阿里云通义千问适配器
type QwenProvider struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewQwenProvider 创建通义千问提供商实例
func NewQwenProvider(cfg ProviderConfig) *QwenProvider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = qwenDefaultBaseURL
	}
	return &QwenProvider{
		apiKey:  cfg.APIKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  newHTTPClient(cfg.TimeoutDuration()),
	}
}

func (p *QwenProvider) Name() string      { return "qwen" }
func (p *QwenProvider) ModelList() []string { return qwenModels }

func (p *QwenProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider qwen: %w", err)
	}

	oaiReq := convertToOpenAIFormat(req, false)
	body, err := MarshalWithExtra(oaiReq, req.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider qwen: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider qwen: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider qwen: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider qwen: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var oaiResp openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&oaiResp); err != nil {
		return nil, fmt.Errorf("provider qwen: decode response: %w", err)
	}

	return convertFromOpenAIResponse(&oaiResp), nil
}

func (p *QwenProvider) StreamChat(ctx context.Context, req *ChatRequest) (StreamReader, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("provider qwen: %w", err)
	}

	oaiReq := convertToOpenAIFormat(req, true)
	body, err := MarshalWithExtra(oaiReq, req.Extra)
	if err != nil {
		return nil, fmt.Errorf("provider qwen: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider qwen: create request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider qwen: do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("provider qwen: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return &openAIStreamReader{
		reader: bufio.NewReader(resp.Body),
		body:   resp.Body,
	}, nil
}

func (p *QwenProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
}

// 编译期接口检查：QwenProvider 实现 ImageGenerator
var _ ImageGenerator = (*QwenProvider)(nil)

// DashScope 图像生成异步任务端点（独立于 OpenAI 兼容域名）
// 参考: https://help.aliyun.com/zh/model-studio/developer-reference/text-to-image-v2-api-reference
// 不同模型族使用不同端点：
//   wanx-*                → text2image/image-synthesis
//   qwen-image-*, z-image-* → multimodal-generation/generation
const (
	dashscopeImageSubmitURLT2I       = "https://dashscope.aliyuncs.com/api/v1/services/aigc/text2image/image-synthesis"
	dashscopeImageSubmitURLMultimod  = "https://dashscope.aliyuncs.com/api/v1/services/aigc/multimodal-generation/generation"
	dashscopeTaskQueryURL            = "https://dashscope.aliyuncs.com/api/v1/tasks/"
	qwenImagePollInterval            = 2 * time.Second
	qwenImageMaxWait                 = 90 * time.Second // 默认轮询超时
)

// pickImageSubmitURL 按模型族选择正确的提交端点
func pickImageSubmitURL(model string) string {
	m := strings.ToLower(model)
	if strings.HasPrefix(m, "qwen-image") || strings.HasPrefix(m, "z-image") {
		return dashscopeImageSubmitURLMultimod
	}
	return dashscopeImageSubmitURLT2I
}

// normalizeDashScopeSize 将 1024x1024 规范为 1024*1024（DashScope 使用 * 分隔）
func normalizeDashScopeSize(size string) string {
	return strings.ReplaceAll(strings.ToLower(size), "x", "*")
}

// dashscopeImageInput 阿里云图像请求 input 字段
type dashscopeImageInput struct {
	Prompt         string `json:"prompt"`
	NegativePrompt string `json:"negative_prompt,omitempty"`
}

// dashscopeImageParameters 阿里云图像请求 parameters 字段
type dashscopeImageParameters struct {
	Size  string `json:"size,omitempty"`
	N     int    `json:"n,omitempty"`
	Seed  int64  `json:"seed,omitempty"`
	Style string `json:"style,omitempty"`
}

// dashscopeImageSubmitRequest 阿里云图像提交请求
type dashscopeImageSubmitRequest struct {
	Model      string                   `json:"model"`
	Input      dashscopeImageInput      `json:"input"`
	Parameters dashscopeImageParameters `json:"parameters,omitempty"`
}

// dashscopeImageSubmitResponse 提交响应（含 task_id）
type dashscopeImageSubmitResponse struct {
	RequestID string `json:"request_id"`
	Output    struct {
		TaskID     string `json:"task_id"`
		TaskStatus string `json:"task_status"`
	} `json:"output"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// dashscopeTaskQueryResponse 任务查询响应
type dashscopeTaskQueryResponse struct {
	RequestID string `json:"request_id"`
	Output    struct {
		TaskID     string `json:"task_id"`
		TaskStatus string `json:"task_status"` // PENDING / RUNNING / SUCCEEDED / FAILED / UNKNOWN
		Results    []struct {
			URL    string `json:"url,omitempty"`
			Code   string `json:"code,omitempty"`
			Message string `json:"message,omitempty"`
		} `json:"results"`
		Code    string `json:"code,omitempty"`
		Message string `json:"message,omitempty"`
	} `json:"output"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// GenerateImage 调用阿里云 DashScope 图像生成接口（异步任务 + 轮询）
// 1. POST 提交任务，取 output.task_id
// 2. 轮询 GET /api/v1/tasks/{task_id} 直到 task_status = SUCCEEDED / FAILED
// 3. 从 output.results[].url 取图片 URL
func (p *QwenProvider) GenerateImage(ctx context.Context, req *ImageRequest) (*ImageResponse, error) {
	if req.Model == "" {
		return nil, fmt.Errorf("provider qwen: model is required")
	}
	if req.Prompt == "" {
		return nil, fmt.Errorf("provider qwen: prompt is required")
	}

	submitURL := pickImageSubmitURL(req.Model)
	size := normalizeDashScopeSize(req.Size)
	n := req.N
	if n <= 0 {
		n = 1
	}

	// 不同端点使用不同请求体
	var body []byte
	var err error
	if submitURL == dashscopeImageSubmitURLMultimod {
		// qwen-image / z-image 使用 messages content 格式
		content := []map[string]interface{}{{"text": req.Prompt}}
		multiReq := map[string]interface{}{
			"model": req.Model,
			"input": map[string]interface{}{
				"messages": []map[string]interface{}{
					{"role": "user", "content": content},
				},
			},
			"parameters": map[string]interface{}{
				"size":      size,
				"n":         n,
				"watermark": false,
			},
		}
		if req.NegativePrompt != "" {
			multiReq["parameters"].(map[string]interface{})["negative_prompt"] = req.NegativePrompt
		}
		if req.Seed > 0 {
			multiReq["parameters"].(map[string]interface{})["seed"] = req.Seed
		}
		body, err = MarshalWithExtra(multiReq, req.Extra)
	} else {
		// wanx-* 使用 input.prompt + parameters 格式
		submitReq := &dashscopeImageSubmitRequest{
			Model: req.Model,
			Input: dashscopeImageInput{
				Prompt:         req.Prompt,
				NegativePrompt: req.NegativePrompt,
			},
			Parameters: dashscopeImageParameters{
				Size:  size,
				N:     n,
				Seed:  req.Seed,
				Style: req.Style,
			},
		}
		body, err = MarshalWithExtra(submitReq, req.Extra)
	}
	if err != nil {
		return nil, fmt.Errorf("provider qwen: marshal image request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, submitURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider qwen: create image submit request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	// text2image 端点是异步（async header），multimodal-generation 是同步（无 header）
	isAsync := submitURL == dashscopeImageSubmitURLT2I
	if isAsync {
		httpReq.Header.Set("X-DashScope-Async", "enable")
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider qwen: submit image task: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider qwen: image submit returned %d: %s", resp.StatusCode, string(respBody))
	}

	// 同步模式：直接解析返回的图片 URL（multimodal-generation）
	if !isAsync {
		var syncResp struct {
			RequestID string `json:"request_id"`
			Output    struct {
				Choices []struct {
					Message struct {
						Content []struct {
							Image string `json:"image,omitempty"`
							Text  string `json:"text,omitempty"`
						} `json:"content"`
					} `json:"message"`
				} `json:"choices"`
			} `json:"output"`
			Message string `json:"message,omitempty"`
			Code    string `json:"code,omitempty"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&syncResp); err != nil {
			return nil, fmt.Errorf("provider qwen: decode sync image response: %w", err)
		}
		data := make([]ImageData, 0)
		for _, ch := range syncResp.Output.Choices {
			for _, c := range ch.Message.Content {
				if c.Image != "" {
					data = append(data, ImageData{URL: c.Image})
				}
			}
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("provider qwen: sync image response no url: %s %s", syncResp.Code, syncResp.Message)
		}
		return &ImageResponse{Created: time.Now().Unix(), Data: data}, nil
	}

	// 异步模式：取 task_id 轮询
	var submitResp dashscopeImageSubmitResponse
	if err := json.NewDecoder(resp.Body).Decode(&submitResp); err != nil {
		return nil, fmt.Errorf("provider qwen: decode submit response: %w", err)
	}
	if submitResp.Output.TaskID == "" {
		msg := submitResp.Message
		if msg == "" {
			msg = submitResp.Code
		}
		return nil, fmt.Errorf("provider qwen: image submit missing task_id: %s", msg)
	}
	return p.pollImageTask(ctx, submitResp.Output.TaskID)
}

// pollImageTask 轮询 DashScope 异步图像任务直到完成
func (p *QwenProvider) pollImageTask(ctx context.Context, taskID string) (*ImageResponse, error) {
	// 为轮询建立独立 deadline（若用户 ctx 已有更短超时则用更短的）
	pollCtx, cancel := context.WithTimeout(ctx, qwenImageMaxWait)
	defer cancel()

	ticker := time.NewTicker(qwenImagePollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-pollCtx.Done():
			return nil, fmt.Errorf("provider qwen: image task %s timed out: %w", taskID, pollCtx.Err())
		case <-ticker.C:
			queryURL := dashscopeTaskQueryURL + taskID
			qReq, err := http.NewRequestWithContext(pollCtx, http.MethodGet, queryURL, nil)
			if err != nil {
				return nil, fmt.Errorf("provider qwen: create task query: %w", err)
			}
			qReq.Header.Set("Authorization", "Bearer "+p.apiKey)

			qResp, err := p.client.Do(qReq)
			if err != nil {
				return nil, fmt.Errorf("provider qwen: query task: %w", err)
			}
			respBody, _ := io.ReadAll(qResp.Body)
			qResp.Body.Close()

			if qResp.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("provider qwen: task query returned %d: %s", qResp.StatusCode, string(respBody))
			}

			var tq dashscopeTaskQueryResponse
			if err := json.Unmarshal(respBody, &tq); err != nil {
				return nil, fmt.Errorf("provider qwen: decode task query: %w", err)
			}

			switch tq.Output.TaskStatus {
			case "SUCCEEDED":
				data := make([]ImageData, 0, len(tq.Output.Results))
				for _, r := range tq.Output.Results {
					if r.URL != "" {
						data = append(data, ImageData{URL: r.URL})
					}
				}
				if len(data) == 0 {
					return nil, fmt.Errorf("provider qwen: task succeeded but no image URLs returned")
				}
				return &ImageResponse{
					Created: time.Now().Unix(),
					Data:    data,
				}, nil
			case "FAILED":
				msg := tq.Output.Message
				if msg == "" {
					msg = tq.Output.Code
				}
				return nil, fmt.Errorf("provider qwen: image task failed: %s", msg)
			case "PENDING", "RUNNING":
				continue // 继续轮询
			default:
				// UNKNOWN 等未知状态也继续轮询
				continue
			}
		}
	}
}

// ============ 视频生成（VideoGenerator）============
var _ VideoGenerator = (*QwenProvider)(nil)

const (
	// DashScope 视频生成异步端点
	// 参考: https://help.aliyun.com/zh/model-studio/developer-reference/video-generation-api
	dashscopeVideoSubmitURL = "https://dashscope.aliyuncs.com/api/v1/services/aigc/video-generation/video-synthesis"
	qwenVideoPollInterval   = 5 * time.Second
	qwenVideoMaxWait        = 8 * time.Minute
)

type dashscopeVideoInput struct {
	Prompt         string `json:"prompt"`
	NegativePrompt string `json:"negative_prompt,omitempty"`
	ImgURL         string `json:"img_url,omitempty"` // 图生视频
}

type dashscopeVideoParameters struct {
	Duration   int    `json:"duration,omitempty"`
	Resolution string `json:"resolution,omitempty"`
	PromptExtend bool `json:"prompt_extend,omitempty"`
	Seed       int64  `json:"seed,omitempty"`
	Size       string `json:"size,omitempty"`
}

type dashscopeVideoSubmitRequest struct {
	Model      string                   `json:"model"`
	Input      dashscopeVideoInput      `json:"input"`
	Parameters dashscopeVideoParameters `json:"parameters,omitempty"`
}

type dashscopeVideoTaskResponse struct {
	RequestID string `json:"request_id"`
	Output    struct {
		TaskID     string `json:"task_id"`
		TaskStatus string `json:"task_status"`
		VideoURL   string `json:"video_url"`
		Results    []struct {
			URL string `json:"url"`
		} `json:"results"`
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"output"`
}

// GenerateVideo 阿里云 DashScope 视频生成（异步任务）
func (p *QwenProvider) GenerateVideo(ctx context.Context, req *VideoRequest) (*VideoResponse, error) {
	if req.Model == "" || req.Prompt == "" {
		return nil, fmt.Errorf("provider qwen: model and prompt required")
	}
	submitReq := &dashscopeVideoSubmitRequest{
		Model: req.Model,
		Input: dashscopeVideoInput{
			Prompt:         req.Prompt,
			NegativePrompt: req.NegativePrompt,
			ImgURL:         req.ImageURL,
		},
		Parameters: dashscopeVideoParameters{
			Duration:   req.Duration,
			Resolution: req.Resolution,
			Seed:       req.Seed,
			Size:       req.Resolution,
		},
	}
	body, err := MarshalWithExtra(submitReq, req.Extra)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, dashscopeVideoSubmitURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("X-DashScope-Async", "enable")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider qwen: submit video: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider qwen: video submit %d: %s", resp.StatusCode, string(errBody))
	}
	var submitResp dashscopeVideoTaskResponse
	if err := json.NewDecoder(resp.Body).Decode(&submitResp); err != nil {
		return nil, err
	}
	if submitResp.Output.TaskID == "" {
		return nil, fmt.Errorf("provider qwen: video submit missing task_id")
	}
	return p.pollVideoTask(ctx, submitResp.Output.TaskID)
}

func (p *QwenProvider) pollVideoTask(ctx context.Context, taskID string) (*VideoResponse, error) {
	pollCtx, cancel := context.WithTimeout(ctx, qwenVideoMaxWait)
	defer cancel()
	ticker := time.NewTicker(qwenVideoPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-pollCtx.Done():
			return nil, fmt.Errorf("provider qwen: video task %s timeout: %w", taskID, pollCtx.Err())
		case <-ticker.C:
			qReq, err := http.NewRequestWithContext(pollCtx, http.MethodGet, dashscopeTaskQueryURL+taskID, nil)
			if err != nil {
				return nil, err
			}
			qReq.Header.Set("Authorization", "Bearer "+p.apiKey)
			qResp, err := p.client.Do(qReq)
			if err != nil {
				return nil, err
			}
			respBody, _ := io.ReadAll(qResp.Body)
			qResp.Body.Close()
			if qResp.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("provider qwen: video query %d: %s", qResp.StatusCode, string(respBody))
			}
			var tq dashscopeVideoTaskResponse
			if err := json.Unmarshal(respBody, &tq); err != nil {
				return nil, err
			}
			switch tq.Output.TaskStatus {
			case "SUCCEEDED":
				url := tq.Output.VideoURL
				if url == "" && len(tq.Output.Results) > 0 {
					url = tq.Output.Results[0].URL
				}
				if url == "" {
					return nil, fmt.Errorf("provider qwen: video succeeded but no url")
				}
				return &VideoResponse{
					Created: time.Now().Unix(),
					Data:    []VideoData{{URL: url}},
					TaskID:  taskID,
				}, nil
			case "FAILED":
				return nil, fmt.Errorf("provider qwen: video task failed: %s", tq.Output.Message)
			case "PENDING", "RUNNING":
				continue
			default:
				continue
			}
		}
	}
}

// ============ TTS（SpeechSynthesizer）============
// 阿里云 DashScope 的 qwen-tts / qwen3-tts 使用 multimodal-generation 端点
// compatible-mode `/audio/speech` 在 DashScope 上未实现，返回 404
var _ SpeechSynthesizer = (*QwenProvider)(nil)

const dashscopeTTSURL = "https://dashscope.aliyuncs.com/api/v1/services/aigc/multimodal-generation/generation"

// OpenAI 六种标准音色 → DashScope qwen-tts 官方音色映射
var qwenVoiceMap = map[string]string{
	"alloy":   "Cherry",
	"echo":    "Ethan",
	"fable":   "Serena",
	"onyx":    "Dylan",
	"nova":    "Chelsie",
	"shimmer": "Jada",
}

type dashscopeTTSResponse struct {
	RequestID string `json:"request_id"`
	Output    struct {
		Audio struct {
			URL  string `json:"url"`
			Data string `json:"data"`
			// expires_at 可能是 int 或 string，用 interface{} 避免解码失败
			ExpiresAt interface{} `json:"expires_at"`
		} `json:"audio"`
		FinishReason string `json:"finish_reason"`
	} `json:"output"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func (p *QwenProvider) SynthesizeSpeech(ctx context.Context, req *TTSRequest) (*TTSResponse, error) {
	if req.Input == "" {
		return nil, fmt.Errorf("provider qwen: tts input required")
	}

	voice := req.Voice
	if mapped, ok := qwenVoiceMap[strings.ToLower(voice)]; ok {
		voice = mapped
	}
	if voice == "" {
		voice = "Cherry"
	}

	format := req.ResponseFormat
	if format == "" {
		format = "mp3"
	}

	reqBody := map[string]interface{}{
		"model": req.Model,
		"input": map[string]interface{}{
			"text": req.Input,
		},
		"parameters": map[string]interface{}{
			"voice":  voice,
			"format": format,
		},
	}
	if req.Speed > 0 {
		reqBody["parameters"].(map[string]interface{})["rate"] = req.Speed
	}

	body, err := MarshalWithExtra(reqBody, req.Extra)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, dashscopeTTSURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider qwen: do tts: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider qwen: tts %d: %s", resp.StatusCode, string(errBody))
	}

	var ttsResp dashscopeTTSResponse
	if err := json.NewDecoder(resp.Body).Decode(&ttsResp); err != nil {
		return nil, fmt.Errorf("provider qwen: decode tts: %w", err)
	}

	// DashScope 返回音频 URL（签名 URL），需下载二进制
	if ttsResp.Output.Audio.URL == "" {
		return nil, fmt.Errorf("provider qwen: tts no audio url in response: %s", ttsResp.Message)
	}

	dlCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	dlReq, _ := http.NewRequestWithContext(dlCtx, http.MethodGet, ttsResp.Output.Audio.URL, nil)
	dlResp, err := p.client.Do(dlReq)
	if err != nil {
		return nil, fmt.Errorf("provider qwen: download tts audio: %w", err)
	}
	defer dlResp.Body.Close()
	if dlResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("provider qwen: download tts audio status %d", dlResp.StatusCode)
	}
	audio, err := io.ReadAll(dlResp.Body)
	if err != nil {
		return nil, err
	}

	ct := dlResp.Header.Get("Content-Type")
	if ct == "" {
		switch format {
		case "wav":
			ct = "audio/wav"
		case "opus":
			ct = "audio/opus"
		default:
			ct = "audio/mpeg"
		}
	}
	return &TTSResponse{Audio: audio, ContentType: ct, RequestID: ttsResp.RequestID}, nil
}

// ============ ASR（SpeechTranscriber）============
// 阿里云 DashScope 的 qwen-asr / qwen3-asr 使用 multimodal-generation 端点，
// 通过 base64 data URI 将音频内嵌在 messages 中（避免先上传 OSS）
var _ SpeechTranscriber = (*QwenProvider)(nil)

const dashscopeASRURL = "https://dashscope.aliyuncs.com/api/v1/services/aigc/multimodal-generation/generation"

// guessAudioMIME 根据文件名扩展名猜测 MIME
func guessAudioMIME(filename string) string {
	lower := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(lower, ".mp3"):
		return "audio/mp3"
	case strings.HasSuffix(lower, ".wav"):
		return "audio/wav"
	case strings.HasSuffix(lower, ".m4a"):
		return "audio/mp4"
	case strings.HasSuffix(lower, ".flac"):
		return "audio/flac"
	case strings.HasSuffix(lower, ".ogg"):
		return "audio/ogg"
	case strings.HasSuffix(lower, ".aac"):
		return "audio/aac"
	default:
		return "audio/mp3"
	}
}

type dashscopeASRResponse struct {
	RequestID string `json:"request_id"`
	Output    struct {
		Choices []struct {
			Message struct {
				Content []struct {
					Text string `json:"text,omitempty"`
				} `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	} `json:"output"`
	Usage struct {
		DurationSeconds float64 `json:"duration,omitempty"`
	} `json:"usage"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func (p *QwenProvider) TranscribeSpeech(ctx context.Context, req *ASRRequest) (*ASRResponse, error) {
	if len(req.Audio) == 0 {
		return nil, fmt.Errorf("provider qwen: asr audio required")
	}

	filename := req.Filename
	if filename == "" {
		filename = "audio.mp3"
	}
	mime := guessAudioMIME(filename)
	dataURI := "data:" + mime + ";base64," + base64EncodeToString(req.Audio)

	asrOpts := map[string]interface{}{
		"enable_lid": true,
		"enable_itn": true,
	}
	if req.Language != "" {
		asrOpts["language"] = req.Language
	}

	reqBody := map[string]interface{}{
		"model": req.Model,
		"input": map[string]interface{}{
			"messages": []map[string]interface{}{
				{
					"role": "user",
					"content": []map[string]interface{}{
						{"audio": dataURI},
					},
				},
			},
		},
		"parameters": map[string]interface{}{
			"asr_options": asrOpts,
		},
	}
	if req.Prompt != "" {
		reqBody["input"].(map[string]interface{})["messages"].([]map[string]interface{})[0]["content"] =
			append(reqBody["input"].(map[string]interface{})["messages"].([]map[string]interface{})[0]["content"].([]map[string]interface{}),
				map[string]interface{}{"text": req.Prompt})
	}

	body, err := MarshalWithExtra(reqBody, req.Extra)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, dashscopeASRURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider qwen: do asr: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("provider qwen: asr %d: %s", resp.StatusCode, string(respBody))
	}

	var parsed dashscopeASRResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, err
	}

	text := ""
	if len(parsed.Output.Choices) > 0 {
		for _, c := range parsed.Output.Choices[0].Message.Content {
			text += c.Text
		}
	}
	return &ASRResponse{
		Text:     text,
		Language: req.Language,
		Duration: parsed.Usage.DurationSeconds,
	}, nil
}

// base64EncodeToString 独立包装，避免与其他文件的 base64 引用冲突
func base64EncodeToString(b []byte) string {
	return stdBase64.StdEncoding.EncodeToString(b)
}
