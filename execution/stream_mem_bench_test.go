package execution

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	openagent "github.com/yusheng-g/openagent-go"
)

// benchStreamTool emits pre-built chunks via ExecuteStream.
// Chunks are pre-allocated so the benchmark only measures the
// runtime's streaming accumulation, not chunk creation.
type benchStreamTool struct {
	chunks []string
}

func (t *benchStreamTool) Definition() openagent.FunctionDefinition {
	return openagent.FunctionDefinition{
		Name:       "bench_stream",
		Parameters: openagent.SchemaOf[struct{}](),
	}
}

func (t *benchStreamTool) Execute(ctx context.Context, args json.RawMessage) *openagent.ToolResult {
	return &openagent.ToolResult{Content: strings.Join(t.chunks, "")}
}

func (t *benchStreamTool) ExecuteStream(ctx context.Context, args json.RawMessage) <-chan openagent.ToolStreamChunk {
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

// BenchmarkStreamingLargeOutput feeds 10 MB of chunked data through the
// streaming select loop and reports B/op and HeapDelta. On the pre-fix
// commit buf grows unbounded (~10 MB); on the post-fix commit it is
// capped at 1 MiB.
func BenchmarkStreamingLargeOutput(b *testing.B) {
	// 100 chunks × 100 KB = 10 MB total.
	chunkSize := 100 * 1024
	numChunks := 100
	chunks := make([]string, numChunks)
	for i := range chunks {
		chunks[i] = strings.Repeat("B", chunkSize)
	}
	tool := &benchStreamTool{chunks: chunks}
	e := newTestExec(tool)

	ctx := context.Background()
	session := openagent.Session{ID: "bench"}
	call := openagent.ToolCall{
		ID:       "1",
		Function: openagent.ToolCallFunction{Name: "bench_stream", Arguments: "{}"},
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		ch := make(chan openagent.StreamEvent, 16)
		e.execute(ctx, session, call, ch)
		close(ch)
	}

	b.StopTimer()
	runtime.ReadMemStats(&after)

	heapDelta := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	b.ReportMetric(float64(heapDelta), "HeapDelta-bytes")
}
