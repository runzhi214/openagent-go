package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	openagent "github.com/yusheng-g/openagent-go"
	"github.com/yusheng-g/openagent-go/cmd/cli/config"
	ctxpkg "github.com/yusheng-g/openagent-go/context"
	"github.com/yusheng-g/openagent-go/kernel"
	openviking "github.com/yusheng-g/openagent-go/provider/openviking"
)

// ovSyncMock is a minimal OpenViking double counting messages/batch
// requests, for behaviorally distinguishing sync-mode wiring.
type ovSyncMock struct {
	*httptest.Server

	mu      sync.Mutex
	batches int
}

func newOVSyncMock() *ovSyncMock {
	m := &ovSyncMock{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"result": map[string]any{"session_id": "s"},
		})
	})
	mux.HandleFunc("/api/v1/sessions/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if r.Method == http.MethodPost && strings.HasSuffix(path, "/messages/batch") {
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			n := 0
			if msgs, ok := body["messages"].([]any); ok {
				n = len(msgs)
			}
			m.mu.Lock()
			m.batches++
			m.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok",
				"result": map[string]any{"session_id": "s", "pending_tokens": 10, "message_count": n},
			})
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(path, "/commit") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok",
				"result": map[string]any{"task_id": "t", "archived": true},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"result": map[string]any{"message_count": 0, "pending_tokens": 0, "last_commit_at": ""},
		})
	})
	m.Server = httptest.NewServer(mux)
	return m
}

func (m *ovSyncMock) batchCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.batches
}

// waitBatches polls until the async extractor's worker has produced n
// batch requests (or times out).
func (m *ovSyncMock) waitBatches(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if m.batchCount() >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d messages/batch requests, got %d", n, m.batchCount())
}

// fakeMemoryProvider satisfies ctxpkg.MemoryProvider for wiring tests.
type fakeMemoryProvider struct{}

func (fakeMemoryProvider) Recall(_ context.Context, _ ctxpkg.ContextScope, _ string, _ int) ([]ctxpkg.MemoryEntry, error) {
	return nil, nil
}

func (fakeMemoryProvider) Store(_ context.Context, _ ctxpkg.ContextScope, _ ctxpkg.MemoryItem) error {
	return nil
}

// TestBuildExtractor_SyncMode: sync mode with memory on OpenViking
// builds a ConversationSyncer — Extract forwards the run delta to the
// OV session endpoint with NO model configured (nil modelFn). Sync is
// also the DEFAULT: an unset extraction_mode behaves identically
// (covered by TestBuildExtractor_Selection).
func TestBuildExtractor_SyncMode(t *testing.T) {
	mock := newOVSyncMock()
	defer mock.Close()

	cfg := &config.Config{
		OpenViking: config.OpenVikingConfig{
			Endpoint:       mock.URL,
			ExtractionMode: "sync",
		},
	}
	ovClient, cleanup, err := applyContextProviders(cfg, &kernel.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	e := buildExtractor(cfg, ovClient, fakeMemoryProvider{}, nil)
	if e == nil {
		t.Fatal("sync mode must build an extractor without a model")
	}
	e.Extract(context.Background(), ctxpkg.ContextScope{UserID: "u1", SessionID: "c1"}, nil,
		[]openagent.Message{{Role: openagent.RoleUser, Content: "hello"}})
	mock.waitBatches(t, 1)
}

// TestBuildExtractor_Selection: the mode/backend matrix. Sync-mode +
// OpenViking-served memory + no builtin override forwards deltas over
// HTTP — with sync as the default that includes an UNSET mode and,
// notably, requires no model. An explicit "distill" takes the LLM path
// (no OV HTTP on Extract) or builds nothing without a model.
func TestBuildExtractor_Selection(t *testing.T) {
	modelFn := func() openagent.Model { return nil }
	delta := []openagent.Message{{Role: openagent.RoleUser, Content: "x"}}

	cases := []struct {
		name     string
		mode     string
		builtin  string // context_providers.memory override
		modelFn  func() openagent.Model
		provider ctxpkg.MemoryProvider
		wantNil  bool
		wantHTTP bool
	}{
		{name: "explicit sync", mode: "sync", provider: fakeMemoryProvider{}, wantHTTP: true},
		{name: "unset defaults to sync", provider: fakeMemoryProvider{}, wantHTTP: true},
		{name: "unset without model", provider: fakeMemoryProvider{}, modelFn: nil, wantHTTP: true},
		{name: "explicit distill", mode: "distill", provider: fakeMemoryProvider{}, modelFn: modelFn},
		{name: "distill without model", mode: "distill", provider: fakeMemoryProvider{}, modelFn: nil, wantNil: true},
		{name: "sync but builtin memory", mode: "sync", builtin: "builtin", provider: fakeMemoryProvider{}, modelFn: modelFn},
		{name: "no provider", mode: "sync", modelFn: modelFn, wantNil: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := newOVSyncMock()
			defer mock.Close()

			cfg := &config.Config{
				ContextProviders: config.ContextProviderConfig{Memory: tc.builtin},
				OpenViking: config.OpenVikingConfig{
					Endpoint:       mock.URL,
					ExtractionMode: tc.mode,
				},
			}
			ovClient, err := openviking.NewClientWithSession(mock.URL, "", openviking.SessionConfig{SessionIDSeed: "/t"})
			if err != nil {
				t.Fatal(err)
			}
			e := buildExtractor(cfg, ovClient, tc.provider, tc.modelFn)
			if tc.wantNil {
				if e != nil {
					t.Errorf("buildExtractor = %v, want nil", e)
				}
				return
			}
			if e == nil {
				t.Fatal("buildExtractor = nil, want non-nil")
			}
			e.Extract(context.Background(), ctxpkg.ContextScope{UserID: "u1", SessionID: "c1"}, nil, delta)
			if tc.wantHTTP {
				mock.waitBatches(t, 1)
			} else if got := mock.batchCount(); got != 0 {
				t.Errorf("messages/batch requests = %d, want 0 (no OV HTTP on this path)", got)
			}
		})
	}
}
