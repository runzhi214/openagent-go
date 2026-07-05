package openagent

import (
	"context"
	"encoding/json"
)

// FunctionDefinition is the JSON Schema definition of a tool function.
// Follows OpenAI function calling format.
type FunctionDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"` // JSON Schema

	// EndTurn, when true, tells the runner to end the agent turn loop
	// immediately after executing this tool. Used by handoff tools
	// (transfer_to_*) — aligning with OpenAI Agents SDK's NextStepHandoff.
	EndTurn bool `json:"-"`
}

// Tool represents a callable tool. Both local tools and MCP-imported tools
// implement this interface — the Runner does not distinguish between them.
type Tool interface {
	Definition() FunctionDefinition
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// ToolStreamChunk is a single chunk of streaming output from a tool that
// implements [StreamExecutor]. Chunks are concatenated to form the final
// tool result; they are also emitted as [StreamToolProgress] events for
// real-time display.
type ToolStreamChunk struct {
	Content string `json:"content"`
	Error   error  `json:"-"`
}

// StreamExecutor is an optional interface for tools that produce streaming
// output during execution. The Runner checks for this interface before
// calling [Tool.Execute]:
//
//	if se, ok := tool.(StreamExecutor); ok {
//	    // streaming path — chunks emitted as StreamToolProgress events
//	} else {
//	    // blocking path — Tool.Execute, no intermediate events
//	}
//
// The chunks returned by ExecuteStream are concatenated to produce the
// final tool result. Tools that implement this interface should close
// the channel when execution is complete.
type StreamExecutor interface {
	ExecuteStream(ctx context.Context, args json.RawMessage) <-chan ToolStreamChunk
}
