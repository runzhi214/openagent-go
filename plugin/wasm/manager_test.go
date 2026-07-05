package wasm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	openagent "github.com/yusheng-g/openagent-go"
)

// pluginsDir points to the pre-built .wasm files.
var pluginsDir string

func init() {
	// Find the plugins directory relative to the repo root.
	// When running with "go test ./..." the working directory is the package dir.
	cwd, _ := os.Getwd()
	dir := filepath.Join(cwd, "../../plugins")
	if _, err := os.Stat(dir); err == nil {
		pluginsDir = dir
	}
}

func TestEchoTool(t *testing.T) {
	if pluginsDir == "" {
		t.Skip("plugins directory not found")
	}

	ctx := context.Background()
	mgr := NewManager(pluginsDir)
	if err := mgr.Discover(ctx); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	defer mgr.Close()

	tools := mgr.Tools()
	if len(tools) == 0 {
		t.Fatal("no tool plugins loaded")
	}

	// Find the echo tool.
	var echo openagent.Tool
	for _, tl := range tools {
		if tl.Definition().Name == "echo" {
			echo = tl
			break
		}
	}
	if echo == nil {
		t.Fatal("echo tool not found")
	}

	def := echo.Definition()
	if def.Name != "echo" {
		t.Errorf("name = %q, want echo", def.Name)
	}
	if def.Description == "" {
		t.Error("description is empty")
	}

	// Execute echo tool.
	result, err := echo.Execute(ctx, json.RawMessage(`{"message":"hello"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result != "you said: hello" {
		t.Errorf("result = %q, want %q", result, "you said: hello")
	}
	t.Logf("echo tool: %s", result)
}

func TestStageLogger(t *testing.T) {
	if pluginsDir == "" {
		t.Skip("plugins directory not found")
	}

	ctx := context.Background()
	mgr := NewManager(pluginsDir)
	if err := mgr.Discover(ctx); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	defer mgr.Close()

	observer := mgr.Observer()
	if observer == nil {
		t.Fatal("observer is nil, expected stage plugin")
	}

	// Should not panic — stage plugin matches all events.
	observer.ObserveStage(ctx, openagent.StageEvent{
		Name:  openagent.StageModelCall,
		Phase: "enter",
	})
	observer.ObserveStage(ctx, openagent.StageEvent{
		Name:  openagent.StageModelCall,
		Phase: "leave",
	})
	t.Log("stage observer dispatched successfully")
}

func TestMultiplePlugins(t *testing.T) {
	if pluginsDir == "" {
		t.Skip("plugins directory not found")
	}

	ctx := context.Background()
	mgr := NewManager(pluginsDir)
	if err := mgr.Discover(ctx); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	defer mgr.Close()

	tools := mgr.Tools()
	observer := mgr.Observer()

	if len(tools) != 1 {
		t.Errorf("expected 1 tool, got %d", len(tools))
	}
	if observer == nil {
		t.Error("expected stage observer, got nil")
	}
	t.Logf("loaded: %d tool(s), stage observer: %v", len(tools), observer != nil)
}
