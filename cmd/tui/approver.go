package main

import (
	"context"

	openagent "github.com/yusheng-g/openagent-go"
)

// approveRequest bridges the synchronous Approver interface to the
// asynchronous bubbletea main loop via channels.
type approveRequest struct {
	call    openagent.ToolCall
	respond chan approveResponse
}

type approveResponse struct {
	allowed bool
	reason  string
}

// TUIApprover implements openagent.Approver. When Approve is called by the
// runner, it sends a request to the bubbletea main loop and blocks until
// the user makes a decision (Y/N keypress).
type TUIApprover struct {
	requests chan<- approveRequest
}

func (a *TUIApprover) Approve(ctx context.Context, call openagent.ToolCall, _ openagent.FunctionDefinition, _ openagent.Session) (bool, string) {
	resp := make(chan approveResponse, 1)
	select {
	case a.requests <- approveRequest{call: call, respond: resp}:
	case <-ctx.Done():
		return false, ctx.Err().Error()
	}

	select {
	case <-ctx.Done():
		return false, ctx.Err().Error()
	case r := <-resp:
		return r.allowed, r.reason
	}
}
