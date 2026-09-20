package rest

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yusheng-g/openagent-go/agent"
	"github.com/yusheng-g/openagent-go/kernel"
)

// newTestHandler creates a Handler with no models — the state that
// triggers lazy model reload.
func newTestHandler() *Handler {
	agentCfg := agent.New("test", agent.WithMaxTurns(1))
	return NewHandler(agentCfg, kernel.Deps{})
}

// TestHandler_TryReloadModels_NoFn verifies that tryReloadModels is a
// no-op (returns false) when no reload callback is configured.
func TestHandler_TryReloadModels_NoFn(t *testing.T) {
	h := newTestHandler()
	if h.tryReloadModels(context.Background()) {
		t.Error("tryReloadModels returned true with no reload fn configured")
	}
}

// TestHandler_TryReloadModels_Succeeds verifies that a successful reload
// returns true and subsequent calls skip via the fast path.
func TestHandler_TryReloadModels_Succeeds(t *testing.T) {
	h := newTestHandler()
	var callCount int32
	h.SetModelReloadFn(func(ctx context.Context) bool {
		atomic.AddInt32(&callCount, 1)
		// Simulate registration so hasModels() returns true on re-check.
		h.models["test/model"] = nil
		return true
	})

	if !h.tryReloadModels(context.Background()) {
		t.Error("tryReloadModels returned false on success")
	}
	if callCount != 1 {
		t.Errorf("callback called %d times, want 1", callCount)
	}

	if !h.tryReloadModels(context.Background()) {
		t.Error("tryReloadModels returned false after success")
	}
	if callCount != 1 {
		t.Errorf("callback called %d times after second call, want 1", callCount)
	}
}

// TestHandler_TryReloadModels_FailThenSucceed verifies that a failed reload
// does not permanently block — the next call retries and can succeed.
func TestHandler_TryReloadModels_FailThenSucceed(t *testing.T) {
	h := newTestHandler()
	var callCount int32
	h.SetModelReloadFn(func(ctx context.Context) bool {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			return false
		}
		h.models["test/model"] = nil
		return true
	})

	if h.tryReloadModels(context.Background()) {
		t.Error("first call returned true, want false")
	}
	if callCount != 1 {
		t.Errorf("callback called %d times, want 1", callCount)
	}

	if !h.tryReloadModels(context.Background()) {
		t.Error("second call returned false, want true")
	}
	if callCount != 2 {
		t.Errorf("callback called %d times, want 2", callCount)
	}

	if !h.tryReloadModels(context.Background()) {
		t.Error("third call returned false, want true")
	}
	if callCount != 2 {
		t.Errorf("callback called %d times, want 2", callCount)
	}
}

// TestHandler_TryReloadModels_PanicRecovery verifies that a panic in the
// reload callback is recovered and treated as a failure.
func TestHandler_TryReloadModels_PanicRecovery(t *testing.T) {
	h := newTestHandler()
	var callCount int32
	h.SetModelReloadFn(func(ctx context.Context) bool {
		atomic.AddInt32(&callCount, 1)
		panic("simulated plugin bug")
	})

	if h.tryReloadModels(context.Background()) {
		t.Error("tryReloadModels returned true despite panic")
	}
	if callCount != 1 {
		t.Errorf("callback called %d times, want 1", callCount)
	}

	if h.tryReloadModels(context.Background()) {
		t.Error("tryReloadModels returned true despite panic (2nd call)")
	}
	if callCount != 2 {
		t.Errorf("callback called %d times, want 2", callCount)
	}
}

// TestHandler_TryReloadModels_ConcurrentSerializes verifies that concurrent
// calls to tryReloadModels are serialized.
func TestHandler_TryReloadModels_ConcurrentSerializes(t *testing.T) {
	h := newTestHandler()
	var callCount int32
	h.SetModelReloadFn(func(ctx context.Context) bool {
		atomic.AddInt32(&callCount, 1)
		h.models["test/model"] = nil
		return true
	})

	const n = 5
	var wg sync.WaitGroup
	wg.Add(n)
	results := make([]bool, n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx] = h.tryReloadModels(context.Background())
		}(i)
	}
	wg.Wait()

	for i, r := range results {
		if !r {
			t.Errorf("goroutine %d: result = false, want true", i)
		}
	}
	if callCount > 1 {
		t.Errorf("callback called %d times, want at most 1 (serialized)", callCount)
	}
}
