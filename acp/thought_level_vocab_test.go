package acp

import (
	"context"
	"slices"
	"strings"
	"testing"

	openacp "github.com/yusheng-g/openagent-go/acp/sdk"
	"github.com/yusheng-g/openagent-go/agent"
	"github.com/yusheng-g/openagent-go/kernel"
)

// newVocabServer builds an AgentServer with the given models registered
// through SetModel — the same path the assembly layer and the settings
// reload use — so the built-in vocabulary detection (utils table inside
// openai.New) is exercised end to end.
func newVocabServer(models map[string][]string) *AgentServer {
	srv := NewAgentServer(agent.New("test"), kernel.Deps{}, newFakeStore(), nil)
	for key, vocab := range models {
		provider, modelID, _ := strings.Cut(key, "/")
		srv.SetModel(provider, modelID, "sk-test", "", 0, 0, vocab)
	}
	return srv
}

// putVocabSession registers a session directly (no SDK mux needed), the
// plan_concurrency_test pattern.
func putVocabSession(srv *AgentServer, sid, modelKey, thoughtLevel string) *agentSession {
	ss := &agentSession{
		id:          openacp.SessionId(sid),
		cwd:         "/tmp",
		mode:        "semi-auto",
		firstPrompt: false,
	}
	cfg := map[openacp.SessionConfigId]any{"model": modelKey}
	if thoughtLevel != "" {
		cfg["thought_level"] = thoughtLevel
	}
	ss.config = cfg
	srv.putSession(openacp.SessionId(sid), ss)
	return ss
}

func thoughtLevelOption(t *testing.T, opts []openacp.SessionConfigOption) openacp.SessionConfigOption {
	t.Helper()
	for _, o := range opts {
		if o.ID == "thought_level" {
			return o
		}
	}
	t.Fatal("config options must include thought_level")
	return openacp.SessionConfigOption{}
}

func optionValues(opt openacp.SessionConfigOption) []string {
	out := make([]string, 0, len(opt.Options))
	for _, v := range opt.Options {
		out = append(out, v.Value)
	}
	return out
}

// TestThoughtLevelOptionsFromVocabulary: the advertised options and the
// default come from the current model's vocabulary. glm-5.3-flash hits the
// built-in table (low/high/max, default high — medium is NOT advertised);
// an unknown model falls back to the generic vocabulary (default medium).
func TestThoughtLevelOptionsFromVocabulary(t *testing.T) {
	srv := newVocabServer(map[string][]string{"baidu/glm-5.3-flash": nil})
	srv.SetDefaultModelID("baidu/glm-5.3-flash")

	resp, err := srv.OnListConfigOptions(context.Background(), openacp.ListConfigOptionsRequest{})
	if err != nil {
		t.Fatalf("OnListConfigOptions: %v", err)
	}
	tl := thoughtLevelOption(t, resp.ConfigOptions)
	if want := []string{"low", "high", "max"}; !slices.Equal(optionValues(tl), want) {
		t.Errorf("thought_level options = %v, want %v", optionValues(tl), want)
	}
	if tl.CurrentValue != "high" {
		t.Errorf("thought_level currentValue = %q, want high (model default)", tl.CurrentValue)
	}
	for _, v := range tl.Options {
		if v.Value == "medium" {
			t.Error("thought_level options must not advertise medium for glm-5.3-flash")
		}
	}

	// Unknown model: generic vocabulary, historical medium default.
	srv2 := newVocabServer(map[string][]string{"baidu/some-model": nil})
	srv2.SetDefaultModelID("baidu/some-model")
	resp2, err := srv2.OnListConfigOptions(context.Background(), openacp.ListConfigOptionsRequest{})
	if err != nil {
		t.Fatalf("OnListConfigOptions: %v", err)
	}
	tl2 := thoughtLevelOption(t, resp2.ConfigOptions)
	if want := []string{"low", "medium", "high"}; !slices.Equal(optionValues(tl2), want) {
		t.Errorf("thought_level options = %v, want %v", optionValues(tl2), want)
	}
	if tl2.CurrentValue != "medium" {
		t.Errorf("thought_level currentValue = %q, want medium (generic default)", tl2.CurrentValue)
	}
}

// TestThoughtLevelSettingsVocabOverride: an explicitly declared vocabulary
// (settings reasoning_efforts) overrides the built-in detection — including
// widening it beyond the generic set.
func TestThoughtLevelSettingsVocabOverride(t *testing.T) {
	srv := newVocabServer(map[string][]string{"baidu/glm-5.2": {"none", "low"}})
	srv.SetDefaultModelID("baidu/glm-5.2")

	resp, err := srv.OnListConfigOptions(context.Background(), openacp.ListConfigOptionsRequest{})
	if err != nil {
		t.Fatalf("OnListConfigOptions: %v", err)
	}
	tl := thoughtLevelOption(t, resp.ConfigOptions)
	if want := []string{"none", "low"}; !slices.Equal(optionValues(tl), want) {
		t.Errorf("thought_level options = %v, want %v", optionValues(tl), want)
	}
	if tl.CurrentValue != "low" {
		t.Errorf("thought_level currentValue = %q, want low (last entry, no medium/high)", tl.CurrentValue)
	}
}

// TestSetThoughtLevelOutsideVocabRejected: setting a value outside the
// current model's vocabulary is a JSON-RPC error, and the session keeps
// its previous value; an in-vocabulary value is accepted.
func TestSetThoughtLevelOutsideVocabRejected(t *testing.T) {
	srv := newVocabServer(map[string][]string{"baidu/glm-5.3-flash": nil})
	ss := putVocabSession(srv, "s1", "baidu/glm-5.3-flash", "high")

	_, err := srv.OnSetSessionConfigOption(context.Background(), openacp.SetSessionConfigOptionRequest{
		SessionID: "s1", ConfigID: "thought_level", Value: "medium",
	})
	if err == nil {
		t.Fatal("setting medium on glm-5.3-flash must be rejected")
	}
	if !strings.Contains(err.Error(), "not supported") || !strings.Contains(err.Error(), "low, high, max") {
		t.Errorf("rejection should name the supported vocabulary, got: %v", err)
	}
	if v, _ := ss.ConfigString("thought_level"); v != "high" {
		t.Errorf("rejected set must not mutate config, thought_level = %q", v)
	}

	if _, err := srv.OnSetSessionConfigOption(context.Background(), openacp.SetSessionConfigOptionRequest{
		SessionID: "s1", ConfigID: "thought_level", Value: "low",
	}); err != nil {
		t.Fatalf("in-vocab set: %v", err)
	}
	if v, _ := ss.ConfigString("thought_level"); v != "low" {
		t.Errorf("thought_level = %q, want low", v)
	}
}

// TestModelSwitchRevalidatesThoughtLevel: switching a session holding a
// legal-for-the-old-model effort (medium on glm-5.2) onto a model whose
// vocabulary rejects it (glm-5.3-flash) falls back to the new model's
// default and persists it — no invalid effort survives the switch.
func TestModelSwitchRevalidatesThoughtLevel(t *testing.T) {
	srv := newVocabServer(map[string][]string{
		"baidu/glm-5.2":       nil,
		"baidu/glm-5.3-flash": nil,
	})
	ss := putVocabSession(srv, "s1", "baidu/glm-5.2", "medium")

	if _, err := srv.OnSetSessionConfigOption(context.Background(), openacp.SetSessionConfigOptionRequest{
		SessionID: "s1", ConfigID: "model", Value: "baidu/glm-5.3-flash",
	}); err != nil {
		t.Fatalf("model switch: %v", err)
	}
	if v, _ := ss.ConfigString("thought_level"); v != "high" {
		t.Errorf("thought_level after switch = %q, want high (glm-5.3-flash default)", v)
	}
	if v, _ := ss.ConfigString("model"); v != "baidu/glm-5.3-flash" {
		t.Errorf("model = %q, want baidu/glm-5.3-flash", v)
	}

	// The stale medium is now also rejected by direct set.
	if _, err := srv.OnSetSessionConfigOption(context.Background(), openacp.SetSessionConfigOptionRequest{
		SessionID: "s1", ConfigID: "thought_level", Value: "medium",
	}); err == nil {
		t.Error("medium must be rejected on glm-5.3-flash after the switch")
	}
}

// TestBuildRuntimeForSessionSelfHealsThoughtLevel: a session persisted with
// an effort the current model rejects (saved on another model, or before
// the vocabulary constraint existed) is healed to the model default when
// its runtime is built.
func TestBuildRuntimeForSessionSelfHealsThoughtLevel(t *testing.T) {
	srv := newVocabServer(map[string][]string{"baidu/glm-5.3-flash": nil})
	ss := putVocabSession(srv, "s1", "baidu/glm-5.3-flash", "medium")

	rt := srv.buildRuntimeForSession("s1", ss)
	if rt == nil {
		t.Fatal("buildRuntimeForSession returned nil")
	}
	if v, _ := ss.ConfigString("thought_level"); v != "high" {
		t.Errorf("thought_level after runtime build = %q, want high (self-healed)", v)
	}
}
