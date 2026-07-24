package wasm

import (
	"log/slog"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	openagent "github.com/yusheng-g/openagent-go"
	"github.com/yusheng-g/openagent-go/plugin/wasmhost"
)

// Manager discovers and manages WASM plugins from a directory.
type Manager struct {
	dir string

	mu        sync.Mutex
	ldr       loader
	tools     []openagent.Tool
	observers []*wasmObserver
	sessions  []*wasmSession

	hostAPI *wasmhost.HostAPI

	onAbort func(reason string)
}

// NewManager creates a Manager for the given plugin directory.
func NewManager(dir string) *Manager {
	return &Manager{dir: dir}
}

// WithHostAPI configures the host exports (keyring_get/set, http_request,
// log_info/warn/error) that WASM plugins can import via the "host" module.
// Call before [Manager.Discover].
func (m *Manager) WithHostAPI(h *wasmhost.HostAPI) *Manager {
	m.hostAPI = h
	return m
}

// wasmSession wraps an agent:sessions WASM plugin. It exports session_init(...)
// and session_destroy(...) instead of run().
type wasmSession struct {
	mod  *module
	meta PluginMeta
}

// invokeSessionInit calls the guest's session_init() and returns the parsed
// SessionConfig, or nil if the export is missing / returns null.
func (ws *wasmSession) invokeSessionInit(ctx context.Context, sc SessionCtx) (*SessionConfig, error) {
	fn := ws.mod.mod.ExportedFunction("session_init")
	if fn == nil {
		return nil, nil
	}
	bs, _ := json.Marshal(sc)
	out, err := ws.mod.invoke(ctx, "session_init", bs)
	if err != nil {
		return nil, err
	}
	if out == nil || string(out) == "null" {
		return nil, nil
	}
	var cfg SessionConfig
	if err := json.Unmarshal(out, &cfg); err != nil {
		return nil, fmt.Errorf("parse session config: %w", err)
	}
	return &cfg, nil
}

func (ws *wasmSession) invokeSessionDestroy(ctx context.Context, sc SessionCtx) error {
	fn := ws.mod.mod.ExportedFunction("session_destroy")
	if fn == nil {
		return nil
	}
	bs, _ := json.Marshal(sc)
	_, err := ws.mod.invoke(ctx, "session_destroy", bs)
	return err
}

// OnSessionInit calls every agent:sessions plugin's session_init export.
// Returns a merged SessionConfig (last non-nil wins for each field).
func (m *Manager) OnSessionInit(ctx context.Context, sc SessionCtx) *SessionConfig {
	m.mu.Lock()
	sessions := make([]*wasmSession, len(m.sessions))
	copy(sessions, m.sessions)
	m.mu.Unlock()

	var merged SessionConfig
	for _, ws := range sessions {
		cfg, err := ws.invokeSessionInit(ctx, sc)
		if err != nil {
			slog.Error("wasm session_init failed", "plugin", ws.meta.Name, "error", err)
			continue
		}
		if cfg != nil {
			mergeConfig(&merged, cfg)
		}
	}
	return &merged
}

// OnSessionDestroy calls every agent:sessions plugin's session_destroy export.
func (m *Manager) OnSessionDestroy(ctx context.Context, sc SessionCtx) {
	m.mu.Lock()
	sessions := make([]*wasmSession, len(m.sessions))
	copy(sessions, m.sessions)
	m.mu.Unlock()

	for _, ws := range sessions {
		if err := ws.invokeSessionDestroy(ctx, sc); err != nil {
			slog.Error("wasm session_destroy failed", "plugin", ws.meta.Name, "error", err)
		}
	}
}

func mergeConfig(dst, src *SessionConfig) {
	if len(src.SystemPrompts) > 0 {
		dst.SystemPrompts = src.SystemPrompts
	}
	if src.Description != "" {
		dst.Description = src.Description
	}
	if len(src.Tools) > 0 {
		dst.Tools = src.Tools
	}
	if src.MaxTurns > 0 {
		dst.MaxTurns = src.MaxTurns
	}
	if src.MaxWorkingTokens > 0 {
		dst.MaxWorkingTokens = src.MaxWorkingTokens
	}
	if src.SkillDir != "" {
		dst.SkillDir = src.SkillDir
	}
	if src.MemoryPath != "" {
		dst.MemoryPath = src.MemoryPath
	}
}

// OnAbort registers a callback invoked when a stage plugin returns action=abort.
func (m *Manager) OnAbort(fn func(reason string)) {
	m.mu.Lock()
	m.onAbort = fn
	m.mu.Unlock()
}

// Discover scans the plugin directory for .wasm files, instantiates each one,
// reads its metadata, and registers it as a Tool or Stage plugin.
func (m *Manager) Discover(ctx context.Context) error {
	if m.dir == "" {
		return nil
	}

	entries, err := os.ReadDir(m.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("plugin dir: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Lazy-init wazero runtime.
	if m.ldr.runtime == nil {
		ldr, err := newLoader(ctx)
		if err != nil {
			return fmt.Errorf("init wazero: %w", err)
		}
		// Register host exports BEFORE loading any plugin module.
		if m.hostAPI != nil {
			if err := m.hostAPI.RegisterHostModule(ctx, ldr.runtime); err != nil {
				ldr.Close(ctx)
				return fmt.Errorf("register host module: %w", err)
			}
		}
		m.ldr = ldr
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".wasm" {
			continue
		}
		path := filepath.Join(m.dir, entry.Name())
		if err := m.loadOne(ctx, path); err != nil {
			return fmt.Errorf("plugin %s: %w", entry.Name(), err)
		}
	}

	return nil
}

func (m *Manager) loadOne(ctx context.Context, path string) error {
	wasmBytes, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}

	mod, err := m.ldr.loadModule(ctx, filepath.Base(path), wasmBytes)
	if err != nil {
		return err
	}

	meta, err := mod.parseMeta(ctx)
	if err != nil {
		return err
	}

	switch meta.Type {
	case PluginTypeTools:
		m.tools = append(m.tools, &wasmTool{mod: mod, meta: meta})
	case PluginTypeObservers:
		m.observers = append(m.observers, &wasmObserver{mod: mod, meta: meta, name: meta.Name})
	case PluginTypeSessions:
		m.sessions = append(m.sessions, &wasmSession{mod: mod, meta: meta})
	default:
		slog.Info("wasm skipping unknown plugin type", "file", filepath.Base(path), "type", meta.Type)
		return nil
	}

	return nil
}

// Tools returns loaded Tool plugins as openagent.Tool values.
func (m *Manager) Tools() []openagent.Tool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tools
}

// Observer returns a RunObserver that dispatches to matching Stage plugins.
func (m *Manager) Observer() openagent.RunObserver {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.observers) == 0 {
		return nil
	}
	return &observerRouter{mgr: m}
}

// Close releases the wazero runtime.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ldr.runtime == nil {
		return nil
	}
	return m.ldr.Close(context.Background())
}

// observerRouter dispatches stage events to matching WASM stage plugins.
type observerRouter struct {
	mgr *Manager
}

func (o *observerRouter) ObserveStage(ctx context.Context, event openagent.StageEvent) {
	o.mgr.mu.Lock()
	stages := o.mgr.observers
	onAbort := o.mgr.onAbort
	o.mgr.mu.Unlock()

	for _, s := range stages {
		if !s.matches(event) {
			continue
		}
		out, err := s.invoke(ctx, event)
		if err != nil {
			if out != nil && out.Action == ActionAbort && onAbort != nil {
				onAbort(out.Reason)
				return
			}
			slog.Error("wasm observer error", "plugin", s.meta.Name, "stage", event.Name, "phase", event.Phase, "error", err)
			continue
		}
		slog.Info("wasm observer", "plugin", s.meta.Name, "stage", event.Name, "phase", event.Phase, "action", out.Action)
	}
}
