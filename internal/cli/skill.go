package cli

import (
	_ "embed"
	"path/filepath"
)

// SkillMD is the using-agentbus2 skill, installed by `init --global` into each
// harness's personal skills directory so it ships with the binary it
// describes: ~/.claude/skills for Claude Code, ~/.agents/skills for Codex, and
// ~/.grok/skills for Grok Build (ported from v1 internal/cli/init.go).
//
//go:embed skills/using-agentbus2/SKILL.md
var SkillMD []byte

// skillPath is where the skill goes under a skills directory.
var skillPath = filepath.Join("skills", "using-agentbus2", "SKILL.md")
