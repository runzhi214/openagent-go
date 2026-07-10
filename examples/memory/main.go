// Memory example: demonstrates the 3-layer memory model.
//
// Layer 1 — Working (Recent):  last N messages, kept in the prompt.
// Layer 2 — Compressed:        summary + retrieval hints for older turns.
// Layer 3 — Archive (Search):   full history, searchable via FTS5 / vectors.
//
// Key design principle: summary is DERIVED data, not a replacement.
// Original messages are NEVER deleted — Search always queries the full archive.
//
// This example:
//
//  1. Teaches the agent a fact ("favourite colour is cerulean").
//
//  2. Runs several more turns so the working window no longer contains it
//     and compression is triggered.
//
//  3. Calls Memory.Search explicitly to show the fact was archived and
//     can be retrieved — this is memory, not chat history.
//
//  4. Verifies the full message archive is intact (no deletion).
//
//  5. Demonstrates incremental compression (ThroughIndex tracking).
//
//	go run ./examples/memory/
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	openagent "github.com/yusheng-g/openagent-go"
	"github.com/yusheng-g/openagent-go/memory/sqlite"
	"github.com/yusheng-g/openagent-go/model/openai"
)

func main() {
	apiKey := os.Getenv("OPENAGENT_API_KEY")
	modelID := os.Getenv("OPENAGENT_MODEL")
	baseURL := os.Getenv("OPENAGENT_BASE_URL")

	model := openai.New(apiKey, modelID, baseURL).WithContextWindow(128_000)

	// SQLite memory backend with FTS5 full-text search.
	// Add .WithEmbedder(emb) to enable vector semantic search.
	mem, err := sqlite.New("./memory_demo.db")
	if err != nil {
		fmt.Fprintf(os.Stderr, "memory: %v\n", err)
		os.Exit(1)
	}
	defer os.Remove("./memory_demo.db")

	// Optional: enable auto-compaction with incremental/rolling summaries.
	// Uncomment to test the compression path (requires OPENAGENT_API_KEY).
	//
	//	summarizer := openai.NewSummarizer(apiKey, modelID, baseURL)
	//	mem.WithSummarizer(summarizer)

	agent := openagent.NewAgent("memory-demo",
		openagent.WithModel(model),
		openagent.WithInstructions("You are a concise assistant. Answer directly without filler. Use previous conversation context when relevant."),
		openagent.WithMemory(mem),
		openagent.WithMaxWorkingTokens(5000), // small window to trigger compaction early
	)

	session := openagent.Session{
		ID: "memory-demo-session", UserID: "user-1",
		AgentName: "memory-demo", ModelID: modelID,
		CreatedAt: time.Now(),
	}
	ctx := context.Background()

	// ── Phase 1: teach the agent a fact ──
	fmt.Println("━━━ Phase 1: teaching ━━━")
	_, _ = agent.Run(ctx, session, openagent.UserMessage("My favourite colour is cerulean. Got it?"))

	// ── Phase 2: run several turns to push the fact out of the working window ──
	fmt.Println("━━━ Phase 2: building history ━━━")
	questions := []string{
		"What is the capital of France?",
		"How many planets are in the solar system?",
		"Name a Shakespeare play.",
		"What is 12 * 34?",
		"Recommend a good book.",
	}
	for _, q := range questions {
		_, err := agent.Run(ctx, session, openagent.UserMessage(q))
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
		time.Sleep(200 * time.Millisecond) // rate-limit
	}

	// ── Phase 3: query from the archive ──
	fmt.Println("\n━━━ Phase 3: recall from memory ━━━")
	result, err := agent.Run(ctx, session,
		openagent.UserMessage("What is my favourite colour? I told you earlier."))
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Agent: %s\n\n", result.FinalOutput)

	// ── Demonstrate the memory layers ──
	fmt.Println("━━━ Memory layers ━━━")

	// Layer 1: Recent (working window — last N messages, a VIEW not a deletion)
	recent, _ := mem.Recent(ctx, session.ID, 5)
	fmt.Printf("\n[Layer 1 — Recent] last %d messages:\n", len(recent))
	for _, m := range recent {
		fmt.Printf("  %s: %s\n", m.Role, truncate(m.Content, 80))
	}

	// Layer 2: Compressed (summary of older turns) — with ThroughIndex tracking
	comp, err := mem.Compressed(ctx, session.ID)
	if err == nil && comp != nil && comp.Summary != "" {
		fmt.Printf("\n[Layer 2 — Compressed] summary:\n  %s\n", comp.Summary)
		fmt.Printf("  ThroughIndex: %d (messages covered by this summary)\n", comp.ThroughIndex)
		for _, h := range comp.Hints {
			fmt.Printf("  Hint: %s (query: %s)\n", h.Description, h.Query)
		}
	} else {
		fmt.Println("\n[Layer 2 — Compressed] not yet available (needs Summarizer config)")
	}

	// Layer 3: Search (full archive — all original messages preserved)
	results, _ := mem.Search(ctx, session.ID, "favourite colour", 3)
	fmt.Printf("\n[Layer 3 — Search] 'favourite colour' → %d hits:\n", len(results))
	for i, r := range results {
		fmt.Printf("  %d. score=%.2f  %s\n", i+1, r.Score, truncate(r.Message.Content, 80))
	}

	// ── Verify archive integrity: ALL messages are preserved (no deletion) ──
	fmt.Println("\n━━━ Archive integrity check ━━━")
	allMessages, _ := mem.Recent(ctx, session.ID, 1000)
	fmt.Printf("Total messages in archive: %d\n", len(allMessages))
	if len(allMessages) >= 12 {
		fmt.Println("✓ Archive intact — messages preserved after compression")
	} else {
		fmt.Println("✗ Archive appears truncated — messages were lost")
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
