// Package acp provides an abstraction over the Agent Client Protocol (ACP).
//
// It defines openagent-go's own ACP types and a Client for connecting to
// external ACP agent processes. The default implementation uses
// coder/acp-go-sdk internally, but the interfaces are designed so that
// alternative implementations can be plugged in.
//
// Import as:
//
//	openacp "github.com/yusheng-g/openagent-go/acp"
package acp

// ── Content / Prompt ──

// ContentBlock represents a piece of content in a prompt.
type ContentBlock struct {
	Text string // plain text or markdown
}

// PromptRequest is the input to an agent prompt turn.
type PromptRequest struct {
	SessionID string
	Blocks    []ContentBlock
}

// PromptResponse is the result of a prompt turn.
type PromptResponse struct {
	StopReason string // "end_turn", "cancelled", "refusal", etc.
}

// ── Session ──

// NewSessionRequest configures a new ACP session.
type NewSessionRequest struct {
	Cwd        string
	McpServers []McpServer
}

// NewSessionResponse is the result of creating a session.
type NewSessionResponse struct {
	SessionID string
}

// McpServer describes an MCP server the agent should connect to.
// For HTTP/S: set URL. For stdio: set Command + Args.
type McpServer struct {
	Name    string
	URL     string   // HTTP endpoint (e.g. "http://localhost:PORT")
	Command string   // executable path (for stdio MCP servers)
	Args    []string // command arguments
}

// ── Initialize ──

// InitializeRequest is the ACP handshake request.
type InitializeRequest struct {
	ProtocolVersion int
	ClientName      string
	ClientVersion   string
}

// InitializeResponse is the ACP handshake response.
type InitializeResponse struct {
	ProtocolVersion int
	AgentName       string
	AgentVersion    string
}

// ── Event handler ──

// EventHandler receives streaming events from the agent during Prompt.
// Implement this interface and register it via Session.SetEventHandler
// to receive real-time updates.
type EventHandler interface {
	OnAgentMessage(text string)
	OnAgentThought(text string)
	OnToolCall(tc ToolCallEvent)
}

// ToolCallEvent represents a tool invocation by the agent.
type ToolCallEvent struct {
	ID        string
	Title     string
	RawInput  any    // parameters sent to the tool
	Status    string // "in_progress", "completed", "failed"
	RawOutput any    // tool result (when Status == "completed")
}
