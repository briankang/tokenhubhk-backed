package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"tokenhub-server/internal/pkg/errcode"
)

// R 标准JSON响应包装结构体
type R struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// PageData 分页结果包装结构体
type PageData struct {
	List     interface{} `json:"list"`
	Total    int64       `json:"total"`
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
}

// Success 发送200成功响应
func Success(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, R{
		Code:    0,
		Message: "ok",
		Data:    data,
	})
}

// Error 发送错误响应，包含HTTP状态码和应用错误码
func Error(c *gin.Context, httpStatus int, appErr *errcode.AppError) {
	if appErr == nil {
		appErr = errcode.ErrInternal
	}
	msg := appErr.MsgKey
	// 尝试从上下文获取i18n翻译
	if translated, exists := c.Get("i18n_msg"); exists {
		if fn, ok := translated.(func(string) string); ok {
			if t := fn(appErr.MsgKey); t != "" {
				msg = t
			}
		}
	}
	c.JSON(httpStatus, R{
		Code:    appErr.Code,
		Message: msg,
	})
}

// ErrorMsg 发送带纯文本消息的错误响应
func ErrorMsg(c *gin.Context, httpStatus int, code int, msg string) {
	c.JSON(httpStatus, R{
		Code:    code,
		Message: msg,
	})
}

// PageResult 发送分页查询响应
func PageResult(c *gin.Context, list interface{}, total int64, page, pageSize int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	c.JSON(http.StatusOK, R{
		Code:    0,
		Message: "ok",
		Data: PageData{
			List:     list,
			Total:    total,
			Page:     page,
			PageSize: pageSize,
		},
	})
}

// OpenAIErrorStruct OpenAI 格式的错误响应结构体
type OpenAIErrorStruct struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param"` // 可为 null
	Code    string `json:"code"`
}

// OpenAIErrorResponse OpenAI 格式的错误响应包装
type OpenAIErrorResponse struct {
	Error OpenAIErrorStruct `json:"error"`
}

// SendOpenAIError 发送 OpenAI 格式的错误响应
// 用于 /v1/* 路由组，确保与 OpenAI API 兼容
func SendOpenAIError(c *gin.Context, httpStatus int, message, errorType, code string) {
	c.JSON(httpStatus, OpenAIErrorResponse{
		Error: OpenAIErrorStruct{
			Message: message,
			Type:    errorType,
			Param:   "null",
			Code:    code,
		},
	})
}

// SendOpenAIErrorWithParam 发送带参数的 OpenAI 格式错误响应
func SendOpenAIErrorWithParam(c *gin.Context, httpStatus int, message, errorType, param, code string) {
	c.JSON(httpStatus, OpenAIErrorResponse{
		Error: OpenAIErrorStruct{
			Message: message,
			Type:    errorType,
			Param:   param,
			Code:    code,
		},
	})
}
