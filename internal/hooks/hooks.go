// Package hooks embeds the two agent-side Python scripts staged into a
// worktree on a target without a lectern binary.
//
// They run on the TARGET, which may be any LXC or SSH box, so they are
// stdlib-only. A target with a lectern binary runs their Go ports instead
// (`lectern helper approval-hook` and `lectern helper lec`, internal/helpers)
// and needs no python3. Embedding them keeps the Go binary self-contained —
// there is no hooks/ directory to deploy alongside it.
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
