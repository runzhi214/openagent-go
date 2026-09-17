package kernel

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	openagent "github.com/yusheng-g/openagent-go"
	"github.com/yusheng-g/openagent-go/agent"
	ctxpkg "github.com/yusheng-g/openagent-go/context"
	"github.com/yusheng-g/openagent-go/tokenizer"
)

// TestPromptBreakdown_Layers verifies that buildPrompt populates the
// per-layer token breakdown: system, dynamic, and summary layers are
// set by buildPrompt; working/tool/total/window are set by run().
func TestPromptBreakdown_LayersAfterBuildPrompt(t *testing.T) {
	model := &fakeModelWithToolCall{toolName: "noop", toolArgs: "{}", callID: "c1"}
	cfg := agent.New("test",
		agent.WithModel(model),
		agent.WithMaxTurns(1),
		agent.WithSystemPrompts("You are a helpful assistant."),
	)
	rt := New(cfg, Deps{})

	session := openagent.Session{
		ID:             "pb-test",
		ModelID:        "test-model",
		ProjectContext: "Some project rules.",
	}
	ac := &ctxpkg.AgentContext{
		Messages: []openagent.Message{openagent.UserMessage("hello")},
	}

	_, err := rt.buildPrompt(context.Background(), session, ac)
	if err != nil {
		t.Fatalf("buildPrompt: %v", err)
	}

	pb := rt.PromptBreakdown()
	if pb == nil {
		t.Fatal("PromptBreakdown() = nil after buildPrompt")
	}

	if pb.SystemTokens <= 0 {
		t.Errorf("SystemTokens = %d, want > 0", pb.SystemTokens)
	}
	if pb.DynamicTokens <= 0 {
		t.Errorf("DynamicTokens = %d, want > 0", pb.DynamicTokens)
	}
	if pb.SummaryTokens <= 0 {
		t.Errorf("SummaryTokens = %d, want > 0", pb.SummaryTokens)
	}

	modelID := openagent.TokenizerModelID(model)
	wantStatic := "You are a helpful assistant.\n\n## Project Context\n\nSome project rules."
	wantSystem := tokenizer.Count(modelID, wantStatic) + 4
	if pb.SystemTokens != wantSystem {
		t.Errorf("SystemTokens = %d, want %d", pb.SystemTokens, wantSystem)
	}

	wantSummary := tokenizer.Count(modelID, "## Conversation Summary\n\n(no prior conversation history)") + 4
	if pb.SummaryTokens != wantSummary {
		t.Errorf("SummaryTokens = %d, want %d", pb.SummaryTokens, wantSummary)
	}

	if pb.WorkingTokens != 0 || pb.ToolTokens != 0 || pb.TotalTokens != 0 {
		t.Errorf("working/tool/total should be 0 before run(), got working=%d tool=%d total=%d",
			pb.WorkingTokens, pb.ToolTokens, pb.TotalTokens)
	}
}

// TestPromptBreakdown_CompletedAfterRun verifies that run() fills in
// the remaining layers: working, tool, total, and context window.
func TestPromptBreakdown_CompletedAfterRun(t *testing.T) {
	model := &fakeModelWithToolCall{toolName: "noop", toolArgs: "{}", callID: "c1"}
	tool := &nonStreamingTool{}
	cfg := agent.New("test",
		agent.WithModel(model),
		agent.WithMaxTurns(1),
		agent.WithSystemPrompts("System prompt."),
	)
	rt := New(cfg, Deps{Tools: []openagent.Tool{tool}})

	session := openagent.Session{ID: "pb-run-test", ModelID: "test-model"}
	_, err := rt.Run(context.Background(), session, openagent.UserMessage("go"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	pb := rt.PromptBreakdown()
	if pb == nil {
		t.Fatal("PromptBreakdown() = nil after Run")
	}

	if pb.WorkingTokens <= 0 {
		t.Errorf("WorkingTokens = %d, want > 0", pb.WorkingTokens)
	}
	if pb.ToolTokens <= 0 {
		t.Errorf("ToolTokens = %d, want > 0 (tool definitions present)", pb.ToolTokens)
	}
	if pb.TotalTokens <= 0 {
		t.Errorf("TotalTokens = %d, want > 0", pb.TotalTokens)
	}
	if pb.ContextWindow != 128_000 {
		t.Errorf("ContextWindow = %d, want 128000", pb.ContextWindow)
	}
	if pb.SystemTokens <= 0 || pb.DynamicTokens <= 0 || pb.SummaryTokens <= 0 {
		t.Errorf("system/dynamic/summary should be set by buildPrompt: sys=%d dyn=%d sum=%d",
			pb.SystemTokens, pb.DynamicTokens, pb.SummaryTokens)
	}
}

// TestPromptBreakdown_NilBeforeFirstBuild verifies the default state.
func TestPromptBreakdown_NilBeforeFirstBuild(t *testing.T) {
	cfg := agent.New("test", agent.WithMaxTurns(1))
	rt := New(cfg, Deps{})
	if pb := rt.PromptBreakdown(); pb != nil {
		t.Fatalf("PromptBreakdown() = %v, want nil before first buildPrompt", pb)
	}
}

// TestPromptBreakdown_JSONSerializable verifies the struct round-trips
// through JSON — required by the WASM host export runtime_context_usage.
func TestPromptBreakdown_JSONSerializable(t *testing.T) {
	pb := &PromptBreakdown{
		SystemTokens:  100,
		DynamicTokens: 200,
		SummaryTokens: 50,
		WorkingTokens: 300,
		ToolTokens:    75,
		TotalTokens:   725,
		ContextWindow: 128000,
	}
	b, err := json.Marshal(pb)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got PromptBreakdown
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got != *pb {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, pb)
	}
	if !strings.Contains(string(b), "system_tokens") {
		t.Errorf("JSON missing system_tokens key: %s", b)
	}
}
