package wasm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/tetratelabs/wazero/api"

	openagent "github.com/yusheng-g/openagent-go"
	"github.com/yusheng-g/openagent-go/plugin/wasmhost"
)

// ── fake api.Module ──
//
// The decorator's guest boundary is wazero's api.Module — an interface.
// These fakes embed the interfaces (unexercised methods panic if the
// host's needs grow) and implement exactly the surface CallWithInput
// touches: ExportedFunction, Memory().Read/Write, and Function.Call.
// The JSON keys the fake decodes with are written independently of the
// host's APIErrorInput struct, so the round-trip pins the wire contract.

type fakeMemory struct {
	api.Memory
	buf    []byte
	cursor uint32
}

func newFakeMemory() *fakeMemory {
	// cursor starts above 0 so ptr==0 stays the null sentinel.
	return &fakeMemory{buf: make([]byte, 64*1024), cursor: 16}
}

func (m *fakeMemory) Write(offset uint32, data []byte) bool {
	if uint64(offset)+uint64(len(data)) > uint64(len(m.buf)) {
		return false
	}
	copy(m.buf[offset:], data)
	return true
}

func (m *fakeMemory) Read(offset uint32, count uint32) ([]byte, bool) {
	if uint64(offset)+uint64(count) > uint64(len(m.buf)) {
		return nil, false
	}
	return m.buf[offset : offset+count], true
}

func (m *fakeMemory) Size() uint32 { return uint32(len(m.buf)) }

// alloc bumps the cursor (16-aligned); dealloc is a no-op.
func (m *fakeMemory) alloc(size uint32) uint32 {
	ptr := m.cursor
	m.cursor += (size + 15) &^ 15
	return ptr
}

type fakeFunction struct {
	api.Function
	fn func(ctx context.Context, params []uint64) ([]uint64, error)
}

func (f *fakeFunction) Call(ctx context.Context, params ...uint64) ([]uint64, error) {
	return f.fn(ctx, params)
}

type fakeModule struct {
	api.Module
	mem *fakeMemory
	fns map[string]*fakeFunction
}

func (f *fakeModule) Name() string       { return "fake" }
func (f *fakeModule) String() string     { return "fake" }
func (f *fakeModule) Memory() api.Memory { return f.mem }
func (f *fakeModule) ExportedFunction(name string) api.Function {
	if fn, ok := f.fns[name]; ok {
		return fn
	}
	return nil
}

// newFakeHostModule wires the standard exports: alloc (bump), dealloc
// (no-op), and on_api_error dispatching to rule.
func newFakeHostModule(rule func(input map[string]any) (string, error)) *fakeModule {
	mem := newFakeMemory()
	fm := &fakeModule{mem: mem, fns: map[string]*fakeFunction{}}
	fm.fns["alloc"] = &fakeFunction{fn: func(ctx context.Context, params []uint64) ([]uint64, error) {
		return []uint64{uint64(mem.alloc(uint32(params[0])))}, nil
	}}
	fm.fns["dealloc"] = &fakeFunction{fn: func(ctx context.Context, params []uint64) ([]uint64, error) {
		return nil, nil
	}}
	fm.fns["on_api_error"] = &fakeFunction{fn: func(ctx context.Context, params []uint64) ([]uint64, error) {
		ptr, length := uint32(params[0]), uint32(params[1])
		data, ok := mem.Read(ptr, length)
		if !ok {
			return nil, errors.New("read input out of bounds")
		}
		var input map[string]any
		if err := json.Unmarshal(data, &input); err != nil {
			return nil, fmt.Errorf("bad input JSON: %w", err)
		}
		hint, err := rule(input)
		if err != nil {
			return nil, err
		}
		out, err := json.Marshal(APIErrorOutput{Hint: hint})
		if err != nil {
			return nil, err
		}
		optr := mem.alloc(uint32(len(out)))
		if !mem.Write(optr, out) {
			return nil, errors.New("write output out of bounds")
		}
		return []uint64{wasmhost.Pack(optr, uint32(len(out)))}, nil
	}}
	return fm
}

// tokenhubRule mirrors the openagent-plugins TokenHub hint plugin's
// matching contract: 429 + tokenhub base URL → hint, else none. Decoding
// with raw map keys (not APIErrorInput) pins the JSON field names.
func tokenhubRule(input map[string]any) (string, error) {
	status, _ := input["status_code"].(float64)
	baseURL, _ := input["base_url"].(string)
	if int(status) == 429 && strings.Contains(baseURL, "tokenhub") {
		return "To fix this: configure your own provider quota.", nil
	}
	return "", nil
}

// fakeModuleWithAPIError wraps a fakeModule in the host module type,
// probed as decorator-eligible.
func fakeModuleWithAPIError(rule func(input map[string]any) (string, error)) *module {
	return &module{mod: newFakeHostModule(rule), hasAPIError: true}
}

// TestAPIErrorHint_RoundTrip: a 429 from tokenhub surfaces the plugin's
// hint through the full host→guest→host path (marshal, alloc, write,
// call, read, unmarshal).
func TestAPIErrorHint_RoundTrip(t *testing.T) {
	p := &apiErrorPlugin{mod: fakeModuleWithAPIError(tokenhubRule), name: "fake-tokenhub"}

	hint := p.apiErrorHint(openagent.APIErrorInfo{
		StatusCode: 429,
		BaseURL:    "http://tokenhub.example/v1",
		ModelID:    "glm-5.3",
		Err:        errors.New("POST: 429 Too Many Limits"),
	})
	if hint != "To fix this: configure your own provider quota." {
		t.Fatalf("hint = %q, want the tokenhub hint", hint)
	}
}

// TestAPIErrorHint_NonMatchingIsEmpty: a non-matching error (401) yields
// no hint — the plugin filters, the host appends only non-empty strings.
func TestAPIErrorHint_NonMatchingIsEmpty(t *testing.T) {
	p := &apiErrorPlugin{mod: fakeModuleWithAPIError(tokenhubRule), name: "fake-tokenhub"}

	if hint := p.apiErrorHint(openagent.APIErrorInfo{StatusCode: 401, BaseURL: "http://tokenhub.example/v1"}); hint != "" {
		t.Errorf("401 hint = %q, want empty", hint)
	}
	if hint := p.apiErrorHint(openagent.APIErrorInfo{StatusCode: 429, BaseURL: "http://other.example/v1"}); hint != "" {
		t.Errorf("non-tokenhub 429 hint = %q, want empty", hint)
	}
}

// TestAPIErrorHint_GuestFailureDegradesToEmpty: an invoking or parsing
// failure in the guest must degrade to "no hint", never an error.
func TestAPIErrorHint_GuestFailureDegradesToEmpty(t *testing.T) {
	p := &apiErrorPlugin{mod: fakeModuleWithAPIError(func(input map[string]any) (string, error) {
		return "", errors.New("guest exploded")
	}), name: "broken"}
	if hint := p.apiErrorHint(openagent.APIErrorInfo{StatusCode: 429, BaseURL: "http://tokenhub.example"}); hint != "" {
		t.Errorf("failed guest hint = %q, want empty", hint)
	}

	// Unparseable output (not JSON) — same degradation.
	p2 := &apiErrorPlugin{mod: fakeModuleWithAPIError(func(input map[string]any) (string, error) {
		return "not-json", nil
	}), name: "garbage"}
	// "not-json" is not valid JSON for APIErrorOutput — the fake marshals
	// via APIErrorOutput so it always parses; force garbage by wrapping a
	// rule that the fake will fail to... instead verify with a raw-output
	// module.
	p2.mod = &module{mod: newRawOutputModule(), hasAPIError: true}
	if hint := p2.apiErrorHint(openagent.APIErrorInfo{StatusCode: 429}); hint != "" {
		t.Errorf("unparseable output hint = %q, want empty", hint)
	}
}

// newRawOutputModule returns a module whose on_api_error returns bytes
// that are not valid JSON.
func newRawOutputModule() *fakeModule {
	mem := newFakeMemory()
	fm := &fakeModule{mem: mem, fns: map[string]*fakeFunction{}}
	fm.fns["alloc"] = &fakeFunction{fn: func(ctx context.Context, params []uint64) ([]uint64, error) {
		return []uint64{uint64(mem.alloc(uint32(params[0])))}, nil
	}}
	fm.fns["dealloc"] = &fakeFunction{fn: func(ctx context.Context, params []uint64) ([]uint64, error) {
		return nil, nil
	}}
	fm.fns["on_api_error"] = &fakeFunction{fn: func(ctx context.Context, params []uint64) ([]uint64, error) {
		garbage := []byte("not json at all")
		optr := mem.alloc(uint32(len(garbage)))
		mem.Write(optr, garbage)
		return []uint64{wasmhost.Pack(optr, uint32(len(garbage)))}, nil
	}}
	return fm
}

// TestManagerAPIErrorDecorator_FirstNonEmptyWins: plugins are consulted
// in load order; the first non-empty hint wins and later plugins are not
// consulted after a winner.
func TestManagerAPIErrorDecorator_FirstNonEmptyWins(t *testing.T) {
	var consulted []string
	var mu sync.Mutex
	track := func(name string) func(input map[string]any) (string, error) {
		return func(input map[string]any) (string, error) {
			mu.Lock()
			consulted = append(consulted, name)
			mu.Unlock()
			return "", nil
		}
	}
	mgr := &Manager{apiErrPlugins: []*apiErrorPlugin{
		{mod: fakeModuleWithAPIError(track("first")), name: "first"},
		{mod: fakeModuleWithAPIError(func(input map[string]any) (string, error) {
			mu.Lock()
			consulted = append(consulted, "second")
			mu.Unlock()
			return "second-wins", nil
		}), name: "second"},
		{mod: fakeModuleWithAPIError(func(input map[string]any) (string, error) {
			mu.Lock()
			consulted = append(consulted, "third")
			mu.Unlock()
			return "third", nil
		}), name: "third"},
	}}

	d := mgr.APIErrorDecorator()
	if d == nil {
		t.Fatal("decorator must be non-nil with eligible plugins")
	}
	if hint := d(openagent.APIErrorInfo{StatusCode: 429}); hint != "second-wins" {
		t.Fatalf("hint = %q, want second-wins (first non-empty)", hint)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(consulted) != 2 || consulted[0] != "first" || consulted[1] != "second" {
		t.Errorf("consulted = %v, want [first second] (winner stops the walk)", consulted)
	}
}

// TestManagerAPIErrorDecorator_BrokenPluginSkipped: a plugin whose guest
// fails is skipped and the next one still gets its chance.
func TestManagerAPIErrorDecorator_BrokenPluginSkipped(t *testing.T) {
	mgr := &Manager{apiErrPlugins: []*apiErrorPlugin{
		{mod: fakeModuleWithAPIError(func(input map[string]any) (string, error) {
			return "", errors.New("boom")
		}), name: "broken"},
		{mod: fakeModuleWithAPIError(func(input map[string]any) (string, error) {
			return "healthy-hint", nil
		}), name: "healthy"},
	}}

	if hint := mgr.APIErrorDecorator()(openagent.APIErrorInfo{StatusCode: 429}); hint != "healthy-hint" {
		t.Fatalf("hint = %q, want healthy-hint (broken plugin skipped)", hint)
	}
}

// TestManagerAPIErrorDecorator_NilWhenEmpty: no eligible plugins → nil
// (callers treat nil as "no decoration configured").
func TestManagerAPIErrorDecorator_NilWhenEmpty(t *testing.T) {
	if d := (&Manager{}).APIErrorDecorator(); d != nil {
		t.Fatal("empty manager must yield nil decorator")
	}
}

// TestAPIErrorDecorator_CallsSerialized: the decorator runs on model-call
// goroutines that 429 storms force into concurrency; module.invokeMu must
// serialize guest entry. The fake counts in-flight executions — the max
// must stay 1. Run with -race for the memory-model half of the contract.
func TestAPIErrorDecorator_CallsSerialized(t *testing.T) {
	var inFlight, maxInFlight int32
	mod := fakeModuleWithAPIError(func(input map[string]any) (string, error) {
		cur := atomic.AddInt32(&inFlight, 1)
		for {
			old := atomic.LoadInt32(&maxInFlight)
			if cur <= old || atomic.CompareAndSwapInt32(&maxInFlight, old, cur) {
				break
			}
		}
		atomic.AddInt32(&inFlight, -1)
		return "hint", nil
	})
	mgr := &Manager{apiErrPlugins: []*apiErrorPlugin{{mod: mod, name: "fake"}}}
	d := mgr.APIErrorDecorator()

	const n = 32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if hint := d(openagent.APIErrorInfo{StatusCode: 429}); hint != "hint" {
				t.Errorf("hint = %q, want hint", hint)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&maxInFlight); got != 1 {
		t.Fatalf("max in-flight guest calls = %d, want 1 (invokeMu must serialize)", got)
	}
}

// TestLoadModule_APIErrorProbe: the load-time probe must set hasAPIError
// from the on_api_error export's presence. Real PDK binaries — the
// observer example (built against the updated PDK) exports it; the tool
// example (built before the ABI) does not. Skips when binaries are not
// built (local-dev verification gate, same contract as the decision
// compat tests).
func TestLoadModule_APIErrorProbe(t *testing.T) {
	ctx := context.Background()
	ldr, cleanup := newLoaderWithHost(t, ctx)
	defer cleanup()

	obs := loadBinary(t, ctx, ldr, observerWasm)
	if !obs.hasAPIError {
		t.Error("observer binary (new PDK) should probe hasAPIError=true")
	}
	if _, err := os.Stat(toolWasm); err == nil {
		tool := loadBinary(t, ctx, ldr, toolWasm)
		if tool.hasAPIError {
			t.Error("stale tool binary (pre-ABI) should probe hasAPIError=false — rebuild removed the skip premise?")
		}
	} else {
		t.Skipf("tool binary not built, skipping the pre-ABI half: %v", err)
	}
}

// TestAPIErrorHint_RealBinaryRoundTrip: the real observer example's
// on_api_error (429 → non-empty hint, 401 → empty) through the actual
// wasm boundary. Skips when the binary is not built.
func TestAPIErrorHint_RealBinaryRoundTrip(t *testing.T) {
	ctx := context.Background()
	ldr, cleanup := newLoaderWithHost(t, ctx)
	defer cleanup()

	mod := loadBinary(t, ctx, ldr, observerWasm)
	if !mod.hasAPIError {
		t.Fatal("observer binary lacks on_api_error — rebuild examples/plugin/observer against the updated PDK")
	}
	p := &apiErrorPlugin{mod: mod, name: "observer_logger"}

	hint := p.apiErrorHint(openagent.APIErrorInfo{
		StatusCode: 429,
		BaseURL:    "http://tokenhub.example/v1",
		Err:        errors.New("POST: 429 Too Many Limits"),
	})
	if hint == "" {
		t.Error("real observer binary should return a non-empty hint for 429")
	}
	if hint := p.apiErrorHint(openagent.APIErrorInfo{StatusCode: 401}); hint != "" {
		t.Errorf("real observer binary 401 hint = %q, want empty", hint)
	}
}
