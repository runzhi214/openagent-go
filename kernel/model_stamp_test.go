package kernel

import (
	"context"
	"strings"
	"sync"
	"testing"

	openagent "github.com/yusheng-g/openagent-go"
	"github.com/yusheng-g/openagent-go/agent"
)

// stopModel always answers with a plain stop — a single-turn run needs no
// tools. Two instances are distinguishable by identity, which is exactly what
// the model-stamp assertions compare.
type stopModel struct{}

func (m *stopModel) ChatCompletion(_ context.Context, _ openagent.ChatCompletionRequest) (*openagent.ChatCompletionResponse, error) {
	return &openagent.ChatCompletionResponse{
		Choices: []openagent.Choice{{
			Message:      openagent.Message{Role: openagent.RoleAssistant, Content: "done"},
			FinishReason: "stop",
		}},
	}, nil
}

func (m *stopModel) ChatCompletionStream(_ context.Context, _ openagent.ChatCompletionRequest) (openagent.StreamReader, error) {
	return nil, nil // fallback to non-streaming
}

func (m *stopModel) ContextWindow() int { return 128_000 }

// bigMessage returns a prompt large enough to overflow the working budget's
// 500-token floor (prepare.go), forcing turn-0 auto-compaction.
func bigMessage(t *testing.T) string {
	t.Helper()
	return strings.Repeat("overflow ", 1000) // ~9000 chars ≈ 2000+ tokens
}

// captureSummarizer records the ctx each Summarize call received so tests
// can assert which model the kernel stamped into it.
type captureSummarizer struct {
	mu   sync.Mutex
	seen []context.Context
}

func (c *captureSummarizer) Summarize(ctx context.Context, _ []openagent.Message, _ *openagent.CompressedContext) (*openagent.CompressedContext, error) {
	c.mu.Lock()
	c.seen = append(c.seen, ctx)
	c.mu.Unlock()
	return &openagent.CompressedContext{Summary: "captured summary"}, nil
}

func (c *captureSummarizer) stampedModels() []openagent.Model {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]openagent.Model, len(c.seen))
	for i, ctx := range c.seen {
		m, _ := openagent.ModelFromContext(ctx)
		out[i] = m
	}
	return out
}

// TestRun_AutoCompactionSeesSessionModel pins the mid-session-switch fix:
// run() stamps its resolved model into ctx, and auto-compaction (turn-0
// prepareMemory → Compressor.Compact → Summarizer.Summarize) receives that
// ctx — so the summarizer resolves the session's CURRENT model (the
// session.Model override the ACP layer sets per prompt), not a stale
// server default. A session without the override stamps the agent model.
//
// The prompt is oversized on purpose: the working budget has a 500-token
// floor (prepare.go), so a tiny budget alone cannot force compaction.
func TestRun_AutoCompactionSeesSessionModel(t *testing.T) {
	sum := &captureSummarizer{}
	store := newMemSessionStore(sum)
	cfgModel := &stopModel{}
	sessionModel := &stopModel{}
	cfg := agent.New("test",
		agent.WithModel(cfgModel),
		agent.WithMaxTurns(1),
		agent.WithMaxWorkingTokens(1),
	)
	deps := Deps{SessionStore: store, Compressor: store}
	rt := New(cfg, deps)

	// Seed an oversized history message, then prompt small: the tail-retain
	// scan needs a retainable tail, so a lone overflowing message never
	// triggers compaction (overflow == len(msgs) is a no-op).
	_ = store.Append(context.Background(), "auto-s", openagent.UserMessage(bigMessage(t)))
	for evt := range rt.RunStream(context.Background(), openagent.Session{ID: "auto-s", Model: sessionModel}, openagent.UserMessage("go")) {
		if evt.Type == openagent.StreamError {
			t.Fatalf("unexpected run error: %v", evt.Error)
		}
	}

	ms := sum.stampedModels()
	if len(ms) == 0 {
		t.Fatal("auto-compaction did not run; summarizer saw no ctx")
	}
	for i, m := range ms {
		if m != openagent.Model(sessionModel) {
			t.Errorf("summarize[%d] resolved model %p, want the session model %p (session.Model override must win over the agent default)", i, m, sessionModel)
		}
	}
}

// TestRun_AutoCompactionSeesAgentModel: without a session.Model override the
// stamp is the agent's configured model.
func TestRun_AutoCompactionSeesAgentModel(t *testing.T) {
	sum := &captureSummarizer{}
	store := newMemSessionStore(sum)
	cfgModel := &stopModel{}
	cfg := agent.New("test",
		agent.WithModel(cfgModel),
		agent.WithMaxTurns(1),
		agent.WithMaxWorkingTokens(1),
	)
	rt := New(cfg, Deps{SessionStore: store, Compressor: store})

	// Same tail-retain setup as the session-override test above.
	_ = store.Append(context.Background(), "auto-s2", openagent.UserMessage(bigMessage(t)))
	for evt := range rt.RunStream(context.Background(), openagent.Session{ID: "auto-s2"}, openagent.UserMessage("go")) {
		if evt.Type == openagent.StreamError {
			t.Fatalf("unexpected run error: %v", evt.Error)
		}
	}

	ms := sum.stampedModels()
	if len(ms) == 0 {
		t.Fatal("auto-compaction did not run; summarizer saw no ctx")
	}
	for i, m := range ms {
		if m != openagent.Model(cfgModel) {
			t.Errorf("summarize[%d] resolved model %p, want the agent model %p", i, m, cfgModel)
		}
	}
}

// TestCompressAll_ModelStampCallerWins: the ACP /compact callback stamps the
// session's current model before calling CompressAll (it may have switched
// since the last prompt). That stamp must win over the runtime's last-run
// model — otherwise compaction sticks to a stale model after a switch.
func TestCompressAll_ModelStampCallerWins(t *testing.T) {
	store := &memStore{}
	for i := 0; i < 4; i++ {
		_ = store.Append(context.Background(), "caller-s", openagent.UserMessage("msg"))
	}
	comp := &fakeCompressor{}
	runModel := &stopModel{}
	rt := New(agent.New("test", agent.WithModel(runModel), agent.WithMaxTurns(1)),
		Deps{SessionStore: store, Compressor: comp})

	// One run so runModel is resolved (CompressAll's fallback source).
	for evt := range rt.RunStream(context.Background(), openagent.Session{ID: "caller-run"}, openagent.UserMessage("hi")) {
		if evt.Type == openagent.StreamError {
			t.Fatalf("unexpected run error: %v", evt.Error)
		}
	}

	callerModel := &stopModel{}
	if _, err := rt.CompressAll(openagent.WithModel(context.Background(), callerModel), "caller-s"); err != nil {
		t.Fatalf("CompressAll: %v", err)
	}
	if got := comp.lastModel(); got != openagent.Model(callerModel) {
		t.Errorf("compact model = %p, want the caller-stamped model %p (caller stamp must win over last-run model)", got, callerModel)
	}
}

// TestCompressAll_ModelStampFallsBackToLastRun: an unstamped ctx (tests,
// non-ACP callers) falls back to the runtime's last-run model.
func TestCompressAll_ModelStampFallsBackToLastRun(t *testing.T) {
	store := &memStore{}
	for i := 0; i < 4; i++ {
		_ = store.Append(context.Background(), "fallback-s", openagent.UserMessage("msg"))
	}
	comp := &fakeCompressor{}
	runModel := &stopModel{}
	rt := New(agent.New("test", agent.WithModel(runModel), agent.WithMaxTurns(1)),
		Deps{SessionStore: store, Compressor: comp})

	for evt := range rt.RunStream(context.Background(), openagent.Session{ID: "fallback-run"}, openagent.UserMessage("hi")) {
		if evt.Type == openagent.StreamError {
			t.Fatalf("unexpected run error: %v", evt.Error)
		}
	}

	if _, err := rt.CompressAll(context.Background(), "fallback-s"); err != nil {
		t.Fatalf("CompressAll: %v", err)
	}
	if got := comp.lastModel(); got != openagent.Model(runModel) {
		t.Errorf("compact model = %p, want the last-run model %p (fallback for unstamped callers)", got, runModel)
	}
}
