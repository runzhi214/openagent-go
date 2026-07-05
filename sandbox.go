package openagent

import "context"

// Command represents a command to execute in a sandbox.
type Command struct {
	Program string   // executable name or path
	Args    []string // arguments
	Env     []string // environment variables (KEY=VALUE)
	WorkDir string   // working directory
	Stdin   string   // optional stdin content
}

// Result is the output of a command executed in a sandbox.
type Result struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// Sandbox isolates command execution. Tool implementations that execute
// code (ShellTool, CodeTool, etc.) use this interface. nil Sandbox = no
// sandbox provided — tools should refuse to execute or use a default.
//
// NOTE: This interface is defined for future use. No built-in tools or
// examples currently implement it. See BUGS.md #35.
type Sandbox interface {
	Run(ctx context.Context, cmd Command) (Result, error)
}
