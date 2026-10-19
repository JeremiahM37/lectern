package tui

// Wave 9 commands: /claim, /restart, /storage, /checkpoint N and live,
// /undo [n], and the hidden-by-default aliases the registry was missing.
// Each backend capability is an optional interface, checked by type
// assertion, so a Backend without it says so instead of failing.

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// ClaimRow is one file claim, as /claim lists it.
type ClaimRow struct {
	Path, Label string
	Age         time.Duration
	Mine        bool // held by this tab
}

// FileClaimer is the cross-tab claim table behind /claim.
type FileClaimer interface {
	Claims() []ClaimRow
	// ForceClaim takes a file from another tab. It returns the label of the
	// tab that lost it, or "" when nobody held it.
	ForceClaim(path string) (string, error)
	// ReleaseClaim drops this tab's claim on a file; false means it held none.
	ReleaseClaim(path string) (bool, error)
	// ReleaseAllClaims drops every claim this tab holds and returns the count.
	ReleaseAllClaims() int
}

// StorageItem is one area of Scribe's own data for this project.
type StorageItem struct {
	Key, Label, Path string
	Bytes            int64
	Files            int
}

// StorageInspector sizes and cleans Scribe's own data. It never touches the
// user's files: only the areas it reports.
type StorageInspector interface {
	StorageReport() []StorageItem
	// CleanStorage empties the named areas and returns the bytes freed.
	CleanStorage(keys []string) (int64, error)
}

// HookEvent is one hook event with the number of hooks it runs.
type HookEvent struct {
	Event string
	Hooks int
	Off   bool // switched off for this session
}

// HookSwitcher turns the hook events of this session on and off.
type HookSwitcher interface {
	HookEvents() []HookEvent
	SetHookEventOff(event string, off bool) error
}

// SessionPrivacy holds forbidden-path patterns for this session only.
type SessionPrivacy interface {
	SessionForbidden() []string
	AddSessionForbidden(pat string) bool
	RemoveSessionForbidden(pat string) bool
}

// cmdErrText is a command's failure, shown in the chat as "Command error: ...".
func cmdErrText(text string) tea.Cmd {
	return func() tea.Msg { return flashMsg{kind: "err", text: text, chat: true} }
}

// cmdHint reports a mistake in how a command was typed (an invalid choice).
// It shows in the flash line only, like a usage hint, so a typo does not
// leave an error block in the transcript.
func cmdHint(text string) tea.Cmd {
	return func() tea.Msg { return flashMsg{kind: "err", text: text} }
}

// runLine runs another command line, for aliases that name an existing one.
func runLine(m *Model, line string) tea.Cmd {
	c, args := findCommand(line)
	if c == nil {
		return cmdErrText("no command " + line)
	}
	return c.Run(m, args)
}

func init() {
	registerCommands(
		&command{Name: "claim", Args: "[force|release <path>|release-all]",
			Desc: "File claims across tabs; force takes a file from another tab", Group: "Tabs",
			Run: func(m *Model, args string) tea.Cmd { return claimCommand(m, args) }},
		&command{Name: "restart", Desc: "Restart Scribe in place, keeping this session", Group: "System",
			Run: func(m *Model, _ string) tea.Cmd { return restartCommand(m) }},
		&command{Name: "storage", Args: "[clean [cache|plans|sessions]]",
			Desc: "Scribe's stored data for this project: sizes and cleanup", Group: "System",
			Run: func(m *Model, args string) tea.Cmd { return storageCommand(m, args) }},
		// Aliases the registry was missing. Each names an existing command.
		&command{Name: "model events", Desc: "Model calls, retries and errors", Group: "Models",
			Run: func(m *Model, _ string) tea.Cmd { return runLine(m, "model-events") }},
		&command{Name: "compaction", Aliases: []string{"compact-v2-logs"}, Desc: "Compaction log", Group: "Intelligence",
			Run: func(m *Model, _ string) tea.Cmd {
				m.pushModal(newCompactionLogView(m.th, m.cur().be))
				return nil
			}},
		&command{Name: "edit", Args: "<file[:line]>", Desc: "Open a file in the editor", Group: "Tabs",
			Run: func(m *Model, args string) tea.Cmd { return runLine(m, "editor open "+args) }},
		&command{Name: "open", Args: "<file[:line]>", Desc: "Open a file in the editor", Group: "Tabs",
			Run: func(m *Model, args string) tea.Cmd { return runLine(m, "editor open "+args) }},
		&command{Name: "editor-settings", Desc: "Editor and LSP integration settings", Group: "Settings",
			Run: func(m *Model, _ string) tea.Cmd { return runLine(m, "editor settings") }},
		&command{Name: "files", Desc: "Files changed by this session's turns", Group: "Tabs",
			Run: func(m *Model, _ string) tea.Cmd { return runLine(m, "changes") }},
	)
}

// restartPlan is set by /restart. tui.Run executes it once the terminal is
// back to normal, so the new process starts from a clean screen.
type restartPlan struct {
	exe  string
	args []string
}

var pendingRestart *restartPlan

// restartCommand saves the session and quits, to be re-run in place. A
// running turn is stopped first. Nothing is restarted when the executable
// cannot be found, because the quit would then be a dead end.
func restartCommand(m *Model) tea.Cmd {
	t := m.cur()
	exe, err := os.Executable()
	if err != nil {
		return cmdErrText("restart: cannot find the Scribe executable: " + err.Error())
	}
	args := []string{"code"}
	if id := t.be.SessionID(); id != "" {
		args = append(args, "--resume", id)
	}
	if t.busy {
		t.be.Abort()
	}
	pendingRestart = &restartPlan{exe: exe, args: args}
	return m.beginShutdown()
}

// execPendingRestart re-runs Scribe as /restart asked. It returns only when
// nothing was pending or the exec failed.
func execPendingRestart() error {
	p := pendingRestart
	if p == nil {
		return nil
	}
	pendingRestart = nil
	argv := append([]string{p.exe}, p.args...)
	return syscall.Exec(p.exe, argv, os.Environ())
}

// claimCommand is /claim and its subcommands.
func claimCommand(m *Model, args string) tea.Cmd {
	t := m.cur()
	fc, ok := t.be.(FileClaimer)
	if !ok {
		return cmdErrText("this session cannot list file claims")
	}
	f := strings.Fields(args)
	verb, rest := "", ""
	if len(f) > 0 {
		verb = strings.ToLower(f[0])
		rest = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(args), f[0]))
	}
	switch verb {
	case "":
		rows := fc.Claims()
		lines := []string{"Files claimed by Scribe tabs. A claim lasts 5 minutes after its last edit, or until its tab goes idle."}
		if len(rows) == 0 {
			lines = append(lines, "", "No files are claimed.")
		}
		for _, r := range rows {
			who := r.Label
			if r.Mine {
				who = "this tab"
			}
			lines = append(lines, fmt.Sprintf("  %s  %s, %s ago", r.Path, who, r.Age))
		}
		lines = append(lines, "", "/claim force <path> takes a file from another tab. /claim release <path> drops yours.")
		m.openInfo("File claims", "lock", lines)
		return nil
	case "force":
		if rest == "" {
			return cmdErrText("Usage: /claim force <path>")
		}
		displaced, err := fc.ForceClaim(rest)
		if err != nil {
			return cmdErrText(err.Error())
		}
		if displaced == "" {
			return flashCmd("ok", "Claimed "+rest)
		}
		t.chat.AddSystem("info", "Took "+rest+" from "+displaced+".")
		return flashCmd("ok", "Took "+rest+" from "+displaced)
	case "release":
		if rest == "" {
			return cmdErrText("Usage: /claim release <path>, or /claim release-all")
		}
		held, err := fc.ReleaseClaim(rest)
		if err != nil {
			return cmdErrText(err.Error())
		}
		if !held {
			return flashCmd("info", "This tab does not claim "+rest)
		}
		return flashCmd("ok", "Released "+rest)
	case "release-all", "releaseall", "release_all":
		n := fc.ReleaseAllClaims()
		return flashCmd("ok", fmt.Sprintf("Released %d claim(s) from this tab", n))
	}
	return cmdErrText("Usage: /claim, /claim force <path>, /claim release <path>, /claim release-all")
}

// storageCommand is /storage: a report, and /storage clean with a confirm.
// Plain clean empties the regenerable areas (cache, plans); sessions are
// removed only when named, because they hold conversation history.
func storageCommand(m *Model, args string) tea.Cmd {
	si, ok := m.cur().be.(StorageInspector)
	if !ok {
		return cmdErrText("this session cannot report storage")
	}
	f := strings.Fields(args)
	if len(f) == 0 {
		var lines []string
		var total int64
		for _, it := range si.StorageReport() {
			total += it.Bytes
			lines = append(lines, fmt.Sprintf("%-40s %9s  %d files", it.Label, humanBytes(it.Bytes), it.Files))
			lines = append(lines, "    "+it.Path)
		}
		lines = append(lines, "", "Total "+humanBytes(total)+". Only Scribe's own data is listed; your project files are never touched.",
			"Clean with /storage clean (cache, plans and subagent reports) or /storage clean sessions.")
		m.openInfo("Storage", "database", lines)
		return nil
	}
	if strings.ToLower(f[0]) != "clean" {
		return cmdErrText("Usage: /storage, or /storage clean [cache|plans|sessions]")
	}
	keys := []string{"cache", "plans", "reports"}
	if len(f) > 1 {
		keys = nil
		for _, k := range f[1:] {
			k = strings.ToLower(k)
			if k != "cache" && k != "plans" && k != "sessions" {
				return cmdErrText("unknown storage area " + k + "; use cache, plans or sessions")
			}
			keys = append(keys, k)
		}
	}
	var size int64
	for _, it := range si.StorageReport() {
		for _, k := range keys {
			if it.Key == k {
				size += it.Bytes
			}
		}
	}
	t := m.cur()
	msg := fmt.Sprintf("Deletes the contents of %s (%s). The project's own files are not touched.", strings.Join(keys, ", "), humanBytes(size))
	m.pushModal(NewConfirm(m.th, "Clean Scribe storage?", msg, true, func() tea.Cmd {
		freed, err := si.CleanStorage(keys)
		if err != nil {
			t.chat.AddSystem("error", "Storage clean failed: "+err.Error())
			return nil
		}
		t.chat.AddSystem("info", "Freed "+humanBytes(freed)+" of Scribe storage.")
		return nil
	}))
	return nil
}

// humanBytes formats a size for a person.
func humanBytes(n int64) string {
	const k = 1024
	switch {
	case n < k:
		return fmt.Sprintf("%d B", n)
	case n < k*k:
		return fmt.Sprintf("%.1f KB", float64(n)/k)
	case n < k*k*k:
		return fmt.Sprintf("%.1f MB", float64(n)/(k*k))
	}
	return fmt.Sprintf("%.2f GB", float64(n)/(k*k*k))
}

// checkpointCommand is /checkpoint: list, undo [n], redo, live, and a turn
// number to jump to it. save records the working tree as a git ref (see
// checkpointSaveCommand).
func checkpointCommand(m *Model, args string) tea.Cmd {
	t := m.cur()
	f := strings.Fields(args)
	if len(f) == 0 {
		m.openCheckpoints()
		return nil
	}
	switch strings.ToLower(f[0]) {
	case "list":
		m.openCheckpoints()
		return nil
	case "undo":
		if len(f) == 1 {
			m.confirmUndo()
			return nil
		}
		return undoMany(m, f[1])
	case "redo":
		return redoCommand(m)
	case "live":
		t.browse = -1
		t.chat.Bottom()
		return flashCmd("info", "Live view")
	case "save":
		return checkpointSaveCommand(m, f[1:])
	}
	turn, err := strconv.Atoi(f[0])
	if err != nil {
		return cmdErrText("Usage: /checkpoint, /checkpoint <turn>, undo [n], redo or live")
	}
	t.cps = t.be.Checkpoints()
	for i, cp := range t.cps {
		if cp.Turn != turn {
			continue
		}
		if cp.Undone {
			return cmdErrText(fmt.Sprintf("Turn %d is undone. Redo it first with /redo.", turn))
		}
		t.browse = i
		t.chat.JumpToTurn(cp.Turn)
		return flashCmd("info", fmt.Sprintf("Viewing turn %d (/checkpoint live returns)", turn))
	}
	return cmdErrText(fmt.Sprintf("No checkpoint for turn %d. /checkpoint lists them.", turn))
}

// undoCommand is /undo: with no count it asks first, as Ctrl+Z does.
func undoCommand(m *Model, args string) tea.Cmd {
	args = strings.TrimSpace(args)
	if args == "" {
		m.confirmUndo()
		return nil
	}
	return undoMany(m, args)
}

// undoMany reverts the last n active turns without a confirm (reversible
// with /redo). It stops at the first failure and reports what it did.
func undoMany(m *Model, count string) tea.Cmd {
	n, err := strconv.Atoi(strings.TrimSpace(count))
	if err != nil || n < 1 {
		return cmdErrText("Usage: /undo [n], where n is a positive number of turns")
	}
	t := m.cur()
	// The undos run off the UI loop; Busy() stays true behind the wait modal
	// until the result is applied, so no key acts on a half-restored tree.
	return m.runBusy("Undoing", func() func() tea.Cmd {
		done, files := 0, 0
		var stopErr error
		for i := 0; i < n; i++ {
			cp, err := t.be.Undo()
			if err != nil {
				stopErr = err
				break
			}
			done++
			files += len(cp.Files)
		}
		return func() tea.Cmd {
			if done == 0 && stopErr != nil {
				return cmdErrText("Undo failed: " + stopErr.Error())
			}
			if stopErr != nil {
				t.chat.AddSystem("error", fmt.Sprintf("Undo stopped after %d turn(s): %v", done, stopErr))
			}
			t.browse = -1
			msg := fmt.Sprintf("Undid %d turn(s): %d files restored. /redo brings them back.", done, files)
			t.chat.AddSystem("info", msg)
			return flashCmd("ok", msg)
		}
	})
}

// redoCommand re-applies the last undone turn.
func redoCommand(m *Model) tea.Cmd {
	be := m.cur().be
	return m.runBusy("Redoing", func() func() tea.Cmd {
		cp, err := be.Redo()
		return func() tea.Cmd {
			if err != nil {
				return cmdErr(err)
			}
			return redone(m, cp)
		}
	})
}

// hookCommand is /hooks: a list of events with their switch state, and
// `/hooks off <event>` / `/hooks on <event>` for this session.
func hookCommand(m *Model, args string) tea.Cmd {
	t := m.cur()
	hs, ok := t.be.(HookSwitcher)
	f := strings.Fields(args)
	if len(f) == 0 {
		if !ok {
			m.openHookList()
			return nil
		}
		evs := hs.HookEvents()
		if len(evs) == 0 {
			m.openInfo("Hooks", "bolt", []string{"No hooks are configured.", "Add them under \"hooks\" in config.json."})
			return nil
		}
		lines := []string{"Hook events for this session. Switching one off lasts until Scribe exits; nothing is written to a config file."}
		for _, e := range evs {
			state := "on"
			if e.Off {
				state = "off (session only)"
			}
			lines = append(lines, fmt.Sprintf("  %-18s %d hook(s)  %s", e.Event, e.Hooks, state))
		}
		lines = append(lines, "", "/hooks off <event> or /hooks on <event>")
		m.openInfo("Hooks", "bolt", lines)
		return nil
	}
	verb := strings.ToLower(f[0])
	if verb != "on" && verb != "off" && verb != "enable" && verb != "disable" || len(f) < 2 {
		return cmdErrText("Usage: /hooks, /hooks off <event>, or /hooks on <event>")
	}
	if !ok {
		return cmdErrText("this session cannot switch hooks")
	}
	off := verb == "off" || verb == "disable"
	event := f[1]
	if err := hs.SetHookEventOff(event, off); err != nil {
		return cmdErrText(err.Error())
	}
	if off {
		return flashCmd("info", event+" hooks are off for this session")
	}
	return flashCmd("ok", event+" hooks are on")
}

// privacySessionCommand is `/privacy session <pattern>` (add),
// `/privacy session remove <pattern>`, or `/privacy session` (list).
func privacySessionCommand(m *Model, rest string) tea.Cmd {
	sp, ok := m.cur().be.(SessionPrivacy)
	if !ok {
		return cmdErrText("this session cannot hold session-only privacy patterns")
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		lines := []string{"Forbidden for this session only. They are lost when Scribe exits."}
		if len(sp.SessionForbidden()) == 0 {
			lines = append(lines, "", "No session patterns yet.")
		}
		for _, p := range sp.SessionForbidden() {
			lines = append(lines, "  "+p)
		}
		m.openInfo("Session forbidden paths", "shield", lines)
		return nil
	}
	if verb, pat, _ := strings.Cut(rest, " "); verb == "remove" {
		pat = strings.TrimSpace(pat)
		if pat == "" || !sp.RemoveSessionForbidden(pat) {
			return cmdErrText("No session pattern " + pat)
		}
		return flashCmd("ok", "Lifted "+pat+" for this session")
	}
	if !sp.AddSessionForbidden(rest) {
		return flashCmd("info", rest+" is already forbidden for this session")
	}
	return flashCmd("ok", rest+" is forbidden for this session")
}

// openHookList shows the configured hooks from the config, for a backend that
// cannot switch them per session.
func (m *Model) openHookList() {
	c := fields7(m.cur().be)
	if len(c.Hooks) == 0 {
		m.openInfo("Hooks", "bolt", []string{"No hooks are configured.", "Add them under \"hooks\" in config.json."})
		return
	}
	var lines []string
	for _, h := range c.Hooks {
		matcher := h.Matcher
		if matcher == "" {
			matcher = "all tools"
		}
		lines = append(lines, h.Event+"  ("+matcher+")")
		extra := ""
		if h.If != "" {
			extra += " if " + h.If
		}
		if h.Once {
			extra += " once"
		}
		if h.Async {
			extra += " async"
		}
		lines = append(lines, "    "+h.Command+extra)
	}
	m.openInfo("Hooks", "bolt", lines)
}
