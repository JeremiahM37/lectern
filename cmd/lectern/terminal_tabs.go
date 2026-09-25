package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"golang.org/x/term"
)

// Tabs live on the machine running the TUI. Consequently the same operation
// works through SSH, a Windows SSH client, and a local terminal without GUI RPC.
// A newly bootstrapped workspace owns a private tmux socket, never the user's
// agent tmux server. Closing the workspace closes clients, not agent sessions.
func openTerminalTab(base, token, kind, id string, batch bool) error {
	if err := validateTerminal([]string{kind, id}, false); err != nil {
		return err
	}
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("terminal tabs need tmux: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if os.Getenv("TMUX") == "" {
		return runTerminalWorkspace(tmux, executable, base, token, kind, id, batch)
	}
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return fmt.Errorf("cannot identify the dashboard's tmux pane")
	}
	output, err := exec.Command(tmux, "-S", workspaceSocket(os.Getenv("TMUX")), "display-message", "-p", "-t", pane, "#{session_id}").CombinedOutput()
	if err != nil {
		return fmt.Errorf("find terminal workspace: %s", cleanTerminalError(output))
	}
	target := strings.TrimSpace(string(output))
	if !validWorkspaceSession(target) {
		return fmt.Errorf("invalid terminal workspace identity")
	}
	return createTerminalTab(tmux, []string{"-S", workspaceSocket(os.Getenv("TMUX"))}, target, executable, base, token, kind, id)
}

func validWorkspaceSession(id string) bool {
	if len(id) < 2 || id[0] != '$' {
		return false
	}
	for _, c := range id[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
func cleanTerminalError(b []byte) string { return strings.TrimSpace(string(b)) }

// The private launch script keeps credentials out of argv and tmux command
// text. It removes only its own generated files before starting the attachment.
func terminalTabScript(executable, base, token, host, kind, id, script, dir string) string {
	return "#!/bin/sh\n" +
		"export LECTERN_API=" + shellq.Quote(base) + "\n" +
		"export LECTERN_AUTH_TOKEN=" + shellq.Quote(token) + "\n" +
		"export LECTERN_ATTACH_HOST=" + shellq.Quote(host) + "\n" +
		"export LECTERN_TAB_VIEW=1\n" +
		"unset TMUX TMUX_PANE LECTERN_INITIAL_BATCH LECTERN_INITIAL_SESSION\n" +
		"rm -f -- " + shellq.Quote(script) + "\nrmdir -- " + shellq.Quote(dir) + "\n" +
		shellq.Quote(executable) + " attach " + shellq.Quote(kind) + " " + shellq.Quote(id) + "\n" +
		"result=$?\nif [ \"$result\" -ne 0 ]; then printf '\\nAttachment failed; press Enter to close this tab.\\n'; read -r reply; fi\nexit \"$result\"\n"
}
func createTerminalTab(tmux string, prefix []string, target, executable, base, token, kind, id string) error {
	dir, err := os.MkdirTemp("", "lectern-tab-")
	if err != nil {
		return err
	}
	script := filepath.Join(dir, "open.sh")
	if err = os.WriteFile(script, []byte(terminalTabScript(executable, base, token, os.Getenv("LECTERN_ATTACH_HOST"), kind, id, script, dir)), 0700); err != nil {
		os.RemoveAll(dir)
		return err
	}
	args := append(append([]string(nil), prefix...), "new-window", "-d", "-t", target+":", "-n", kind+" "+id, "--", script)
	if output, err := exec.Command(tmux, args...).CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		return fmt.Errorf("open terminal tab: %s", cleanTerminalError(output))
	}
	return nil
}

func runTerminalWorkspace(tmux, executable, base, token, kind, id string, batch bool) error {
	dir, err := os.MkdirTemp("", "lectern-workspace-")
	if err != nil {
		return err
	}
	socket := filepath.Join(dir, "sock")
	originalTTY, _ := term.GetState(int(os.Stdin.Fd()))
	cleanup := func() {
		_ = exec.Command(tmux, "-S", socket, "kill-server").Run()
		_ = os.RemoveAll(dir)
		if originalTTY != nil {
			_ = term.Restore(int(os.Stdin.Fd()), originalTTY)
		}
	}
	defer cleanup()
	stopSignals := watchAttachmentSignals(cleanup)
	defer stopSignals()
	config := filepath.Join(dir, "tmux.conf")
	// Ctrl-g belongs to the workspace so Ctrl-b still reaches agent terminals.
	conf := "set -g mouse on\nset -g prefix C-g\nunbind C-b\nbind C-g send-prefix\nset -g status-left ' Lectern | '\nset -g status-right ' Ctrl-g n/p: tabs | Ctrl-g 0: Sessions '\nset -g status-left-length 12\nset -g status-right-length 44\nset -g status-interval 0\nset -g exit-empty on\nset -g default-shell /bin/sh\n"
	if err = os.WriteFile(config, []byte(conf), 0600); err != nil {
		return err
	}
	dashboard := filepath.Join(dir, "dashboard.sh")
	body := "#!/bin/sh\nexport LECTERN_INITIAL_SESSION=" + shellq.Quote(id) + "\nexport LECTERN_INITIAL_BATCH=" + fmt.Sprint(batch) + "\n" + shellq.Quote(executable) + " console\n" + shellq.Quote(tmux) + " -S " + shellq.Quote(socket) + " detach-client -s workspace\n"
	if err = os.WriteFile(dashboard, []byte(body), 0700); err != nil {
		return err
	}
	width, height, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		width, height = 120, 35
	}
	command := exec.Command(tmux, "-S", socket, "-f", config, "new-session", "-d", "-s", "workspace", "-n", "Sessions", "-x", fmt.Sprint(width), "-y", fmt.Sprint(height), "--", dashboard)
	command.Env = append(withoutEnv(os.Environ(), "TMUX"), "LECTERN_API="+base, "LECTERN_AUTH_TOKEN="+token)
	if out, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("start terminal workspace: %s", cleanTerminalError(out))
	}
	if err = createTerminalTab(tmux, []string{"-S", socket}, "workspace", executable, base, token, kind, id); err != nil {
		return err
	}
	attach := exec.Command(tmux, "-S", socket, "attach-session", "-t", "workspace")
	attach.Env = portableTerm(withoutEnv(os.Environ(), "TMUX"), terminfoDirs())
	attach.Stdin, attach.Stdout, attach.Stderr = os.Stdin, os.Stdout, os.Stderr
	return attach.Run()
}
