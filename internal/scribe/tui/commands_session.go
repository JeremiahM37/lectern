package tui

import (
	"errors"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Continuer is implemented by backends that can re-run an interrupted turn.
type Continuer interface {
	// Continue re-runs the session's unfinished turn in the background. It
	// reports false, and runs nothing, when the last turn finished.
	Continue() (bool, error)
}

func init() {
	registerCommands(
		&command{Name: "session", Args: "[continue|history|new|clear|rename|export]",
			Desc: "Session actions: continue an interrupted turn, or browse sessions", Group: "Session",
			Run: func(m *Model, args string) tea.Cmd { return m.sessionCommand(args) }},
		&command{Name: "continue", Desc: "Continue an interrupted turn (the same as /session continue)", Group: "Session",
			Run: func(m *Model, _ string) tea.Cmd { return m.continueTurn() }},
	)
}

// sessionCommand is `/session` and its subcommands. Bare `/session` opens the
// session browser; `/session continue` re-runs an interrupted turn; the other
// subcommands are the top-level commands of the same name.
func (m *Model) sessionCommand(args string) tea.Cmd {
	if strings.TrimSpace(args) == "" {
		m.openSessionPicker()
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(args), "clear") {
		return clearViewCommand(m, true)
	}
	c, rest := findCommand(args)
	if c == nil || c.Name == "session" {
		return cmdErrText("Usage: /session continue, history, new, clear, rename or export")
	}
	return c.Run(m, rest)
}

// continueTurn re-runs the active tab's interrupted turn. A running turn must
// finish or be stopped first, and a finished one is reported, not re-run.
func (m *Model) continueTurn() tea.Cmd {
	t := m.cur()
	c, ok := t.be.(Continuer)
	if !ok {
		return cmdErr(errors.New("this session cannot continue a turn"))
	}
	m.enterChat()
	if t.busy {
		return flashCmd("info", "A turn is running; wait for it or stop it first")
	}
	started, err := c.Continue()
	if err != nil {
		return cmdErr(err)
	}
	if !started {
		t.chat.AddSystem("info", "Nothing to continue: the last turn finished.")
		return nil
	}
	t.busy = true
	t.since = time.Now()
	return flashCmd("ok", "Continuing the interrupted turn")
}
