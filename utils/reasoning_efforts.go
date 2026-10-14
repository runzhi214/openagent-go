package utils

import "strings"

// ModelReasoningEfforts returns the built-in reasoning_effort vocabulary
// for the given model ID — the values its backing provider actually
// accepts. It mirrors ModelContextWindow: case-insensitive substring
// matching, nil for unknown models so callers fall back to the generic
// low/medium/high default. Settings "reasoning_efforts" overrides this
// table per model.
//
// Only probe-verified entries belong here. glm-5.3-flash is an
// always-thinking model: the OpenGW gateway intermittently rejects
// "medium" (2026-10-14 probe: 71 byte-identical requests, 10×400
// "该模型始终思考，不支持关闭思考；请使用 low、high 或 max", ~14%) and
// deterministically rejects "none". glm-5.3 is deliberately NOT listed —
// probed none→200 (3/3), so it accepts the default vocabulary.
func ModelReasoningEfforts(modelID string) []string {
	lower := strings.ToLower(modelID)

	switch {
	// ── GLM (Zhipu) ──
	case has(lower, "glm-5.3-flash"):
		return []string{"low", "high", "max"}
	}

	return nil
}
