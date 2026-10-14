package config

import (
	"encoding/json"
	"slices"
	"testing"
)

// TestModelConfigReasoningEffortsParse: the models array accepts the
// legacy string form (no vocabulary) and the object form with
// reasoning_efforts side by side.
func TestModelConfigReasoningEffortsParse(t *testing.T) {
	raw := `{
	  "api_key": "sk-test",
	  "base_url": "https://example.com/v1",
	  "models": ["gpt-4o", {"id": "glm-5.3-flash", "reasoning_efforts": ["low", "high", "max"]}]
	}`
	var p ProviderConfig
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(p.Models) != 2 {
		t.Fatalf("models = %d, want 2", len(p.Models))
	}
	if p.Models[0].ID != "gpt-4o" || p.Models[0].ReasoningEfforts != nil {
		t.Errorf("legacy string form: id=%q vocab=%v, want gpt-4o with nil vocab", p.Models[0].ID, p.Models[0].ReasoningEfforts)
	}
	if p.Models[1].ID != "glm-5.3-flash" {
		t.Errorf("object form id = %q", p.Models[1].ID)
	}
	if want := []string{"low", "high", "max"}; !slices.Equal(p.Models[1].ReasoningEfforts, want) {
		t.Errorf("reasoning_efforts = %v, want %v", p.Models[1].ReasoningEfforts, want)
	}
}
