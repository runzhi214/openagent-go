package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	openaisdk "github.com/openai/openai-go/v3"
	openagent "github.com/yusheng-g/openagent-go"
)

// fakeAPIErr builds an *openaisdk.Error the way the SDK would after a real
// HTTP exchange — Request/Response populated so Error() is printable.
func fakeAPIErr(status int, retryAfter string) *openaisdk.Error {
	resp := &http.Response{
		StatusCode: status,
		Header:     http.Header{},
	}
	if retryAfter != "" {
		resp.Header.Set("Retry-After", retryAfter)
	}
	return &openaisdk.Error{
		Code:       "too_many_limits",
		Message:    "Too Many Limits",
		StatusCode: status,
		Request:    &http.Request{Method: "POST", URL: &url.URL{Scheme: "http", Host: "tokenhub.example", Path: "/chat/completions"}},
		Response:   resp,
	}
}

// TestToRetryableError_429MessageReplaced: a decorated 429 surfaces the
// decorator's text as the entire message; the original 429 text is gone
// from display but the RetryableError classification and Retry-After
// survive in the unwrap chain (the kernel's retry loop depends on them).
func TestToRetryableError_429MessageReplaced(t *testing.T) {
	const msg = "🚦 服务繁忙：请按文档配置个人 provider（https://example.com/tokenhub）。"
	var calls int32
	m := New("key", "glm-5.3", "http://tokenhub.example/v1").
		WithAPIErrorDecorator(func(openagent.APIErrorInfo) string {
			atomic.AddInt32(&calls, 1)
			return msg
		})

	err := m.toRetryableError(fakeAPIErr(429, "3"))
	if err == nil {
		t.Fatal("expected error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("decorator calls = %d, want 1", got)
	}
	if err.Error() != msg {
		t.Errorf("message = %q, want the replacement verbatim", err.Error())
	}
	if strings.Contains(err.Error(), "429") {
		t.Errorf("original provider text must be replaced, not shown: %q", err.Error())
	}

	var rep *openagent.ReplacedError
	if !errors.As(err, &rep) {
		t.Fatalf("replaced error must be *ReplacedError, got %T", err)
	}
	var re *openagent.RetryableError
	if !errors.As(err, &re) {
		t.Fatalf("RetryableError must survive the replacement (unwrap chain), got %T", err)
	}
	if re.RetryAfter != 3*time.Second {
		t.Errorf("RetryAfter = %v, want 3s (replacement must not clobber it)", re.RetryAfter)
	}
}

// TestToRetryableError_ReplacementOnNonRetryableAPIError: the decorator
// is consulted for every status-coded API error, not only retryable ones —
// the plugin decides which statuses it cares about. A replaced 401 stays
// non-retryable.
func TestToRetryableError_ReplacementOnNonRetryableAPIError(t *testing.T) {
	m := New("key", "m", "http://localhost:1").
		WithAPIErrorDecorator(func(openagent.APIErrorInfo) string { return "请检查 API key" })

	err := m.toRetryableError(fakeAPIErr(401, ""))
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "请检查 API key" {
		t.Errorf("401 message = %q, want the replacement", err.Error())
	}
	var re *openagent.RetryableError
	if errors.As(err, &re) {
		t.Errorf("401 must stay non-retryable, got *RetryableError")
	}
}

// TestToRetryableError_EmptyReturnPassthrough: a decorator that returns ""
// keeps the original message — "" means "no replacement".
func TestToRetryableError_EmptyReturnPassthrough(t *testing.T) {
	m := New("key", "m", "http://localhost:1").
		WithAPIErrorDecorator(func(openagent.APIErrorInfo) string { return "" })

	err := m.toRetryableError(fakeAPIErr(429, ""))
	var rep *openagent.ReplacedError
	if errors.As(err, &rep) {
		t.Errorf("empty replacement must not wrap in ReplacedError")
	}
	var re *openagent.RetryableError
	if !errors.As(err, &re) {
		t.Fatalf("still retryable, got %T", err)
	}
	if !strings.Contains(err.Error(), "429 Too Many Requests") {
		t.Errorf("original message must be kept: %q", err.Error())
	}
}

// TestToRetryableError_NonAPIErrorNotConsulted: transport-layer failures
// (no HTTP status) never reach the decorator — the contract covers API
// errors only.
func TestToRetryableError_NonAPIErrorNotConsulted(t *testing.T) {
	var calls int32
	m := New("key", "m", "http://localhost:1").
		WithAPIErrorDecorator(func(openagent.APIErrorInfo) string {
			atomic.AddInt32(&calls, 1)
			return "must not appear"
		})

	err := m.toRetryableError(errors.New("dial tcp: connection refused"))
	if atomic.LoadInt32(&calls) != 0 {
		t.Errorf("decorator must not be consulted for transport errors, calls = %d", calls)
	}
	if strings.Contains(err.Error(), "must not appear") {
		t.Errorf("transport error must not be replaced: %v", err)
	}
	if !strings.Contains(err.Error(), "chat completion: ") {
		t.Errorf("non-API errors keep the chat completion wrap: %v", err)
	}
}

// TestToRetryableError_NilDecoratorUnchanged: no decorator configured —
// behavior identical to the pre-decoration wrap.
func TestToRetryableError_NilDecoratorUnchanged(t *testing.T) {
	m := New("key", "m", "http://localhost:1")

	err := m.toRetryableError(fakeAPIErr(429, "7"))
	var re *openagent.RetryableError
	if !errors.As(err, &re) {
		t.Fatalf("retryable, got %T", err)
	}
	if re.RetryAfter != 7*time.Second {
		t.Errorf("RetryAfter = %v, want 7s", re.RetryAfter)
	}
	var rep *openagent.ReplacedError
	if errors.As(err, &rep) {
		t.Errorf("no decorator, no ReplacedError expected")
	}
}

// TestToRetryableError_DecoratorSeesErrorContext: the decorator receives
// the status, base URL, and model of the failing call.
func TestToRetryableError_DecoratorSeesErrorContext(t *testing.T) {
	var got openagent.APIErrorInfo
	m := New("key", "glm-5.3", "http://tokenhub.example/v1").
		WithAPIErrorDecorator(func(info openagent.APIErrorInfo) string {
			got = info
			return ""
		})

	m.toRetryableError(fakeAPIErr(429, ""))
	if got.StatusCode != 429 {
		t.Errorf("StatusCode = %d, want 429", got.StatusCode)
	}
	if got.BaseURL != "http://tokenhub.example/v1" {
		t.Errorf("BaseURL = %q, want the model's base URL", got.BaseURL)
	}
	if got.ModelID != "glm-5.3" {
		t.Errorf("ModelID = %q, want glm-5.3", got.ModelID)
	}
	if got.Err == nil {
		t.Error("Err must carry the underlying error")
	}
}

// TestChatCompletionStream_429MessageReplaced: end-to-end through the real
// openai-go SDK — an HTTP 429 from the wire surfaces with the replacement
// as the whole message, retryable, Retry-After intact.
func TestChatCompletionStream_429MessageReplaced(t *testing.T) {
	const msg = "🚦 触发限流：请配置个人 provider（见 https://example.com）。"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"code":"too_many_limits","message":"Too Many Limits"}}`))
	}))
	defer srv.Close()

	m := New("key", "glm-5.3", srv.URL).
		WithAPIErrorDecorator(func(openagent.APIErrorInfo) string { return msg })

	reader, err := m.ChatCompletionStream(context.Background(), openagent.ChatCompletionRequest{Model: "glm-5.3"})
	if err == nil && reader != nil {
		reader.Close()
		t.Fatal("expected the 429 to surface as the stream error")
	}
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != msg {
		t.Errorf("stream 429 message = %q, want the replacement verbatim", err.Error())
	}
	var re *openagent.RetryableError
	if !errors.As(err, &re) {
		t.Fatalf("stream 429 must be retryable, got %T", err)
	}
	if re.RetryAfter != 2*time.Second {
		t.Errorf("RetryAfter = %v, want 2s", re.RetryAfter)
	}
}

// TestChatCompletionStream_SuccessNoDecoration: the happy path never
// consults the decorator — decoration is error-path only, zero overhead
// on successful streams.
func TestChatCompletionStream_SuccessNoDecoration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	var calls int32
	m := New("key", "glm-5.3", srv.URL).
		WithAPIErrorDecorator(func(openagent.APIErrorInfo) string {
			atomic.AddInt32(&calls, 1)
			return "must not appear"
		})

	reader, err := m.ChatCompletionStream(context.Background(), openagent.ChatCompletionRequest{Model: "glm-5.3"})
	if err != nil {
		t.Fatalf("ChatCompletionStream: %v", err)
	}
	defer reader.Close()
	for reader.Next() {
	}
	if rerr := reader.Err(); rerr != nil {
		t.Fatalf("reader.Err: %v", rerr)
	}
	if calls != 0 {
		t.Errorf("decorator consulted on success %d times, want 0", calls)
	}
}
