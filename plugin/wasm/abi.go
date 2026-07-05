// Package wasm provides a runtime plugin system via WASM modules.
// Plugins are loaded from a directory and exposed as standard openagent
// interfaces: Tool plugins → []Tool, Stage plugins → RunObserver.
//
// The plugin system is itself pluggable: if the user never creates a Manager
// or specifies no plugin directory, the system is inert.
//
//	// Without plugins: nothing changes.
//	agent := openagent.NewAgent("bot", openagent.WithModel(model))
//
//	// With plugins:
//	mgr := wasm.NewManager("./plugins")
//	mgr.Discover(ctx)
//	agent := openagent.NewAgent("bot",
//	    openagent.WithModel(model),
//	    openagent.WithTools(mgr.Tools()...),
//	    openagent.WithRunObserver(mgr.Observer()),
//	)
package wasm

import "encoding/json"

// ── Plugin metadata (returned by guest's metadata() export) ──

// PluginMeta is the JSON metadata blob every .wasm module exports via metadata().
type PluginMeta struct {
	Type        string          `json:"type"`        // "tool" or "stage"
	Name        string          `json:"name"`        // unique name
	Description string          `json:"description"` // human-readable
	Parameters  json.RawMessage `json:"parameters,omitempty"`  // tool: JSON Schema
	Stage       string          `json:"stage,omitempty"`       // stage: which stage
	Phase       string          `json:"phase,omitempty"`       // stage: "enter" | "leave" | "*"
}

// ── Stage input/output ──

// StageInput is passed to stage plugins' run().
type StageInput struct {
	Name   string         `json:"name"`             // stage constant e.g. "model.call"
	Phase  string         `json:"phase"`            // "enter" or "leave"
	Detail map[string]any `json:"detail,omitempty"` // optional metadata
	Error  string         `json:"error,omitempty"`  // non-empty if stage failed
}

// StageOutput is returned from stage plugins' run().
type StageOutput struct {
	Action string `json:"action"` // "continue" or "abort"
	Reason string `json:"reason,omitempty"`
}

// ── Tool input/output ──

// ToolInput is passed to tool plugins' execute().
type ToolInput struct {
	Args json.RawMessage `json:"args"`
}

// ToolOutput is returned from tool plugins' execute().
type ToolOutput struct {
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// ── Plugin type constants ──

const (
	PluginTypeTool  = "tool"
	PluginTypeStage = "stage"
)

// ── Stage name constants (match openagent.StageXxx) ──

const (
	StageMemoryFetch  = "memory.fetch"
	StageGuardIn      = "guard.in"
	StagePromptBuild  = "prompt.build"
	StageModelCall    = "model.call"
	StageGuardOut     = "guard.out"
	StageToolExecute  = "tool.execute"
	StageMemoryAppend = "memory.append"
)

// ── Stage phase constants ──

const (
	PhaseEnter = "enter"
	PhaseLeave = "leave"
	PhaseAll   = "*"
)

// ── Stage action constants ──

const (
	ActionContinue = "continue"
	ActionAbort    = "abort"
)
