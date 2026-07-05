package openagent

import (
	"context"
	"encoding/json"
)

// RunHooks provides lifecycle callbacks in the Runner mainline.
// Naming follows OpenAI Agents SDK RunHooks conventions.
// nil RunHooks = no callbacks.
type RunHooks interface {
	// OnAgentStart is called once when agent.Run() begins, before the loop.
	OnAgentStart(ctx context.Context, req ChatCompletionRequest) error
	// OnAgentEnd is called once when agent.Run() finishes (success, error, or cancel).
	OnAgentEnd(ctx context.Context, req ChatCompletionRequest, resp *ChatCompletionResponse, err error)
	// OnToolStart is called before each Tool.Execute.
	OnToolStart(ctx context.Context, tool FunctionDefinition, args json.RawMessage) error
	// OnToolEnd is called after each Tool.Execute finishes.
	OnToolEnd(ctx context.Context, tool FunctionDefinition, args json.RawMessage, result string, err error)
}
