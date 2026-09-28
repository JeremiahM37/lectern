package sessions

import (
	"path/filepath"
	"strings"
)

// Leaving the mouse to the terminal in Claude Code.
//
// Claude Code's fullscreen renderer ("tui": "fullscreen") turns on full mouse
// tracking, so the terminal forwards every click to Claude: native selection
// and copy, clicking a link, and Lectern's own clickable file paths and
// right-click menu all stop working in its sessions. CLAUDE_CODE_DISABLE_MOUSE
// keeps the fullscreen rendering and leaves the mouse alone. People who set it
// in ~/.bashrc do not get it here: Lectern launches through a non-interactive
// `bash -c`, which reads no rc file. So Lectern sets it itself.
const (
	// EnvClaudeDisableMouse is the variable Claude Code (and OpenClaude, its
	// fork) reads.
	EnvClaudeDisableMouse = "CLAUDE_CODE_DISABLE_MOUSE"
	// TerminalMouseSetting is the global settings row: on unless it is "0".
	// A project's claude_terminal_mouse column overrides it with "1" or "0".
	TerminalMouseSetting = "claude_terminal_mouse"
)

// honoursDisableMouse says whether an agent is Claude Code or a fork of it
// that reads EnvClaudeDisableMouse, by name or by the binary it runs.
func honoursDisableMouse(spec Spec) bool {
	names := []string{spec.Name}
	if fields := strings.Fields(spec.Command); len(fields) > 0 {
		names = append(names, filepath.Base(fields[0]))
	}
	for _, name := range names {
		if name == "claude" || name == "openclaude" {
			return true
		}
	}
	return false
}

// terminalMouseWanted resolves the project override, then the global setting.
func (m *Manager) terminalMouseWanted(projectID *int64) bool {
	if projectID != nil {
		if project, err := m.DB.Project(*projectID); err == nil {
			switch strings.TrimSpace(project.ClaudeTerminalMouse) {
			case "1":
				return true
			case "0":
				return false
			}
		}
	}
	return strings.TrimSpace(m.DB.Setting(TerminalMouseSetting)) != "0"
}

// applyTerminalMouse sets EnvClaudeDisableMouse in env when the setting is on
// and returns whether it did, which the launch configuration records. A value
// already in env came from the agent, project, profile or caller and is kept,
// so an explicit "0" still gives the mouse to Claude. The caller drops a value
// an earlier launch of the same session injected before calling this, so a
// resume follows the setting as it is now.
func (m *Manager) applyTerminalMouse(env map[string]string, spec Spec, projectID *int64) bool {
	if !honoursDisableMouse(spec) {
		return false
	}
	if _, explicit := env[EnvClaudeDisableMouse]; explicit {
		return false
	}
	if !m.terminalMouseWanted(projectID) {
		return false
	}
	env[EnvClaudeDisableMouse] = "1"
	return true
}
