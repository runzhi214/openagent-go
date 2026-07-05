package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"

	openagent "github.com/yusheng-g/openagent-go"
)

// Summarizer implements openagent.Summarizer via OpenAI ChatCompletion.
type Summarizer struct {
	client  openaisdk.Client
	modelID string
}

// NewSummarizer creates a Summarizer. modelID defaults to the same model
// used for conversations (chat model, not embedding model).
func NewSummarizer(apiKey, modelID, baseURL string) *Summarizer {
	return &Summarizer{
		client: openaisdk.NewClient(
			option.WithAPIKey(apiKey),
			option.WithBaseURL(baseURL),
		),
		modelID: modelID,
	}
}

// Summarize compresses messages into a summary with retrieval hints.
func (s *Summarizer) Summarize(ctx context.Context, messages []openagent.Message) (*openagent.CompressedContext, error) {
	// Build conversation transcript
	var transcript strings.Builder
	for _, m := range messages {
		transcript.WriteString(string(m.Role))
		transcript.WriteString(": ")
		transcript.WriteString(m.Content)
		if len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				transcript.WriteString("\n  tool_call: ")
				transcript.WriteString(tc.Function.Name)
				transcript.WriteString("(")
				transcript.WriteString(tc.Function.Arguments)
				transcript.WriteString(")")
			}
		}
		if m.Role == openagent.RoleTool {
			transcript.WriteString(" // tool result")
		}
		transcript.WriteString("\n")
	}

	params := openaisdk.ChatCompletionNewParams{
		Model: openaisdk.ChatModel(s.modelID),
		Messages: []openaisdk.ChatCompletionMessageParamUnion{
			openaisdk.SystemMessage(summarizerPrompt),
			openaisdk.UserMessage(transcript.String()),
		},
		Temperature: param.NewOpt(0.3),
		MaxTokens:   param.NewOpt(int64(1024)),
	}

	completion, err := s.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("summarize: %w", err)
	}

	content := completion.Choices[0].Message.Content
	return parseSummaryResponse(content)
}

func parseSummaryResponse(content string) (*openagent.CompressedContext, error) {
	// Strip markdown code fences if present
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var cc openagent.CompressedContext
	if err := json.Unmarshal([]byte(content), &cc); err != nil {
		// Fallback: treat entire response as a plain summary
		return &openagent.CompressedContext{Summary: content}, nil
	}
	return &cc, nil
}

const summarizerPrompt = `You are a conversation summarizer. Compress the following conversation into:

1. A concise SUMMARY (2-4 sentences) capturing key facts, decisions, and context.
2. 0-5 RETRIEVAL HINTS — each a short label + a search query to find the original details.

Respond with ONLY valid JSON (no markdown, no commentary):
{
  "summary": "string",
  "hints": [
    {"description": "short label", "query": "search terms"}
  ]
}

Rules:
- Preserve user preferences, personal info, and factual claims verbatim.
- Hints should be specific search queries, not generic descriptions.
- If the conversation is trivial (greetings, chitchat), hints can be empty.`

var _ openagent.Summarizer = (*Summarizer)(nil)
