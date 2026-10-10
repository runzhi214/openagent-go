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

	ctxpkg "github.com/yusheng-g/openagent-go/context"
)

// TestRecall_EndpointAndParsing: the Memory provider calls
// /api/v1/search/recall (not /search) with the configured quotas, and
// parses entries into MemoryEntry. Content falls back to summary →
// abstract → uri when the server returns mode="uri" (no full content).
func TestRecall_EndpointAndParsing(t *testing.T) {
	var gotPath string
	var gotBody recallRequest

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/search/recall", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		resp := map[string]any{
			"status": "ok",
			"result": map[string]any{
				"entries": []map[string]any{
					{
						"uri":     "viking://user/default/memories/preferences/deploy.md",
						"score":   0.92,
						"type":    "preferences",
						"mode":    "full",
						"content": "I prefer terraform for infrastructure.",
					},
					{
						"uri":      "viking://user/default/memories/events/chat-2024.md",
						"score":    0.71,
						"type":     "events",
						"mode":     "summary",
						"summary":  "Discussed kubectl rollout restart.",
						"abstract": "kubectl rollout event",
					},
					{
						"uri":      "viking://user/default/memories/entities/port.md",
						"score":    0.55,
						"type":     "entities",
						"mode":     "uri",
						"abstract": "port 5432",
					},
				},
				"rendered": "",
				"stats":    map[string]any{},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client, err := NewClient(srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	mem := NewMemoryWithRecall(client, RecallConfig{
		Quotas:   map[string]int{"events": 8, "entities": 8, "preferences": 2, "experiences": 0},
		MaxChars: 3000,
		MinScore: 0.2,
	})

	entries, err := mem.Recall(context.Background(), ctxpkg.ContextScope{}, "terraform deploy", 5)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}

	if gotPath != "/api/v1/search/recall" {
		t.Errorf("requested %q, want /api/v1/search/recall", gotPath)
	}
	if gotBody.Query != "terraform deploy" {
		t.Errorf("query = %q, want %q", gotBody.Query, "terraform deploy")
	}
	if gotBody.MaxChars != 3000 {
		t.Errorf("max_chars = %d, want 3000", gotBody.MaxChars)
	}
	if gotBody.MinScore != 0.2 {
		t.Errorf("min_score = %v, want 0.2", gotBody.MinScore)
	}
	if gotBody.Render != false {
		t.Errorf("render = %v, want false (openagent renders itself)", gotBody.Render)
	}
	if gotBody.Quotas["preferences"] != 2 {
		t.Errorf("preferences quota = %d, want 2", gotBody.Quotas["preferences"])
	}
	if gotBody.Quotas["events"] != 8 {
		t.Errorf("events quota = %d, want 8", gotBody.Quotas["events"])
	}

	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}

	cases := []struct {
		content string
		score   float64
		kind    string
	}{
		{"I prefer terraform for infrastructure.", 0.92, "preferences"},
		{"Discussed kubectl rollout restart.", 0.71, "events"},
		{"port 5432", 0.55, "entities"},
	}
	for i, c := range cases {
		if entries[i].Content != c.content {
			t.Errorf("entry[%d] content = %q, want %q", i, entries[i].Content, c.content)
		}
		if entries[i].Score != c.score {
			t.Errorf("entry[%d] score = %v, want %v", i, entries[i].Score, c.score)
		}
		if string(entries[i].Kind) != c.kind {
			t.Errorf("entry[%d] kind = %q, want %q", i, entries[i].Kind, c.kind)
		}
	}
}

// TestRecall_DefaultQuotas: NewMemory (no explicit config) uses the
// server-default quotas so a bare endpoint produces non-empty results.
func TestRecall_DefaultQuotas(t *testing.T) {
	var gotBody recallRequest
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/search/recall", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"result": map[string]any{"entries": []any{}, "rendered": "", "stats": map[string]any{}},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client, err := NewClient(srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	mem := NewMemory(client)
	_, _ = mem.Recall(context.Background(), ctxpkg.ContextScope{}, "test", 5)

	if gotBody.Quotas["events"] != 10 {
		t.Errorf("default events quota = %d, want 10", gotBody.Quotas["events"])
	}
	if gotBody.Quotas["entities"] != 10 {
		t.Errorf("default entities quota = %d, want 10", gotBody.Quotas["entities"])
	}
	if gotBody.Quotas["preferences"] != 3 {
		t.Errorf("default preferences quota = %d, want 3", gotBody.Quotas["preferences"])
	}
	if gotBody.Quotas["experiences"] != 0 {
		t.Errorf("default experiences quota = %d, want 0", gotBody.Quotas["experiences"])
	}
	if gotBody.MaxChars != 6500 {
		t.Errorf("default max_chars = %d, want 6500", gotBody.MaxChars)
	}
	if gotBody.MinScore != 0.1 {
		t.Errorf("default min_score = %v, want 0.1", gotBody.MinScore)
	}
}

// TestRecall_EmptyEntries: an empty recall result is not an error — the
// caller (context runtime) treats nil entries as "no memories".
func TestRecall_EmptyEntries(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/search/recall", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"result": map[string]any{"entries": []any{}, "rendered": "", "stats": map[string]any{}},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client, err := NewClient(srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	mem := NewMemory(client)
	entries, err := mem.Recall(context.Background(), ctxpkg.ContextScope{}, "nothing matches", 5)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("got %d entries, want 0", len(entries))
	}
}

// TestRecall_URIFallback: when the server returns mode="uri" with no
// content/summary/abstract, the entry's content falls back to the URI so
// the caller always gets useful text.
func TestRecall_URIFallback(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/search/recall", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"result": map[string]any{
				"entries": []map[string]any{
					{
						"uri":   "viking://user/default/memories/entities/redis.md",
						"score": 0.4,
						"type":  "entities",
						"mode":  "uri",
					},
				},
				"rendered": "",
				"stats":    map[string]any{},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client, err := NewClient(srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	mem := NewMemory(client)
	entries, err := mem.Recall(context.Background(), ctxpkg.ContextScope{}, "redis", 5)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].Content != "viking://user/default/memories/entities/redis.md" {
		t.Errorf("content = %q, want URI fallback", entries[0].Content)
	}
}

// ── Session reuse + threshold commit tests ──

// sessionMockServer is a test double for the OpenViking session API.
// It records all requests and serves configurable responses. Counters
// model server-side truth: liveMsgCount persists across client
// instances (like the real server across process restarts) and resets
// on commit (keep_recent_count=0 archives everything).
type sessionMockServer struct {
	*httptest.Server

	mu              sync.Mutex
	createCount     int
	commitBodies    []map[string]any
	commitSessions  []string
	addMsgBodies    []map[string]any
	getCount        int
	pendingTokens   int    // value returned in add_message response
	getLastCommitAt string // last_commit_at returned by GET detail
	sessionExists   bool   // GET returns 200 (true) or 404 (false)
	addMsgStatus    int    // 0 = normal, 404 = simulate expired session (first call only)
	addMsgCallCount int
	liveMsgCount    int // server-reported live message count
	commitCount     int
}

func newSessionMockServer() *sessionMockServer {
	s := &sessionMockServer{
		pendingTokens: 100, // low default, below threshold
	}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			s.mu.Lock()
			s.createCount++
			var body map[string]any
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			s.mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			sid, _ := body["session_id"].(string)
			if sid == "" {
				sid = "test-session"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok",
				"result": map[string]any{"session_id": sid},
			})
		}
	})

	mux.HandleFunc("/api/v1/sessions/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		if r.Method == http.MethodGet && !strings.Contains(path, "/messages") && !strings.Contains(path, "/commit") {
			s.mu.Lock()
			s.getCount++
			exists := s.sessionExists
			pt := s.pendingTokens
			mc := s.liveMsgCount
			lca := s.getLastCommitAt
			s.mu.Unlock()

			if !exists {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status": "error",
					"error":  map[string]any{"code": "NOT_FOUND", "message": "session not found"},
				})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok",
				"result": map[string]any{
					"session_id":     "test-session",
					"pending_tokens": pt,
					"message_count":  mc,
					"last_commit_at": lca,
				},
			})
			return
		}

		if r.Method == http.MethodPost && strings.HasSuffix(path, "/messages/batch") {
			s.mu.Lock()
			s.addMsgCallCount++
			var body map[string]any
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			s.addMsgBodies = append(s.addMsgBodies, body)
			pt := s.pendingTokens
			if s.addMsgStatus == 404 && s.addMsgCallCount == 1 {
				s.mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status": "error",
					"error":  map[string]any{"code": "NOT_FOUND", "message": "session expired"},
				})
				return
			}
			// Server truth: one live message per message in the batch
			// (not per request).
			if msgs, ok := body["messages"].([]any); ok {
				s.liveMsgCount += len(msgs)
			} else {
				s.liveMsgCount++
			}
			mc := s.liveMsgCount
			s.mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok",
				"result": map[string]any{
					"session_id":     "test-session",
					"pending_tokens": pt,
					"message_count":  mc,
				},
			})
			return
		}

		if r.Method == http.MethodPost && strings.HasSuffix(path, "/commit") {
			s.mu.Lock()
			s.commitCount++
			var body map[string]any
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			s.commitBodies = append(s.commitBodies, body)
			s.commitSessions = append(s.commitSessions, path)
			s.liveMsgCount = 0 // keep_recent_count=0 archives everything
			s.mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok",
				"result": map[string]any{"task_id": "task-1", "archived": true},
			})
			return
		}
	})

	s.Server = httptest.NewServer(mux)
	return s
}

// TestStore_SessionReuse: 3 Store calls share one OV session — only 1
// create request (lazy on first call), no per-call commit.
func TestStore_SessionReuse(t *testing.T) {
	srv := newSessionMockServer()
	defer srv.Close()

	client, err := NewClientWithSession(srv.URL, "", SessionConfig{
		SessionIDSeed: "/test/config",
	})
	if err != nil {
		t.Fatal(err)
	}
	mem := NewMemory(client)

	for i := 0; i < 3; i++ {
		err := mem.Store(context.Background(), ctxpkg.ContextScope{UserID: "alice", SessionID: "conv-1"},
			ctxpkg.MemoryItem{Content: "fact " + string(rune('A'+i)), Topic: "t"})
		if err != nil {
			t.Fatalf("Store[%d]: %v", i, err)
		}
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.createCount != 1 {
		t.Errorf("create session count = %d, want 1 (session reused)", srv.createCount)
	}
	if srv.commitCount != 0 {
		t.Errorf("commit count = %d, want 0 (below threshold)", srv.commitCount)
	}
	if len(srv.addMsgBodies) != 3 {
		t.Errorf("add_message count = %d, want 3", len(srv.addMsgBodies))
	}
}

// TestStore_NoCommitBelowThreshold: pending_tokens below the token
// threshold and message count below the message threshold → no commit.
func TestStore_NoCommitBelowThreshold(t *testing.T) {
	srv := newSessionMockServer()
	defer srv.Close()
	srv.mu.Lock()
	srv.pendingTokens = 25000 // below default 30000
	srv.mu.Unlock()

	client, _ := NewClientWithSession(srv.URL, "", SessionConfig{
		SessionIDSeed: "/test/config",
		// defaults: token=30000, msg=20
	})
	mem := NewMemory(client)

	err := mem.Store(context.Background(), ctxpkg.ContextScope{SessionID: "conv-1"}, ctxpkg.MemoryItem{Content: "some fact"})
	if err != nil {
		t.Fatal(err)
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.commitCount != 0 {
		t.Errorf("commit count = %d, want 0 (below threshold)", srv.commitCount)
	}
}

// TestStore_CommitAtTokenThreshold: pending_tokens >= threshold → commit
// with keep_recent_count=0 (archive-everything buffer semantics). No
// retention fields — the WM v2 turn-retention parameters apply to user
// conversations, not knowledge buffers.
func TestStore_CommitAtTokenThreshold(t *testing.T) {
	srv := newSessionMockServer()
	defer srv.Close()
	srv.mu.Lock()
	srv.pendingTokens = 30000 // exactly at threshold
	srv.mu.Unlock()

	client, _ := NewClientWithSession(srv.URL, "", SessionConfig{
		SessionIDSeed:     "/test/config",
		MinCommitInterval: 1 * time.Millisecond, // avoid interval blocking
	})
	mem := NewMemory(client)

	err := mem.Store(context.Background(), ctxpkg.ContextScope{SessionID: "conv-1"}, ctxpkg.MemoryItem{Content: "trigger commit"})
	if err != nil {
		t.Fatal(err)
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.commitCount != 1 {
		t.Fatalf("commit count = %d, want 1", srv.commitCount)
	}
	body := srv.commitBodies[0]
	if body["keep_recent_count"] != float64(0) {
		t.Errorf("keep_recent_count = %v, want 0 (archive everything)", body["keep_recent_count"])
	}
	for _, k := range []string{"retention_mode", "keep_recent_turn_count", "retained_message_token_budget", "min_raw_tail_steps"} {
		if _, ok := body[k]; ok {
			t.Errorf("commit body must not contain %q (buffer model), got %v", k, body[k])
		}
	}
}

// TestStore_CommitAtMessageThreshold: the server-reported live message
// count (message_count in the add_message response) reaches the
// threshold even when pending_tokens is low.
func TestStore_CommitAtMessageThreshold(t *testing.T) {
	srv := newSessionMockServer()
	defer srv.Close()
	srv.mu.Lock()
	srv.pendingTokens = 100 // well below 30000
	srv.mu.Unlock()

	client, _ := NewClientWithSession(srv.URL, "", SessionConfig{
		SessionIDSeed:          "/test/config",
		CommitMessageThreshold: 3, // low for fast test
		MinCommitInterval:      1 * time.Millisecond,
	})
	mem := NewMemory(client)

	for i := 0; i < 3; i++ {
		err := mem.Store(context.Background(), ctxpkg.ContextScope{SessionID: "conv-1"},
			ctxpkg.MemoryItem{Content: "fact", Topic: "t"})
		if err != nil {
			t.Fatalf("Store[%d]: %v", i, err)
		}
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.commitCount != 1 {
		t.Errorf("commit count = %d, want 1 (message threshold)", srv.commitCount)
	}
}

// TestStore_MinInterval: a second commit within MinCommitInterval is
// suppressed even if the threshold is crossed.
func TestStore_MinInterval(t *testing.T) {
	srv := newSessionMockServer()
	defer srv.Close()
	srv.mu.Lock()
	srv.pendingTokens = 30000
	srv.mu.Unlock()

	client, _ := NewClientWithSession(srv.URL, "", SessionConfig{
		SessionIDSeed:     "/test/config",
		MinCommitInterval: 10 * time.Second, // long interval to suppress second commit
	})
	mem := NewMemory(client)

	// First store: triggers commit (pending >= 30000).
	_ = mem.Store(context.Background(), ctxpkg.ContextScope{SessionID: "conv-1"}, ctxpkg.MemoryItem{Content: "first"})

	// Second store: pending still 30000, but within interval → no commit.
	_ = mem.Store(context.Background(), ctxpkg.ContextScope{SessionID: "conv-1"}, ctxpkg.MemoryItem{Content: "second"})

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.commitCount != 1 {
		t.Errorf("commit count = %d, want 1 (second suppressed by interval)", srv.commitCount)
	}
}

// TestStore_PeerIDAndRole: add_message includes peer_id from scope.UserID
// and role="user" — OV's extraction contract sources user memories
// (profile/preferences/entities/events) from user-role content only;
// assistant-role content feeds agent-scope types the assistant
// memory_policy disables.
func TestStore_PeerIDAndRole(t *testing.T) {
	srv := newSessionMockServer()
	defer srv.Close()

	client, _ := NewClientWithSession(srv.URL, "", SessionConfig{
		SessionIDSeed: "/test/config",
	})
	mem := NewMemory(client)

	err := mem.Store(context.Background(), ctxpkg.ContextScope{UserID: "alice@example.com", SessionID: "conv-1"},
		ctxpkg.MemoryItem{Content: "prefers terraform"})
	if err != nil {
		t.Fatal(err)
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.addMsgBodies) != 1 {
		t.Fatalf("add_message count = %d, want 1", len(srv.addMsgBodies))
	}
	msgs := srv.addMsgBodies[0]["messages"].([]any)
	msg := msgs[0].(map[string]any)
	if msg["role"] != "user" {
		t.Errorf("role = %v, want user", msg["role"])
	}
	if msg["peer_id"] != "alice@example.com" {
		t.Errorf("peer_id = %v, want alice@example.com", msg["peer_id"])
	}
}

// TestStore_DefaultPeerID: an empty scope.UserID (local CLI/TUI session)
// falls back to the default peer so extraction still has a target —
// without a peer_id, user-role knowledge is silently dropped under the
// assistant memory_policy.
func TestStore_DefaultPeerID(t *testing.T) {
	srv := newSessionMockServer()
	defer srv.Close()

	client, _ := NewClientWithSession(srv.URL, "", SessionConfig{
		SessionIDSeed: "/test/config",
	})
	mem := NewMemory(client)

	err := mem.Store(context.Background(), ctxpkg.ContextScope{SessionID: "conv-1"},
		ctxpkg.MemoryItem{Content: "some local fact"})
	if err != nil {
		t.Fatal(err)
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.addMsgBodies) != 1 {
		t.Fatalf("add_message count = %d, want 1", len(srv.addMsgBodies))
	}
	msg := srv.addMsgBodies[0]["messages"].([]any)[0].(map[string]any)
	if msg["peer_id"] != defaultPeerID {
		t.Errorf("peer_id = %v, want fallback %q", msg["peer_id"], defaultPeerID)
	}
}

// TestStore_RestartSeedsCounting: a restarted client (same seed, fresh
// in-memory state) adopts the server-reported counters via GET, so
// message-count accumulation continues across process restarts instead
// of resetting. ensureSession itself must NOT commit — an active
// conversation keeps accumulating; the startup sweep owns abandoned
// sessions.
func TestStore_RestartSeedsCounting(t *testing.T) {
	srv := newSessionMockServer()
	defer srv.Close()
	srv.mu.Lock()
	srv.sessionExists = true
	srv.mu.Unlock()

	cfg := SessionConfig{
		SessionIDSeed:          "/test/config",
		CommitMessageThreshold: 3,
		MinCommitInterval:      1 * time.Millisecond,
	}

	// First process: 2 stores, below threshold → no commit. The mock
	// (like the real server) persists liveMsgCount across clients.
	c1, _ := NewClientWithSession(srv.URL, "", cfg)
	mem1 := NewMemory(c1)
	for i := 0; i < 2; i++ {
		if err := mem1.Store(context.Background(), ctxpkg.ContextScope{SessionID: "conv-1"},
			ctxpkg.MemoryItem{Content: "fact", Topic: "t"}); err != nil {
			t.Fatalf("Store[%d]: %v", i, err)
		}
	}
	srv.mu.Lock()
	if srv.commitCount != 0 {
		srv.mu.Unlock()
		t.Fatalf("pre-restart commit count = %d, want 0", srv.commitCount)
	}
	if srv.liveMsgCount != 2 {
		srv.mu.Unlock()
		t.Fatalf("server liveMsgCount = %d, want 2", srv.liveMsgCount)
	}
	srv.mu.Unlock()

	// Simulated restart: a NEW client with the same seed. ensureSession
	// GET seeds local state from the server truth (message_count=2);
	// one more write crosses the threshold → commit.
	c2, _ := NewClientWithSession(srv.URL, "", cfg)
	mem2 := NewMemory(c2)
	if err := mem2.Store(context.Background(), ctxpkg.ContextScope{SessionID: "conv-1"},
		ctxpkg.MemoryItem{Content: "third fact", Topic: "t"}); err != nil {
		t.Fatal(err)
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.commitCount != 1 {
		t.Errorf("post-restart commit count = %d, want 1 (counting continued from server truth)", srv.commitCount)
	}
}

// TestStore_SessionNotFound: GET returns 404 → POST creates a new session.
func TestStore_SessionNotFound(t *testing.T) {
	srv := newSessionMockServer()
	defer srv.Close()
	srv.mu.Lock()
	srv.sessionExists = false // 404 on GET
	srv.mu.Unlock()

	client, _ := NewClientWithSession(srv.URL, "", SessionConfig{
		SessionIDSeed: "/test/config",
	})
	ovSID := client.SessionIDFor("conv-1")

	_, err := client.AddMessage(context.Background(), "assistant", "hello", "", ovSID)
	if err != nil {
		t.Fatal(err)
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.createCount != 1 {
		t.Errorf("create session count = %d, want 1 (after 404)", srv.createCount)
	}
}

// TestStore_AddMessage404Retry: add_message returns 404 (session expired)
// → client resets sessionID, recreates, and retries successfully.
func TestStore_AddMessage404Retry(t *testing.T) {
	srv := newSessionMockServer()
	defer srv.Close()
	srv.mu.Lock()
	srv.sessionExists = true // ensureSession succeeds
	srv.addMsgStatus = 404   // first add_message returns 404
	srv.mu.Unlock()

	client, _ := NewClientWithSession(srv.URL, "", SessionConfig{
		SessionIDSeed: "/test/config",
	})
	ovSID := client.SessionIDFor("conv-1")

	_, err := client.AddMessage(context.Background(), "assistant", "retry me", "", ovSID)
	if err != nil {
		t.Fatalf("AddMessage should succeed after retry, got: %v", err)
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	// First add_message failed (404), second succeeded after session recreation.
	if srv.addMsgCallCount != 2 {
		t.Errorf("add_message call count = %d, want 2 (original + retry)", srv.addMsgCallCount)
	}
}

// TestStore_Flush: Flush forces a commit even below threshold.
func TestStore_Flush(t *testing.T) {
	srv := newSessionMockServer()
	defer srv.Close()
	srv.mu.Lock()
	srv.pendingTokens = 100 // well below 30000
	srv.mu.Unlock()

	client, _ := NewClientWithSession(srv.URL, "", SessionConfig{
		SessionIDSeed: "/test/config",
	})
	mem := NewMemory(client)
	ovSID := client.SessionIDFor("conv-1")

	// Add a message (below threshold, no commit). Uses AddMessage (not
	// Store) to isolate the Flush trigger: Store would also call
	// MaybeCommit, but since pending_tokens=100 < 30000, it's a no-op
	// anyway — AddMessage alone is sufficient and more precise.
	_, _ = mem.client.AddMessage(context.Background(), "assistant", "pending", "", ovSID)

	srv.mu.Lock()
	if srv.commitCount != 0 {
		srv.mu.Unlock()
		t.Fatalf("pre-Flush commit count = %d, want 0", srv.commitCount)
	}
	srv.mu.Unlock()

	// Flush forces commit.
	if err := client.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.commitCount != 1 {
		t.Errorf("post-Flush commit count = %d, want 1", srv.commitCount)
	}
}

// TestStore_MemoryPolicyAtCreation: POST /sessions includes memory_policy
// with self=false, peer=true (assistant role).
func TestStore_MemoryPolicyAtCreation(t *testing.T) {
	// We need to capture the create body — extend the mock inline.
	var createBody map[string]any

	// Use a custom handler to capture the create body.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions" {
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &createBody)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok",
				"result": map[string]any{"session_id": "test-session"},
			})
			return
		}
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/sessions/") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "error",
				"error":  map[string]any{"code": "NOT_FOUND", "message": "not found"},
			})
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/messages/batch") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok",
				"result": map[string]any{"pending_tokens": 100},
			})
			return
		}
	}))
	defer srv2.Close()

	client, _ := NewClientWithSession(srv2.URL, "", SessionConfig{
		SessionIDSeed: "/test/config",
	})
	ovSID := client.SessionIDFor("conv-1")

	_, err := client.AddMessage(context.Background(), "assistant", "msg", "", ovSID)
	if err != nil {
		t.Fatal(err)
	}

	if createBody == nil {
		t.Fatal("create session body not captured")
	}
	mp, ok := createBody["memory_policy"].(map[string]any)
	if !ok {
		t.Fatalf("memory_policy not in create body: %v", createBody)
	}
	self, ok := mp["self"].(map[string]any)
	if !ok || self["enabled"] != false {
		t.Errorf("memory_policy.self.enabled = %v, want false", self["enabled"])
	}
	peer, ok := mp["peer"].(map[string]any)
	if !ok || peer["enabled"] != true {
		t.Errorf("memory_policy.peer.enabled = %v, want true", peer["enabled"])
	}
}

// TestStore_PerConversationSessions: two different SessionIDs produce
// two separate OV sessions — each conversation gets its own session.
func TestStore_PerConversationSessions(t *testing.T) {
	srv := newSessionMockServer()
	defer srv.Close()

	client, _ := NewClientWithSession(srv.URL, "", SessionConfig{
		SessionIDSeed: "/test/config",
	})
	mem := NewMemory(client)

	// Conversation A
	_ = mem.Store(context.Background(), ctxpkg.ContextScope{UserID: "alice", SessionID: "conv-a"},
		ctxpkg.MemoryItem{Content: "fact A", Topic: "t"})
	// Conversation B
	_ = mem.Store(context.Background(), ctxpkg.ContextScope{UserID: "alice", SessionID: "conv-b"},
		ctxpkg.MemoryItem{Content: "fact B", Topic: "t"})

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.createCount != 2 {
		t.Errorf("create session count = %d, want 2 (one per conversation)", srv.createCount)
	}
	if len(srv.addMsgBodies) != 2 {
		t.Errorf("add_message count = %d, want 2", len(srv.addMsgBodies))
	}
}

// TestStore_LegacyFallback: SessionIDSeed empty → Remember uses legacy
// per-call create+commit (no session reuse).
func TestStore_LegacyFallback(t *testing.T) {
	srv := newSessionMockServer()
	defer srv.Close()

	// NewClient(endpoint, apiKey): second arg is the API key (empty = no
	// auth, fine for the mock server). No SessionIDSeed → legacy mode.
	client, _ := NewClient(srv.URL, "") // legacy: no SessionIDSeed

	for i := 0; i < 2; i++ {
		_, err := client.Remember(context.Background(), "legacy fact "+string(rune('A'+i)))
		if err != nil {
			t.Fatalf("Remember[%d]: %v", i, err)
		}
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	// Legacy mode: each call creates its own session + commits.
	if srv.createCount != 2 {
		t.Errorf("create session count = %d, want 2 (legacy per-call)", srv.createCount)
	}
	if srv.commitCount != 2 {
		t.Errorf("commit count = %d, want 2 (legacy per-call)", srv.commitCount)
	}
}
