package user

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tokenhub-server/internal/pkg/response"
)

// ========================================================================
// ImageUploadHandler — Playground 视觉调试图片上传（代理到免费公开图床）
//
// 设计要点：
//  1. 前端以 multipart/form-data（field=image）提交；后端做类型/大小校验后
//     代理转发到 catbox.moe；若失败自动降级到 0x0.st。
//  2. 两个服务均无需 API Key，响应为纯文本 URL。
//  3. 仅鉴权用户可调用（挂在 userGroup 下）。
//  4. 返回 {url, provider}，前端 save 后作为 OpenAI image_url 参入对话。
// ========================================================================

// ImageUploadHandler 图片上传处理器
type ImageUploadHandler struct {
	client *http.Client
}

// NewImageUploadHandler 创建处理器实例
func NewImageUploadHandler() *ImageUploadHandler {
	return &ImageUploadHandler{
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// Register 注册路由
func (h *ImageUploadHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/upload/image", h.UploadImage)
}

// 允许的 MIME 类型（白名单）
var allowedImageMIME = map[string]bool{
	"image/jpeg": true,
	"image/jpg":  true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
}

// 允许的扩展名（白名单）
var allowedImageExt = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
}

// 单文件大小上限（10MB）
const maxImageSize = 10 * 1024 * 1024

// UploadImage POST /user/upload/image
// 请求: multipart/form-data, field=image
// 响应: {url: string, provider: string}
func (h *ImageUploadHandler) UploadImage(c *gin.Context) {
	// 限制 body 大小，防止恶意大文件
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxImageSize+1024)

	fileHeader, err := c.FormFile("image")
	if err != nil {
		response.ErrorMsg(c, http.StatusBadRequest, 40001, "缺少 image 字段或文件超出 10MB")
		return
	}

	if fileHeader.Size > maxImageSize {
		response.ErrorMsg(c, http.StatusBadRequest, 40002, "图片大小不能超过 10MB")
		return
	}
	if fileHeader.Size == 0 {
		response.ErrorMsg(c, http.StatusBadRequest, 40003, "文件为空")
		return
	}

	// 校验扩展名 + Content-Type（双重过滤）
	ext := strings.ToLower(filepath.Ext(fileHeader.Filename))
	if !allowedImageExt[ext] {
		response.ErrorMsg(c, http.StatusBadRequest, 40004, "仅支持 jpg/png/gif/webp 格式")
		return
	}
	contentType := fileHeader.Header.Get("Content-Type")
	if contentType != "" && !allowedImageMIME[contentType] {
		response.ErrorMsg(c, http.StatusBadRequest, 40005, "不支持的 Content-Type: "+contentType)
		return
	}

	// 读入内存（≤10MB 可接受）
	src, err := fileHeader.Open()
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50001, "读取文件失败")
		return
	}
	defer src.Close()

	imgBytes, err := io.ReadAll(src)
	if err != nil {
		response.ErrorMsg(c, http.StatusInternalServerError, 50002, "读取文件失败")
		return
	}

	// 尝试 catbox.moe 主路径
	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()

	logger := zap.L()
	uploadURL, err := h.uploadToCatbox(ctx, imgBytes, fileHeader.Filename)
	if err == nil && uploadURL != "" {
		logger.Info("image uploaded via catbox",
			zap.Int64("size", fileHeader.Size),
			zap.String("filename", fileHeader.Filename),
			zap.String("url", uploadURL),
		)
		response.Success(c, gin.H{"url": uploadURL, "provider": "catbox"})
		return
	}
	logger.Warn("catbox upload failed, fallback to 0x0.st", zap.Error(err))

	// 降级到 0x0.st
	ctx2, cancel2 := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel2()
	fallbackURL, err2 := h.uploadTo0x0(ctx2, imgBytes, fileHeader.Filename)
	if err2 == nil && fallbackURL != "" {
		logger.Info("image uploaded via 0x0.st",
			zap.Int64("size", fileHeader.Size),
			zap.String("filename", fileHeader.Filename),
			zap.String("url", fallbackURL),
		)
		response.Success(c, gin.H{"url": fallbackURL, "provider": "0x0.st"})
		return
	}
	logger.Error("all image upload providers failed",
		zap.Error(err),
		zap.NamedError("fallback_err", err2),
	)
	response.ErrorMsg(c, http.StatusBadGateway, 50201, "图片上传失败，请稍后重试")
}

// uploadToCatbox 上传到 catbox.moe（匿名模式，无需 key，永久保留）
// API: POST https://catbox.moe/user/api.php
// Fields: reqtype=fileupload, fileToUpload=<file>
// Response: 纯文本 URL（成功）或错误文本
func (h *ImageUploadHandler) uploadToCatbox(ctx context.Context, data []byte, filename string) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("reqtype", "fileupload"); err != nil {
		return "", err
	}
	fw, err := w.CreateFormFile("fileToUpload", filename)
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(data); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://catbox.moe/user/api.php", &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("User-Agent", "TokenHubHK/1.0")

	resp, err := h.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(body))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("catbox HTTP %d: %s", resp.StatusCode, text)
	}
	if !strings.HasPrefix(text, "https://") {
		return "", fmt.Errorf("catbox returned non-URL response: %s", text)
	}
	if _, err := url.ParseRequestURI(text); err != nil {
		return "", fmt.Errorf("catbox returned invalid URL: %s", text)
	}
	return text, nil
}

// uploadTo0x0 上传到 0x0.st（匿名模式，无需 key，小文件可保留 365 天）
// API: POST https://0x0.st
// Fields: file=<file>
// Response: 纯文本 URL
func (h *ImageUploadHandler) uploadTo0x0(ctx context.Context, data []byte, filename string) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(data); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://0x0.st", &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	// 0x0.st 要求 User-Agent
	req.Header.Set("User-Agent", "TokenHubHK-Uploader/1.0 (+https://www.tokenhubhk.com)")

	resp, err := h.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(body))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("0x0.st HTTP %d: %s", resp.StatusCode, text)
	}
	if !strings.HasPrefix(text, "https://") {
		return "", fmt.Errorf("0x0.st returned non-URL response: %s", text)
	}
	if _, err := url.ParseRequestURI(text); err != nil {
		return "", fmt.Errorf("0x0.st returned invalid URL: %s", text)
	}
	return text, nil
}
