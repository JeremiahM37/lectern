package drivers

// Real tmux, a real local process, a real fifo — the only stand-in is the
// agent binary, a python3 script that speaks claude's streaming-input/output
// protocol shape closely enough to prove Lectern's plumbing (not an LLM).

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/testutil"
)

func isolateTmux(t *testing.T) {
	t.Helper()
	dir, err := os.MkdirTemp("", "lec-drivers-tmux-")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_TMPDIR", dir)
	t.Cleanup(func() {
		testutil.CleanupTmux(t, dir)
		os.RemoveAll(dir)
	})
}

func requireRealTools(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"tmux", "bash", "python3"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed; this test needs the real thing", bin)
		}
	}
}

// stubClaudeSteer echoes each streaming-input user message back as an
// assistant text event plus a result event, and exits cleanly on stdin EOF
// (which is what closeCommand's end sentinel, consumed by the pump, produces
// for a process reading real stdin).
const stubClaudeSteer = `#!/usr/bin/env python3
import sys, json
print(json.dumps({"type":"system","subtype":"init","session_id":"stub-1","tools":[]}), flush=True)
for raw in sys.stdin:
    raw = raw.strip()
    if not raw:
        continue
    try:
        msg = json.loads(raw)
        text = msg["message"]["content"][0]["text"]
    except Exception:
        text = raw
    print(json.dumps({"type":"assistant","message":{"content":[{"type":"text","text":"echo:" + text}]}}), flush=True)
    print(json.dumps({"type":"result","subtype":"success","total_cost_usd":0.02,
                       "duration_ms":1,"num_turns":1,"result":"ok","session_id":"stub-1"}), flush=True)
sys.exit(0)
`

func TestClaudeSteerDeliversMidRunMessageAndClosesCleanly(t *testing.T) {
	testutil.RequireIsolated(t)
	requireRealTools(t)
	dir := t.TempDir()
	isolateTmux(t)

	wt := filepath.Join(dir, "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(dir, "fake-claude")
	if err := os.WriteFile(stub, []byte(stubClaudeSteer), 0o755); err != nil {
		t.Fatal(err)
	}

	ex := executor.NewLocal()
	ctx := context.Background()
	spec := Spec{
		Agent: "claude", Bin: stub, Worktree: wt, TmuxSession: "lec-drv-steer",
		Prompt: "hello", PollInterval: 30 * time.Millisecond,
	}
	run, err := claudeSteerDriver{}.Start(ctx, ex, spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	texts := make(chan string, 10)
	go func() {
		for ev := range run.Events() {
			if ev.Type == "text" {
				if s, ok := ev.Payload["text"].(string); ok {
					texts <- s
				}
			}
		}
		close(texts)
	}()

	deadline := time.After(10 * time.Second)
	select {
	case first := <-texts:
		if first != "echo:hello" {
			t.Fatalf("first echo: %q", first)
		}
	case <-deadline:
		t.Fatal("never received the initial prompt's echo")
	}

	// mid-run steering: the process is still alive, waiting on its fifo-backed
	// stdin, when this message is sent.
	if err := run.Send(ctx, "steer message"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case second := <-texts:
		if second != "echo:steer message" {
			t.Fatalf("steered echo: %q", second)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the steered message was never reflected in events")
	}

	if err := run.Cancel(ctx); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case v, ok := <-texts:
		if ok {
			t.Fatalf("unexpected extra text event: %q", v)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("events channel never closed after Cancel")
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := run.Wait(waitCtx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit code: %d (err=%q)", res.ExitCode, res.Err)
	}

	deadline2 := time.Now().Add(3 * time.Second)
	for exec.Command("tmux", "has-session", "-t", "=lec-drv-steer").Run() == nil {
		if time.Now().After(deadline2) {
			t.Fatal("tmux session is still alive after the run finished")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestClaudeSteerRejectsSendAfterEnd(t *testing.T) {
	testutil.RequireIsolated(t)
	requireRealTools(t)
	dir := t.TempDir()
	isolateTmux(t)
	wt := filepath.Join(dir, "wt")
	os.MkdirAll(wt, 0o755)
	stub := filepath.Join(dir, "fake-claude")
	os.WriteFile(stub, []byte(stubClaudeSteer), 0o755)

	ex := executor.NewLocal()
	ctx := context.Background()
	run, err := claudeSteerDriver{}.Start(ctx, ex, Spec{
		Agent: "claude", Bin: stub, Worktree: wt, TmuxSession: "lec-drv-steer-2",
		Prompt: "hi", PollInterval: 30 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	go func() {
		for range run.Events() {
		}
	}()
	if err := run.Cancel(ctx); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := run.Wait(waitCtx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if err := run.Send(ctx, "too late"); err == nil {
		t.Fatal("Send after the run ended must fail")
	} else if !strings.Contains(err.Error(), "already ended") {
		t.Fatalf("unexpected error: %v", err)
	}
}
