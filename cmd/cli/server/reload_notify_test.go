package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	openagent "github.com/yusheng-g/openagent-go"
	"github.com/yusheng-g/openagent-go/acp"
	openacp "github.com/yusheng-g/openagent-go/acp/sdk"
	"github.com/yusheng-g/openagent-go/agent"
	"github.com/yusheng-g/openagent-go/cmd/cli/config"
	"github.com/yusheng-g/openagent-go/kernel"
)

// newReloadTestServer builds an AgentServer wired to an
// updateRecordingClient and one active session, plus a settingsWatcher
// pointing at settingsPath. Mirrors the wiring the production ACP path
// installs at startup (acp.go activeWatcher.Store).
func newReloadTestServer(t *testing.T, settingsPath string, withSession bool) (*settingsWatcher, *updateRecordingClient, openacp.SessionId) {
	t.Helper()
	mdl := &slowModel{}
	cfg := agent.New("test",
		agent.WithModel(mdl),
		agent.WithSystemPrompts("test"),
		agent.WithMaxTurns(3),
	)
	srv := acp.NewAgentServer(cfg, kernel.Deps{}, newMemStore(), map[string]openagent.Model{"test/m": mdl})
	client := &updateRecordingClient{}
	srv.SetClientRequester(client)

	var sid openacp.SessionId
	if withSession {
		resp, err := srv.OnNewSession(context.Background(), openacp.NewSessionRequest{Cwd: t.TempDir()})
		if err != nil {
			t.Fatalf("OnNewSession: %v", err)
		}
		sid = resp.SessionID
	}

	sw := &settingsWatcher{cfgPath: settingsPath, prev: &config.Config{}, srv: srv}
	return sw, client, sid
}

// writeSettings writes raw settings bytes to a temp settings.json and
// returns its path.
func writeSettings(t *testing.T, raw string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	return path
}

// TestReloadNotifiesSessions_Success: a successful reload (new provider)
// broadcasts settings_reload_result with success=true and a non-empty
// applied list to the active session.
func TestReloadNotifiesSessions_Success(t *testing.T) {
	path := writeSettings(t, `{
		"log": {"level": "info"},
		"provider": {"prov1": {"api_key": "sk-test", "models": ["m1"]}}
	}`)
	sw, client, sid := newReloadTestServer(t, path, true)

	res := sw.reload(context.Background())
	if res.ParseError != "" || len(res.Violations) > 0 {
		t.Fatalf("reload failed unexpectedly: %+v", res)
	}

	notifs := client.reloadNotifs()
	if len(notifs) != 1 {
		t.Fatalf("settings_reload_result notifications = %d, want 1", len(notifs))
	}
	n := notifs[0]
	if n.sid != string(sid) {
		t.Errorf("sid = %q, want %q", n.sid, sid)
	}
	if !n.success {
		t.Errorf("success = false, want true")
	}
	if len(n.applied) == 0 {
		t.Errorf("applied list empty, want the provider entry")
	}
	if n.parseError != "" || len(n.violations) > 0 {
		t.Errorf("unexpected failure fields: parse_error=%q violations=%v", n.parseError, n.violations)
	}
}

// TestReloadNotifiesSessions_ParseError: an unparseable settings.json
// broadcasts success=false with the parse error, and the ReloadResult
// reports it so the caller (and the frontend) knows the reload failed.
func TestReloadNotifiesSessions_ParseError(t *testing.T) {
	path := writeSettings(t, `{not json`)
	sw, client, _ := newReloadTestServer(t, path, true)

	res := sw.reload(context.Background())
	if res.ParseError == "" {
		t.Fatalf("ParseError empty, want the parse failure reported")
	}

	notifs := client.reloadNotifs()
	if len(notifs) != 1 {
		t.Fatalf("settings_reload_result notifications = %d, want 1 (failures must broadcast too)", len(notifs))
	}
	n := notifs[0]
	if n.success {
		t.Errorf("success = true, want false")
	}
	if n.parseError == "" {
		t.Errorf("parse_error empty in notification, want the error text")
	}
	if len(n.applied) > 0 {
		t.Errorf("applied = %v on a failed reload, want empty", n.applied)
	}
}

// TestReloadNotifiesSessions_Violations: a reload introducing a NEW enum
// violation (invalid default_mode) is blocked and broadcasts success=false
// with the violations listed.
func TestReloadNotifiesSessions_Violations(t *testing.T) {
	path := writeSettings(t, `{"default_mode": "bogus"}`)
	sw, client, _ := newReloadTestServer(t, path, true)

	res := sw.reload(context.Background())
	if len(res.Violations) == 0 {
		t.Fatalf("Violations empty, want the default_mode violation reported")
	}

	notifs := client.reloadNotifs()
	if len(notifs) != 1 {
		t.Fatalf("settings_reload_result notifications = %d, want 1", len(notifs))
	}
	n := notifs[0]
	if n.success {
		t.Errorf("success = true, want false")
	}
	if len(n.violations) == 0 {
		t.Errorf("violations empty in notification, want them listed")
	}
	if !strings.Contains(strings.Join(n.violations, ";"), "default_mode") {
		t.Errorf("violations %v do not mention default_mode", n.violations)
	}
}

// TestReloadNotifies_NoServerIsNoOp: REST/run mode (srv=nil) must not
// panic and must still return the reload result — only the broadcast is
// skipped.
func TestReloadNotifies_NoServerIsNoOp(t *testing.T) {
	path := writeSettings(t, `{"log": {"level": "info"}}`)
	sw := &settingsWatcher{cfgPath: path, prev: &config.Config{}, srv: nil}

	res := sw.reload(context.Background())
	if res.ParseError != "" {
		t.Fatalf("unexpected parse error: %q", res.ParseError)
	}
}

// TestReloadNotifies_NoSessionsNoSends: with no active sessions the
// broadcast is a no-op (no panic, nothing recorded by the transport).
func TestReloadNotifies_NoSessionsNoSends(t *testing.T) {
	path := writeSettings(t, `{
		"log": {"level": "info"},
		"provider": {"prov1": {"api_key": "sk-test", "models": ["m1"]}}
	}`)
	sw, client, _ := newReloadTestServer(t, path, false)

	res := sw.reload(context.Background())
	if res.ParseError != "" || len(res.Violations) > 0 {
		t.Fatalf("reload failed unexpectedly: %+v", res)
	}
	if got := client.reloadNotifs(); len(got) != 0 {
		t.Errorf("settings_reload_result sent with no active sessions: %+v", got)
	}
	if got := client.configUpdates(); got != 0 {
		t.Errorf("config_option_update sent with no active sessions: %d", got)
	}
}
