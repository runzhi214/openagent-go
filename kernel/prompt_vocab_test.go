package kernel

import (
	"context"
	"testing"

	openagent "github.com/yusheng-g/openagent-go"
	"github.com/yusheng-g/openagent-go/agent"
)

// vocabFakeModel declares a restricted reasoning-effort vocabulary, like
// the real openai.Model does for glm-5.3-flash (low/high/max).
type vocabFakeModel struct {
	vocab []string
}

func (m *vocabFakeModel) ChatCompletion(ctx context.Context, req openagent.ChatCompletionRequest) (*openagent.ChatCompletionResponse, error) {
	return &openagent.ChatCompletionResponse{}, nil
}

func (m *vocabFakeModel) ChatCompletionStream(ctx context.Context, req openagent.ChatCompletionRequest) (openagent.StreamReader, error) {
	return nil, nil
}

func (m *vocabFakeModel) ContextWindow() int { return 128_000 }

func (m *vocabFakeModel) ReasoningEfforts() []string { return m.vocab }

// TestBuildModelRequestEffortVocabulary: an effort outside the model's
// vocabulary (saved on another model, or the pre-constraint medium
// default) must never reach the wire — it falls back to the model's
// default effort. In-vocabulary efforts and unrestricted models pass
// through unchanged.
func TestBuildModelRequestEffortVocabulary(t *testing.T) {
	build := func(model openagent.Model, effort string) string {
		a := agent.New("test", agent.WithModel(model))
		a.ReasoningEffort = effort
		rt := New(a, Deps{})
		req := rt.buildModelRequest(openagent.Session{ModelID: "m"}, nil)
		return req.ReasoningEffort
	}

	flash := &vocabFakeModel{vocab: []string{"low", "high", "max"}}
	if got := build(flash, "medium"); got != "high" {
		t.Errorf("out-of-vocab medium: effort = %q, want fallback %q", got, "high")
	}
	if got := build(flash, "none"); got != "high" {
		t.Errorf("out-of-vocab none: effort = %q, want fallback %q", got, "high")
	}
	if got := build(flash, "low"); got != "low" {
		t.Errorf("in-vocab low: effort = %q, want low (unchanged)", got)
	}

	unrestricted := &vocabFakeModel{vocab: nil}
	if got := build(unrestricted, "medium"); got != "medium" {
		t.Errorf("unrestricted model: effort = %q, want medium (passthrough)", got)
	}
}
