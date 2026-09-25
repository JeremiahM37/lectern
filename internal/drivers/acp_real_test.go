package drivers

// Real tmux, a real local process, a real fifo, a real broker/DB — the only
// stand-in is the agent binary, a small python3 JSON-RPC state machine
// (stubACPAgent) speaking exactly the ACP methods acp.go's doc comment lists
// (read off @agentclientprotocol/sdk@0.14.1's schema, not guessed), not an
// LLM.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/testutil"
)

// stubACPAgent exercises, over one worktree-scoped session:
//   - initialize / session/new handshake
//   - an agent_message_chunk and a tool_call → tool_call_update timeline pair
//   - fs/write_text_file + fs/read_text_file round-tripping a file INSIDE the
//     worktree, then an fs/read_text_file OUTSIDE it (/etc/passwd) that the
//     driver must refuse before ever asking the target to read it
//   - session/request_permission, answered only once the operator (the
//     test, standing in via the broker) decides
//   - a second session/prompt (the steered follow-up Send() queues while the
//     first is still in flight)
//   - a clean exit once the fifo's end sentinel closes its stdin
const stubACPAgent = `#!/usr/bin/env python3
import sys, json, os

def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

turn1_id = [None]
for raw in sys.stdin:
    raw = raw.strip()
    if not raw:
        continue
    msg = json.loads(raw)
    method = msg.get("method")
    if method == "initialize":
        send({"jsonrpc": "2.0", "id": msg["id"], "result": {
            "protocolVersion": 1, "agentCapabilities": {}, "authMethods": []}})
    elif method == "session/new":
        send({"jsonrpc": "2.0", "id": msg["id"], "result": {"sessionId": "sess-1"}})
    elif method == "session/prompt":
        text = msg["params"]["prompt"][0]["text"]
        if text == "do the thing":
            turn1_id[0] = msg["id"]
            send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": "sess-1",
                  "update": {"sessionUpdate": "agent_message_chunk",
                             "content": {"type": "text", "text": "thinking..."}}}})
            send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": "sess-1",
                  "update": {"sessionUpdate": "tool_call", "toolCallId": "tc-1",
                             "title": "run something", "kind": "execute", "status": "in_progress"}}})
            send({"jsonrpc": "2.0", "id": "fsw-1", "method": "fs/write_text_file", "params": {
                  "sessionId": "sess-1", "path": os.path.join(os.getcwd(), "note.txt"), "content": "hello-acp"}})
        else:
            send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": "sess-1",
                  "update": {"sessionUpdate": "agent_message_chunk",
                             "content": {"type": "text", "text": "steered:" + text}}}})
            send({"jsonrpc": "2.0", "id": msg["id"], "result": {"stopReason": "end_turn"}})
    elif method is None and msg.get("id") == "fsw-1":
        send({"jsonrpc": "2.0", "id": "fsr-1", "method": "fs/read_text_file", "params": {
              "sessionId": "sess-1", "path": os.path.join(os.getcwd(), "note.txt")}})
    elif method is None and msg.get("id") == "fsr-1":
        ok = msg.get("result", {}).get("content") == "hello-acp"
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": "sess-1",
              "update": {"sessionUpdate": "agent_message_chunk",
                         "content": {"type": "text", "text": "fs-roundtrip-ok" if ok else "fs-roundtrip-bad"}}}})
        send({"jsonrpc": "2.0", "id": "fsr-escape", "method": "fs/read_text_file", "params": {
              "sessionId": "sess-1", "path": "/etc/passwd"}})
    elif method is None and msg.get("id") == "fsr-escape":
        refused = "error" in msg
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": "sess-1",
              "update": {"sessionUpdate": "agent_message_chunk",
                         "content": {"type": "text",
                                     "text": "escape-refused" if refused else "escape-allowed-BUG"}}}})
        send({"jsonrpc": "2.0", "id": "perm-1", "method": "session/request_permission", "params": {
              "sessionId": "sess-1",
              "toolCall": {"toolCallId": "tc-1", "title": "run something", "kind": "execute"},
              "options": [{"optionId": "allow-1", "name": "Allow", "kind": "allow_once"},
                          {"optionId": "reject-1", "name": "Reject", "kind": "reject_once"}]}})
    elif method is None and msg.get("id") == "perm-1":
        outcome = msg.get("result", {}).get("outcome", {})
        approved = outcome.get("outcome") == "selected" and outcome.get("optionId") == "allow-1"
        send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": "sess-1",
              "update": {"sessionUpdate": "tool_call_update", "toolCallId": "tc-1",
                         "status": "completed" if approved else "failed",
                         "content": [{"type": "content", "content": {"type": "text",
                                      "text": "approved-output" if approved else "denied"}}]}}})
        send({"jsonrpc": "2.0", "id": turn1_id[0], "result": {"stopReason": "end_turn"}})
sys.exit(0)
`

func TestACPDriverFullTurnToolsFsPermissionAndSteer(t *testing.T) {
	testutil.RequireIsolated(t)
	requireRealTools(t)
	dir := t.TempDir()
	isolateTmux(t)
	wt := filepath.Join(dir, "wt")
	os.MkdirAll(wt, 0o755)
	stub := filepath.Join(dir, "fake-acp-agent")
	os.WriteFile(stub, []byte(stubACPAgent), 0o755)

	br, db, attemptID := brokerRig(t)
	ex := executor.NewLocal()
	ctx := context.Background()

	run, err := acpDriver{}.Start(ctx, ex, Spec{
		Bin: stub, Worktree: wt, TmuxSession: "lec-drv-acp-1",
		PermissionMode: "default", Prompt: "do the thing", PollInterval: 30 * time.Millisecond,
		Broker: br, AttemptID: attemptID, ApprovalTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Start (handshake): %v", err)
	}

	events := make(chan agentsEventLike, 64)
	go func() {
		for ev := range run.Events() {
			events <- agentsEventLike{typ: ev.Type, payload: ev.Payload}
		}
		close(events)
	}()

	// steer WHILE the first turn is still in flight — this must be queued
	// (see acpDriver's doc comment) rather than accepted as a second
	// concurrent session/prompt, which the protocol has no way to express.
	if err := run.Send(ctx, "steer text"); err != nil {
		t.Fatalf("Send (queued while active): %v", err)
	}

	// the stub's execCommandApproval-equivalent (session/request_permission)
	// must have produced a real pending approval row for this attempt
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
		t.Fatal("session/request_permission never reached the broker as a pending approval")
	}
	if row := br.Decide(approvalID, "approved", "looks fine", "test"); row == nil {
		t.Fatal("could not decide the approval")
	}

	want := map[string]bool{
		"fs-roundtrip-ok":    false, // fs/write_text_file + fs/read_text_file round trip, worktree-confined
		"escape-refused":     false, // fs/read_text_file outside the worktree was refused BEFORE reaching the target
		"steered:steer text": false, // the queued Send() became a real second session/prompt
	}
	sawToolUse, sawApprovedResult, sawResults := false, false, 0
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && (sawResults < 2 || !allTrue(want)) {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("event stream closed before the run finished")
			}
			if ev.typ == "text" {
				if text, _ := ev.payload["text"].(string); text != "" {
					if _, tracked := want[text]; tracked {
						want[text] = true
					}
				}
			}
			if ev.typ == "tool_use" {
				if name, _ := ev.payload["name"].(string); name == "Bash" {
					sawToolUse = true
				}
			}
			if ev.typ == "tool_result" {
				if content, _ := ev.payload["content"].(string); content == "approved-output" {
					if isErr, _ := ev.payload["is_error"].(bool); !isErr {
						sawApprovedResult = true
					}
				}
			}
			if ev.typ == "result" {
				sawResults++
			}
		case <-time.After(200 * time.Millisecond):
		}
	}
	for text, seen := range want {
		if !seen {
			t.Errorf("never observed expected timeline text %q", text)
		}
	}
	if !sawToolUse {
		t.Error("no tool_use event for the execute-kind tool call")
	}
	if !sawApprovedResult {
		t.Error("no tool_result reflecting the broker's approved decision")
	}
	if sawResults < 2 {
		t.Errorf("expected a result event for both the initial and the steered turn, got %d", sawResults)
	}

	if err := run.Cancel(ctx); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := run.Wait(waitCtx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit code: %d (err=%q)", res.ExitCode, res.Err)
	}
	if res.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want sess-1", res.SessionID)
	}
}

func allTrue(m map[string]bool) bool {
	for _, v := range m {
		if !v {
			return false
		}
	}
	return true
}

// stubACPPermissionDenied answers a single turn's one permission request and
// completes the turn either way, letting a test observe the DENY path (the
// combined test above only exercises approve).
const stubACPPermissionDenied = `#!/usr/bin/env python3
import sys, json

def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

for raw in sys.stdin:
    raw = raw.strip()
    if not raw:
        continue
    msg = json.loads(raw)
    method = msg.get("method")
    if method == "initialize":
        send({"jsonrpc": "2.0", "id": msg["id"], "result": {
            "protocolVersion": 1, "agentCapabilities": {}, "authMethods": []}})
    elif method == "session/new":
        send({"jsonrpc": "2.0", "id": msg["id"], "result": {"sessionId": "sess-1"}})
    elif method == "session/prompt":
        send({"jsonrpc": "2.0", "id": "perm-1", "method": "session/request_permission", "params": {
              "sessionId": "sess-1",
              "toolCall": {"toolCallId": "tc-1", "title": "rm -rf", "kind": "execute"},
              "options": [{"optionId": "allow-1", "name": "Allow", "kind": "allow_once"},
                          {"optionId": "reject-1", "name": "Reject", "kind": "reject_once"}]}})
        _prompt_id = msg["id"]
        for raw2 in sys.stdin:
            raw2 = raw2.strip()
            if not raw2:
                continue
            resp = json.loads(raw2)
            if resp.get("id") == "perm-1":
                outcome = resp.get("result", {}).get("outcome", {})
                approved = outcome.get("outcome") == "selected" and outcome.get("optionId") == "allow-1"
                send({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": "sess-1",
                      "update": {"sessionUpdate": "tool_call_update", "toolCallId": "tc-1",
                                 "status": "completed" if approved else "failed", "content": []}}})
                send({"jsonrpc": "2.0", "id": _prompt_id, "result": {"stopReason": "end_turn"}})
                break
sys.exit(0)
`

func TestACPDriverPermissionDenied(t *testing.T) {
	testutil.RequireIsolated(t)
	requireRealTools(t)
	dir := t.TempDir()
	isolateTmux(t)
	wt := filepath.Join(dir, "wt")
	os.MkdirAll(wt, 0o755)
	stub := filepath.Join(dir, "fake-acp-deny")
	os.WriteFile(stub, []byte(stubACPPermissionDenied), 0o755)

	br, db, attemptID := brokerRig(t)
	ex := executor.NewLocal()
	ctx := context.Background()

	run, err := acpDriver{}.Start(ctx, ex, Spec{
		Bin: stub, Worktree: wt, TmuxSession: "lec-drv-acp-deny",
		PermissionMode: "default", Prompt: "rm everything", PollInterval: 30 * time.Millisecond,
		Broker: br, AttemptID: attemptID, ApprovalTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	var approvalID int64
	for time.Now().Before(deadline) {
		rows, err := db.ApprovalsByStatus("pending")
		if err == nil {
			for _, r := range rows {
				if r.AttemptID == attemptID {
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
		t.Fatal("permission request never reached the broker")
	}
	if row := br.Decide(approvalID, "denied", "no", "test"); row == nil {
		t.Fatal("could not decide the approval")
	}

	sawDeniedResult := false
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !sawDeniedResult {
		select {
		case ev, ok := <-run.Events():
			if !ok {
				break
			}
			if ev.Type == "tool_result" {
				if isErr, _ := ev.Payload["is_error"].(bool); isErr {
					sawDeniedResult = true
				}
			}
		case <-time.After(200 * time.Millisecond):
		}
	}
	if !sawDeniedResult {
		t.Fatal("denying the approval never produced an is_error tool_result")
	}

	// wait for the run to actually end (not just deferred) before returning:
	// t.Cleanup's tmux teardown races a lingering process otherwise — the
	// stub is still exiting on its own, and killing a session tmux has
	// already reaped between the cleanup's own list and kill calls fails
	// with a confusing "exit status 1" that has nothing to do with this
	// test's actual assertions.
	if err := run.Cancel(context.Background()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := run.Wait(waitCtx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

// stubACPNeverInitializes never answers "initialize" — what an unbuildable
// npx package or a binary that does not actually speak ACP looks like.
const stubACPNeverInitializes = `#!/usr/bin/env python3
import sys
sys.exit(1)
`

func TestACPDriverInitializeFailureSurfacesClearError(t *testing.T) {
	testutil.RequireIsolated(t)
	requireRealTools(t)
	dir := t.TempDir()
	isolateTmux(t)
	wt := filepath.Join(dir, "wt")
	os.MkdirAll(wt, 0o755)
	stub := filepath.Join(dir, "fake-acp-broken")
	os.WriteFile(stub, []byte(stubACPNeverInitializes), 0o755)

	ex := executor.NewLocal()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := acpDriver{}.Start(ctx, ex, Spec{
		Bin: stub, Worktree: wt, TmuxSession: "lec-drv-acp-broken",
		Prompt: "do it", PollInterval: 30 * time.Millisecond,
		HandshakeTimeout: 500 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected a clear error when the ACP handshake never completes; got nil")
	}
	if !strings.Contains(err.Error(), "initialize") && !strings.Contains(err.Error(), "handshake") {
		t.Errorf("error does not explain what failed: %v", err)
	}
}
