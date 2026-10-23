package acp

import (
	"context"
	"testing"
	"time"

	openacp "github.com/yusheng-g/openagent-go/acp/sdk"
	"github.com/yusheng-g/openagent-go/agent"
	"github.com/yusheng-g/openagent-go/kernel"
	"github.com/yusheng-g/openagent-go/session"
)

// newPruneServer builds an AgentServer with two models registered
// (prov1/m1, prov2/m2 — sorted order makes prov1/m1 the default) and one
// active session. SetModel alone never touches the default (production
// sets it at NewAgentServer construction or via the lazy-reload
// postReload hook), so the test pins it explicitly.
func newPruneServer(t *testing.T) (*AgentServer, openacp.SessionId, *agentSession) {
	t.Helper()
	srv := NewAgentServer(agent.New("test"), kernel.Deps{}, nil, nil)
	srv.SetModel("prov1", "m1", "sk", "", 0, 0, nil)
	srv.SetModel("prov2", "m2", "sk", "", 0, 0, nil)
	srv.SetDefaultModelID("prov1/m1")
	resp, err := srv.OnNewSession(context.Background(), openacp.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("OnNewSession: %v", err)
	}
	sid := resp.SessionID
	return srv, sid, srv.getSession(sid)
}

// sessionModel returns the session's current model config value.
func sessionModel(ss *agentSession) string {
	v, _ := ss.ConfigString("model")
	return v
}

// TestPruneModels_RemovesAndMigrates: pruning a non-default model removes
// it from the registry and migrates sessions referencing it to the
// (unchanged) default.
func TestPruneModels_RemovesAndMigrates(t *testing.T) {
	srv, sid, ss := newPruneServer(t)
	ss.SetConfigValue("model", "prov2/m2")

	srv.PruneModels(context.Background(), []string{"prov2/m2"})

	if _, ok := srv.LookupModel("prov2/m2"); ok {
		t.Error("prov2/m2 still registered after prune")
	}
	if _, ok := srv.LookupModel("prov1/m1"); !ok {
		t.Error("prov1/m1 must survive the prune")
	}
	if got := sessionModel(ss); got != "prov1/m1" {
		t.Errorf("session %s model = %q, want migrated to default prov1/m1", sid, got)
	}
}

// TestPruneModels_DefaultRemovedFallsBack: pruning the default model
// falls the default back to the first remaining model (sorted) and
// migrates sessions that were on the old default.
func TestPruneModels_DefaultRemovedFallsBack(t *testing.T) {
	srv, sid, ss := newPruneServer(t)
	// Session stays on the default prov1/m1.

	srv.PruneModels(context.Background(), []string{"prov1/m1"})

	if _, ok := srv.LookupModel("prov1/m1"); ok {
		t.Error("prov1/m1 still registered after prune")
	}
	if got := srv.GetDefaultModelID(); got != "prov2/m2" {
		t.Errorf("default after pruning the default = %q, want prov2/m2 (sorted-first fallback)", got)
	}
	if got := sessionModel(ss); got != "prov2/m2" {
		t.Errorf("session %s model = %q, want migrated to new default prov2/m2", sid, got)
	}
}

// TestPruneModels_EmptyRegistryKeepsStaleKey: pruning the last model
// leaves the registry empty; sessions keep their stale key (the no-model
// path on next prompt is the correct outcome) and nothing panics.
func TestPruneModels_EmptyRegistryKeepsStaleKey(t *testing.T) {
	srv := NewAgentServer(agent.New("test"), kernel.Deps{}, nil, nil)
	srv.SetModel("prov1", "m1", "sk", "", 0, 0, nil)
	srv.SetDefaultModelID("prov1/m1")
	resp, err := srv.OnNewSession(context.Background(), openacp.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("OnNewSession: %v", err)
	}
	ss := srv.getSession(resp.SessionID)

	srv.PruneModels(context.Background(), []string{"prov1/m1"})

	if got := srv.GetDefaultModelID(); got != "" {
		t.Errorf("default after pruning everything = %q, want empty", got)
	}
	if got := sessionModel(ss); got != "prov1/m1" {
		t.Errorf("session model = %q, want the stale key prov1/m1 kept (no migration target)", got)
	}
}

// TestBuildConfigOptions_DanglingModelAdvertisesDefault: a session whose
// model config references an unregistered key (persisted session loaded
// after a prune) is advertised with the default, never a value the
// registry cannot resolve.
func TestBuildConfigOptions_DanglingModelAdvertisesDefault(t *testing.T) {
	srv, sid, ss := newPruneServer(t)
	ss.SetConfigValue("model", "gone/x")

	opts := srv.buildConfigOptions(sid)
	var model *openacp.SessionConfigOption
	for i := range opts {
		if opts[i].ID == "model" {
			model = &opts[i]
			break
		}
	}
	if model == nil {
		t.Fatal("config options must include the model select")
	}
	if model.CurrentValue != "prov1/m1" {
		t.Errorf("model currentValue = %q, want default prov1/m1 (dangling gone/x must fall back)", model.CurrentValue)
	}
}

// danglingStore is a session.Store whose Get returns a fixed SessionInfo,
// for testing the session/load and session/resume restore paths.
type danglingStore struct {
	info *session.SessionInfo
}

func (f *danglingStore) Save(context.Context, session.SessionInfo) error { return nil }
func (f *danglingStore) Get(context.Context, string) (*session.SessionInfo, error) {
	return f.info, nil
}
func (f *danglingStore) List(context.Context) ([]session.SessionInfo, error) { return nil, nil }
func (f *danglingStore) Delete(context.Context, string) error                { return nil }
func (f *danglingStore) Close() error                                        { return nil }

// newDanglingRestoreServer builds a server with prov1/m1 registered and a
// store holding a session whose persisted model config is "gone/x".
func newDanglingRestoreServer(t *testing.T) *AgentServer {
	t.Helper()
	created := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	store := &danglingStore{info: &session.SessionInfo{
		ID:        "s-dangling",
		CreatedAt: created,
		Meta: map[string]any{
			"mode":   "auto",
			"config": map[string]any{"model": "gone/x"},
		},
	}}
	srv := NewAgentServer(agent.New("test"), kernel.Deps{}, store, nil)
	srv.SetModel("prov1", "m1", "sk", "", 0, 0, nil)
	srv.SetDefaultModelID("prov1/m1")
	return srv
}

// TestOnLoadSession_DanglingModelReset: a persisted session referencing a
// pruned model is reset to the server default on load.
func TestOnLoadSession_DanglingModelReset(t *testing.T) {
	srv := newDanglingRestoreServer(t)

	if _, err := srv.OnLoadSession(context.Background(), openacp.LoadSessionRequest{SessionID: "s-dangling"}, nil); err != nil {
		t.Fatalf("OnLoadSession: %v", err)
	}
	ss := srv.getSession("s-dangling")
	if ss == nil {
		t.Fatal("session not registered after load")
	}
	if got := sessionModel(ss); got != "prov1/m1" {
		t.Errorf("restored session model = %q, want reset to default prov1/m1", got)
	}
}

// TestOnResumeSession_DanglingModelReset: same as the load path — the
// resume path restores persisted config too.
func TestOnResumeSession_DanglingModelReset(t *testing.T) {
	srv := newDanglingRestoreServer(t)

	if _, err := srv.OnResumeSession(context.Background(), openacp.ResumeSessionRequest{SessionID: "s-dangling"}); err != nil {
		t.Fatalf("OnResumeSession: %v", err)
	}
	ss := srv.getSession("s-dangling")
	if ss == nil {
		t.Fatal("session not registered after resume")
	}
	if got := sessionModel(ss); got != "prov1/m1" {
		t.Errorf("restored session model = %q, want reset to default prov1/m1", got)
	}
}
