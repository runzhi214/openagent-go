// Package mcp integrates openagent-go with the Model Context Protocol (MCP).
//
// It provides:
//   - Server: expose openagent.Tool instances as MCP tools
//   - Client: import MCP server tools as openagent.Tool instances
//
// Import as:
//
//	openmcp "github.com/yusheng-g/openagent-go/mcp"
package mcp

import (
	"encoding/json"
	"fmt"

	openagent "github.com/yusheng-g/openagent-go"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToMCPTool converts an openagent FunctionDefinition to an MCP Tool.
// The InputSchema is passed through as-is (json.RawMessage is valid JSON Schema).
func ToMCPTool(def openagent.FunctionDefinition) *mcpsdk.Tool {
	return &mcpsdk.Tool{
		Name:        def.Name,
		Description: def.Description,
		InputSchema: def.Parameters,
	}
}

// ToFunctionDefinition converts an MCP Tool to an openagent FunctionDefinition.
func ToFunctionDefinition(t mcpsdk.Tool) (openagent.FunctionDefinition, error) {
	params, err := json.Marshal(t.InputSchema)
	if err != nil {
		return openagent.FunctionDefinition{}, fmt.Errorf("mcp: marshal tool %q schema: %w", t.Name, err)
	}
	return openagent.FunctionDefinition{
		Name:        t.Name,
		Description: t.Description,
		Parameters:  json.RawMessage(params),
	}, nil
}
