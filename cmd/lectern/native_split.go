package main

// Splitting a native attachment (docs/terminal-client.md). The private tmux
// server this client starts sets its default-command to split.sh, so every
// new pane or window made without a command of its own — tmux's right-click
// menu, Ctrl+] % and ", Ctrl+] c — runs a shell on the session's machine, in
// the directory the agent's pane is in, instead of a shell on this machine in
// $HOME. The operator's own tmux and its configuration are not involved.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/JeremiahM37/lectern/v2/cmd/lectern/localruntime"
	"github.com/JeremiahM37/lectern/v2/internal/config"
)

// terminalSplitFlag is the private entry point split.sh runs.
const terminalSplitFlag = "--terminal-split"

// terminalSplitCommand runs `lectern --terminal-split KIND ID BASE`: it becomes
// the shell on the session's target. When that fails it says why and waits,
// so the pane does not vanish without a word.
func terminalSplitCommand(args []string) int {
	if len(args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: lectern "+terminalSplitFlag+" KIND ID BASE")
		return 2
	}
	argv, err := splitShellArgv(args[0], args[1], args[2], os.Getenv("LECTERN_AUTH_TOKEN"), os.Getenv("LECTERN_ATTACH_HOST"), "agent")
	if err == nil {
		err = execArgv(argv)
	}
	fmt.Fprintln(os.Stderr, "lectern: could not open a shell on the session's machine: "+err.Error())
	fmt.Fprint(os.Stderr, "Press Enter to close this pane.")
	_, _ = fmt.Scanln()
	return 1
}

// splitShellArgv is the command for a new pane. Through an SSH alias the
// hosted peer resolves it (as for attach); otherwise the control plane on
// this machine answers with a command to run here. The shell is a new tracked Lectern shell session
// on the session's target.
func splitShellArgv(kind, id, base, token, attachHost, dirMode string) ([]string, error) {
	argv, _, err := splitShell(kind, id, base, token, attachHost, dirMode)
	return argv, err
}

// splitShell also names the new shell session, when this machine created it.
func splitShell(kind, id, base, token, attachHost, dirMode string) ([]string, string, error) {
	if dirMode == "" {
		dirMode = "agent"
	}
	if dirMode != "agent" && dirMode != "workdir" {
		return nil, "", errors.New("--dir is agent or workdir")
	}
	if err := validateTerminal([]string{kind, id}, false); err != nil {
		return nil, "", err
	}
	if attachHost != "" {
		if strings.HasPrefix(attachHost, "-") || strings.ContainsAny(attachHost, " \t\r\n") {
			return nil, "", errors.New("invalid SSH alias")
		}
		return []string{"env", "TERM=xterm-256color", "ssh", "-tt", attachHost, hostedRemoteBinary, "--hosted-attach", "split", kind, id, dirMode}, "", nil
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return nil, "", err
	}
	// The command names paths on the control-plane host; never run it elsewhere.
	if parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" && parsed.Hostname() != "::1" {
		return nil, "", errors.New("set LECTERN_ATTACH_HOST to the server's SSH alias for native attachment")
	}
	req, err := http.NewRequest("POST", strings.TrimRight(base, "/")+"/api/term/"+url.PathEscape(kind)+"/"+url.PathEscape(id)+"/split?dir="+dirMode, nil)
	if err != nil {
		return nil, "", err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	var data struct {
		Argv    []string `json:"attach_argv"`
		Detail  string   `json:"detail"`
		Session struct {
			ID int64 `json:"id"`
		} `json:"session"`
	}
	_ = json.NewDecoder(res.Body).Decode(&data)
	if res.StatusCode != 201 {
		if data.Detail == "" {
			data.Detail = res.Status
		}
		return nil, "", errors.New(data.Detail)
	}
	if len(data.Argv) == 0 {
		return nil, "", errors.New("no shell command")
	}
	return data.Argv, strconv.FormatInt(data.Session.ID, 10), nil
}

// execArgv replaces this process with argv, so the pane is the shell.
func execArgv(argv []string) error {
	binary, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	return syscall.Exec(binary, argv, os.Environ())
}

// ---- `lectern split`: a shell beside an attachment, from the terminal's own split

// Each native attachment leaves a small record in a private directory on this
// machine, so `lectern split`, run in a new split of the terminal (kitty,
// WezTerm, Ghostty), can find the session the neighbouring window is attached
// to. The private tmux server touches the record whenever its window gains
// focus, so the newest record is the window the operator was just in. No
// credential is written.
type attachState struct {
	Kind       string            `json:"kind"`
	ID         string            `json:"id"`
	Base       string            `json:"base"`
	AttachHost string            `json:"attach_host,omitempty"`
	Local      bool              `json:"local,omitempty"`
	TmuxDir    string            `json:"tmux_dir,omitempty"`
	PID        int               `json:"pid"`
	Started    int64             `json:"started"`
	Terminal   map[string]string `json:"terminal,omitempty"`
	path       string
	focused    time.Time
}

// terminalKeys identify the terminal window and instance an attachment runs in.
var terminalKeys = []string{"KITTY_PID", "KITTY_WINDOW_ID", "WEZTERM_UNIX_SOCKET", "WEZTERM_PANE", "TERM_PROGRAM", "WINDOWID"}

func attachStateDir() string {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, fmt.Sprintf("lectern-attachments-%d", os.Getuid()))
}

// writeAttachState records this attachment; the returned function removes it.
func writeAttachState(path string, controls nativeControls) func() {
	if path == "" {
		return func() {}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return func() {}
	}
	state := attachState{Kind: controls.Kind, ID: controls.ID, Base: controls.Base, AttachHost: os.Getenv("LECTERN_ATTACH_HOST"),
		Local: controls.Local, TmuxDir: os.Getenv("TMUX_TMPDIR"), PID: os.Getpid(), Started: time.Now().Unix(), Terminal: map[string]string{}}
	for _, key := range terminalKeys {
		if value := os.Getenv(key); value != "" {
			state.Terminal[key] = value
		}
	}
	data, _ := json.Marshal(state)
	if os.WriteFile(path, data, 0o600) != nil {
		return func() {}
	}
	return func() { _ = os.Remove(path) }
}

func readAttachStates() []attachState {
	matches, _ := filepath.Glob(filepath.Join(attachStateDir(), "*.json"))
	var states []attachState
	for _, file := range matches {
		info, err := os.Stat(file)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		var state attachState
		if json.Unmarshal(data, &state) != nil || validateTerminal([]string{state.Kind, state.ID}, false) != nil {
			continue
		}
		if !processAlive(state.PID) {
			_ = os.Remove(file)
			continue
		}
		state.path, state.focused = file, info.ModTime()
		states = append(states, state)
	}
	return states
}

// chooseAttachState picks the attachment a new split belongs to: one in the
// same terminal instance when that is known, the one most recently focused,
// and a question when two are too close to tell apart (or when asked).
func chooseAttachState(states []attachState, env func(string) string, pick bool, in io.Reader, out io.Writer, interactive bool) (attachState, error) {
	for _, key := range []string{"KITTY_PID", "WEZTERM_UNIX_SOCKET"} {
		if value := env(key); value != "" {
			var same []attachState
			for _, state := range states {
				if state.Terminal[key] == value {
					same = append(same, state)
				}
			}
			if len(same) > 0 {
				states = same
			}
			break
		}
	}
	if len(states) == 0 {
		return attachState{}, errors.New("no attached Lectern session on this machine; attach first (lectern attach, lectern claude) or pass --session")
	}
	sort.Slice(states, func(i, j int) bool { return states[i].focused.After(states[j].focused) })
	ambiguous := len(states) > 1 && states[0].focused.Sub(states[1].focused) < time.Second
	if !pick && (len(states) == 1 || !ambiguous || !interactive) {
		return states[0], nil
	}
	if !interactive {
		return attachState{}, errors.New("several attached sessions; pass --session")
	}
	fmt.Fprintln(out, "Open a shell beside which session?")
	for i, state := range states {
		fmt.Fprintf(out, "  %d) %s %s (attached %s)\n", i+1, state.Kind, state.ID, time.Unix(state.Started, 0).Format("15:04"))
	}
	fmt.Fprint(out, "Number: ")
	var n int
	if _, err := fmt.Fscanln(in, &n); err != nil || n < 1 || n > len(states) {
		return attachState{}, errors.New("no session chosen")
	}
	return states[n-1], nil
}

const splitUsage = "usage: lectern split [--session current|ID|KIND/ID] [--dir agent|workdir] [--pick]"

// splitCommand is `lectern split`: a new tracked shell on the machine of the
// session attached in the neighbouring window (or --session), in the agent's
// current directory (or --dir workdir), attached here with Lectern controls.
func splitCommand(cfg *config.Config, args []string) error {
	session, dirMode, pick := "current", "agent", false
	for i := 0; i < len(args); i++ {
		value := func() (string, error) {
			if i+1 >= len(args) {
				return "", errors.New(splitUsage)
			}
			i++
			return args[i], nil
		}
		var err error
		switch args[i] {
		case "--session":
			session, err = value()
		case "--dir":
			dirMode, err = value()
		case "--pick":
			pick = true
		case "-h", "--help":
			fmt.Println(splitUsage)
			return nil
		default:
			err = errors.New(splitUsage)
		}
		if err != nil {
			return err
		}
	}
	var state attachState
	if session == "current" {
		var err error
		state, err = chooseAttachState(readAttachStates(), os.Getenv, pick, os.Stdin, os.Stderr, interactiveTerminal())
		if err != nil {
			return err
		}
	} else {
		kind, id := "session", session
		if k, rest, ok := strings.Cut(session, "/"); ok {
			kind, id = k, rest
		}
		state = attachState{Kind: kind, ID: id, Base: env("LECTERN_API", "http://127.0.0.1:"+strconv.Itoa(cfg.Port)), AttachHost: os.Getenv("LECTERN_ATTACH_HOST")}
		// The same session attached here already knows how it was reached.
		for _, known := range readAttachStates() {
			if known.Kind == kind && known.ID == id {
				state = known
				break
			}
		}
	}
	token := cfg.AuthToken
	if state.Local {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		ep, err := localruntime.Ensure(ctx, binary, cfg)
		if err != nil {
			return err
		}
		state.Base, token = ep.URL, ep.Token
		_ = os.Setenv("TMUX_TMPDIR", ep.TmuxDir)
	} else if state.TmuxDir != "" {
		_ = os.Setenv("TMUX_TMPDIR", state.TmuxDir)
	}
	argv, shellID, err := splitShell(state.Kind, state.ID, state.Base, token, state.AttachHost, dirMode)
	if err != nil {
		return err
	}
	if shellID == "" || shellID == "0" {
		return runAttachment(argv, nil)
	}
	return runAttachment(argv, &nativeControls{Kind: "session", ID: shellID, Base: state.Base, Token: token, Local: state.Local})
}
