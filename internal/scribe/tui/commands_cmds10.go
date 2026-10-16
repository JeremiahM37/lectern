package tui

// Wave 10 commands: /checkpoint save, /session clear (which also clears the
// task list), /ctxwin and /context-window, and the /unclaim and /force-claim
// aliases. The backend methods they use are optional interfaces here, so the
// Backend interface is unchanged; app/backend_cmds10.go implements them.

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// CheckpointSaver records the working tree as a named checkpoint without
// touching the working tree, the index or HEAD. It returns the ref written.
type CheckpointSaver interface {
	SaveCheckpoint(name string) (ref string, err error)
}

// TaskClearer empties the agent's task list.
type TaskClearer interface {
	ClearTasks() error
}

// ContextWindower shows and sets the context-window override for the current
// model. override is 0 when none is set; window is what the agent uses.
type ContextWindower interface {
	ContextWindow() (ref string, window, override int)
	SetContextWindow(tokens int) error
}

func init() {
	registerCommands(
		&command{Name: "ctxwin", Aliases: []string{"context-window"}, Args: "[tokens|64k|0]",
			Desc: "Show the context window; set it for this model (0 removes the override)", Group: "Models",
			Run: func(m *Model, args string) tea.Cmd { return contextWindowCommand(m, args) }},
		&command{Name: "unclaim", Args: "<path>", Desc: "Release this tab's claim on a file (same as /claim release)", Group: "Tabs",
			Run: func(m *Model, args string) tea.Cmd { return claimCommand(m, strings.TrimSpace("release "+args)) }},
		&command{Name: "force-claim", Args: "<path>", Desc: "Take a file from another tab (same as /claim force)", Group: "Tabs",
			Run: func(m *Model, args string) tea.Cmd { return claimCommand(m, strings.TrimSpace("force "+args)) }},
	)
}

// clearViewCommand asks, then clears the transcript and the queued steering
// messages. With tasks, it also empties the task list (/session clear).
func clearViewCommand(m *Model, tasks bool) tea.Cmd {
	t := m.cur()
	msg := "The messages on screen are cleared. The saved session is kept."
	if tasks {
		msg = "The messages on screen and the task list are cleared. The saved session is kept."
	}
	m.pushModal(NewConfirm(m.th, "Clear conversation?", msg, true, func() tea.Cmd {
		t.chat.Clear()
		t.steerQ = nil // queued steering messages go with the view
		if tc, ok := t.be.(TaskClearer); tasks && ok {
			if err := tc.ClearTasks(); err != nil {
				return cmdErr(err)
			}
		}
		return nil
	}))
	return nil
}

// checkpointSaveCommand is `/checkpoint save [name]`. Without a name the
// checkpoint is named by the time it was saved.
func checkpointSaveCommand(m *Model, args []string) tea.Cmd {
	cs, ok := m.cur().be.(CheckpointSaver)
	if !ok {
		return cmdErrText("this session cannot save checkpoints")
	}
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	ref, err := cs.SaveCheckpoint(name)
	if err != nil {
		return cmdErr(err)
	}
	return flashCmd("ok", "Saved checkpoint "+ref)
}

// contextWindowCommand is /ctxwin: with no argument it shows the window and
// the override; with a size it sets the override for the current model.
func contextWindowCommand(m *Model, args string) tea.Cmd {
	cw, ok := m.cur().be.(ContextWindower)
	if !ok {
		return cmdErrText("this session cannot show or set its context window")
	}
	f := strings.Fields(args)
	if len(f) == 0 {
		ref, window, override := cw.ContextWindow()
		lines := []string{"Model      " + ref, fmt.Sprintf("Window     %d tokens", window)}
		if override > 0 {
			lines = append(lines, fmt.Sprintf("Override   %d tokens (set in config)", override))
		} else {
			lines = append(lines, "Override   none; the model's own window is used")
		}
		lines = append(lines, "", "/ctxwin <tokens> sets it for this model, e.g. /ctxwin 64k. /ctxwin 0 removes it.")
		m.openInfo("Context window", "gauge", lines)
		return nil
	}
	n, err := parseTokenCount(f[0])
	if err != nil {
		return cmdErrText(err.Error())
	}
	if err := cw.SetContextWindow(n); err != nil {
		return cmdErr(err)
	}
	ref, _, _ := cw.ContextWindow()
	if n == 0 {
		return flashCmd("ok", "Removed the context window override for "+ref)
	}
	return flashCmd("ok", fmt.Sprintf("Context window for %s set to %d tokens", ref, n))
}

// parseTokenCount reads a token count typed the way people write it: 64000,
// 64,000, 64k, 1.5m. Zero is allowed (it removes an override).
func parseTokenCount(s string) (int, error) {
	raw := strings.ToLower(strings.NewReplacer(",", "", "_", "").Replace(strings.TrimSpace(s)))
	mult := 1.0
	switch {
	case strings.HasSuffix(raw, "k"):
		mult, raw = 1000, strings.TrimSuffix(raw, "k")
	case strings.HasSuffix(raw, "m"):
		mult, raw = 1000000, strings.TrimSuffix(raw, "m")
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("Usage: /ctxwin <tokens>, e.g. /ctxwin 64k; /ctxwin 0 removes the override")
	}
	return int(v*mult + 0.5), nil
}
