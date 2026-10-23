package server

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	openacp "github.com/yusheng-g/openagent-go/acp/sdk"
)

// rewriteSettings overwrites the settings file for a prune test.
func rewriteSettings(t *testing.T, path, raw string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
		t.Fatalf("rewrite settings: %v", err)
	}
}

// TestReloadPrunesDeletedFileProviders: end-to-end through sw.reload — a
// provider removed from settings.json is pruned from the registry, the
// session that used it is migrated to the surviving default, and the
// broadcast advertises the migrated model.
func TestReloadPrunesDeletedFileProviders(t *testing.T) {
	path := writeSettings(t, `{
		"provider": {
			"prov1": {"api_key": "sk", "models": ["m1"]},
			"prov2": {"api_key": "sk", "models": ["m2"]}
		}
	}`)
	sw, client, sid := newReloadTestServer(t, path, true)

	// First reload registers both providers.
	res := sw.reload(context.Background())
	if res.ParseError != "" || len(res.Violations) > 0 {
		t.Fatalf("first reload failed unexpectedly: %+v", res)
	}
	// The test server starts with a placeholder model (test/m) as its
	// default; pin the default to prov1/m1 so the prune's migration
	// target is deterministic (SetModel never touches the default — the
	// same reason newPruneServer pins it in the acp-package tests).
	if !sw.srv.SetDefaultModelID("prov1/m1") {
		t.Fatal("SetDefaultModelID(prov1/m1) failed")
	}
	// Point the session at prov2/m2 via the public config API.
	if _, err := sw.srv.OnSetSessionConfigOption(context.Background(), openacp.SetSessionConfigOptionRequest{
		SessionID: sid, ConfigID: "model", Value: "prov2/m2",
	}); err != nil {
		t.Fatalf("set session model: %v", err)
	}

	// Remove prov2 from the file and reload.
	rewriteSettings(t, path, `{
		"provider": {"prov1": {"api_key": "sk", "models": ["m1"]}}
	}`)
	res = sw.reload(context.Background())
	if res.ParseError != "" || len(res.Violations) > 0 {
		t.Fatalf("prune reload failed unexpectedly: %+v", res)
	}

	// Registry: prov2/m2 pruned, prov1/m1 kept.
	ids := sw.srv.ModelIDs()
	if !slices.Contains(ids, "prov1/m1") || slices.Contains(ids, "prov2/m2") {
		t.Errorf("registry after prune = %v, want [prov1/m1] only", ids)
	}
	// The session was migrated to the surviving default — observable via
	// the config_option_update broadcast that follows the prune.
	if got := client.modelCurrent(); got != "prov1/m1" {
		t.Errorf("broadcast model current = %q, want migrated prov1/m1", got)
	}
	// The reload result notification reports success and the removal.
	notifs := client.reloadNotifs()
	if len(notifs) == 0 {
		t.Fatal("no settings_reload_result after prune reload")
	}
	last := notifs[len(notifs)-1]
	if !last.success {
		t.Errorf("settings_reload_result success = false, want true (%+v)", last)
	}
	joined := strings.Join(last.applied, ";")
	if !strings.Contains(joined, "removed 1 model") {
		t.Errorf("applied %v does not report the removal", last.applied)
	}
}

// TestReloadKeepsNonFileModels: models registered outside settings.json
// (cli:settings plugins, wasm runtime_set_model_config) are absent from
// the file-key baseline and must survive a reload that prunes file
// models.
func TestReloadKeepsNonFileModels(t *testing.T) {
	path := writeSettings(t, `{
		"provider": {
			"prov1": {"api_key": "sk", "models": ["m1"]},
			"prov2": {"api_key": "sk", "models": ["m2"]}
		}
	}`)
	sw, _, _ := newReloadTestServer(t, path, true)
	sw.reload(context.Background())

	// A non-file model, the way a plugin or runtime registration adds one.
	sw.srv.SetModel("pluginprov", "x", "sk", "", 0, 0, nil)

	// Remove prov2 from the file: the baseline covers prov1 and prov2, so
	// prov2 must be pruned while pluginprov/x (absent from the baseline)
	// survives.
	rewriteSettings(t, path, `{
		"provider": {"prov1": {"api_key": "sk", "models": ["m1"]}}
	}`)
	res := sw.reload(context.Background())
	if res.ParseError != "" || len(res.Violations) > 0 {
		t.Fatalf("prune reload failed unexpectedly: %+v", res)
	}

	ids := sw.srv.ModelIDs()
	if !slices.Contains(ids, "pluginprov/x") {
		t.Errorf("non-file model pluginprov/x was pruned: registry = %v", ids)
	}
	if slices.Contains(ids, "prov2/m2") {
		t.Errorf("file model prov2/m2 survived: registry = %v", ids)
	}
}

// TestReloadNilBaselineDisablesPrune: a nil file-key baseline (the file
// was unreadable when the watcher was built) must not prune anything on
// the FIRST successful reload — the watcher cannot know what the file
// used to contain. The baseline is rebuilt from that first reload's file
// state, so deletions resume from the next reload onward. This pins the
// fail-safe window documented on settingsKeys.
func TestReloadNilBaselineDisablesPrune(t *testing.T) {
	path := writeSettings(t, `{
		"provider": {
			"prov1": {"api_key": "sk", "models": ["m1"]},
			"prov2": {"api_key": "sk", "models": ["m2"]}
		}
	}`)
	sw, _, _ := newReloadTestServer(t, path, true)
	sw.reload(context.Background()) // healthy baseline from the helper

	// Simulate a watcher built while the file was unreadable: nil
	// baseline, and the file now declares less than the registry holds.
	sw.settingsKeys = nil
	rewriteSettings(t, path, `{
		"provider": {"prov1": {"api_key": "sk", "models": ["m1"]}}
	}`)
	res := sw.reload(context.Background())
	if res.ParseError != "" || len(res.Violations) > 0 {
		t.Fatalf("reload failed unexpectedly: %+v", res)
	}

	ids := sw.srv.ModelIDs()
	if !slices.Contains(ids, "prov2/m2") {
		t.Errorf("prov2/m2 pruned from a nil baseline, want the first reload to delete nothing: registry = %v", ids)
	}
	// The baseline was rebuilt from the reload's file state — a later
	// reload does not retroactively "catch" the deletion either (prov2 is
	// now outside the baseline, like a plugin model).
	rewriteSettings(t, path, `{
		"provider": {"prov1": {"api_key": "sk", "models": ["m1"]}}
	}`)
	sw.reload(context.Background())
	ids = sw.srv.ModelIDs()
	if !slices.Contains(ids, "prov2/m2") {
		t.Errorf("prov2/m2 pruned on the follow-up reload, want it kept (outside the rebuilt baseline): registry = %v", ids)
	}
}
