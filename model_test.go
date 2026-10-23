package openagent

import (
	"context"
	"testing"
)

// TestWithModel_RoundTrip mirrors TestWithRunInfo_RoundTrip: absent → ok=false,
// stamped → same instance recovered, and the stamp survives sibling re-stamps
// (WithRunInfo/WithSession shadow only their own keys).
func TestWithModel_RoundTrip(t *testing.T) {
	if _, ok := ModelFromContext(context.Background()); ok {
		t.Fatal("ModelFromContext(empty ctx) ok=true, want false")
	}

	m := &fakeWindowModel{cw: 1000}
	ctx := WithRunInfo(WithModel(context.Background(), m), RunInfo{RunID: "r", TurnID: -1})
	got, ok := ModelFromContext(ctx)
	if !ok {
		t.Fatal("ModelFromContext(stamped ctx) ok=false, want true")
	}
	if got != Model(m) {
		t.Fatalf("ModelFromContext returned %v, want the stamped instance", got)
	}
}
