package tui

// Slow work for a modal (git, shell, a model call) runs off the event loop.
// The tea.Cmd that does the work never touches the modal. It returns a
// modalDone whose apply function runs on the event loop, so a modal's fields
// are only ever written from one goroutine. deliverModal finds the owner
// anywhere in the stack, so a result still lands when another modal was
// opened meanwhile, and a result for a modal that was closed is dropped.

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// modalDone carries a finished background job to the modal that started it.
type modalDone struct {
	owner Modal
	apply func() (tea.Cmd, bool) // reports whether the owner should close
}

// asyncModal runs work in a goroutine. work returns the apply function, which
// the event loop runs against owner.
func asyncModal(owner Modal, work func() func() (tea.Cmd, bool)) tea.Cmd {
	return func() tea.Msg { return modalDone{owner: owner, apply: work()} }
}

// deliverModal applies a finished job to its owner when the owner is still open.
func (m *Model) deliverModal(msg modalDone) tea.Cmd {
	for _, md := range m.modals {
		if md == msg.owner {
			cmd, done := msg.apply()
			if done {
				m.closeModal(md)
			}
			return cmd
		}
	}
	return nil
}

// shellRun runs command in the workdir, waiting at most wait.
func shellRun(be Backend, command string, wait time.Duration) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	return be.Shell(ctx, command)
}

// shellFlash runs command and reports the outcome as a flash line.
func shellFlash(be Backend, command, okText, failText string) tea.Cmd {
	return func() tea.Msg {
		out, code, err := shellRun(be, command, 2*time.Minute)
		if err == nil && code == 0 {
			return flashMsg{kind: "ok", text: okText}
		}
		detail := outputLine(out)
		if detail == "" && err != nil {
			detail = err.Error()
		}
		if detail == "" {
			detail = fmt.Sprintf("exit %d", code)
		}
		return flashMsg{kind: "err", text: failText + ": " + detail}
	}
}

// outputLine joins the non-empty lines of command output with " · ".
func outputLine(out string) string {
	var parts []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			parts = append(parts, l)
		}
	}
	return strings.Join(parts, " · ")
}

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinner is the current frame of a busy indicator, driven by the clock.
func spinner() string {
	return spinFrames[int(time.Now().UnixMilli()/80)%len(spinFrames)]
}

// maxListed caps how many files a confirm lists before "and N more".
const maxListed = 12

// listConfirm is a Confirm that lists items under its message.
type listConfirm struct{ *Confirm }

// newListConfirm makes a yes/no dialog with a bulleted list of items.
func newListConfirm(th theme.Theme, title, intro string, items []string, danger bool, onYes func() tea.Cmd) *listConfirm {
	body := intro
	for i, it := range items {
		if i == maxListed {
			body += fmt.Sprintf("\n  … and %d more", len(items)-maxListed)
			break
		}
		body += "\n  " + it
	}
	return &listConfirm{NewConfirm(th, title, body, danger, onYes)}
}

// Update forwards to the embedded Confirm but returns the wrapper, so the
// modal stack keeps the value it holds.
func (l *listConfirm) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	_, cmd, done := l.Confirm.Update(msg)
	return l, cmd, done
}

// Size grows with the message so a long list is not cut off.
func (l *listConfirm) Size(cols, rows int) (int, int) {
	lines := strings.Count(l.Confirm.message, "\n") + 1 + 4
	return min(72, cols-4), clamp(lines, 9, rows-4)
}
