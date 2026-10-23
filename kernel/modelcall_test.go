package kernel

import (
	"errors"
	"strings"
	"testing"

	openagent "github.com/yusheng-g/openagent-go"
)

// wrapRetryExhaustion decides what the run surfaces when the model-call
// retry budget is spent. A plain RetryableError keeps the "max retries
// exceeded" prefix (fail-loud diagnostics); a ReplacedError (decorator
// rewrote the message) passes through untouched — the message is already
// user-facing guidance, and retry bookkeeping would bury the fix.
func TestWrapRetryExhaustion(t *testing.T) {
	plain := &openagent.RetryableError{Err: errors.New("429 Too Many Limits")}
	got := wrapRetryExhaustion(plain)
	if !strings.Contains(got.Error(), "max retries exceeded") {
		t.Errorf("plain retryable should keep the exhaustion prefix: %q", got.Error())
	}
	if !strings.Contains(got.Error(), "429 Too Many Limits") {
		t.Errorf("plain retryable should keep the original text: %q", got.Error())
	}

	const msg = "🚦 请配置个人 provider。"
	replaced := &openagent.ReplacedError{
		Message: msg,
		Err:     &openagent.RetryableError{Err: errors.New("429 Too Many Limits")},
	}
	got = wrapRetryExhaustion(replaced)
	if got.Error() != msg {
		t.Errorf("replaced message = %q, want the replacement verbatim (no prefix)", got.Error())
	}
	if !errors.Is(got, replaced.Err) {
		t.Error("unwrap chain to the original error must survive")
	}
	var re *openagent.RetryableError
	if !errors.As(got, &re) {
		t.Error("retryable classification must survive for downstream errors.As consumers")
	}
}
