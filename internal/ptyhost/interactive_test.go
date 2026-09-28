//go:build !windows

package ptyhost

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/testutil"
	"github.com/creack/pty"
)

func TestPortableAttachChild(t *testing.T) {
	if os.Getenv("LECTERN_PORTABLE_TEST_CHILD") != "1" {
		return
	}
	err := AttachProcess([]string{"sh", "-c", "printf 'AGENT_READY\\n'; cat"}, os.Stdin, os.Stdout, TerminalControls{
		Action: func(action string, _ func(string) error) error {
			fmt.Fprintf(os.Stdout, "ACTION_%s", action)
			return nil
		},
		Status: func() string { return "waiting" },
		History: func(text string) error {
			if !strings.Contains(text, "AGENT_READY") {
				return fmt.Errorf("history omitted output")
			}
			fmt.Fprint(os.Stdout, "HISTORY_VIEW")
			return nil
		},
		Links: func(text string, width int, insert func(string) error) error {
			if !strings.Contains(text, "AGENT_READY") || width != 80 {
				return fmt.Errorf("incorrect screen passed to link picker")
			}
			fmt.Fprint(os.Stdout, "LINK_PICKER")
			return insert("path-from-picker\n")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPortableAttachmentControlsWithoutTmux(t *testing.T) {
	testutil.RequireIsolated(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestPortableAttachChild$")
	cmd.Env = append(os.Environ(), "LECTERN_PORTABLE_TEST_CHILD=1")
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
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
	wait := func(want string) {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
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
	wait("AGENT_READY")
	wait("Ctrl+] menu")
	wait("Needs you")
	_, _ = f.Write([]byte("\x1dm"))
	wait("ACTION_menu")
	_, _ = f.Write([]byte("\x1c"))
	wait("ACTION_upload")
	_, _ = f.Write([]byte("\x1de"))
	wait("LINK_PICKER")
	wait("path-from-picker")
	_, _ = f.Write([]byte("\x1d["))
	wait("HISTORY_VIEW")
	_, _ = f.Write([]byte("still-alive\n"))
	wait("still-alive")
	_, _ = f.Write([]byte("\x1dd"))
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("detach did not return")
	}
}
