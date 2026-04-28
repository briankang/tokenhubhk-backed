package sms

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Sender 发送短信验证码。
type Sender interface {
	Send(ctx context.Context, req SendRequest) (*SendResult, error)
}

// CaptchaVerifier 校验阿里云验证码 2.0。
type CaptchaVerifier interface {
	Verify(ctx context.Context, req CaptchaVerifyRequest) (*CaptchaVerifyResult, error)
}

type SendRequest struct {
	PhoneE164         string
	Code              string
	AccessKeyID       string
	AccessKeySecret   string
	RegionID          string
	Endpoint          string
	SignName          string
	TemplateCode      string
	TemplateParamName string
}

type SendResult struct {
	Success   bool
	RequestID string
	Code      string
	Message   string
}

type CaptchaVerifyRequest struct {
	AccessKeyID        string
	AccessKeySecret    string
	RegionID           string
	Endpoint           string
	SceneID            string
	CaptchaVerifyParam string
}

type CaptchaVerifyResult struct {
	Success   bool
	RequestID string
	Code      string
	Message   string
}

// AliyunClient 使用阿里云 OpenAPI V3 签名直接调用短信和验证码接口。
// 保持标准库实现，避免运行环境必须预装阿里云 SDK。
type AliyunClient struct {
	httpClient *http.Client
}

func NewAliyunClient() *AliyunClient {
	return &AliyunClient{httpClient: &http.Client{Timeout: 10 * time.Second}}
}

func (c *AliyunClient) Send(ctx context.Context, req SendRequest) (*SendResult, error) {
	if req.Endpoint == "" {
		req.Endpoint = "dysmsapi.aliyuncs.com"
	}
	if req.RegionID == "" {
		req.RegionID = "cn-hangzhou"
	}
	paramName := req.TemplateParamName
	if paramName == "" {
		paramName = "code"
	}
	paramBytes, _ := json.Marshal(map[string]string{paramName: req.Code})
	form := url.Values{}
	form.Set("PhoneNumbers", LocalCNPhone(req.PhoneE164))
	form.Set("SignName", req.SignName)
	form.Set("TemplateCode", req.TemplateCode)
	form.Set("TemplateParam", string(paramBytes))

	var out struct {
		RequestID string `json:"RequestId"`
		Code      string `json:"Code"`
		Message   string `json:"Message"`
		BizID     string `json:"BizId"`
	}
	if err := c.callACS3(ctx, aliyunCall{
		Endpoint:        req.Endpoint,
		AccessKeyID:     req.AccessKeyID,
		AccessKeySecret: req.AccessKeySecret,
		Action:          "SendSms",
		Version:         "2017-05-25",
		RegionID:        req.RegionID,
		Form:            form,
	}, &out); err != nil {
		return nil, err
	}
	return &SendResult{
		Success:   strings.EqualFold(out.Code, "OK"),
		RequestID: out.RequestID,
		Code:      out.Code,
		Message:   out.Message,
	}, nil
}

func (c *AliyunClient) Verify(ctx context.Context, req CaptchaVerifyRequest) (*CaptchaVerifyResult, error) {
	if req.Endpoint == "" {
		req.Endpoint = "captcha.cn-shanghai.aliyuncs.com"
	}
	if req.RegionID == "" {
		req.RegionID = "cn-shanghai"
	}
	form := url.Values{}
	form.Set("SceneId", req.SceneID)
	form.Set("CaptchaVerifyParam", req.CaptchaVerifyParam)

	var out map[string]interface{}
	if err := c.callACS3(ctx, aliyunCall{
		Endpoint:        req.Endpoint,
		AccessKeyID:     req.AccessKeyID,
		AccessKeySecret: req.AccessKeySecret,
		Action:          "VerifyIntelligentCaptcha",
		Version:         "2023-03-05",
		RegionID:        req.RegionID,
		Form:            form,
	}, &out); err != nil {
		return nil, err
	}
	return parseCaptchaVerifyResponse(out), nil
}

func parseCaptchaVerifyResponse(out map[string]interface{}) *CaptchaVerifyResult {
	success := false
	if v, ok := boolValue(out["VerifyResult"]); ok {
		success = v
	}
	verifyCode, _ := out["VerifyCode"].(string)
	if result, ok := out["Result"].(map[string]interface{}); ok {
		if v, ok := boolValue(result["VerifyResult"]); ok {
			success = v
		}
		if verifyCode == "" {
			verifyCode, _ = result["VerifyCode"].(string)
		}
	}
	if verifyCode == "" {
		verifyCode, _ = out["Code"].(string)
	}
	message, _ := out["Message"].(string)
	requestID, _ := out["RequestId"].(string)
	return &CaptchaVerifyResult{Success: success, Code: verifyCode, Message: message, RequestID: requestID}
}

func boolValue(v interface{}) (bool, bool) {
	switch typed := v.(type) {
	case bool:
		return typed, true
	case string:
		if strings.EqualFold(typed, "true") {
			return true, true
		}
		if strings.EqualFold(typed, "false") {
			return false, true
		}
	}
	return false, false
}

type aliyunCall struct {
	Endpoint        string
	AccessKeyID     string
	AccessKeySecret string
	Action          string
	Version         string
	RegionID        string
	Form            url.Values
}

func (c *AliyunClient) callACS3(ctx context.Context, call aliyunCall, out interface{}) error {
	if call.AccessKeyID == "" || call.AccessKeySecret == "" {
		return fmt.Errorf("aliyun access key is not configured")
	}
	body := call.Form.Encode()
	endpoint := strings.TrimPrefix(strings.TrimPrefix(call.Endpoint, "https://"), "http://")
	u := "https://" + endpoint + "/"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewBufferString(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Host", endpoint)
	req.Header.Set("x-acs-action", call.Action)
	req.Header.Set("x-acs-version", call.Version)
	req.Header.Set("x-acs-date", time.Now().UTC().Format(time.RFC3339))
	req.Header.Set("x-acs-signature-nonce", randomNonce())
	if call.RegionID != "" {
		req.Header.Set("x-acs-region-id", call.RegionID)
	}
	signACS3(req, body, call.AccessKeyID, call.AccessKeySecret)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("aliyun %s failed: status=%d body=%s", call.Action, resp.StatusCode, string(respBytes))
	}
	if err := json.Unmarshal(respBytes, out); err != nil {
		return fmt.Errorf("decode aliyun response: %w", err)
	}
	return nil
}

func signACS3(req *http.Request, body, accessKeyID, accessKeySecret string) {
	contentHash := sha256.Sum256([]byte(body))
	req.Header.Set("x-acs-content-sha256", hex.EncodeToString(contentHash[:]))

	canonicalHeaders, signedHeaders := canonicalACSHeaders(req.Header)
	canonicalRequest := strings.Join([]string{
		req.Method,
		"/",
		"",
		canonicalHeaders,
		signedHeaders,
		hex.EncodeToString(contentHash[:]),
	}, "\n")
	reqHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := "ACS3-HMAC-SHA256\n" + hex.EncodeToString(reqHash[:])
	mac := hmac.New(sha256.New, []byte(accessKeySecret))
	mac.Write([]byte(stringToSign))
	signature := hex.EncodeToString(mac.Sum(nil))
	req.Header.Set("Authorization", fmt.Sprintf(
		"ACS3-HMAC-SHA256 Credential=%s,SignedHeaders=%s,Signature=%s",
		accessKeyID, signedHeaders, signature,
	))
}

func canonicalACSHeaders(header http.Header) (string, string) {
	keys := make([]string, 0, len(header))
	for k := range header {
		lower := strings.ToLower(k)
		if lower == "authorization" {
			continue
		}
		if lower == "host" || strings.HasPrefix(lower, "x-acs-") {
			keys = append(keys, lower)
		}
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, k+":"+strings.TrimSpace(header.Get(k)))
	}
	return strings.Join(lines, "\n") + "\n", strings.Join(keys, ";")
}

func randomNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
