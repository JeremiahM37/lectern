package ptyhost

import (
	"bytes"
	"encoding/base64"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/testutil"
)

// TestMain lets the test binary stand in for `lectern ptyhost serve`, so the
// host these tests talk to is a separate, detached process started exactly
// as Lectern starts one (Ensure).
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "ptyhost" && os.Args[2] == "serve" {
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		socket := fs.String("socket", "", "")
		_ = fs.Parse(os.Args[3:])
		if err := Serve(Options{Socket: *socket, Idle: 3 * time.Second, Build: "test"}); err != nil && err != ErrRunning {
			os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// requireRealProcesses gates tests that start programs. On Linux they run in
// the reviewed isolated runner like every real-process test here; the macOS
// and Windows CI machines are disposable and have no tmux or Lectern
// sessions of anyone's to disturb.
func requireRealProcesses(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "linux" {
		testutil.RequireIsolated(t)
		return
	}
	if os.Getenv("CI") != "true" && os.Getenv("LECTERN_PTYHOST_TESTS") != "1" {
		t.Skip("set LECTERN_PTYHOST_TESTS=1 to start real programs")
	}
}

// shortDir is a temp directory whose socket path fits in sun_path (108
// bytes), which t.TempDir's test-named paths may not.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "lpty")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// shellFor is an interactive shell, a line that makes it print a result,
// and the result — computed, so the typed line itself never matches.
func shellFor() (argv []string, line, want string) {
	if runtime.GOOS == "windows" {
		return []string{"cmd.exe"}, "set /a 6*7+1000", "1042"
	}
	return []string{"/bin/sh"}, "echo pty-$((6*7+1000))", "pty-1042"
}

type harness struct {
	t      *testing.T
	socket string
	bin    string
}

func (h harness) run(args ...string) (string, string, int) {
	var out, errb bytes.Buffer
	cli := &CLI{Socket: h.socket, Bin: h.bin, Stdout: &out, Stderr: &errb}
	code := cli.Run(args)
	return out.String(), errb.String(), code
}

func (h harness) must(args ...string) string {
	h.t.Helper()
	out, errOut, code := h.run(args...)
	if code != 0 {
		h.t.Fatalf("pty %s: exit %d: %s", strings.Join(args, " "), code, errOut)
	}
	return out
}

func (h harness) waitCapture(target, want string) string {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		out := h.must("capture-pane", "-p", "-t", target)
		if strings.Contains(out, want) {
			return out
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("pane never showed %q:\n%s", want, out)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestHostKeepsASessionAcrossClients(t *testing.T) {
	requireRealProcesses(t)
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	h := harness{t: t, socket: filepath.Join(shortDir(t), "s.sock"), bin: bin}
	t.Cleanup(func() { h.run("kill-session", "-t", "=smoke") })
	argv, line, want := shellFor()

	// Starting a session starts the host, detached, as a process of its own.
	h.must(append([]string{"new-session", "-d", "-s", "smoke", "-x", "100", "-y", "30", "-e", "LECTERN_MARK=m1", "--"}, argv...)...)
	c, err := Dial(h.socket)
	if err != nil {
		t.Fatal(err)
	}
	if c.Hello.PID == os.Getpid() || c.Hello.Protocol != Protocol {
		t.Fatalf("host = %+v", c.Hello)
	}
	c.Close()
	if _, _, code := h.run("has-session", "-t", "=smoke"); code != 0 {
		t.Fatal("the new session is not there")
	}
	if _, errOut, code := h.run("has-session", "-t", "=nope"); code != 1 || !strings.Contains(errOut, "can't find session: =nope") {
		t.Fatalf("missing session: exit %d %q", code, errOut)
	}
	if got := h.must("show-environment", "-t", "=smoke:", "LECTERN_MARK"); got != "LECTERN_MARK=m1\n" {
		t.Fatalf("show-environment = %q", got)
	}

	// Send text, then read it back from the screen model.
	h.must("send-keys", "-t", "=smoke:", "-l", line)
	h.must("send-keys", "-t", "=smoke:", "Enter")
	h.waitCapture("=smoke:", want)

	// Resize: the program and the screen model both see the new size.
	h.must("resize-window", "-t", "=smoke", "-x", "90", "-y", "20")
	if got := strings.TrimSpace(h.must("display-message", "-p", "-t", "=smoke:", "#{window_width}x#{window_height}")); got != "90x20" {
		t.Fatalf("size = %q", got)
	}
	if rows := strings.Count(h.must("capture-pane", "-p", "-t", "=smoke:"), "\n"); rows != 20 {
		t.Fatalf("capture has %d rows, want 20", rows)
	}

	// Attach: the snapshot shows the screen, and typing reaches the program.
	c, err = Dial(h.socket)
	if err != nil {
		t.Fatal(err)
	}
	st, err := c.Attach("smoke", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, st, want)
	line2, want2 := strings.Replace(line, "1000", "2000", 1), strings.Replace(want, "1042", "2042", 1)
	if err := st.Write([]byte(line2 + "\r")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, st, want2)
	// The client goes away — as it does when the Lectern server restarts or
	// is upgraded. The session does not.
	st.Close()
	c2, err := Dial(h.socket)
	if err != nil {
		t.Fatal(err)
	}
	st2, err := c2.Attach("smoke", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, st2, want2) // the reattached snapshot still has the last output
	st2.Close()

	// The batched poll and the probe use the sessions package's framing.
	poll := h.must("poll", "-S", "10", "smoke", "gone")
	lines := strings.Split(strings.TrimSpace(poll), "\n")
	if len(lines) != 3 || lines[2] != PollEnd || !strings.HasPrefix(lines[1], b64("gone")+"\tmissing\t") {
		t.Fatalf("poll = %q", poll)
	}
	fields := strings.Split(lines[0], "\t")
	text, _ := base64.StdEncoding.DecodeString(fields[2])
	if fields[1] != "ok" || !strings.Contains(string(text), want2) {
		t.Fatalf("poll frame = %q", lines[0])
	}
	probe := h.must("probe", "smoke")
	if !strings.HasPrefix(probe, b64("smoke")+"\tok\t") || !strings.HasSuffix(probe, PollEnd+"\n") {
		t.Fatalf("probe = %q", probe)
	}

	// A tracking identity is set once, and only a matching one can stop it.
	h.must("set-option", "-o", "-t", "=smoke:", "@lectern-tracking-identity", "abc")
	if _, _, code := h.run("set-option", "-o", "-t", "=smoke:", "@lectern-tracking-identity", "zzz"); code == 0 {
		t.Fatal("set-option -o replaced an identity")
	}
	h.must("if-shell", "-F", "-t", "=smoke:", "#{==:#{@lectern-tracking-identity},zzz}", "kill-session -t =smoke")
	if _, _, code := h.run("has-session", "-t", "=smoke"); code != 0 {
		t.Fatal("a mismatched identity stopped the session")
	}
	h.must("if-shell", "-F", "-t", "=smoke:", "#{==:#{@lectern-tracking-identity},abc}", "kill-session -t =smoke")
	if _, _, code := h.run("has-session", "-t", "=smoke"); code != 1 {
		t.Fatal("the session survived kill-session")
	}
}

func TestSessionEndsWithItsProgram(t *testing.T) {
	requireRealProcesses(t)
	bin, _ := os.Executable()
	h := harness{t: t, socket: filepath.Join(shortDir(t), "s.sock"), bin: bin}
	line := "echo bye; exit 3"
	h.must("new-session", "-d", "-s", "brief", line)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, _, code := h.run("has-session", "-t", "=brief"); code == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the session outlived its program")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// With nothing left the host exits by itself; a later session gets a
	// fresh one, which is how an upgraded binary takes over.
	deadline = time.Now().Add(15 * time.Second)
	for {
		c, err := Dial(h.socket)
		if IsNotRunning(err) {
			break
		}
		if c != nil {
			c.Close()
		}
		if time.Now().After(deadline) {
			t.Fatal("an idle host did not exit")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func readUntil(t *testing.T, st *Stream, want string) {
	t.Helper()
	var seen strings.Builder
	done := make(chan error, 1)
	go func() {
		for {
			b, err := st.Read()
			if err != nil {
				done <- err
				return
			}
			seen.Write(b)
			if strings.Contains(seen.String(), want) {
				done <- nil
				return
			}
		}
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stream ended before %q: %v\n%s", want, err, seen.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("stream never showed %q", want)
	}
}
