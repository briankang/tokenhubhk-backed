package v1

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"tokenhub-server/internal/provider"
)

type responsesRequest struct {
	Model           string      `json:"model"`
	Input           interface{} `json:"input"`
	Instructions    string      `json:"instructions,omitempty"`
	MaxOutputTokens int         `json:"max_output_tokens,omitempty"`
	Temperature     *float64    `json:"temperature,omitempty"`
	TopP            *float64    `json:"top_p,omitempty"`
	Stream          bool        `json:"stream,omitempty"`
}

type ResponsesHandler struct {
	*CompletionsHandler
}

func NewResponsesHandler(comp *CompletionsHandler) *ResponsesHandler {
	return &ResponsesHandler{CompletionsHandler: comp}
}

func (h *ResponsesHandler) Register(rg *gin.RouterGroup) {
	rg.POST("/responses", h.Responses)
}

func (h *ResponsesHandler) Responses(c *gin.Context) {
	var req responsesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "Invalid request: " + err.Error(), "type": "invalid_request_error"}})
		return
	}
	if req.Model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "field 'model' is required", "type": "invalid_request_error"}})
		return
	}
	text := responsesInputText(req.Input)
	if strings.TrimSpace(text) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "field 'input' is required", "type": "invalid_request_error"}})
		return
	}

	messages := make([]provider.Message, 0, 2)
	if strings.TrimSpace(req.Instructions) != "" {
		messages = append(messages, provider.Message{Role: "system", Content: req.Instructions})
	}
	messages = append(messages, provider.Message{Role: "user", Content: text})

	chatReq := chatCompletionRequest{
		Model:       req.Model,
		Messages:    messages,
		MaxTokens:   req.MaxOutputTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stream:      req.Stream,
	}
	body, _ := json.Marshal(chatReq)
	c.Request.Body = ioNopCloser{Reader: bytes.NewReader(body)}
	c.Request.ContentLength = int64(len(body))
	h.ChatCompletions(c)
}

func responsesInputText(input interface{}) string {
	switch v := input.(type) {
	case string:
		return v
	case []interface{}:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				if s, ok := m["content"].(string); ok {
					parts = append(parts, s)
				} else if s, ok := m["text"].(string); ok {
					parts = append(parts, s)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

type ioNopCloser struct {
	*bytes.Reader
}

func (n ioNopCloser) Close() error { return nil }
