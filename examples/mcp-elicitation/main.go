// Elicitation demo MCP server — the acceptance fixture for the
// MCP → ACP elicitation bridge (docs/elicitation.md).
//
// It exposes one tool, ask_user, which sends a form-mode elicitation
// request MID-CALL and echoes the user's answer back as the tool result.
// Wire it into an ACP frontend (AI_Shell_WEB via acpws) to see the form
// popup end to end:
//
//	settings.json:
//	  "mcp_servers": {
//	    "elicit-demo": { "command": "/path/to/elicitation-demo" }
//	  }
//
//	go run ./examples/mcp-elicitation/
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// askUserArgs is the tool input (what the model supplies).
type askUserArgs struct {
	Topic string `json:"topic" description:"what to ask the user about"`
}

func main() {
	ctx := context.Background()

	server := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:    "elicitation-demo",
		Version: "1.0.0",
	}, nil)

	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "ask_user",
		Description: "Ask the user a structured question mid-call (form elicitation) and return their answer.",
	}, askUser)

	if err := server.Run(ctx, &mcpsdk.StdioTransport{}); err != nil {
		fmt.Fprintf(os.Stderr, "elicitation-demo: %v\n", err)
		os.Exit(1)
	}
}

func askUser(ctx context.Context, req *mcpsdk.CallToolRequest, args askUserArgs) (*mcpsdk.CallToolResult, any, error) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"strategy": map[string]any{
				"type":        "string",
				"enum":        []string{"conservative", "balanced", "aggressive"},
				"description": "How to approach: " + args.Topic,
			},
			"replicas": map[string]any{
				"type":        "number",
				"minimum":     1,
				"maximum":     10,
				"description": "How many replicas to provision",
			},
			"notes": map[string]any{
				"type":        "string",
				"description": "Optional free-form notes",
			},
		},
		"required": []string{"strategy"},
	}

	res, err := req.Session.Elicit(ctx, &mcpsdk.ElicitParams{
		Message:         fmt.Sprintf("The ask_user tool needs your input (topic: %s).", args.Topic),
		RequestedSchema: schema,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("elicitation failed: %w", err)
	}

	switch res.Action {
	case "accept":
		body, _ := json.MarshalIndent(res.Content, "", "  ")
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "User accepted:\n" + string(body)}},
		}, nil, nil
	case "decline":
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "User declined the form."}},
		}, nil, nil
	default: // "cancel" and anything else
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "User dismissed the form (cancelled)."}},
		}, nil, nil
	}
}
