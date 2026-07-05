// Package slog implements openagent.RunHooks with log/slog.
// Zero external dependencies — uses only the standard library.
//
// Usage:
//
//	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
//	hooks := sloghooks.New(logger)
//	agent := openagent.NewAgent("bot", openagent.WithRunHooks(hooks))
package slog

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	openagent "github.com/yusheng-g/openagent-go"
)

// Hooks implements openagent.RunHooks via log/slog.
type Hooks struct {
	logger *slog.Logger
}

// New creates a Hooks that logs to the given slog.Logger.
func New(logger *slog.Logger) *Hooks {
	return &Hooks{logger: logger}
}

func (h *Hooks) OnAgentStart(ctx context.Context, req openagent.ChatCompletionRequest) error {
	msgCount := len(req.Messages)
	h.logger.InfoContext(ctx, "agent start",
		"model", req.Model,
		"messages", msgCount,
		"tools", len(req.Tools),
	)
	return nil
}

func (h *Hooks) OnAgentEnd(ctx context.Context, req openagent.ChatCompletionRequest, resp *openagent.ChatCompletionResponse, err error) {
	attrs := []slog.Attr{
		slog.String("model", req.Model),
	}
	if resp != nil {
		attrs = append(attrs,
			slog.Int("prompt_tokens", resp.Usage.PromptTokens),
			slog.Int("completion_tokens", resp.Usage.CompletionTokens),
			slog.Int("total_tokens", resp.Usage.TotalTokens),
		)
	}
	if err != nil {
		attrs = append(attrs, slog.String("error", err.Error()))
		h.logger.LogAttrs(ctx, slog.LevelError, "agent end", attrs...)
	} else {
		h.logger.LogAttrs(ctx, slog.LevelInfo, "agent end", attrs...)
	}
}

func (h *Hooks) OnToolStart(ctx context.Context, tool openagent.FunctionDefinition, args json.RawMessage) error {
	start := time.Now()
	// Store start time in context for OnToolEnd — not possible with this interface,
	// so we log it at start and trust the caller to call OnToolEnd next.
	_ = start
	h.logger.DebugContext(ctx, "tool start",
		"tool", tool.Name,
		"args", string(args),
	)
	return nil
}

func (h *Hooks) OnToolEnd(ctx context.Context, tool openagent.FunctionDefinition, args json.RawMessage, result string, err error) {
	attrs := []slog.Attr{
		slog.String("tool", tool.Name),
	}
	if err != nil {
		attrs = append(attrs, slog.String("error", err.Error()))
		h.logger.LogAttrs(ctx, slog.LevelError, "tool end", attrs...)
	} else {
		attrs = append(attrs, slog.Int("result_len", len(result)))
		h.logger.LogAttrs(ctx, slog.LevelDebug, "tool end", attrs...)
	}
}

var _ openagent.RunHooks = (*Hooks)(nil)
