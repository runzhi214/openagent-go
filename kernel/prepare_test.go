package kernel

import (
	"strings"
	"testing"

	openagent "github.com/yusheng-g/openagent-go"
	ctxpkg "github.com/yusheng-g/openagent-go/context"
)

func TestEnsureValidWorkingSet_UserStartUnchanged(t *testing.T) {
	// Working set already starts with a user — returned unchanged.
	msgs := []openagent.Message{
		{Role: openagent.RoleUser, Content: "hello"},
		{Role: openagent.RoleAssistant, Content: "hi"},
	}
	got := ensureValidWorkingSet(msgs)
	if len(got) != 2 {
		t.Fatalf("expected 2 msgs (unchanged), got %d", len(got))
	}
}

func TestEnsureValidWorkingSet_EmptyInjectsUser(t *testing.T) {
	// Empty working set — inject a synthetic <system-reminder> user.
	got := ensureValidWorkingSet(nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 (injected user), got %d", len(got))
	}
	if got[0].Role != openagent.RoleUser {
		t.Fatalf("expected RoleUser, got %s", got[0].Role)
	}
	if !strings.Contains(got[0].Content, "<system-reminder>") {
		t.Fatalf("expected <system-reminder> in content, got: %s", got[0].Content)
	}
	if !got[0].Transient {
		t.Fatalf("expected Transient=true (not persisted)")
	}
}

func TestEnsureValidWorkingSet_AssistantStartPrependsUser(t *testing.T) {
	// Working set starts with assistant tool_calls — prepend transient user
	// so TrimOrphanToolCalls' head-stripping does not delete the pairs.
	msgs := []openagent.Message{
		{Role: openagent.RoleAssistant, ToolCalls: []openagent.ToolCall{{ID: "t1"}}},
		{Role: openagent.RoleTool, ToolCallID: "t1"},
	}
	got := ensureValidWorkingSet(msgs)
	if len(got) != 3 {
		t.Fatalf("expected 3 (prepended user + 2 originals), got %d", len(got))
	}
	if got[0].Role != openagent.RoleUser {
		t.Fatalf("expected first msg RoleUser, got %s", got[0].Role)
	}
	if !got[0].Transient {
		t.Fatalf("expected prepended user Transient=true")
	}
	if got[1].Role != openagent.RoleAssistant {
		t.Fatalf("expected second msg RoleAssistant, got %s", got[1].Role)
	}
	if got[2].Role != openagent.RoleTool {
		t.Fatalf("expected third msg RoleTool, got %s", got[2].Role)
	}
}

func TestEnsureValidWorkingSet_ToolStartPrependsUser(t *testing.T) {
	// Working set starts with an orphan tool message — prepend user.
	msgs := []openagent.Message{
		{Role: openagent.RoleTool, ToolCallID: "orphan", Content: "stale"},
	}
	got := ensureValidWorkingSet(msgs)
	if len(got) != 2 {
		t.Fatalf("expected 2 (prepended user + 1 original), got %d", len(got))
	}
	if got[0].Role != openagent.RoleUser {
		t.Fatalf("expected first msg RoleUser, got %s", got[0].Role)
	}
}

func TestEnsureThenTrim_PreservesCompletePairs(t *testing.T) {
	// The critical integration: ensureValidWorkingSet runs BEFORE
	// TrimOrphanToolCalls. A working set of complete assistant→tool pairs
	// (no user) must survive both steps intact — the prepended user
	// prevents head-stripping from deleting the pairs.
	msgs := []openagent.Message{
		{Role: openagent.RoleAssistant, ToolCalls: []openagent.ToolCall{{ID: "t1"}}},
		{Role: openagent.RoleTool, ToolCallID: "t1", Content: "result1"},
		{Role: openagent.RoleAssistant, ToolCalls: []openagent.ToolCall{{ID: "t2"}}},
		{Role: openagent.RoleTool, ToolCallID: "t2", Content: "result2"},
	}
	ensured := ensureValidWorkingSet(msgs)
	trimmed := ctxpkg.TrimOrphanToolCalls(ensured)

	// Expected: [user(transient), assistant(tc), tool, assistant(tc), tool]
	if len(trimmed) != 5 {
		t.Fatalf("expected 5 msgs (user + 2 complete pairs), got %d: %+v", len(trimmed), trimmed)
	}
	if trimmed[0].Role != openagent.RoleUser {
		t.Fatalf("expected first msg RoleUser, got %s", trimmed[0].Role)
	}
	if trimmed[1].Role != openagent.RoleAssistant || len(trimmed[1].ToolCalls) == 0 {
		t.Fatalf("expected second msg assistant with tool_calls, got %+v", trimmed[1])
	}
	if trimmed[2].Role != openagent.RoleTool {
		t.Fatalf("expected third msg RoleTool, got %s", trimmed[2].Role)
	}
}

func TestEnsureThenTrim_DropsIncompleteTrailingPair(t *testing.T) {
	// A trailing incomplete assistant→tool pair (no tool result) is still
	// dropped by TrimOrphanToolCalls — ensureValidWorkingSet only prevents
	// head-stripping, not body cleanup.
	msgs := []openagent.Message{
		{Role: openagent.RoleAssistant, ToolCalls: []openagent.ToolCall{{ID: "t1"}}},
		{Role: openagent.RoleTool, ToolCallID: "t1", Content: "result1"},
		{Role: openagent.RoleAssistant, ToolCalls: []openagent.ToolCall{{ID: "t2"}}},
		// no tool result for t2
	}
	ensured := ensureValidWorkingSet(msgs)
	trimmed := ctxpkg.TrimOrphanToolCalls(ensured)

	// Expected: [user(transient), assistant(tc), tool] — t2 group dropped
	if len(trimmed) != 3 {
		t.Fatalf("expected 3 msgs (user + 1 complete pair, trailing incomplete dropped), got %d: %+v", len(trimmed), trimmed)
	}
}
