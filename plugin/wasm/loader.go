package wasm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// loader manages a wazero runtime for WASM plugin modules.
// Pure WASM modules (no imports) load directly; modules importing WASI
// can be supported by calling InstantiateWASI before loading.
type loader struct {
	runtime wazero.Runtime
}

func newLoader(ctx context.Context) (loader, error) {
	rt := wazero.NewRuntime(ctx)
	return loader{runtime: rt}, nil
}

func (l loader) Close(ctx context.Context) error {
	return l.runtime.Close(ctx)
}

// loadModule instantiates a .wasm file and returns a module wrapper.
func (l loader) loadModule(ctx context.Context, name string, wasmBytes []byte) (*module, error) {
	cfg := wazero.NewModuleConfig().WithName(name)
	mod, err := l.runtime.InstantiateWithConfig(ctx, wasmBytes, cfg)
	if err != nil {
		return nil, fmt.Errorf("instantiate: %w", err)
	}
	return &module{mod: mod}, nil
}

// ── module wraps a wazero api.Module ──

type module struct {
	mod api.Module
}

// call a function by name, passing (ptr, len) and receiving a packed (ptr, len) uint64.
func (m *module) call(ctx context.Context, fnName string, ptr, length uint32) (uint32, uint32, error) {
	fn := m.mod.ExportedFunction(fnName)
	if fn == nil {
		return 0, 0, fmt.Errorf("plugin missing export %q", fnName)
	}
	results, err := fn.Call(ctx, uint64(ptr), uint64(length))
	if err != nil {
		return 0, 0, fmt.Errorf("%s: %w", fnName, err)
	}
	if len(results) < 1 {
		return 0, 0, fmt.Errorf("%s: no result", fnName)
	}
	// Guest returns a single uint64: high 32 = ptr, low 32 = len.
	packed := results[0]
	return uint32(packed >> 32), uint32(packed & 0xFFFFFFFF), nil
}

// alloc calls guest's alloc(size) → offset.
func (m *module) alloc(ctx context.Context, size uint32) (uint32, error) {
	fn := m.mod.ExportedFunction("alloc")
	if fn == nil {
		return 0, fmt.Errorf("plugin missing export alloc")
	}
	results, err := fn.Call(ctx, uint64(size))
	if err != nil {
		return 0, fmt.Errorf("alloc: %w", err)
	}
	return uint32(results[0]), nil
}

// metadataJSON calls guest's metadata() → packed(ptr, len) and returns the JSON bytes.
func (m *module) metadataJSON(ctx context.Context) ([]byte, error) {
	fn := m.mod.ExportedFunction("metadata")
	if fn == nil {
		return nil, fmt.Errorf("plugin missing export metadata")
	}
	results, err := fn.Call(ctx) // no parameters
	if err != nil {
		return nil, fmt.Errorf("metadata: %w", err)
	}
	if len(results) < 1 {
		return nil, fmt.Errorf("metadata: no result")
	}
	packed := results[0]
	ptr := uint32(packed >> 32)
	length := uint32(packed & 0xFFFFFFFF)

	data, ok := m.mod.Memory().Read(ptr, length)
	if !ok {
		return nil, fmt.Errorf("metadata: read out of bounds (%d, %d)", ptr, length)
	}
	out := make([]byte, length)
	copy(out, data)
	return out, nil
}

// invoke calls a function with JSON input and returns JSON output.
// It handles the alloc/copy/call/read cycle.
func (m *module) invoke(ctx context.Context, fnName string, input []byte) ([]byte, error) {
	// 1. Allocate memory in guest
	buf, err := m.alloc(ctx, uint32(len(input)))
	if err != nil {
		return nil, err
	}

	// 2. Write input JSON into guest memory
	if !m.mod.Memory().Write(buf, input) {
		return nil, fmt.Errorf("%s: write out of bounds", fnName)
	}

	// 3. Call the function
	ptr, length, err := m.call(ctx, fnName, buf, uint32(len(input)))
	if err != nil {
		return nil, err
	}

	// 4. Read output JSON from guest memory
	if length == 0 {
		return nil, nil
	}
	data, ok := m.mod.Memory().Read(ptr, length)
	if !ok {
		return nil, fmt.Errorf("%s: read result out of bounds (%d, %d)", fnName, ptr, length)
	}
	out := make([]byte, length)
	copy(out, data)
	return out, nil
}

// parseMeta reads and unmarshals the plugin metadata.
func (m *module) parseMeta(ctx context.Context) (PluginMeta, error) {
	raw, err := m.metadataJSON(ctx)
	if err != nil {
		return PluginMeta{}, err
	}
	var meta PluginMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return PluginMeta{}, fmt.Errorf("parse metadata: %w", err)
	}
	return meta, nil
}
