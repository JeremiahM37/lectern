// Package hooks embeds the two agent-side Python scripts staged into every
// worktree.
//
// They stay Python and stdlib-only on purpose: they run on the TARGET, which may
// be any LXC or SSH box, and python3 is the one interpreter every one of them
// already has. Embedding them keeps the Go binary self-contained — there is no
// hooks/ directory to deploy alongside it.
package hooks

import _ "embed"

// Hook is the PreToolUse gate: it blocks a tool call until the operator decides.
//
//go:embed hook.py
var Hook []byte

// ADK is the agent-side kit that lets a running agent file follow-up cards and
// leave durable project notes.
//
//go:embed lec.py
var ADK []byte
