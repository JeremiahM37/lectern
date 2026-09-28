//go:build !windows

package drivers

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/helpers/helperstest"
	"github.com/JeremiahM37/lectern/v2/internal/sessions/backend"
	"github.com/JeremiahM37/lectern/v2/internal/testutil"
)

func TestHelperProcess(t *testing.T) { helperstest.Serve() }

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// pumpStep is one writer: it opens the fifo, writes chunk and leaves, and the
// relay has then printed out in total.
type pumpStep struct{ chunk, out string }

// runPump drives a relay through steps like the steer path does (one short
// writer per message) and returns its output and exit status.
func runPump(t *testing.T, cmd *exec.Cmd, fifo string, steps []pumpStep) (string, int) {
	t.Helper()
	var out lockedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &lockedBuffer{}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan int, 1)
	go func() {
		err := cmd.Wait()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		exited <- code
	}()
	for _, s := range steps {
		f, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(s.chunk)
		f.Close()
		deadline := time.Now().Add(5 * time.Second)
		for out.String() != s.out && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		// give a step that prints nothing time to be read
		time.Sleep(50 * time.Millisecond)
	}
	select {
	case code := <-exited:
		return out.String(), code
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		<-exited
		t.Fatalf("the relay did not exit; output so far %q", out.String())
		return "", 0
	}
}

// The Go pump must relay exactly what pump.py relays — line endings read as
// Python's text mode reads them, blank lines dropped, a line left without a
// newline delivered when its writer leaves — and exit as it does.
func TestPumpHelperParity(t *testing.T) {
	testutil.RequireIsolated(t)
	helperstest.RequirePython(t)
	scenarios := map[string][]pumpStep{
		"relay": {
			{"a\n", "a\n"},
			{"\n\n", "a\n"},
			{"b\r\nc\rd\n", "a\nb\nc\nd\n"},
			{"partial", "a\nb\nc\nd\npartial\n"},
			{"e\r", "a\nb\nc\nd\npartial\ne\n"},
			{"\nf\n", "a\nb\nc\nd\npartial\ne\nf\n"},
			{"café \U0001F600\n", "a\nb\nc\nd\npartial\ne\nf\ncafé \U0001F600\n"},
			{"g\n" + endSentinel + "\nnever\n", "a\nb\nc\nd\npartial\ne\nf\ncafé \U0001F600\ng\n"},
		},
		"sentinel without newline": {
			{"x\n", "x\n"},
			{endSentinel, "x\n"},
		},
		"not UTF-8": {
			{"ok\n", "ok\n"},
			{"\xff\n", "ok\n"},
		},
	}
	for name, steps := range scenarios {
		t.Run(name, func(t *testing.T) {
			run := func(goHelper bool) (string, int) {
				dir := t.TempDir()
				fifo := filepath.Join(dir, "steer.fifo")
				if err := syscall.Mkfifo(fifo, 0o600); err != nil {
					t.Fatal(err)
				}
				env := helperstest.Env(dir)
				if goHelper {
					return runPump(t, helperstest.Command(env, "pump", fifo), fifo, steps)
				}
				script := filepath.Join(dir, "pump.py")
				os.WriteFile(script, []byte(pumpScript), 0o644)
				cmd := exec.Command("python3", script, fifo)
				cmd.Env = env
				return runPump(t, cmd, fifo, steps)
			}
			pyOut, pyCode := run(false)
			goOut, goCode := run(true)
			if pyOut != goOut || pyCode != goCode {
				t.Errorf("python: exit %d %q\ngo:     exit %d %q", pyCode, pyOut, goCode, goOut)
			}
			if want := steps[len(steps)-1].out; goOut != want {
				t.Errorf("relayed %q, want %q", goOut, want)
			}
		})
	}
}

// stagePump picks the Go relay on a target with a lectern binary, and keeps
// writing and running pump.py everywhere else.
func TestStagePumpPicksTheHelper(t *testing.T) {
	ctx := context.Background()
	plain := executor.NewMock(0)
	cmd, err := stagePump(ctx, plain, "/wt/.lectern")
	if err != nil {
		t.Fatal(err)
	}
	if cmd != "python3 /wt/.lectern/pump.py /wt/.lectern/steer.fifo" {
		t.Errorf("fallback: %q", cmd)
	}
	if _, ok := plain.Files()["/wt/.lectern/pump.py"]; !ok {
		t.Error("pump.py was not staged for a target without lectern")
	}

	withLectern := executor.NewMock(0)
	executor.SetTargetEnv(withLectern, executor.TargetEnv{Lectern: "/opt/lectern", SessionBackend: "pty"})
	cmd, err = stagePump(ctx, withLectern, "/wt/.lectern")
	if err != nil {
		t.Fatal(err)
	}
	if cmd != "LECTERN_SESSION_BACKEND=pty /opt/lectern helper pump /wt/.lectern/steer.fifo" {
		t.Errorf("helper: %q", cmd)
	}
	if _, ok := withLectern.Files()["/wt/.lectern/pump.py"]; ok {
		t.Error("pump.py staged although the target runs the Go relay")
	}
	launch := streamLaunchCommand(backend.Tmux, "lec-1", "/wt/.lectern", "/wt", cmd, "claude")
	if !strings.Contains(launch, "cd /wt && LECTERN_SESSION_BACKEND=pty /opt/lectern helper pump /wt/.lectern/steer.fifo | { claude; }") {
		t.Errorf("launch: %s", launch)
	}
}
