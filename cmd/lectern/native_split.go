package main

// Splitting a native attachment (docs/terminal-client.md). The private tmux
// server this client starts sets its default-command to split.sh, so every
// new pane or window made without a command of its own — tmux's right-click
// menu, Ctrl+] % and ", Ctrl+] c — runs a shell on the session's machine, in
// the directory the agent's pane is in, instead of a shell on this machine in
// $HOME. The operator's own tmux and its configuration are not involved.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
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
	argv, err := splitShellArgv(args[0], args[1], args[2], os.Getenv("LECTERN_AUTH_TOKEN"), os.Getenv("LECTERN_ATTACH_HOST"))
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
// this machine answers with a command to run here.
func splitShellArgv(kind, id, base, token, attachHost string) ([]string, error) {
	if err := validateTerminal([]string{kind, id}, false); err != nil {
		return nil, err
	}
	if attachHost != "" {
		if strings.HasPrefix(attachHost, "-") || strings.ContainsAny(attachHost, " \t\r\n") {
			return nil, errors.New("invalid SSH alias")
		}
		return []string{"env", "TERM=xterm-256color", "ssh", "-tt", attachHost, hostedRemoteBinary, "--hosted-attach", "split", kind, id}, nil
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	// The command names paths on the control-plane host; never run it elsewhere.
	if parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" && parsed.Hostname() != "::1" {
		return nil, errors.New("set LECTERN_ATTACH_HOST to the server's SSH alias for native attachment")
	}
	req, err := http.NewRequest("GET", strings.TrimRight(base, "/")+"/api/term/"+url.PathEscape(kind)+"/"+url.PathEscape(id)+"/split", nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var data struct {
		Argv   []string `json:"attach_argv"`
		Detail string   `json:"detail"`
	}
	_ = json.NewDecoder(res.Body).Decode(&data)
	if res.StatusCode != 200 {
		if data.Detail == "" {
			data.Detail = res.Status
		}
		return nil, errors.New(data.Detail)
	}
	if len(data.Argv) == 0 {
		return nil, errors.New("no shell command")
	}
	return data.Argv, nil
}

// execArgv replaces this process with argv, so the pane is the shell.
func execArgv(argv []string) error {
	binary, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	return syscall.Exec(binary, argv, os.Environ())
}
