package openviking

import (
	"context"
	"testing"

	openagent "github.com/yusheng-g/openagent-go"
	ctxpkg "github.com/yusheng-g/openagent-go/context"
)

// TestConversationSyncer_RoleFilter: only user and assistant text
// messages pass; system / tool / pure-tool-call assistant turns are
// dropped. User messages carry the peer attribution; assistant messages
// do not.
func TestConversationSyncer_RoleFilter(t *testing.T) {
	srv := newSessionMockServer()
	defer srv.Close()

	client, _ := NewClientWithSession(srv.URL, "", SessionConfig{
		SessionIDSeed: "/test/config",
	})
	s := NewConversationSyncer(client)

	delta := []openagent.Message{
		{Role: openagent.RoleUser, Content: "帮我看看这个项目的结构"},
		{Role: openagent.RoleAssistant, ToolCalls: []openagent.ToolCall{{ID: "1", Function: openagent.ToolCallFunction{Name: "ls"}}}}, // pure tool-call, no prose
		{Role: openagent.RoleTool, ToolCallID: "1", Content: "file list..."},
		{Role: openagent.RoleAssistant, Content: "项目结构如下:..."},
		{Role: openagent.RoleSystem, Content: "model stopped"},
	}
	s.Extract(context.Background(), ctxpkg.ContextScope{UserID: "alice", SessionID: "conv-1"}, nil, delta)

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.addMsgBodies) != 1 {
		t.Fatalf("batch requests = %d, want 1 (single request for the whole delta)", len(srv.addMsgBodies))
	}
	msgs := srv.addMsgBodies[0]["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages in batch = %d, want 2 (user + assistant text only)", len(msgs))
	}
	m0 := msgs[0].(map[string]any)
	if m0["role"] != "user" || m0["peer_id"] != "alice" {
		t.Errorf("msg[0] = %v, want user role with peer_id=alice", m0)
	}
	m1 := msgs[1].(map[string]any)
	if m1["role"] != "assistant" {
		t.Errorf("msg[1] role = %v, want assistant", m1["role"])
	}
	if _, has := m1["peer_id"]; has {
		t.Errorf("assistant message must not carry peer_id, got %v", m1["peer_id"])
	}
}

// TestConversationSyncer_DefaultPeerID: empty scope.UserID falls back
// to the default peer so user-role content still has an extraction
// target.
func TestConversationSyncer_DefaultPeerID(t *testing.T) {
	srv := newSessionMockServer()
	defer srv.Close()

	client, _ := NewClientWithSession(srv.URL, "", SessionConfig{
		SessionIDSeed: "/test/config",
	})
	s := NewConversationSyncer(client)

	delta := []openagent.Message{{Role: openagent.RoleUser, Content: "用户偏好简洁方案。"}}
	s.Extract(context.Background(), ctxpkg.ContextScope{SessionID: "conv-1"}, nil, delta)

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.addMsgBodies) != 1 {
		t.Fatalf("batch requests = %d, want 1", len(srv.addMsgBodies))
	}
	msg := srv.addMsgBodies[0]["messages"].([]any)[0].(map[string]any)
	if msg["peer_id"] != defaultPeerID {
		t.Errorf("peer_id = %v, want fallback %q", msg["peer_id"], defaultPeerID)
	}
}

// TestConversationSyncer_EmptyDeltaNoop: no delta (or nothing textual
// in it) → no requests at all.
func TestConversationSyncer_EmptyDeltaNoop(t *testing.T) {
	srv := newSessionMockServer()
	defer srv.Close()

	client, _ := NewClientWithSession(srv.URL, "", SessionConfig{
		SessionIDSeed: "/test/config",
	})
	s := NewConversationSyncer(client)
	scope := ctxpkg.ContextScope{SessionID: "conv-1"}

	s.Extract(context.Background(), scope, nil, nil)
	s.Extract(context.Background(), scope, nil, []openagent.Message{
		{Role: openagent.RoleTool, Content: "tool output only"},
	})

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.addMsgBodies) != 0 || srv.createCount != 0 {
		t.Errorf("requests fired (add=%d create=%d), want none", len(srv.addMsgBodies), srv.createCount)
	}
}

// TestConversationSyncer_CommitCheck: after a successful sync the
// commit policy runs (threshold-crossing sessions commit immediately;
// the mock's liveMsgCount grows per request, so a low
// CommitMessageThreshold fires within the same Extract).
func TestConversationSyncer_CommitCheck(t *testing.T) {
	srv := newSessionMockServer()
	defer srv.Close()

	client, _ := NewClientWithSession(srv.URL, "", SessionConfig{
		SessionIDSeed:          "/test/config",
		CommitMessageThreshold: 2,
		MinCommitInterval:      1,
	})
	s := NewConversationSyncer(client)
	scope := ctxpkg.ContextScope{SessionID: "conv-1"}

	s.Extract(context.Background(), scope, nil, []openagent.Message{
		{Role: openagent.RoleUser, Content: "第一句"},
		{Role: openagent.RoleAssistant, Content: "第一个回答"},
	})

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.commitCount != 1 {
		t.Errorf("commit count = %d, want 1 (message threshold 2 crossed by one sync)", srv.commitCount)
	}
}
