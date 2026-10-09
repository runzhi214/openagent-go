package openviking

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
)

// TestMaybeCommit_IntervalSeededFromServer: the MinCommitInterval
// gate uses the last_commit_at reported by GET /sessions/{id}, so a
// restarted process stays throttled instead of double-committing
// within the interval. A commit older than the interval passes.
func TestMaybeCommit_IntervalSeededFromServer(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name       string
		lastCommit string // last_commit_at served by GET detail
		wantCommit bool
	}{
		{
			name:       "recent commit suppressed",
			lastCommit: now.Format(time.RFC3339),
			wantCommit: false,
		},
		{
			name:       "stale commit passes",
			lastCommit: now.Add(-6 * time.Minute).Format(time.RFC3339),
			wantCommit: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newSessionMockServer()
			defer srv.Close()
			srv.mu.Lock()
			srv.sessionExists = true
			srv.pendingTokens = 30000 // token threshold crossed
			srv.getLastCommitAt = tc.lastCommit
			srv.mu.Unlock()

			client, _ := NewClientWithSession(srv.URL, "", SessionConfig{
				SessionIDSeed: "/test/config",
				// default MinCommitInterval: 5m
			})
			ovSID := client.SessionIDFor("conv-1")

			_, err := client.AddMessage(context.Background(), "assistant", "msg", "", ovSID)
			if err != nil {
				t.Fatal(err)
			}
			// Store's MaybeCommit ran inside AddMessage? No — AddMessage
			// does not commit; call the policy check explicitly, as
			// Memory.Store does.
			if err := client.MaybeCommit(context.Background(), ovSID); err != nil {
				t.Fatal(err)
			}

			srv.mu.Lock()
			defer srv.mu.Unlock()
			got := srv.commitCount > 0
			if got != tc.wantCommit {
				t.Errorf("commit fired = %v, want %v (commitCount=%d)", got, tc.wantCommit, srv.commitCount)
			}
		})
	}
}

// TestAbandoned: the sweep predicate — messages present AND idle past
// the grace period. Unreadable timestamps count as stale.
func TestAbandoned(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name string
		d    sessionDetail
		want bool
	}{
		{"no messages", sessionDetail{MessageCount: 0, LastMessageAt: now.Add(-time.Hour).Format(time.RFC3339)}, false},
		{"recent activity", sessionDetail{MessageCount: 5, LastMessageAt: now.Add(-time.Minute).Format(time.RFC3339)}, false},
		{"idle past grace", sessionDetail{MessageCount: 5, LastMessageAt: now.Add(-11 * time.Minute).Format(time.RFC3339)}, true},
		{"unreadable timestamp", sessionDetail{MessageCount: 5, LastMessageAt: ""}, true},
		{"garbage timestamp", sessionDetail{MessageCount: 5, LastMessageAt: "not-a-time"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := abandoned(tc.d); got != tc.want {
				t.Errorf("abandoned(%+v) = %v, want %v", tc.d, got, tc.want)
			}
		})
	}
}

// sweepMockServer models the session listing + detail + commit surface
// for the recovery sweep, with independent per-session state.
type sweepMockServer struct {
	*httptest.Server

	mu       sync.Mutex
	sessions map[string]*sweepSession
	gets     map[string]int // session_id → GET detail count
	commits  []string       // committed session_ids, in order
}

type sweepSession struct {
	messageCount  int
	lastCommitAt  string
	lastMessageAt string
}

func newSweepMockServer(sessions map[string]*sweepSession) *sweepMockServer {
	s := &sweepMockServer{
		sessions: sessions,
		gets:     make(map[string]int),
	}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		listing := make([]map[string]any, 0, len(s.sessions))
		for sid := range s.sessions {
			listing = append(listing, map[string]any{"session_id": sid})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "result": listing})
	})

	mux.HandleFunc("/api/v1/sessions/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		const prefix = "/api/v1/sessions/"
		isCommit := r.Method == http.MethodPost && strings.HasSuffix(path, "/commit")
		var sid string
		if isCommit {
			sid = strings.TrimSuffix(strings.TrimPrefix(path, prefix), "/commit")
		} else {
			sid = strings.TrimPrefix(path, prefix)
		}
		if sid == "" || strings.Contains(sid, "/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		sess, ok := s.sessions[sid]

		if r.Method == http.MethodGet {
			s.gets[sid]++
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok",
				"result": map[string]any{
					"session_id":      sid,
					"message_count":   sess.messageCount,
					"last_commit_at":  sess.lastCommitAt,
					"last_message_at": sess.lastMessageAt,
				},
			})
			return
		}

		if isCommit {
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			s.commits = append(s.commits, sid)
			// keep_recent_count=0 archives everything
			sess.messageCount = 0
			sess.lastCommitAt = time.Now().UTC().Format(time.RFC3339)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok",
				"result": map[string]any{"session_id": sid, "task_id": "task-" + sid, "archived": true},
			})
			return
		}

		w.WriteHeader(http.StatusMethodNotAllowed)
	})

	s.Server = httptest.NewServer(mux)
	return s
}

// TestRecoverUncommitted_Sweep: the startup sweep commits ONLY
// openagent-prefixed sessions that hold messages and have been idle
// past the grace period. Foreign-prefixed sessions are not even
// fetched (prefix filter before the detail round trip). A second run
// is a no-op — the sweep is idempotent.
func TestRecoverUncommitted_Sweep(t *testing.T) {
	old := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	srv := newSweepMockServer(map[string]*sweepSession{
		"openagent-stale":  {messageCount: 5, lastMessageAt: old},
		"openagent-active": {messageCount: 3, lastMessageAt: recent},
		"openagent-empty":  {messageCount: 0, lastMessageAt: old},
		"oc-ses-foreign":   {messageCount: 9, lastMessageAt: old},
	})
	defer srv.Close()

	client, _ := NewClientWithSession(srv.URL, "", SessionConfig{
		SessionIDSeed: "/test/config",
	})
	if err := client.RecoverUncommitted(context.Background()); err != nil {
		t.Fatal(err)
	}

	srv.mu.Lock()
	if len(srv.commits) != 1 || srv.commits[0] != "openagent-stale" {
		srv.mu.Unlock()
		t.Fatalf("commits = %v, want exactly [openagent-stale]", srv.commits)
	}
	if srv.gets["oc-ses-foreign"] != 0 {
		srv.mu.Unlock()
		t.Errorf("foreign session was GET'd %d times, want 0 (prefix-filtered before fetch)", srv.gets["oc-ses-foreign"])
	}
	if srv.gets["openagent-empty"] == 0 {
		srv.mu.Unlock()
		t.Error("empty session should still be fetched (only the predicate skips it)")
	}
	srv.mu.Unlock()

	// Idempotence: the committed session now reports message_count 0,
	// so a second sweep finds nothing to do.
	if err := client.RecoverUncommitted(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.commits) != 1 {
		t.Errorf("second sweep: commits = %v, want unchanged single commit", srv.commits)
	}
}

// TestRecoverUncommitted_LegacyClient: no SessionIDSeed (legacy mode)
// → the sweep is a no-op, no requests at all.
func TestRecoverUncommitted_LegacyClient(t *testing.T) {
	srv := newSweepMockServer(map[string]*sweepSession{
		"openagent-stale": {messageCount: 5, lastMessageAt: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)},
	})
	defer srv.Close()

	client, _ := NewClient(srv.URL, "")
	if err := client.RecoverUncommitted(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.commits) != 0 {
		t.Errorf("commits = %v, want none in legacy mode", srv.commits)
	}
}

// TestCommitResponse_TaskIDLoggedShape: doCommit parses the commit
// response's task_id and archived fields (the extraction task the
// operator tracks in the OV task view).
func TestCommitResponse_TaskIDLoggedShape(t *testing.T) {
	var gotBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sessions/openagent-x/commit", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"result": map[string]any{
				"session_id": "openagent-x",
				"task_id":    "task-42",
				"archived":   true,
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client, _ := NewClientWithSession(srv.URL, "", SessionConfig{SessionIDSeed: "/test/config"})
	taskID, archived, err := client.doCommit(context.Background(), "openagent-x")
	if err != nil {
		t.Fatal(err)
	}
	if taskID != "task-42" {
		t.Errorf("task_id = %q, want task-42", taskID)
	}
	if !archived {
		t.Error("archived = false, want true")
	}
	if gotBody["keep_recent_count"] != float64(0) {
		t.Errorf("keep_recent_count = %v, want 0", gotBody["keep_recent_count"])
	}
	if len(gotBody) != 1 {
		t.Errorf("commit body has extra fields: %v", gotBody)
	}
}
