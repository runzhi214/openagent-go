package openagent

import "context"

// SkillInfo is the lightweight summary of a skill, produced by Discover.
// Name and Description are extracted from YAML frontmatter; Frontmatter
// retains ALL fields (known and unknown). Path is the absolute path to
// the skill directory. The full SKILL.md body is loaded on demand via Load().
type SkillInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Frontmatter map[string]any `json:"frontmatter"`
	Path        string         `json:"path"` // absolute path to skill directory
}

// SkillLoader discovers and loads skills. The loader is configured with
// a root directory at construction time — Discover needs no arguments.
//
// Two-phase loading:
//  1. Discover — scan directories, read frontmatter only (lightweight)
//  2. Load     — read full SKILL.md body on demand
//
// nil SkillLoader = no skills available.
type SkillLoader interface {
	Discover(ctx context.Context) ([]SkillInfo, error)
	Load(ctx context.Context, skill SkillInfo) (string, error)
}
