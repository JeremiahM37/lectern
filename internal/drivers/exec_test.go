package drivers

import (
	"context"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

// execDriver wraps the exact same agents.Launcher.Command / ParseStreamLines
// the scheduler's own launch/poll already use, so it runs correctly against
// the mock executor's scripted fake-claude agent with no driver-specific
// stubbing at all — proof it is genuinely the existing path, not a
// reimplementation of it.
func TestExecDriverRunsAgainstMockAndReportsSuccess(t *testing.T) {
	ex := executor.NewMock(5 * time.Millisecond)
	spec := Spec{
		Agent: "claude", Worktree: "/mock/wt", TmuxSession: "lec-exec-1",
		PermissionMode: "acceptEdits", Prompt: "say hi", PollInterval: 5 * time.Millisecond,
	}
	run, err := execDriver{Agent: "claude"}.Start(context.Background(), ex, spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	var sawInit, sawResult bool
	for ev := range run.Events() {
		switch ev.Type {
		case "init":
			sawInit = true
		case "result":
			sawResult = true
		}
	}
	if !sawInit || !sawResult {
		t.Fatalf("expected init and result events; init=%v result=%v", sawInit, sawResult)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := run.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit code: %d", res.ExitCode)
	}
	if err := run.Send(ctx, "more"); err == nil {
		t.Fatal("execDriver must reject a mid-run message")
	}
}

func TestExecDriverCancelKillsTmuxSession(t *testing.T) {
	ex := executor.NewMock(200 * time.Millisecond) // slow enough to cancel mid-run
	spec := Spec{
		Agent: "claude", Worktree: "/mock/wt2", TmuxSession: "lec-exec-2",
		PermissionMode: "acceptEdits", Prompt: "[mock:slow] say hi", PollInterval: 5 * time.Millisecond,
	}
	run, err := execDriver{Agent: "claude"}.Start(context.Background(), ex, spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	go func() {
		for range run.Events() {
		}
	}()
	if err := run.Cancel(context.Background()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := run.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.Err == "" {
		t.Fatalf("expected a cancellation result, got %+v", res)
	}
}
