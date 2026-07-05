package wasm

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	openagent "github.com/yusheng-g/openagent-go"
)

// Manager discovers and manages WASM plugins from a directory.
//
// Usage:
//
//	mgr := wasm.NewManager("./plugins")
//	if err := mgr.Discover(ctx); err != nil { ... }
//	defer mgr.Close()
//
//	agent := openagent.NewAgent("bot",
//	    openagent.WithModel(model),
//	    openagent.WithTools(mgr.Tools()...),
//	    openagent.WithRunObserver(mgr.Observer()),
//	)
type Manager struct {
	dir string

	mu     sync.Mutex
	ldr    loader
	tools  []openagent.Tool
	stages []*wasmStage

	// onAbort is called when a stage plugin requests abort.
	// nil (default) = abort is ignored. Set via [Manager.OnAbort].
	onAbort func(reason string)
}

// NewManager creates a Manager for the given plugin directory.
// Pass an empty string to create an inert Manager (no plugins loaded).
func NewManager(dir string) *Manager {
	return &Manager{dir: dir}
}

// OnAbort registers a callback invoked when a stage plugin returns action=abort.
// The callback runs synchronously on the observer goroutine — keep it fast.
// A typical use is cancelling the run context so the runner exits at the next
// ctx.Done() checkpoint.
func (m *Manager) OnAbort(fn func(reason string)) {
	m.mu.Lock()
	m.onAbort = fn
	m.mu.Unlock()
}

// Discover scans the plugin directory for .wasm files, instantiates each one,
// reads its metadata, and registers it as a Tool or Stage plugin.
// If the directory is empty or doesn't exist, Discover is a no-op.
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

	// Lazy-init wazero runtime
	if m.ldr.runtime == nil {
		ldr, err := newLoader(ctx)
		if err != nil {
			return fmt.Errorf("init wazero: %w", err)
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
	case PluginTypeTool:
		m.tools = append(m.tools, &wasmTool{mod: mod, meta: meta})
	case PluginTypeStage:
		m.stages = append(m.stages, &wasmStage{mod: mod, meta: meta, name: meta.Name})
	default:
		return fmt.Errorf("unknown plugin type %q", meta.Type)
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
// Returns nil if no stage plugins are loaded.
func (m *Manager) Observer() openagent.RunObserver {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.stages) == 0 {
		return nil
	}
	return &stageObserver{mgr: m}
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

// stageObserver dispatches stage events to matching WASM stage plugins.
type stageObserver struct {
	mgr *Manager
}

func (o *stageObserver) ObserveStage(ctx context.Context, event openagent.StageEvent) {
	o.mgr.mu.Lock()
	stages := o.mgr.stages
	onAbort := o.mgr.onAbort
	o.mgr.mu.Unlock()

	for _, s := range stages {
		if !s.matches(event) {
			continue
		}
		out, err := s.invoke(ctx, event)
		if err != nil {
			// Stage plugin requested abort — invoke the callback if registered.
			// The typical callback cancels the run ctx, causing the runner to exit
			// at its next ctx.Done() checkpoint.
			if out != nil && out.Action == ActionAbort && onAbort != nil {
				onAbort(out.Reason)
				return
			}
			log.Printf("[wasm:%s] %s:%s → ERROR: %v", s.meta.Name, event.Name, event.Phase, err)
			continue
		}
		log.Printf("[wasm:%s] %s:%s → action=%s", s.meta.Name, event.Name, event.Phase, out.Action)
	}
}
