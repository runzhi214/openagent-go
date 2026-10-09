package context

import (
	"context"
	"sync"
	"testing"
	"time"

	openagent "github.com/yusheng-g/openagent-go"
)

// recordingExtractor captures what the worker delivered.
type recordingExtractor struct {
	mu    sync.Mutex
	calls []extractCall
}

type extractCall struct {
	scope    ContextScope
	messages []openagent.Message
	runDelta []openagent.Message
}

func (r *recordingExtractor) Extract(_ context.Context, scope ContextScope, messages []openagent.Message, runDelta []openagent.Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, extractCall{scope: scope, messages: messages, runDelta: runDelta})
}

func (r *recordingExtractor) waitCalls(t *testing.T, n int) []extractCall {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		if len(r.calls) >= n {
			c := append([]extractCall{}, r.calls...)
			r.mu.Unlock()
			return c
		}
		r.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d extraction calls, got %d", n, len(r.calls))
	return nil
}

// TestAsyncExtractor_DeltaAccumulates: two finished runs of the same
// conversation enqueued back-to-back produce ONE worker pass whose
// runDelta is the CONCATENATION of both runs' deltas. The full message
// list is the latest submission (whole-transcript coalescing). A
// replacing coalesce would drop run 1's delta — messages lost forever
// for an incremental sync extractor.
func TestAsyncExtractor_DeltaAccumulates(t *testing.T) {
	rec := &recordingExtractor{}
	async := NewAsyncExtractor(rec)
	defer async.stop()

	scope := ContextScope{UserID: "alice", SessionID: "conv-1"}
	msgs1 := []openagent.Message{{Role: openagent.RoleUser, Content: "run1 full"}}
	delta1 := []openagent.Message{{Role: openagent.RoleUser, Content: "run1 delta"}}
	msgs2 := []openagent.Message{{Role: openagent.RoleUser, Content: "run2 full"}}
	delta2 := []openagent.Message{{Role: openagent.RoleUser, Content: "run2 delta"}}

	// Submit both before the worker drains — the coalescing window.
	async.Extract(context.Background(), scope, msgs1, delta1)
	async.Extract(context.Background(), scope, msgs2, delta2)

	calls := rec.waitCalls(t, 1)
	if len(calls) != 1 {
		t.Fatalf("worker passes = %d, want 1 (coalesced)", len(calls))
	}
	c := calls[0]
	// full list: latest wins
	if len(c.messages) != 1 || c.messages[0].Content != "run2 full" {
		t.Errorf("full messages = %v, want latest submission", c.messages)
	}
	// delta: concatenated, nothing dropped
	if len(c.runDelta) != 2 || c.runDelta[0].Content != "run1 delta" || c.runDelta[1].Content != "run2 delta" {
		t.Errorf("runDelta = %v, want [run1 delta, run2 delta] (accumulated)", c.runDelta)
	}
}

// TestAsyncExtractor_DeltaAcrossDrains: after the worker drains, a new
// submission starts a fresh accumulation — deltas do not leak across
// passes.
func TestAsyncExtractor_DeltaAcrossDrains(t *testing.T) {
	rec := &recordingExtractor{}
	async := NewAsyncExtractor(rec)
	defer async.stop()

	scope := ContextScope{UserID: "bob", SessionID: "conv-9"}
	delta1 := []openagent.Message{{Role: openagent.RoleUser, Content: "a"}}
	async.Extract(context.Background(), scope, delta1, delta1)
	calls := rec.waitCalls(t, 1)
	if len(calls[0].runDelta) != 1 || calls[0].runDelta[0].Content != "a" {
		t.Fatalf("pass 1 delta = %v, want [a]", calls[0].runDelta)
	}

	delta2 := []openagent.Message{{Role: openagent.RoleUser, Content: "b"}}
	async.Extract(context.Background(), scope, delta2, delta2)
	calls = rec.waitCalls(t, 2)
	if len(calls[1].runDelta) != 1 || calls[1].runDelta[0].Content != "b" {
		t.Fatalf("pass 2 delta = %v, want [b] only (fresh accumulation)", calls[1].runDelta)
	}
}
