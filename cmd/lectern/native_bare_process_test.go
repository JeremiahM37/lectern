package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/ptyhost"
	"github.com/JeremiahM37/lectern/v2/internal/testutil"
)

// The attach client without tmux, end to end on a real terminal: this test
// binary runs the client on a pseudo-terminal (a ConPTY on Windows) and the
// client runs a stand-in agent on one of its own. It is what the PTY-backend
// CI job runs on Windows and macOS.

const (
	bareChildEnv  = "LECTERN_BARE_TEST_CHILD"
	bareReportEnv = "LECTERN_BARE_TEST_REPORT"
)

// bareReport appends a line to the file the test reads. The popup cannot
// say what it was asked for on the screen: on Windows the test reads a
// ConPTY, which sends a picture of the screen every frame rather than the
// bytes written to it, and a popup that names its action and closes at once
// is redrawn over before the next frame, so its words are never sent.
func bareReport(line string) {
	f, err := os.OpenFile(os.Getenv(bareReportEnv), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintln(f, line)
}

func requireRealTerminal(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "linux" {
		// Plain `go test` (the release job's quick pass, a contributor's
		// checkout) skips; the reviewed isolated runner runs it.
		if os.Getenv("ADK_TEST_ISOLATED") != "1" {
			t.Skip("runs in the isolated test runner (tools/run-isolated-tests.sh)")
		}
		testutil.RequireIsolated(t)
		return
	}
	if os.Getenv("CI") != "true" && os.Getenv("LECTERN_PTYHOST_TESTS") != "1" {
		t.Skip("set LECTERN_PTYHOST_TESTS=1 to start real programs")
	}
}

// TestBareAttachmentControlsChild is the attach client, when this binary is
// started by TestBareAttachmentControls. Its controls popup only names the
// action it was asked for and closes at once, which is the moment a key used
// to be lost: the client takes the keyboard back just as the next key comes.
func TestBareAttachmentControlsChild(t *testing.T) {
	if os.Getenv(bareChildEnv) != "client" {
		return
	}
	c, err := newBareClient(nativeControls{Kind: "session", ID: "1"})
	if err != nil {
		t.Fatal(err)
	}
	c.popup = func(action string) {
		if action == "" {
			action = "menu"
		}
		bareReport("ACTION_" + action)
	}
	agent := []string{os.Args[0], "-test.run=^TestBareAttachmentControlsAgent$"}
	if err := c.run(agent, false); err != nil {
		t.Fatal(err)
	}
	bareReport("ATTACH_RETURNED")
}

// TestBareAttachmentControlsAgent is the stand-in agent: it echoes lines.
func TestBareAttachmentControlsAgent(t *testing.T) {
	if os.Getenv(bareChildEnv) != "client" {
		return
	}
	fmt.Println("AGENT_READY")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		fmt.Println("ECHO:", scanner.Text())
	}
}

func TestBareAttachmentControls(t *testing.T) {
	requireRealTerminal(t)
	t.Setenv(bareChildEnv, "client")
	report := filepath.Join(t.TempDir(), "report")
	t.Setenv(bareReportEnv, report)
	t.Setenv(ptyhost.SocketEnv, shortSocket(t))
	f, err := ptyhost.StartProcess([]string{os.Args[0], "-test.run=^TestBareAttachmentControlsChild$"}, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var mu sync.Mutex
	var output bytes.Buffer
	go func() {
		buf := make([]byte, 4096)
		for {
			n, e := f.Read(buf)
			mu.Lock()
			output.Write(buf[:n])
			mu.Unlock()
			if e != nil {
				return
			}
		}
	}()
	// wait looks for what the client drew on the screen.
	wait := func(want string) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			ok := bytes.Contains(output.Bytes(), []byte(want))
			mu.Unlock()
			if ok {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("missing %q in %q", want, output.String())
	}
	// reported waits for what the client's popup was asked to do.
	reported := func(want string) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		var got []byte
		for time.Now().Before(deadline) {
			got, _ = os.ReadFile(report)
			if bytes.Contains(got, []byte(want+"\n")) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("client did not report %q; reported %q; screen %q", want, got, output.String())
	}
	type_ := func(s string) {
		t.Helper()
		if _, err := f.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	wait("AGENT_READY")
	wait("Ctrl+]")
	// Each key goes in the moment the previous popup reports, while the
	// client is still taking the keyboard back.
	type_("\x1dm")
	reported("ACTION_menu")
	type_("\x1c")
	reported("ACTION_upload")
	type_("still-alive\r")
	wait("ECHO: still-alive")
	type_("\x1dd")
	reported("ATTACH_RETURNED")
	select {
	case <-f.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("leaving did not end the client")
	}
}

// shortSocket keeps any PTY host this test might reach off the default
// socket; it fits in sun_path where t.TempDir's long names may not.
func shortSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "lbt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir + string(os.PathSeparator) + "p.sock"
}
