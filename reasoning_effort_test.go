package openagent

import "testing"

func TestDefaultReasoningEffort(t *testing.T) {
	cases := []struct {
		vocab []string
		want  string
	}{
		{[]string{"low", "medium", "high"}, "medium"},
		{[]string{"low", "high", "max"}, "high"},
		{[]string{"none", "minimal", "low", "medium", "high"}, "medium"},
		{[]string{"low"}, "low"},
		{[]string{"none", "low"}, "low"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := DefaultReasoningEffort(c.vocab); got != c.want {
			t.Errorf("DefaultReasoningEffort(%v) = %q, want %q", c.vocab, got, c.want)
		}
	}
}

func TestAllowsReasoningEffort(t *testing.T) {
	cases := []struct {
		vocab   []string
		effort  string
		allowed bool
	}{
		{[]string{"low", "high", "max"}, "high", true},
		{[]string{"low", "high", "max"}, "medium", false},
		{[]string{"low", "high", "max"}, "none", false},
		{nil, "medium", true},       // unrestricted
		{nil, "none", true},         // unrestricted
		{[]string{"low"}, "", true}, // empty effort unrestricted
	}
	for _, c := range cases {
		if got := AllowsReasoningEffort(c.vocab, c.effort); got != c.allowed {
			t.Errorf("AllowsReasoningEffort(%v, %q) = %v, want %v", c.vocab, c.effort, got, c.allowed)
		}
	}
}
