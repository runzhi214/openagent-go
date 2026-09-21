package execution

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	openagent "github.com/yusheng-g/openagent-go"
)

// bigStreamTool emits chunks totalling more than maxStreamBytes.
type bigStreamTool struct {
	chunks []string
}

func (t *bigStreamTool) Definition() openagent.FunctionDefinition {
	return openagent.FunctionDefinition{
		Name:       "big_stream",
		Parameters: openagent.SchemaOf[struct{}](),
	}
}

func (t *bigStreamTool) Execute(ctx context.Context, args json.RawMessage) *openagent.ToolResult {
	return &openagent.ToolResult{Content: strings.Join(t.chunks, "")}
}

func (t *bigStreamTool) ExecuteStream(ctx context.Context, args json.RawMessage) <-chan openagent.ToolStreamChunk {
	ch := make(chan openagent.ToolStreamChunk, len(t.chunks))
	go func() {
		defer close(ch)
		for _, c := range t.chunks {
			select {
			case <-ctx.Done():
				return
			default:
			}
			ch <- openagent.ToolStreamChunk{Content: c}
		}
	}()
	return ch
}

// TestStreamingByteCap verifies that output exceeding maxStreamBytes is
// truncated and a marker is appended, while the channel is fully drained
// (no goroutine leak / deadlock).
func TestStreamingByteCap(t *testing.T) {
	// 300 × 4KB = 1.2 MB > maxStreamBytes (1 MiB).
	chunks := make([]string, 300)
	for i := range chunks {
		chunks[i] = strings.Repeat("x", 4096)
	}
	tool := &bigStreamTool{chunks: chunks}
	e := newTestExec(tool)

	ch := make(chan openagent.StreamEvent, 16)
	msg := e.execute(context.Background(), openagent.Session{ID: "s"},
		openagent.ToolCall{ID: "1", Function: openagent.ToolCallFunction{Name: "big_stream", Arguments: "{}"}},
		ch)
	close(ch)

	if !strings.Contains(msg.Content, "output exceeded") {
		t.Fatalf("expected truncation marker in result, got len=%d", len(msg.Content))
	}
	// buf content + marker must be well under the raw 1.2 MB.
	if len(msg.Content) > maxStreamBytes+200 {
		t.Fatalf("result len = %d, want <= %d+200", len(msg.Content), maxStreamBytes)
	}
}

// TestStreamingNormalOutput verifies small outputs pass through unchanged.
func TestStreamingNormalOutput(t *testing.T) {
	tool := &bigStreamTool{chunks: []string{"line1\n", "line2\n", "line3\n"}}
	e := newTestExec(tool)

	ch := make(chan openagent.StreamEvent, 16)
	msg := e.execute(context.Background(), openagent.Session{ID: "s"},
		openagent.ToolCall{ID: "1", Function: openagent.ToolCallFunction{Name: "big_stream", Arguments: "{}"}},
		ch)
	close(ch)

	if msg.Content != "line1\nline2\nline3\n" {
		t.Fatalf("expected exact output, got %q", msg.Content)
	}
	if strings.Contains(msg.Content, "truncated") {
		t.Fatalf("small output should not be truncated: %q", msg.Content)
	}
}

// TestStreamingByteCapDrainsChannel verifies that after truncation the
// tool's channel is fully drained — the ExecuteStream goroutine must not
// block on a full channel.
func TestStreamingByteCapDrainsChannel(t *testing.T) {
	// Generate enough chunks to fill the 16-buffer channel many times over.
	// Each chunk is 64KB; 100 chunks = 6.4 MB, well beyond maxStreamBytes.
	chunks := make([]string, 100)
	for i := range chunks {
		chunks[i] = strings.Repeat("y", 64*1024)
	}
	tool := &bigStreamTool{chunks: chunks}
	e := newTestExec(tool)

	ch := make(chan openagent.StreamEvent, 16)
	done := make(chan openagent.Message, 1)
	go func() {
		msg := e.execute(context.Background(), openagent.Session{ID: "s"},
			openagent.ToolCall{ID: "1", Function: openagent.ToolCallFunction{Name: "big_stream", Arguments: "{}"}},
			ch)
		close(ch)
		done <- msg
	}()

	select {
	case msg := <-done:
		if !strings.Contains(msg.Content, "output exceeded") {
			t.Fatalf("expected truncation marker, got len=%d", len(msg.Content))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("execute hung — channel not drained after truncation")
	}
}
