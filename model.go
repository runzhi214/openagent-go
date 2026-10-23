package openagent

import (
	"context"
	"time"
)

// ChatCompletionRequest is the input to a model call.
// Follows OpenAI Chat Completions API format.
type ChatCompletionRequest struct {
	Model       string               `json:"model"`
	Messages    []Message            `json:"messages"`
	Tools       []FunctionDefinition `json:"tools,omitempty"`
	Temperature *float64             `json:"temperature,omitempty"` // nil = model default
	MaxTokens   int                  `json:"max_tokens,omitempty"`  // 0 = model default
	TopP        *float64             `json:"top_p,omitempty"`       // nil = model default
	Stop        []string             `json:"stop,omitempty"`
	// The value must be in the model's vocabulary — see ReasoningEffortModeler.
	// Providers accept subsets of "none","minimal","low","medium","high","max".
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	Stream          bool   `json:"stream,omitempty"`
}

// ChatCompletionResponse is the result of a model call.
type ChatCompletionResponse struct {
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage,omitempty"`
}

// Choice is a single completion choice.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Usage records token counts for a model call.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	// CacheReadTokens is the number of input tokens served from the
	// provider's prompt cache (billed at the cache-read rate, not the
	// input rate). 0 when the provider reports no caching.
	CacheReadTokens int `json:"cache_read_tokens,omitempty"`
}

// Model is the interface for LLM providers. Implementations handle API-specific
// details (OpenAI, Anthropic, etc.). The Runner calls ChatCompletionStream by
// default and falls back to ChatCompletion if streaming is not available.
type Model interface {
	ChatCompletion(ctx context.Context, req ChatCompletionRequest) (*ChatCompletionResponse, error)

	// ChatCompletionStream returns a stream of response deltas.
	// Return nil, nil if streaming is not supported (Runner falls back to ChatCompletion).
	ChatCompletionStream(ctx context.Context, req ChatCompletionRequest) (StreamReader, error)

	// ContextWindow returns the maximum input token count for this model.
	// Return 0 if unknown (Runner will not auto-compact).
	ContextWindow() int
}

// modelCtxKey is the context key for Model propagation. Unexported to
// prevent external packages from colliding (same pattern as sessionCtxKey).
type modelCtxKey struct{}

// WithModel returns a copy of ctx with the run's resolved model attached.
// The kernel stamps it at run() entry (after session.Model override) so
// downstream LLM calls that are not the main model call — compaction via
// the summarizer, most notably — follow the model the user is actually
// conversing with, including mid-session switches (each run re-stamps).
// Callers recover it via ModelFromContext.
func WithModel(ctx context.Context, m Model) context.Context {
	return context.WithValue(ctx, modelCtxKey{}, m)
}

// ModelFromContext extracts the model the kernel stamped into ctx for the
// current run. ok=false when no model was stamped (custom callers, tests)
// — the caller falls back to its own resolution.
func ModelFromContext(ctx context.Context) (Model, bool) {
	m, ok := ctx.Value(modelCtxKey{}).(Model)
	return m, ok
}

// TokenizerModeler is an optional interface for Models that know which
// tokenizer encoding to use. The Runner calls this to get a canonical
// model name for tiktoken rather than using session.ModelID (which may
// be a user-assigned label like "deepseek-v3" that tiktoken doesn't map).
type TokenizerModeler interface {
	TokenizerModel() string
}

// ReasoningEffortModeler is an optional interface for Models that declare
// which reasoning_effort values their backing provider accepts. The ACP
// server uses it to generate and validate the thought_level option; the
// kernel drops efforts outside the vocabulary before they reach the wire.
// A nil/empty vocabulary means unrestricted (any effort passes through).
type ReasoningEffortModeler interface {
	ReasoningEfforts() []string
}

// DefaultReasoningEfforts is the vocabulary assumed when a model declares
// none — no settings reasoning_efforts and no built-in table match.
func DefaultReasoningEfforts() []string {
	return []string{"low", "medium", "high"}
}

// DefaultReasoningEffort picks the default effort for a vocabulary:
// "medium" when present (the historical default), else "high" (the
// closest tier on always-thinking models), else the last declared entry.
// An empty vocabulary yields "".
func DefaultReasoningEffort(vocab []string) string {
	if len(vocab) == 0 {
		return ""
	}
	for _, want := range []string{"medium", "high"} {
		for _, v := range vocab {
			if v == want {
				return v
			}
		}
	}
	return vocab[len(vocab)-1]
}

// AllowsReasoningEffort reports whether effort is within vocab. An empty
// vocabulary (or empty effort) means unrestricted.
func AllowsReasoningEffort(vocab []string, effort string) bool {
	if len(vocab) == 0 || effort == "" {
		return true
	}
	for _, v := range vocab {
		if v == effort {
			return true
		}
	}
	return false
}

// ModelLister is an optional interface for Models backed by a provider
// that exposes a model discovery endpoint (OpenAI-compatible /models:
// OpenAI, OpenRouter, Together, Groq, Ollama, ...). ListModels returns the
// models available on the provider so the caller can auto-register them
// instead of requiring every model ID in settings.json.
//
// The endpoint path is relative to the Model's configured baseURL — the
// caller decides the /v1 prefix, not this interface. Fields beyond ID
// (max input/output tokens, pricing) come from the built-in lookup table;
// providers that include extra fields in the response (OpenRouter's
// context_length / pricing) override the table.
type ModelLister interface {
	ListModels(ctx context.Context) ([]AvailableModel, error)
}

// AvailableModel describes a model discovered via the provider's /models
// endpoint. It mirrors the fields a manually-configured ModelConfig carries
// so a discovered model can be registered with the same completeness as a
// hand-written one. Zero values mean "unknown" — the caller falls back to
// defaults.
type AvailableModel struct {
	ID                       string
	OwnedBy                  string
	MaxInputTokens           int
	MaxOutputTokens          int
	InputCostPerMillion      float64
	InputCacheCostPerMillion float64
	OutputCostPerMillion     float64
}

// ── Streaming types ──

// StreamReader is an iterator over response deltas.
// Follows the pattern of openai-go v3 ssestream.Stream[T].
type StreamReader interface {
	Next() bool           // advance to next chunk
	Current() StreamChunk // current chunk (valid after Next() == true)
	Err() error           // stream error (check after Next() == false)
	Close() error
}

// StreamChunk is a single chunk in a streaming response.
type StreamChunk struct {
	Choices []StreamDelta `json:"choices"`
	Usage   *Usage        `json:"usage,omitempty"` // last chunk may include usage
}

// StreamDelta is the delta content within a chunk.
type StreamDelta struct {
	Content          string          `json:"content,omitempty"`           // text delta (empty if tool call)
	ReasoningContent string          `json:"reasoning_content,omitempty"` // reasoning/thinking (o1, deepseek-r1)
	ToolCalls        []ToolCallDelta `json:"tool_calls,omitempty"`        // incremental tool call fragments
	FinishReason     string          `json:"finish_reason,omitempty"`     // set in final chunk
}

// ToolCallDelta is an incremental tool call update in a stream chunk.
type ToolCallDelta struct {
	Index    int           `json:"index"` // 0-based, fragments with same index belong together
	ID       string        `json:"id,omitempty"`
	Type     string        `json:"type,omitempty"`
	Function FunctionDelta `json:"function,omitempty"`
}

// FunctionDelta is an incremental function name/arguments update.
type FunctionDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"` // JSON fragment, concatenated across chunks
}

// ── Embedding ──

// Embedder converts text to a dense vector. Implementations: OpenAI, local models.
// nil Embedder means semantic search is disabled; Memory falls back to keyword search.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float64, error)
	Dimensions() int
}

// ── Summarizer ──

// Summarizer compresses old messages into a summary for context window management.
// nil means auto-compaction is disabled.
//
// When previous is non-nil, this is an incremental (rolling) compression —
// the implementation should incorporate the existing summary rather than
// re-summarizing from scratch. The returned CompressedContext.ThroughIndex
// should be set by the caller (memory backend), not the implementation.
type Summarizer interface {
	Summarize(ctx context.Context, messages []Message, previous *CompressedContext) (*CompressedContext, error)
}

// ── Retry ──

// RetryableError wraps a transient error (429, 503) so the Runner can retry.
type RetryableError struct {
	Err        error
	RetryAfter time.Duration // 0 = use default exponential backoff
}

func (e *RetryableError) Error() string { return e.Err.Error() }
func (e *RetryableError) Unwrap() error { return e.Err }

// ── API error decoration ──

// APIErrorInfo describes one provider API error for an APIErrorDecorator.
// StatusCode is the provider's HTTP status (a 0 means the error never
// reached HTTP — decorators are not consulted for those).
type APIErrorInfo struct {
	StatusCode int
	BaseURL    string
	ModelID    string
	Err        error
}

// APIErrorDecorator replaces the user-visible message of a provider API
// error. It returns the complete replacement text ("" = keep the original
// message). The Model backend wraps the error in a ReplacedError when the
// return is non-empty — retryability, Retry-After, and the original error
// (via Unwrap) are preserved; only the displayed text changes.
type APIErrorDecorator func(APIErrorInfo) string

// ReplacedError carries a decorator-provided message that replaces the
// error's display text. The original error stays in the unwrap chain so
// classification (errors.As → *RetryableError, Retry-After) and
// programmatic inspection survive the replacement; only what humans see
// changes. The kernel delivers it as-is on retry exhaustion — no "max
// retries exceeded" prefix, because the message is already user-facing.
type ReplacedError struct {
	Message string
	Err     error
}

func (e *ReplacedError) Error() string { return e.Message }
func (e *ReplacedError) Unwrap() error { return e.Err }
