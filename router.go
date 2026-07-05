package openagent

import (
	"context"
	"fmt"
	"strings"
)

// AgentInfo is a summary of an agent exposed to the Router.
type AgentInfo struct {
	Name        string
	Description string
	Type        AgentType // internal (in-process) or external (ACP/MCP)
}

// Router decides message routing in a Team.
//
// Two responsibilities:
//   - Route: pick the first agent for a new user message
//   - CanHandoff: policy gate on handoffs (nil return = allowed)
//
// nil Router = all agents are valid targets, first agent handles all input.
type Router interface {
	// Route picks the initial agent for a user message.
	// The returned name must match an agent in the Team.
	Route(ctx context.Context, input Message, agents []AgentInfo) (string, error)

	// CanHandoff checks whether a handoff should proceed.
	// Return nil if allowed. Return an error to veto — the error message
	// is injected as a hint into the next agent's prompt.
	// Called before each handoff (not for initial routing).
	CanHandoff(ctx context.Context, entry HandoffEntry, chain []HandoffEntry, session Session) error
}

// ── Default Router: first-agent-wins ──

// FirstAgentRouter routes every message to the first agent in the Team.
// It never vetoes handoffs. Suitable for single-agent or fully model-driven
// handoff scenarios.
type FirstAgentRouter struct{}

func (FirstAgentRouter) Route(_ context.Context, _ Message, agents []AgentInfo) (string, error) {
	if len(agents) == 0 {
		return "", fmt.Errorf("team has no agents")
	}
	return agents[0].Name, nil
}

func (FirstAgentRouter) CanHandoff(_ context.Context, _ HandoffEntry, _ []HandoffEntry, _ Session) error {
	return nil // never veto
}

// ── LLM Router ──

// LLMRouter uses a Model to decide routing. For Route, it asks the model
// which agent best matches the user's intent. CanHandoff always allows.
type LLMRouter struct {
	model Model
}

// NewLLMRouter creates a Router backed by a judge model.
func NewLLMRouter(model Model) *LLMRouter {
	return &LLMRouter{model: model}
}

func (r *LLMRouter) Route(ctx context.Context, input Message, agents []AgentInfo) (string, error) {
	if len(agents) == 0 {
		return "", fmt.Errorf("team has no agents")
	}
	if len(agents) == 1 {
		return agents[0].Name, nil
	}

	prompt := buildRouterPrompt(agents)
	resp, err := r.model.ChatCompletion(ctx, ChatCompletionRequest{
		Messages: []Message{
			{Role: RoleSystem, Content: prompt},
			{Role: RoleUser, Content: input.Content},
		},
		MaxTokens: 64,
	})
	if err != nil || len(resp.Choices) == 0 {
		// Fall back to first agent on model error or empty response
		return agents[0].Name, nil
	}

	// The model should return just the agent name
	chosen := resp.Choices[0].Message.Content
	for _, a := range agents {
		if a.Name == chosen {
			return a.Name, nil
		}
	}
	// Fuzzy match: check if any agent name appears in the response
	for _, a := range agents {
		if containsWord(chosen, a.Name) {
			return a.Name, nil
		}
	}
	return agents[0].Name, nil
}

func (r *LLMRouter) CanHandoff(_ context.Context, _ HandoffEntry, _ []HandoffEntry, _ Session) error {
	return nil
}

func buildRouterPrompt(agents []AgentInfo) string {
	p := "You are a router. Given a user message, pick the best agent.\n\nAgents:\n"
	for _, a := range agents {
		p += fmt.Sprintf("- %s: %s\n", a.Name, a.Description)
	}
	p += "\nReply with ONLY the agent name (one word)."
	return p
}

func containsWord(s, word string) bool {
	return strings.Contains(s, word)
}
