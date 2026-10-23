package wasm

import (
	"context"
	"encoding/json"
	"log/slog"

	openagent "github.com/yusheng-g/openagent-go"
)

// apiErrorPlugin adapts one WASM module exporting on_api_error to the
// decorator consult. The export is probed at load (module.hasAPIError);
// binaries compiled before the ABI never receive a consult.
type apiErrorPlugin struct {
	mod  *module
	name string // human label for logging
}

// apiErrorHint invokes the guest's on_api_error and returns its hint. A
// guest failure (invoke error, unparseable output) degrades to "no
// hint" — decoration must never turn a model error into a host error.
// context.Background matches the observer worker's guest-call contract:
// the runtime cannot interrupt pure-logic guests, so there is nothing a
// caller ctx could cancel.
func (p *apiErrorPlugin) apiErrorHint(info openagent.APIErrorInfo) string {
	errStr := ""
	if info.Err != nil {
		errStr = info.Err.Error()
	}
	input, err := json.Marshal(APIErrorInput{
		StatusCode: info.StatusCode,
		BaseURL:    info.BaseURL,
		ModelID:    info.ModelID,
		Error:      errStr,
	})
	if err != nil {
		slog.Error("wasm api-error plugin marshal failed", "plugin", p.name, "error", err)
		return ""
	}
	out, err := p.mod.invoke(context.Background(), "on_api_error", input)
	if err != nil {
		slog.Error("wasm api-error plugin failed", "plugin", p.name, "error", err)
		return ""
	}
	var o APIErrorOutput
	if err := json.Unmarshal(out, &o); err != nil {
		slog.Error("wasm api-error plugin returned unparseable output", "plugin", p.name, "error", err)
		return ""
	}
	return o.Hint
}

// APIErrorDecorator returns an openagent.APIErrorDecorator backed by the
// loaded plugins that export on_api_error. Plugins are consulted in load
// order; the first non-empty hint wins, and a failing plugin is skipped
// (logged) so one broken plugin cannot silence the rest. nil when no
// plugin is eligible — callers treat nil as "no decoration configured".
func (m *Manager) APIErrorDecorator() openagent.APIErrorDecorator {
	m.mu.Lock()
	plugins := append([]*apiErrorPlugin(nil), m.apiErrPlugins...)
	m.mu.Unlock()
	if len(plugins) == 0 {
		return nil
	}
	return func(info openagent.APIErrorInfo) string {
		for _, p := range plugins {
			if hint := p.apiErrorHint(info); hint != "" {
				return hint
			}
		}
		return ""
	}
}
