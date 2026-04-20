package channel

import (
	"context"
	"errors"
	"net"
	"strings"
)

// ErrorCategory 错误类别 —— 用于区分不同来源的失败，决定是否计入熔断器/如何展示到监控页
type ErrorCategory string

const (
	// ErrCatClientCanceled 客户端主动取消（context.Canceled）
	//   - 与供应商健康无关，不应计入熔断器失败
	ErrCatClientCanceled ErrorCategory = "client_canceled"

	// ErrCatTimeout 超时（context.DeadlineExceeded / net.Error.Timeout()）
	//   - 可能是上游响应慢，但也可能是网络抖动；单次不应触发熔断
	ErrCatTimeout ErrorCategory = "timeout"

	// ErrCatUpstream4xx 上游返回 4xx —— 调用方参数错误 / 认证错误 / 限流
	//   - 4xx 不代表供应商宕机（大多是业务逻辑错误），不计入熔断器失败
	ErrCatUpstream4xx ErrorCategory = "upstream_4xx"

	// ErrCatUpstream5xx 上游返回 5xx —— 真实的供应商服务端错误
	//   - 应计入熔断器失败
	ErrCatUpstream5xx ErrorCategory = "upstream_5xx"

	// ErrCatNetwork 网络层错误（连接拒绝 / DNS 解析失败 / TLS 握手失败等）
	//   - 供应商侧不可达，应计入熔断器失败
	ErrCatNetwork ErrorCategory = "network"

	// ErrCatSuccess 成功（不是错误，仅用于统一分类接口）
	ErrCatSuccess ErrorCategory = "success"

	// ErrCatUnknown 无法归类 —— 保守起见视为失败参与熔断
	ErrCatUnknown ErrorCategory = "unknown"
)

// ClassifyError 根据 err 和上游 HTTP status 推断错误类别
//
// 调用规则：
//   - err==nil && upstreamStatus<400 → ErrCatSuccess
//   - err 或 status 其一存在即按优先级分类
//
// 优先级：context 错误 > net.Error > HTTP status > 字符串 fallback > unknown
func ClassifyError(err error, upstreamStatus int) ErrorCategory {
	// 1. 明确成功
	if err == nil && upstreamStatus >= 200 && upstreamStatus < 400 {
		return ErrCatSuccess
	}
	if err == nil && upstreamStatus == 0 {
		// 既没 err 也没 status，视作未知（通常不该发生）
		return ErrCatUnknown
	}

	// 2. context 错误（客户端取消 / 超时）最优先
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return ErrCatClientCanceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return ErrCatTimeout
		}

		// 3. 网络层错误
		var netErr net.Error
		if errors.As(err, &netErr) {
			if netErr.Timeout() {
				return ErrCatTimeout
			}
			return ErrCatNetwork
		}
	}

	// 4. HTTP status
	if upstreamStatus >= 400 && upstreamStatus < 500 {
		return ErrCatUpstream4xx
	}
	if upstreamStatus >= 500 {
		return ErrCatUpstream5xx
	}

	// 5. 字符串 fallback —— 覆盖未实现 net.Error 接口的常见错误
	if err != nil {
		msg := strings.ToLower(err.Error())
		switch {
		case strings.Contains(msg, "context canceled"),
			strings.Contains(msg, "request canceled"):
			return ErrCatClientCanceled
		case strings.Contains(msg, "deadline exceeded"),
			strings.Contains(msg, "timeout"):
			return ErrCatTimeout
		case strings.Contains(msg, "connection refused"),
			strings.Contains(msg, "no such host"),
			strings.Contains(msg, "network is unreachable"),
			strings.Contains(msg, "connection reset"),
			strings.Contains(msg, "broken pipe"),
			strings.Contains(msg, "i/o timeout"),
			strings.Contains(msg, "tls handshake"):
			return ErrCatNetwork
		}
	}

	return ErrCatUnknown
}

// ShouldCountForBreaker 该错误类别是否应被熔断器计入失败数
//
// 规则（对齐业内主流做法）：
//   - client_canceled: false —— 客户端自己取消的，跟供应商无关
//   - timeout:         false —— 单次超时不足以判定供应商故障；多次超时会有其他探针兜底
//   - upstream_4xx:    false —— 调用方错误（参数/鉴权），不是供应商宕机
//   - upstream_5xx:    true  —— 真实服务端错误
//   - network:         true  —— 连接层不可达，供应商侧问题
//   - unknown:         true  —— 保守起见计入，避免漏报
//   - success:         false —— 成功无需计失败
func (c ErrorCategory) ShouldCountForBreaker() bool {
	switch c {
	case ErrCatSuccess,
		ErrCatClientCanceled,
		ErrCatTimeout,
		ErrCatUpstream4xx:
		return false
	}
	return true
}

// IsSuccess 是否是成功
func (c ErrorCategory) IsSuccess() bool {
	return c == ErrCatSuccess
}

// String 返回字符串表示（便于日志/监控）
func (c ErrorCategory) String() string {
	if c == "" {
		return string(ErrCatUnknown)
	}
	return string(c)
}
