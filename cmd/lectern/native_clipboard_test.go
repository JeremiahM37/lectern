package main

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// tmux 3.7 prints nothing for `list-keys -T root KEY` and pads the listing
// with two spaces; the link bindings must still find tmux's own mouse
// bindings, and fall back to the built-in ones when they cannot.
func TestMouseDefaultsReadTheWholeRootTable(t *testing.T) {
	listing := "bind-key  -T root MouseDown3Pane            if-shell -F -t = \"#{mouse_any_flag}\" { send-keys -M } { display-menu -x M -y M X x { kill-pane } }\n" +
		"bind-key  -T root DoubleClick1Pane          select-pane -t = \\; send-keys -M\n" +
		"bind-key  -T root M-MouseDown3Pane          display-menu -T M\n"
	got := mouseDefaults(listing)
	if got["DoubleClick1Pane"] != "select-pane -t = ; send-keys -M" {
		t.Fatalf("double-click: %q", got["DoubleClick1Pane"])
	}
	if !strings.HasPrefix(got["MouseDown3Pane"], "if-shell -F -t =") {
		t.Fatalf("right-click: %q", got["MouseDown3Pane"])
	}
	fallback := mouseDefaults("")
	if fallback["DoubleClick1Pane"] != defaultDoubleClick || fallback["MouseDown3Pane"] != defaultRightClick {
		t.Fatalf("no fallback: %v", fallback)
	}
	// The fallbacks parse as tmux configuration.
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	dir := t.TempDir()
	conf := filepath.Join(dir, "links.conf")
	os.WriteFile(conf, []byte(linkBindings(filepath.Join(dir, "link.sh"), filepath.Join(dir, "menu.conf"), fallback)), 0o600)
	socket := filepath.Join(dir, "s")
	if out, err := exec.Command(tmuxPath, "-S", socket, "-f", "/dev/null", "new-session", "-d", "sleep 30").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	t.Cleanup(func() { exec.Command(tmuxPath, "-S", socket, "kill-server").Run() })
	if out, err := exec.Command(tmuxPath, "-S", socket, "source-file", conf).CombinedOutput(); err != nil {
		t.Fatalf("fallback bindings do not parse: %v %s", err, out)
	}
}

// The attachment's private server always carries the link bindings.
func TestPrivateServerInstallsTheLinkBindings(t *testing.T) {
	r := newLinkRig(t, codexScreen(t), false, "Jeremiah_Mackey_Cerebras.pdf)")
	keys := r.run("list-keys", "-T", "root")
	for _, want := range []string{"link.sh click", "link.sh menu", "Split: shell in project"} {
		if !strings.Contains(keys, want) {
			t.Fatalf("bindings missing %q:\n%s", want, keys)
		}
	}
}

// fakeTerminal runs argv in a pty, the way a terminal would, and records
// every byte the program writes to it.
func fakeTerminal(t *testing.T, log string, argv ...string) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "term.py")
	body := `import fcntl,os,pty,struct,sys,termios
log=open(sys.argv[1],'ab',buffering=0)
pid,fd=pty.fork()
if pid==0:
    os.execvp(sys.argv[2],sys.argv[2:])
fcntl.ioctl(fd,termios.TIOCSWINSZ,struct.pack('HHHH',30,100,0,0))
while True:
    try: b=os.read(fd,65536)
    except OSError: break
    if not b: break
    log.write(b)
`
	os.WriteFile(script, []byte(body), 0o600)
	cmd := exec.Command("python3", append([]string{script, log}, argv...)...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A copy made by an agent inside the session's tmux (Claude Code runs
// `tmux load-buffer -w -`) reaches the outermost terminal as OSC 52, through
// the attachment's private server; so does a copy-mode selection made in
// the private server itself.
func TestCopiesReachTheOutermostTerminal(t *testing.T) {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	dir := t.TempDir()
	agentSock := filepath.Join(dir, "agent")
	// The session's own tmux, with tmux's defaults (set-clipboard external).
	if out, err := exec.Command(tmuxPath, "-S", agentSock, "-f", "/dev/null", "new-session", "-d", "-s", "agent", "bash --norc").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	t.Cleanup(func() { exec.Command(tmuxPath, "-S", agentSock, "kill-server").Run() })
	priv, err := os.MkdirTemp("", "lc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(priv) })
	plan, err := newNativeWrapPlan(priv, filepath.Join(priv, "sock"), nativeControls{Kind: "session", ID: "17", Base: "http://127.0.0.1:9"},
		[]string{"env", "TERM=xterm-256color", tmuxPath, "-S", agentSock, "attach", "-t", "agent"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.write(); err != nil {
		t.Fatal(err)
	}
	if err := plan.start(tmuxPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { exec.Command(tmuxPath, "-S", plan.socket, "kill-server").Run() })
	log := filepath.Join(dir, "terminal.log")
	fakeTerminal(t, log, tmuxPath, "-S", plan.socket, "attach", "-t", plan.session)
	agentClients := func() bool {
		out, _ := exec.Command(tmuxPath, "-S", agentSock, "list-clients").Output()
		return strings.TrimSpace(string(out)) != ""
	}
	waitFor(t, "the attachment to reach the agent's tmux", agentClients)
	waitFor(t, "the terminal to attach", func() bool {
		out, _ := exec.Command(tmuxPath, "-S", plan.socket, "list-clients").Output()
		return strings.TrimSpace(string(out)) != ""
	})
	waitFor(t, "the agent's shell", func() bool {
		out, _ := exec.Command(tmuxPath, "-S", agentSock, "capture-pane", "-p", "-t", "=agent:").Output()
		return strings.Contains(string(out), "$")
	})
	osc52 := func(text string) func() bool {
		// tmux names no selection (ESC ]52;;…), which terminals such as
		// kitty and xterm take as the clipboard.
		want := regexp.MustCompile(`\x1b\]52;[cps]*;` + regexp.QuoteMeta(base64.StdEncoding.EncodeToString([]byte(text))))
		return func() bool {
			data, _ := os.ReadFile(log)
			return want.Match(data)
		}
	}
	// Claude Code's copy, from inside the agent's pane (naming the server
	// only because this test's tmux is not the default one).
	exec.Command(tmuxPath, "-S", agentSock, "send-keys", "-t", "=agent:", "printf 'copied by the agent' | tmux -S "+agentSock+" load-buffer -w -", "Enter").Run()
	waitFor(t, "the agent's copy to reach the terminal as OSC 52", osc52("copied by the agent"))
	// A selection in the private server's copy mode.
	exec.Command(tmuxPath, "-S", agentSock, "send-keys", "-t", "=agent:", "clear; echo SELECT-ME-PLEASE", "Enter").Run()
	pane := strings.TrimSpace(func() string {
		out, _ := exec.Command(tmuxPath, "-S", plan.socket, "display-message", "-p", "-t", plan.session, "#{pane_id}").Output()
		return string(out)
	}())
	waitFor(t, "the line on screen", func() bool {
		out, _ := exec.Command(tmuxPath, "-S", plan.socket, "capture-pane", "-p", "-t", pane).Output()
		return regexp.MustCompile(`(?m)^SELECT-ME-PLEASE`).Match(out)
	})
	run := func(args ...string) {
		if out, err := exec.Command(tmuxPath, append([]string{"-S", plan.socket}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
	}
	run("copy-mode", "-t", pane)
	run("send-keys", "-t", pane, "-X", "search-backward", "SELECT-ME-PLEASE")
	run("send-keys", "-t", pane, "-X", "begin-selection")
	run("send-keys", "-t", pane, "-X", "end-of-line")
	run("send-keys", "-t", pane, "-X", "copy-pipe-and-cancel")
	waitFor(t, "the selection to reach the terminal as OSC 52", osc52("SELECT-ME-PLEASE"))
}
