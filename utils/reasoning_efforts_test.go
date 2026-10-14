package utils

import (
	"slices"
	"testing"
)

func TestModelReasoningEfforts(t *testing.T) {
	cases := []struct {
		id   string
		want []string
	}{
		{"glm-5.3-flash", []string{"low", "high", "max"}},
		{"GLM-5.3-Flash", []string{"low", "high", "max"}},
		// glm-5.3 is not always-thinking (probed none→200): default vocab.
		{"glm-5.3", nil},
		{"glm-5.2", nil},
		{"deepseek-v4-pro", nil},
		{"gpt-4o", nil},
		{"", nil},
	}
	for _, c := range cases {
		if got := ModelReasoningEfforts(c.id); !slices.Equal(got, c.want) {
			t.Errorf("ModelReasoningEfforts(%q) = %v, want %v", c.id, got, c.want)
		}
	}
}
