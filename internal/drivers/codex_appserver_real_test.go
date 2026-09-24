package drivers

// Real tmux, a real local process, a real fifo, a real broker/DB. The stub
// codex binary is a small JSON-RPC state machine (see stubCodexAppServer)
// exercising exactly the methods codex_appserver.go's doc comment lists —
// read straight off `codex app-server generate-json-schema`, not guessed.

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/broker"
	"github.com/JeremiahM37/lectern/v2/internal/bus"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/sinks"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/testutil"
)

// brokerRig gives an approval test a real Broker backed by a real (temp)
// SQLite DB and bus — the same components production wires in internal/app.
func brokerRig(t *testing.T) (*broker.Broker, *store.DB, int64) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	target, err := db.InsertTarget(&store.Target{Name: "t", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.InsertProject(&store.Project{Name: "p", TargetID: target.ID, RepoPath: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := db.InsertTask(&store.Task{ProjectID: project.ID, Title: "t", Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	att, err := db.InsertAttempt(&store.Attempt{TaskID: task.ID, N: 1, Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	b := bus.New()
	notifier := &sinks.Notifier{DB: db, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	br := broker.New(db, b, notifier, 900*time.Second)
	return br, db, att.ID
}

// stubCodexAppServer implements initialize/thread/start/turn/start/turn/
// steer/turn/interrupt, and on turn/start issues an execCommandApproval
// server request before completing the turn — so a test can prove the
// request reached the broker and the broker's decision reached back to this
// process, not just that JSON was exchanged.
const stubCodexAppServer = `#!/usr/bin/env python3
import sys, json

def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

turn_id = [0]
for raw in sys.stdin:
    raw = raw.strip()
    if not raw:
        continue
    msg = json.loads(raw)
    method = msg.get("method")
    if method == "initialize":
        send({"jsonrpc": "2.0", "id": msg["id"], "result": {
            "codexHome": "/tmp", "platformFamily": "unix", "platformOs": "linux", "userAgent": "stub"}})
    elif method == "thread/start":
        send({"jsonrpc": "2.0", "id": msg["id"], "result": {"thread": {"id": "thread-1"}}})
    elif method == "turn/start":
        turn_id[0] += 1
        tid = "turn-%d" % turn_id[0]
        send({"jsonrpc": "2.0", "id": msg["id"], "result": {"turn": {"id": tid, "status": "in_progress", "items": []}}})
        if turn_id[0] == 1:
            # only the FIRST turn asks for an approval; a later turn/start —
            # which is exactly what Send() issues once the prior turn has
            # already completed, since turn/steer requires an ACTIVE turn —
            # completes directly, echoing the input so the test can tell its
            # follow-up message actually reached a NEW turn.
            send({"jsonrpc": "2.0", "id": "srv-approval-1", "method": "execCommandApproval",
                  "params": {"callId": "call-1", "command": ["echo", "hi"], "conversationId": "thread-1",
                             "cwd": "/tmp", "parsedCmd": []}})
        else:
            text = msg["params"]["input"][0]["text"]
            send({"jsonrpc": "2.0", "method": "item/completed", "params": {
                "threadId": "thread-1", "turnId": tid, "completedAtMs": 0,
                "item": {"id": "item-followup", "type": "agentMessage", "text": "steered:" + text}}})
            send({"jsonrpc": "2.0", "method": "turn/completed",
                  "params": {"threadId": "thread-1", "turn": {"id": tid, "status": "completed", "items": []}}})
    elif method == "turn/steer":
        send({"jsonrpc": "2.0", "id": msg["id"], "result": {"turnId": msg["params"]["expectedTurnId"]}})
        send({"jsonrpc": "2.0", "method": "item/completed", "params": {
            "threadId": "thread-1", "turnId": msg["params"]["expectedTurnId"], "completedAtMs": 0,
            "item": {"id": "item-steer", "type": "agentMessage", "text": "steered:" + msg["params"]["input"][0]["text"]}}})
        send({"jsonrpc": "2.0", "method": "turn/completed",
              "params": {"threadId": "thread-1", "turn": {"id": msg["params"]["expectedTurnId"], "status": "completed", "items": []}}})
    elif method == "turn/interrupt":
        send({"jsonrpc": "2.0", "id": msg["id"], "result": {}})
    elif method is None and msg.get("id") == "srv-approval-1":
        decision = msg.get("result", {}).get("decision")
        send({"jsonrpc": "2.0", "method": "item/completed", "params": {
            "threadId": "thread-1", "turnId": "turn-%d" % turn_id[0], "completedAtMs": 0,
            "item": {"id": "item-1", "type": "commandExecution", "command": "echo hi",
                     "commandActions": [], "cwd": "/tmp", "status": "completed",
                     "aggregatedOutput": json.dumps(decision), "exitCode": 0}}})
        send({"jsonrpc": "2.0", "method": "turn/completed",
              "params": {"threadId": "thread-1", "turn": {"id": "turn-%d" % turn_id[0], "status": "completed", "items": []}}})
sys.exit(0)
`

func TestCodexAppServerApprovalRoutesThroughBrokerAndSteers(t *testing.T) {
	testutil.RequireIsolated(t)
	requireRealTools(t)
	dir := t.TempDir()
	isolateTmux(t)
	wt := filepath.Join(dir, "wt")
	os.MkdirAll(wt, 0o755)
	stub := filepath.Join(dir, "fake-codex")
	os.WriteFile(stub, []byte(stubCodexAppServer), 0o755)

	br, db, attemptID := brokerRig(t)
	ex := executor.NewLocal()
	ctx := context.Background()

	run, err := codexAppServerDriver{}.Start(ctx, ex, Spec{
		Agent: "codex", Bin: stub, Worktree: wt, TmuxSession: "lec-drv-codex-1",
		Prompt: "do the thing", PollInterval: 30 * time.Millisecond,
		Broker: br, AttemptID: attemptID, ApprovalTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Start (handshake): %v", err)
	}

	events := make(chan agentsEventLike, 32)
	go func() {
		for ev := range run.Events() {
			events <- agentsEventLike{typ: ev.Type, payload: ev.Payload}
		}
		close(events)
	}()

	// the stub's execCommandApproval request must have produced a real
	// pending approval row for this attempt
	deadline := time.Now().Add(10 * time.Second)
	var approvalID int64
	for time.Now().Before(deadline) {
		rows, err := db.ApprovalsByStatus("pending")
		if err == nil {
			for _, r := range rows {
				if r.AttemptID == attemptID && r.ToolName == "Bash" {
					approvalID = r.ID
				}
			}
		}
		if approvalID != 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if approvalID == 0 {
		t.Fatal("execCommandApproval never reached the broker as a pending approval")
	}
	if row := br.Decide(approvalID, "approved", "looks fine", "test"); row == nil {
		t.Fatal("could not decide the approval")
	}

	// the stub's response handling only fires item/completed + turn/completed
	// once IT receives our decision back, so seeing them proves the round trip
	sawApprovedOutput := false
	sawResult := false
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !(sawApprovedOutput && sawResult) {
		select {
		case ev := <-events:
			if ev.typ == "tool_result" {
				if content, _ := ev.payload["content"].(string); content == `"approved"` {
					sawApprovedOutput = true
				}
			}
			if ev.typ == "result" {
				sawResult = true
			}
		case <-time.After(200 * time.Millisecond):
		}
	}
	if !sawApprovedOutput {
		t.Fatal("the broker's decision never reached the stub (no approved tool_result observed)")
	}
	if !sawResult {
		t.Fatal("no result event after the turn completed")
	}

	// steer: send a follow-up while the run is otherwise idle between turns
	if err := run.Send(ctx, "keep going"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	sawSteerEcho := false
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !sawSteerEcho {
		select {
		case ev := <-events:
			if ev.typ == "text" {
				if text, _ := ev.payload["text"].(string); text == "steered:keep going" {
					sawSteerEcho = true
				}
			}
		case <-time.After(200 * time.Millisecond):
		}
	}
	if !sawSteerEcho {
		t.Fatal("the steered message was never reflected in events")
	}

	if err := run.Cancel(ctx); err != nil {
		t.Fatalf("Cancel: %v", err)
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
}

// stubCodexHandshakeFails never answers "initialize" at all (it just exits),
// which is what an app-server-incapable or crashing codex binary looks like
// from the driver's side.
const stubCodexHandshakeFails = `#!/usr/bin/env python3
import sys
sys.exit(1)
`

func TestCodexAppServerFallsBackToExecWhenHandshakeFails(t *testing.T) {
	testutil.RequireIsolated(t)
	requireRealTools(t)
	dir := t.TempDir()
	isolateTmux(t)
	wt := filepath.Join(dir, "wt")
	os.MkdirAll(wt, 0o755)

	// One binary, two subcommands: `app-server` fails immediately (forcing the
	// fallback); `exec --json` behaves like a normal successful codex run, so
	// the test can tell the fallback actually ran and actually worked.
	stub := filepath.Join(dir, "fake-codex")
	os.WriteFile(stub, []byte(stubCodexDualMode), 0o755)

	ex := executor.NewLocal()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	run, err := codexAppServerDriver{}.Start(ctx, ex, Spec{
		Agent: "codex", Bin: stub, Worktree: wt, TmuxSession: "lec-drv-codex-fb",
		PermissionMode: "default", Prompt: "do it", PollInterval: 30 * time.Millisecond,
		HandshakeTimeout: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	var sawNotice, sawResult bool
	for ev := range run.Events() {
		if ev.Type == "raw" {
			if line, _ := ev.Payload["line"].(string); line != "" {
				sawNotice = true
			}
		}
		if ev.Type == "result" {
			sawResult = true
		}
	}
	if !sawNotice {
		t.Error("expected a fallback notice event so the operator can see gated approvals were not available")
	}
	if !sawResult {
		t.Error("expected the exec fallback to still complete a normal run")
	}
	res, err := run.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit code: %d (err=%q)", res.ExitCode, res.Err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for exec.Command("tmux", "has-session", "-t", "=lec-drv-codex-fb").Run() == nil {
		if time.Now().After(deadline) {
			t.Fatal("the failed app-server tmux session is still alive")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

const stubCodexDualMode = `#!/bin/sh
if [ "$1" = "app-server" ]; then
  exit 1
fi
echo '{"type":"thread.started","thread_id":"fb-1"}'
echo '{"type":"turn.completed","usage":{"output_tokens":3}}'
exit 0
`

type agentsEventLike struct {
	typ     string
	payload map[string]any
}
