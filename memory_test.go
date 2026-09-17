package openagent

import "testing"

func TestSafeCompressionBoundary_AssistantToolPair(t *testing.T) {
	// Boundary lands on an assistant-with-tool_calls: extend to include
	// the trailing tool results.
	msgs := []Message{
		{Role: RoleUser, Content: "u1"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "t1"}}},
		{Role: RoleTool, ToolCallID: "t1"},
		{Role: RoleUser, Content: "u2"},
	}
	// overflow=2 → extend past tool result → 3 (no user forward-scan)
	got := SafeCompressionBoundary(msgs, 2)
	if got != 3 {
		t.Fatalf("expected 3, got %d", got)
	}
}

func TestSafeCompressionBoundary_NoForwardScanToNextUser(t *testing.T) {
	// overflow lands between two users, on an assistant. Previously the
	// forward-scan moved overflow to the next user (4); now it stays put
	// — the user-first invariant is handled by ensureValidWorkingSet.
	msgs := []Message{
		{Role: RoleUser, Content: "u1"},
		{Role: RoleAssistant, Content: "a1"},
		{Role: RoleAssistant, Content: "a2"},
		{Role: RoleAssistant, Content: "a3"}, // overflow points here
		{Role: RoleUser, Content: "u2"},      // no longer scanned to
		{Role: RoleAssistant, Content: "a4"},
	}
	got := SafeCompressionBoundary(msgs, 3)
	if got != 3 {
		t.Fatalf("expected 3 (no user forward-scan), got %d", got)
	}
}

func TestSafeCompressionBoundary_NoUserKeepsOverflow(t *testing.T) {
	// No user message after overflow — previously compressed everything
	// (len(all)); now keeps overflow as-is. The working set may start with
	// assistant/tool; ensureValidWorkingSet handles the user-first invariant.
	msgs := []Message{
		{Role: RoleUser, Content: "u1"},
		{Role: RoleAssistant, Content: "a1"},
		{Role: RoleAssistant, Content: "a2"},
	}
	// overflow=2 → stays 2 (previously pushed to 3)
	got := SafeCompressionBoundary(msgs, 2)
	if got != 2 {
		t.Fatalf("expected 2 (no full compression), got %d", got)
	}
}

func TestSafeCompressionBoundary_SingleUserKeepsRetention(t *testing.T) {
	// The autonomous-task regression: one user + long assistant→tool chain.
	// Previously the forward-scan found no more users → compress all →
	// working set empty. Now overflow stays at the tool-pair boundary,
	// retaining the recent messages. ensureValidWorkingSet handles the
	// user-first invariant in the prompt assembly stage.
	msgs := []Message{
		{Role: RoleUser, Content: "the only user"},               // 0
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "t1"}}}, // 1
		{Role: RoleTool, ToolCallID: "t1"},                       // 2
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "t2"}}}, // 3
		{Role: RoleTool, ToolCallID: "t2"},                       // 4
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "t3"}}}, // 5
		{Role: RoleTool, ToolCallID: "t3"},                       // 6
	}
	// overflow=4: lastCompressed=all[3] (assistant+tool_calls) → extend
	// past tool result at [4] → overflow=5. No user forward-scan —
	// previously this would have pushed to len(all)=7.
	got := SafeCompressionBoundary(msgs, 4)
	if got != 5 {
		t.Fatalf("expected 5 (tool-pair extension only, no user scan), got %d", got)
	}
}

func TestSafeCompressionBoundary_UserAlreadyAtOverflow(t *testing.T) {
	// A user message is already at the overflow position — no change.
	msgs := []Message{
		{Role: RoleUser, Content: "u1"},
		{Role: RoleAssistant, Content: "a1"},
		{Role: RoleUser, Content: "u2"},
		{Role: RoleAssistant, Content: "a2"},
	}
	got := SafeCompressionBoundary(msgs, 2)
	if got != 2 {
		t.Fatalf("expected 2 (user already there), got %d", got)
	}
}
