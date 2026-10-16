package tui

// Commands that give the menus and the stash browser their entry points, and
// the backend-backed commands that had no slash form: /stash and /drafts,
// /scope and /model-scope (moving a setting between the project and global
// config), /context clear, /commit --staged, and the confirm on /clear.

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func init() {
	// /clear asks first (spec: "Clear conversation?", default No). It clears
	// the view only; the saved session is kept.
	overrideCommands(&command{Name: "clear", Desc: "Clear the view; the session is kept (asks first)", Group: "Session",
		Run: func(m *Model, _ string) tea.Cmd { return clearViewCommand(m, false) }})

	registerCommands(
		&command{Name: "stash", Aliases: []string{"drafts"}, Desc: "Stashed drafts for this folder: restore, delete or clear", Group: "Session",
			Run: func(m *Model, _ string) tea.Cmd { m.openStashPicker(); return nil }},
		&command{Name: "scope", Args: "<setting> <project|global>", Desc: "Move a setting to the project or the global config", Group: "Settings",
			Run: func(m *Model, args string) tea.Cmd {
				f := strings.Fields(args)
				if len(f) != 2 || (f[1] != "project" && f[1] != "global") {
					return cmdErrText("Usage: /scope <setting> project|global")
				}
				return m.moveSetting(f[0], f[1] == "project")
			}},
		&command{Name: "model-scope", Desc: "Save the model to the project or the global config", Group: "Models",
			Run: func(m *Model, args string) tea.Cmd {
				switch strings.ToLower(strings.TrimSpace(args)) {
				case "":
					m.openModelScopePicker()
					return nil
				case "project":
					return m.moveSetting("model", true)
				case "global":
					return m.moveSetting("model", false)
				}
				return cmdErrText("Usage: /model-scope project|global")
			}},
	)
}

// planCommand is /plan. A task starts planning it; with no argument the mode
// toggles between plan and default (spec 02 section 2.1).
func (m *Model) planCommand(args string) tea.Cmd {
	if task := strings.TrimSpace(args); task != "" {
		return tea.Batch(m.setMode("plan"), m.sendPrompt(task))
	}
	if m.cur().be.Mode() == "plan" {
		return m.setMode("default")
	}
	return m.setMode("plan")
}

// contextCommand is `/context clear [skills|memory]` (reset is the same).
// Memory is recalled per prompt, so the backend refuses that target and the
// refusal is shown rather than a false success.
func (m *Model) contextCommand(args string) tea.Cmd {
	f := strings.Fields(args)
	if len(f) == 0 || (f[0] != "clear" && f[0] != "reset") {
		return cmdErrText("Usage: /context, or /context clear [skills|memory]")
	}
	target := ""
	if len(f) > 1 {
		target = f[1]
	}
	cc, ok := m.cur().be.(ContextClearer)
	if !ok {
		return cmdErr(errors.New("this backend cannot clear context"))
	}
	if err := cc.ClearContext(target); err != nil {
		return cmdErr(err)
	}
	return flashCmd("ok", "Skills unloaded from context")
}

// parseCommitArgs reads the optional leading --staged flag. Without it a
// commit stages every change first, as /commit always has.
func parseCommitArgs(args string) (msg string, stagedOnly bool) {
	args = strings.TrimSpace(args)
	if args == "--staged" {
		return "", true
	}
	if rest, ok := strings.CutPrefix(args, "--staged "); ok {
		return strings.TrimSpace(rest), true
	}
	return args, false
}

// commitCommand is /commit: `/commit [--staged] [message]`. With no message the
// suggestion prompt opens; --staged needs a message, so it never silently
// commits more than the person chose to.
func (m *Model) commitCommand(args string) tea.Cmd {
	msg, stagedOnly := parseCommitArgs(args)
	switch {
	case stagedOnly && msg == "":
		return cmdErrText("Usage: /commit --staged <message>")
	case msg == "":
		return m.commitPrompt()
	case stagedOnly:
		return m.gitCommitStaged(msg)
	}
	return m.gitCommit(msg)
}

// gitCommitStaged commits only what is already staged.
func (m *Model) gitCommitStaged(msg string) tea.Cmd {
	sc, ok := m.cur().be.(StagingCommitter)
	if !ok {
		return cmdErr(errors.New("this backend cannot commit only the staged changes"))
	}
	return func() tea.Msg {
		if err := sc.GitCommitWith(msg, false); err != nil {
			return flashMsg{kind: "err", text: "Commit failed: " + err.Error()}
		}
		return flashMsg{kind: "ok", text: "Committed the staged changes"}
	}
}

// gitRepoCommand runs a repository command (init, branch, pull) on the backend
// off the UI goroutine and reports git's first line. A backend without the
// capability runs the same words as a shell command instead.
func (m *Model) gitRepoCommand(fallback string, run func(GitRepoOps) (string, error)) tea.Cmd {
	t := m.cur()
	ops, ok := t.be.(GitRepoOps)
	if !ok {
		return m.runShell(t, fallback)
	}
	m.setFlash("info", "Running "+fallback+"…")
	return func() tea.Msg {
		out, err := run(ops)
		if err != nil {
			return flashMsg{kind: "err", text: err.Error()}
		}
		return flashMsg{kind: "ok", text: outcomeLine(out, fallback+" done")}
	}
}

// outcomeLine is the first non-blank line of a command's output.
func outcomeLine(out, fallback string) string {
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return fallback
}

// moveSetting saves a setting into the project or the global config and
// removes it from the other one. The value is the current one, so nothing
// changes except where it is stored.
func (m *Model) moveSetting(key string, toProject bool) tea.Cmd {
	t := m.cur()
	mover, ok := t.be.(ConfigMover)
	if !ok {
		return cmdErr(errors.New("this backend cannot move settings between scopes"))
	}
	var val any
	if key == "model" {
		val = t.be.Model()
	} else {
		v, known := t.be.Config()[key]
		if !known {
			return cmdErrText("No setting named " + key)
		}
		val = v
	}
	if err := mover.MoveConfig(key, val, toProject, !toProject); err != nil {
		return cmdErr(err)
	}
	scope := "global"
	if toProject {
		scope = "project"
	}
	return flashCmd("ok", fmt.Sprintf("%s is now saved in the %s config", key, scope))
}

// openModelScopePicker asks where the model is saved (/model-scope).
func (m *Model) openModelScopePicker() {
	p := NewPicker(m.th, "Save model to", "model")
	p.Hint = "↑↓ move · Enter save · Esc close"
	p.Items = []PickItem{
		{Value: "project", Label: "Project", Desc: "this project only"},
		{Value: "global", Label: "Global", Desc: "every project, unless a project sets its own"},
	}
	p.refilter()
	p.OnSelect = func(it PickItem) tea.Cmd { return m.moveSetting("model", it.Value == "project") }
	m.pushModal(p)
}
