package openagent

import (
	"context"
	"io"
)

// Memory stores and retrieves conversation history. Three-layer model:
//
//	Layer 1: Working    — recent N turns, kept verbatim in context.
//	                       Recent() auto-compacts when messages exceed the limit.
//	Layer 2: Compressed — summary + retrieval hints for older turns.
//	                       Updated automatically by Recent() when Summarizer is configured.
//	Layer 3: Archive    — full history, searchable on demand via Search().
//
// All methods are scoped by sessionID. nil Memory = no persistence,
// each run starts fresh.
//
// Memory embeds io.Closer — implementations that hold external resources
// (database connections, file handles) must release them in Close().
// Callers that receive a Memory interface should defer Close() to prevent
// resource leaks. Implementations that don't hold resources may return nil.
type Memory interface {
	io.Closer

	// Layer 1: Working memory — most recent N turns, uncompressed.
	// If a Summarizer is configured and stored messages exceed the limit,
	// old messages are automatically compressed and the result is saved
	// via Layer 2.
	Recent(ctx context.Context, sessionID string, n int) ([]Message, error)

	// Layer 2: Compressed context — summary + hints for history beyond Working.
	// Returns nil, nil if no compression has occurred yet.
	Compressed(ctx context.Context, sessionID string) (*CompressedContext, error)

	// Layer 3: Archive search — full-text / vector search over all stored messages.
	Search(ctx context.Context, sessionID, query string, limit int) ([]SearchResult, error)

	// Append adds a message to the conversation history.
	Append(ctx context.Context, sessionID string, msg Message) error

	// DeleteSession removes all data for the given session. After this call,
	// Recent, Compressed, Search, and Append all operate on a clean slate.
	// It is safe to call on a session that doesn't exist.
	DeleteSession(ctx context.Context, sessionID string) error
}

// CompressedContext bundles a summary with retrieval hints for the model.
type CompressedContext struct {
	Summary string          `json:"summary"`
	Hints   []RetrievalHint `json:"hints"`
}

// SearchResult is a single match from Memory.Search.
type SearchResult struct {
	Message  Message `json:"message"`
	Score    float64 `json:"score"`
	Turn     int     `json:"turn"`
}
