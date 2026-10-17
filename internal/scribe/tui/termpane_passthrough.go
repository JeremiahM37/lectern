package tui

// Passthrough (spec 06 §8.2): a command that needs the real terminal, such as
// an editor, a pager or an interactive login prompt, gets the terminal handed
// over. The TUI suspends while the child owns stdin, stdout and stderr, and
// resumes when it exits. The child writes to the real screen, so its output is
// not captured or mirrored. "!command" is the captured form and is unchanged.

import (
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// passthroughLabelWidth caps the command shown in the message after resume.
const passthroughLabelWidth = 60

// passthroughCommand builds the child for command: bash -c in workdir, with
// the user's whole environment. It is the user's own command on the user's
// own terminal (spec 05 §9.5: interactive panes are not scrubbed), so tokens
// that tools such as gh need, and git's credential prompt, stay available.
func passthroughCommand(workdir, command string) *osexec.Cmd {
	c := osexec.Command("bash", "-c", command) // not a login shell: keep the inherited PATH
	c.Dir = workdir
	c.Env = os.Environ()
	return c
}

// passthroughDone is the message sent when the child exits and the TUI has
// the terminal back. A non-zero exit is an error the user should see; a
// failure to start is reported with the reason.
func passthroughDone(command string, err error) tea.Msg {
	label := ansi.Truncate(command, passthroughLabelWidth, "…")
	if err == nil {
		return flashMsg{kind: "info", text: "Back from " + label}
	}
	var ee *osexec.ExitError
	if errors.As(err, &ee) {
		return flashMsg{kind: "err", text: fmt.Sprintf("%s exited with status %d", label, ee.ExitCode()), chat: true}
	}
	return flashMsg{kind: "err", text: "Could not run " + label + ": " + err.Error(), chat: true}
}

// runPassthrough hands the terminal to command until it exits.
func (m *Model) runPassthrough(command string) tea.Cmd {
	command = strings.TrimSpace(command)
	if command == "" {
		return cmdErrText("Usage: /interactive <command>")
	}
	c := passthroughCommand(m.cur().be.Workdir(), command)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return passthroughDone(command, err)
	})
}

func init() {
	registerCommands(
		&command{Name: "interactive", Args: "<command>",
			Desc:  "Run a command with the real terminal (editors, pagers, login prompts); the TUI resumes when it exits",
			Group: "System",
			Run: func(m *Model, args string) tea.Cmd {
				return m.runPassthrough(args)
			}},
	)
}
