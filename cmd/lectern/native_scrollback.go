package main

// Scrolling back in a native attachment.
//
// The private tmux this client wraps around the agent only ever sees what the
// agent's own tmux client paints on the terminal: a screen, redrawn with
// cursor addressing. Lines that the agent's pane scrolled past between two
// repaints, or that an Ink-style program erased and rewrote in place, never
// scroll off the top of that screen, so they never reach the wrapper's history.
// Scrolling the wrapper therefore shows old output, a hole, then the newest
// screen. The agent's own pane keeps every line, and the browser terminal
// reads it from the control plane (GET /api/term/KIND/ID/history). The wheel
// and Ctrl+] [ here read the same thing and page it, so both views agree.
//
// Anything the control plane cannot answer (an older server, a full-screen
// program with no history of its own) falls back to the wrapper's own copy
// mode, which is what scrolling did before.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/console"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

// terminalScrollbackFlag is the private entry point scrollback.sh runs.
const terminalScrollbackFlag = "--terminal-scrollback"

// agentPaneOption marks the pane that holds the agent's attachment; shells
// split off beside it keep tmux's own scrolling.
const agentPaneOption = "@lectern_agent"

// scrollbackWheel is the root-table wheel binding. A program that asked for
// the mouse (the agent's tmux with mouse on) and a pane already in a mode
// get the event itself, as tmux would send it. Wheel motion is never turned
// into Up/Down keys, which would replace the agent's draft with old prompts.
func scrollbackWheel(script string) string {
	return "bind-key -n WheelUpPane if-shell -F " + tmuxDQ("#{||:#{mouse_any_flag},#{pane_in_mode}}") +
		" { send-keys -M } { if-shell -F " + tmuxDQ("#{"+agentPaneOption+"}") + " { " + scrollbackBinding(script) + " } { copy-mode -e } }"
}

// scrollbackBinding is the tmux command that opens the view (also Ctrl+] [).
func scrollbackBinding(script string) string {
	return "run-shell -b " + tmuxDQ(strings.ReplaceAll(shellq.Quote(script), "#", "##"))
}

// historyReply is the control plane's /term/KIND/ID/history answer.
type historyReply struct {
	Text      string `json:"text"`
	AppScreen bool   `json:"app_screen"`
}

var lessVersion = regexp.MustCompile(`^less (\d+)`)

func terminalScrollbackCommand(args []string) int {
	if len(args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: lectern "+terminalScrollbackFlag+" KIND ID BASE")
		return 2
	}
	kind, id, base := args[0], args[1], strings.TrimRight(args[2], "/")
	socket, target, dir := os.Getenv(insertSocketEnv), os.Getenv(insertTargetEnv), os.Getenv(linkDirEnv)
	if validateControlsTarget(kind, id) != nil || socket == "" || dir == "" {
		fmt.Fprintln(os.Stderr, "lectern: scrollback runs only inside a native attachment")
		return 2
	}
	tmux := func(a ...string) error {
		return exec.Command("tmux", append([]string{"-S", socket}, a...)...).Run()
	}
	// One wheel notch is several events; only the first opens the view.
	lock := filepath.Join(dir, "scrollback.lock")
	if info, err := os.Stat(lock); err == nil && time.Since(info.ModTime()) > time.Minute {
		_ = os.Remove(lock)
	}
	held, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0
	}
	held.Close()
	defer os.Remove(lock)

	fallback := func() int {
		_ = tmux("copy-mode", "-e", "-t", target)
		return 0
	}
	c := console.New(base, os.Getenv("LECTERN_AUTH_TOKEN"))
	c.HTTP.Timeout = 25 * time.Second
	text, ok := readScrollback(c, kind, id)
	if !ok {
		return fallback()
	}
	file := filepath.Join(dir, "scrollback.txt")
	if os.WriteFile(file, []byte(text), 0o600) != nil {
		return fallback()
	}
	defer os.Remove(file)
	pager := scrollbackPager(file)
	// display-popup waits for the popup, so the lock holds until it closes.
	if err := tmux("display-popup", "-E", "-w", "100%", "-h", "100%", "-T", "Scrollback · q closes", pager); err != nil {
		return fallback()
	}
	return 0
}

// readScrollback is the agent pane's whole history as the control plane
// holds it, ready to page. It is false when there is nothing better than the
// wrapper's own copy mode: the server is older or unreachable, the history is
// empty, or the program owns the screen and keeps no history of its own.
func readScrollback(c *console.Client, kind, id string) (string, bool) {
	data, err := c.JSON("GET", "/term/"+kind+"/"+id+"/history", nil)
	if err != nil {
		return "", false
	}
	var reply historyReply
	if json.Unmarshal(data, &reply) != nil || reply.AppScreen || strings.TrimSpace(reply.Text) == "" {
		return "", false
	}
	return strings.TrimRight(reply.Text, " \n\t") + "\n", true
}

// scrollbackPager is the shell command that pages file from its newest line.
// A wheel works where less can read the mouse (less 551 and later).
func scrollbackPager(file string) string {
	out, err := exec.Command("less", "--version").Output()
	if err != nil {
		return "more " + shellq.Quote(file)
	}
	if m := lessVersion.FindStringSubmatch(string(out)); m != nil {
		if v, _ := strconv.Atoi(m[1]); v >= 551 {
			return "less -R --mouse --wheel-lines=3 +G " + shellq.Quote(file)
		}
	}
	return "less -R +G " + shellq.Quote(file)
}
