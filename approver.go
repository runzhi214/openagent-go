package openagent

import "context"

// Approver decides whether a tool call should be executed. Called before
// each Tool.Execute. nil Approver = all tool calls are allowed.
//
// The FunctionDefinition carries the tool's description and parameter schema
// so the approval UI can present meaningful context to the user.
type Approver interface {
	Approve(ctx context.Context, call ToolCall, def FunctionDefinition, session Session) (allowed bool, reason string)
}
