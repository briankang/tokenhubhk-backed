// Package uploader 封装对外免费图床 / 文件床（catbox.moe 主 + 0x0.st 降级）的上传逻辑。
//
// 原 image_upload_handler.go 里的 uploadToCatbox / uploadTo0x0 抽取到此处，便于
// 发票 PDF 上传等多个 handler 复用。
package uploader

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client 通用文件上传客户端
type Client struct {
	hc *http.Client
}

// New 构造
func New() *Client {
	return &Client{hc: &http.Client{Timeout: 30 * time.Second}}
}

// UploadWithFallback 先尝试 catbox，失败降级 0x0.st。
// 返回 (url, provider, error)；两家都失败则返回最后错误。
func (c *Client) UploadWithFallback(ctx context.Context, data []byte, filename string) (string, string, error) {
	if u, err := c.UploadToCatbox(ctx, data, filename); err == nil {
		return u, "catbox", nil
	} else if u2, err2 := c.UploadTo0x0(ctx, data, filename); err2 == nil {
		return u2, "0x0.st", nil
	} else {
		return "", "", fmt.Errorf("both catbox and 0x0.st failed: catbox=%v, 0x0=%v", err, err2)
	}
}

// UploadToCatbox 上传到 catbox.moe（永久保留）
func (c *Client) UploadToCatbox(ctx context.Context, data []byte, filename string) (string, error) {
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

	resp, err := c.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	text := strings.TrimSpace(string(body))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("catbox HTTP %d: %s", resp.StatusCode, text)
	}
	if !strings.HasPrefix(text, "https://") {
		return "", fmt.Errorf("catbox returned non-URL: %s", text)
	}
	if _, err := url.ParseRequestURI(text); err != nil {
		return "", fmt.Errorf("catbox returned invalid URL: %s", text)
	}
	return text, nil
}

// UploadTo0x0 上传到 0x0.st（小文件保留 365 天）
func (c *Client) UploadTo0x0(ctx context.Context, data []byte, filename string) (string, error) {
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
	req.Header.Set("User-Agent", "TokenHubHK-Uploader/1.0 (+https://www.tokenhubhk.com)")

	resp, err := c.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	text := strings.TrimSpace(string(body))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("0x0.st HTTP %d: %s", resp.StatusCode, text)
	}
	if !strings.HasPrefix(text, "https://") {
		return "", fmt.Errorf("0x0.st returned non-URL: %s", text)
	}
	if _, err := url.ParseRequestURI(text); err != nil {
		return "", fmt.Errorf("0x0.st returned invalid URL: %s", text)
	}
	return text, nil
}
