package acp

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yusheng-g/openagent-go/agent"
	"github.com/yusheng-g/openagent-go/kernel"
)

// newTestServer creates an AgentServer with no models — the state that
// triggers lazy model reload.
func newTestServer() *AgentServer {
	return NewAgentServer(agent.New("test"), kernel.Deps{}, nil, nil)
}

// TestTryReloadModels_NoFn verifies that tryReloadModels is a no-op
// (returns false) when no reload callback is configured.
func TestTryReloadModels_NoFn(t *testing.T) {
	s := newTestServer()
	if s.tryReloadModels(context.Background()) {
		t.Error("tryReloadModels returned true with no reload fn configured")
	}
}

// TestTryReloadModels_Succeeds verifies that a successful reload returns
// true and registers models so subsequent calls skip via the fast path.
func TestTryReloadModels_Succeeds(t *testing.T) {
	s := newTestServer()
	var callCount int32
	s.SetModelReloadFn(func(ctx context.Context) bool {
		atomic.AddInt32(&callCount, 1)
		// Simulate registration so hasModels() returns true on re-check.
		s.Models["test/model"] = nil
		return true
	})

	if !s.tryReloadModels(context.Background()) {
		t.Error("tryReloadModels returned false on success")
	}
	if callCount != 1 {
		t.Errorf("callback called %d times, want 1", callCount)
	}

	// Second call must not re-invoke — hasModels fast path.
	if !s.tryReloadModels(context.Background()) {
		t.Error("tryReloadModels returned false after success")
	}
	if callCount != 1 {
		t.Errorf("callback called %d times after second call, want 1", callCount)
	}
}

// TestTryReloadModels_FailThenSucceed verifies that a failed reload does
// not permanently block — the next call retries and can succeed.
func TestTryReloadModels_FailThenSucceed(t *testing.T) {
	s := newTestServer()
	var callCount int32
	s.SetModelReloadFn(func(ctx context.Context) bool {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			return false // first call fails
		}
		s.Models["test/model"] = nil
		return true // second call succeeds
	})

	// First call: fails, returns false.
	if s.tryReloadModels(context.Background()) {
		t.Error("first call returned true, want false")
	}
	if callCount != 1 {
		t.Errorf("callback called %d times, want 1", callCount)
	}

	// Second call: retries (no permanent state), succeeds.
	if !s.tryReloadModels(context.Background()) {
		t.Error("second call returned false, want true")
	}
	if callCount != 2 {
		t.Errorf("callback called %d times, want 2", callCount)
	}

	// Third call: fast path, no callback.
	if !s.tryReloadModels(context.Background()) {
		t.Error("third call returned false, want true")
	}
	if callCount != 2 {
		t.Errorf("callback called %d times, want 2", callCount)
	}
}

// TestTryReloadModels_PanicRecovery verifies that a panic in the reload
// callback is recovered and treated as a failure, not crashing the server.
func TestTryReloadModels_PanicRecovery(t *testing.T) {
	s := newTestServer()
	var callCount int32
	s.SetModelReloadFn(func(ctx context.Context) bool {
		atomic.AddInt32(&callCount, 1)
		panic("simulated plugin bug")
	})

	if s.tryReloadModels(context.Background()) {
		t.Error("tryReloadModels returned true despite panic")
	}
	if callCount != 1 {
		t.Errorf("callback called %d times, want 1", callCount)
	}

	// Should be able to call again (no permanent state).
	if s.tryReloadModels(context.Background()) {
		t.Error("tryReloadModels returned true despite panic (2nd call)")
	}
	if callCount != 2 {
		t.Errorf("callback called %d times, want 2", callCount)
	}
}

// TestTryReloadModels_ConcurrentSerializes verifies that concurrent calls
// to tryReloadModels are serialized: only one goroutine runs the callback,
// others see the registry already has models and return without calling.
func TestTryReloadModels_ConcurrentSerializes(t *testing.T) {
	s := newTestServer()
	var callCount int32
	s.SetModelReloadFn(func(ctx context.Context) bool {
		atomic.AddInt32(&callCount, 1)
		s.Models["test/model"] = nil
		return true
	})

	const n = 5
	var wg sync.WaitGroup
	wg.Add(n)
	results := make([]bool, n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx] = s.tryReloadModels(context.Background())
		}(i)
	}
	wg.Wait()

	for i, r := range results {
		if !r {
			t.Errorf("goroutine %d: result = false, want true", i)
		}
	}
	// Callback should be called at most once — concurrent callers either
	// wait for the lock then see hasModels, or are the one that runs.
	if callCount > 1 {
		t.Errorf("callback called %d times, want at most 1 (serialized)", callCount)
	}
}
