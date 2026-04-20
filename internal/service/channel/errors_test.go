package channel

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// fakeNetErr 实现 net.Error 用于测试
type fakeNetErr struct {
	timeout bool
	msg     string
}

func (f *fakeNetErr) Error() string   { return f.msg }
func (f *fakeNetErr) Timeout() bool   { return f.timeout }
func (f *fakeNetErr) Temporary() bool { return false }

func TestClassifyError_Success(t *testing.T) {
	if got := ClassifyError(nil, 200); got != ErrCatSuccess {
		t.Fatalf("status=200 nil err → expected success, got %s", got)
	}
	if got := ClassifyError(nil, 299); got != ErrCatSuccess {
		t.Fatalf("status=299 nil err → expected success, got %s", got)
	}
	if got := ClassifyError(nil, 302); got != ErrCatSuccess {
		t.Fatalf("status=302 nil err → expected success, got %s", got)
	}
}

func TestClassifyError_ClientCanceled(t *testing.T) {
	if got := ClassifyError(context.Canceled, 0); got != ErrCatClientCanceled {
		t.Fatalf("context.Canceled → expected client_canceled, got %s", got)
	}
	// wrapped
	wrapped := errors.New("wrapped: " + context.Canceled.Error())
	// errors.Is won't match because we didn't use %w, but string fallback should
	if got := ClassifyError(wrapped, 0); got != ErrCatClientCanceled {
		t.Fatalf("wrapped canceled via string fallback → expected client_canceled, got %s", got)
	}
}

func TestClassifyError_Timeout(t *testing.T) {
	if got := ClassifyError(context.DeadlineExceeded, 0); got != ErrCatTimeout {
		t.Fatalf("context.DeadlineExceeded → expected timeout, got %s", got)
	}
	// net.Error with Timeout()
	netErr := &fakeNetErr{timeout: true, msg: "dial tcp: i/o timeout"}
	if got := ClassifyError(netErr, 0); got != ErrCatTimeout {
		t.Fatalf("net.Error.Timeout()=true → expected timeout, got %s", got)
	}
}

func TestClassifyError_Network(t *testing.T) {
	// net.Error without Timeout()
	netErr := &fakeNetErr{timeout: false, msg: "connect: connection refused"}
	if got := ClassifyError(netErr, 0); got != ErrCatNetwork {
		t.Fatalf("net.Error non-timeout → expected network, got %s", got)
	}
	// String fallback
	if got := ClassifyError(errors.New("dial tcp: no such host"), 0); got != ErrCatNetwork {
		t.Fatalf("no such host string → expected network, got %s", got)
	}
	if got := ClassifyError(errors.New("tls handshake failure"), 0); got != ErrCatNetwork {
		t.Fatalf("tls handshake string → expected network, got %s", got)
	}
}

func TestClassifyError_Upstream4xx(t *testing.T) {
	tests := []int{400, 401, 403, 404, 422, 429}
	for _, s := range tests {
		if got := ClassifyError(errors.New("upstream returned bad status"), s); got != ErrCatUpstream4xx {
			t.Fatalf("status=%d → expected upstream_4xx, got %s", s, got)
		}
	}
}

func TestClassifyError_Upstream5xx(t *testing.T) {
	tests := []int{500, 502, 503, 504}
	for _, s := range tests {
		if got := ClassifyError(errors.New("upstream returned bad status"), s); got != ErrCatUpstream5xx {
			t.Fatalf("status=%d → expected upstream_5xx, got %s", s, got)
		}
	}
}

func TestClassifyError_ContextWinsOverStatus(t *testing.T) {
	// 即便 status=500，context.Canceled 应优先归为客户端取消
	if got := ClassifyError(context.Canceled, 500); got != ErrCatClientCanceled {
		t.Fatalf("canceled+500 → expected client_canceled, got %s", got)
	}
}

func TestShouldCountForBreaker(t *testing.T) {
	cases := []struct {
		cat  ErrorCategory
		want bool
	}{
		{ErrCatSuccess, false},
		{ErrCatClientCanceled, false},
		{ErrCatTimeout, false},
		{ErrCatUpstream4xx, false},
		{ErrCatUpstream5xx, true},
		{ErrCatNetwork, true},
		{ErrCatUnknown, true},
	}
	for _, tc := range cases {
		if got := tc.cat.ShouldCountForBreaker(); got != tc.want {
			t.Errorf("%s.ShouldCountForBreaker() = %v, want %v", tc.cat, got, tc.want)
		}
	}
}

func TestClassifyError_Unknown(t *testing.T) {
	// 既没 err 也没 status → unknown
	if got := ClassifyError(nil, 0); got != ErrCatUnknown {
		t.Fatalf("nil+0 → expected unknown, got %s", got)
	}
	// err 无法识别 + status 也不在范围 → unknown
	if got := ClassifyError(errors.New("some weird error"), 0); got != ErrCatUnknown {
		t.Fatalf("unknown err → expected unknown, got %s", got)
	}
}

func TestClassifyError_NetOpError(t *testing.T) {
	// 更真实的网络错误：*net.OpError（通常 wrap 系统错误）
	opErr := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: errors.New("connection refused"),
	}
	got := ClassifyError(opErr, 0)
	if got != ErrCatNetwork && got != ErrCatUnknown {
		// OpError 可能通过字符串匹配也可能通过 net.Error 接口匹配
		t.Fatalf("net.OpError → expected network or unknown, got %s", got)
	}
}

// 确保分类工作在典型真实时间成本以内（微秒量级）
func TestClassifyError_Performance(t *testing.T) {
	start := time.Now()
	for i := 0; i < 10000; i++ {
		_ = ClassifyError(errors.New("i/o timeout"), 500)
	}
	dur := time.Since(start)
	if dur > time.Second {
		t.Errorf("10000 classifications took %v, too slow", dur)
	}
}
