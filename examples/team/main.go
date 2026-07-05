// Team example: demonstrates multi-agent orchestration with handoff.
//
//	go run ./examples/team/
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	openagent "github.com/yusheng-g/openagent-go"
	"github.com/yusheng-g/openagent-go/model/openai"
)

func main() {
	apiKey := os.Getenv("OPENAGENT_API_KEY")
	modelID := os.Getenv("OPENAGENT_MODEL")
	baseURL := os.Getenv("OPENAGENT_BASE_URL")

	sharedModel := openai.New(apiKey, modelID, baseURL).
		WithContextWindow(128_000)

	// ── Researcher agent: no tools, just thinks and hands off ──
	researcher := openagent.NewAgent("researcher",
		openagent.WithModel(sharedModel),
		openagent.WithInstructions(`You are a researcher. Your job:
1. Analyze the user's question
2. If it involves calculation, hand off to the calculator agent with a clear math expression
3. If it's a knowledge question, answer it yourself
Be concise.`),
		openagent.WithMaxTurns(2),
	)

	// ── Calculator agent: has calc tool ──
	calculator := openagent.NewAgent("calculator",
		openagent.WithModel(sharedModel),
		openagent.WithInstructions(`You are a calculator. Use the calc tool for arithmetic.
After getting the result, explain it clearly to the user.
Do NOT hand off to anyone — just give the answer.`),
		openagent.WithTools(&calcTool{}),
		openagent.WithMaxTurns(3),
	)

	// ── Build team ──
	team := openagent.NewTeam(
		openagent.WithTeamAgent("researcher", "Analyzes questions, decides if calculation is needed", researcher),
		openagent.WithTeamAgent("calculator", "Performs arithmetic calculations with the calc tool", calculator),
		openagent.WithTeamMaxHandoffs(5),
	)

	ctx := context.Background()
	session := openagent.Session{
		ID:        "team-session-1",
		UserID:    "user-1",
		AgentName: "team",
		ModelID:   modelID,
		CreatedAt: time.Now(),
	}

	// ── Run ──
	fmt.Println("=== Team: researcher + calculator ===")
	fmt.Printf("User: What's 15 * 23 + 100 / 4?\n\n")

	result, err := team.Run(ctx, session, openagent.UserMessage("What's 15 * 23 + 100 / 4?"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Final output: %s\n", result.FinalOutput)
	fmt.Printf("Handoffs: %d\n", len(result.HandoffChain))
	for i, h := range result.HandoffChain {
		fmt.Printf("  %d. %s → %s: %s\n", i+1, h.From, h.To, h.Message)
	}
	fmt.Printf("Total turns: %d\n", result.TotalTurns)
	fmt.Printf("Tokens: prompt=%d completion=%d total=%d\n",
		result.Usage.PromptTokens, result.Usage.CompletionTokens, result.Usage.TotalTokens)
}

// ── Calculator Tool ──

type calcTool struct{}

func (t *calcTool) Definition() openagent.FunctionDefinition {
	return openagent.FunctionDefinition{
		Name:        "calc",
		Description: "Evaluate a math expression. Supports +, -, *, /. Example: '15*23+100/4'",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"expression":{"type":"string","description":"The expression to evaluate"}},"required":["expression"]}`),
	}
}

func (t *calcTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Expression string `json:"expression"`
	}
	json.Unmarshal(args, &params)
	return evaluate(params.Expression), nil
}

// Simple expression evaluator (no eval, just +-*/ on ints).
func evaluate(expr string) string {
	expr = strings.ReplaceAll(expr, " ", "")
	if expr == "" {
		return "0"
	}
	// Handle single number
	if n, ok := parseNum(expr); ok && len(expr) == len(fmt.Sprint(n)) {
		return fmt.Sprint(n)
	}
	// Find rightmost + or - (lowest precedence, left-to-right)
	for i := len(expr) - 1; i >= 0; i-- {
		if expr[i] == '+' {
			return fmt.Sprint(mustInt(evaluate(expr[:i])) + mustInt(evaluate(expr[i+1:])))
		}
		if expr[i] == '-' && i > 0 && !isOp(expr[i-1]) {
			return fmt.Sprint(mustInt(evaluate(expr[:i])) - mustInt(evaluate(expr[i+1:])))
		}
	}
	// Find rightmost * or /
	for i := len(expr) - 1; i >= 0; i-- {
		if expr[i] == '*' {
			return fmt.Sprint(mustInt(evaluate(expr[:i])) * mustInt(evaluate(expr[i+1:])))
		}
		if expr[i] == '/' {
			div := mustInt(evaluate(expr[i+1:]))
			if div == 0 {
				return "error: division by zero"
			}
			return fmt.Sprint(mustInt(evaluate(expr[:i])) / div)
		}
	}
	return expr
}

func isOp(b byte) bool { return b == '+' || b == '-' || b == '*' || b == '/' }

func parseNum(s string) (int, bool) {
	var n int
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

func mustInt(s string) int {
	n, _ := parseNum(s)
	return n
}
