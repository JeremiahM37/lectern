package api_test

// A real-process proof for evals: two stub agent variants, one that creates
// the file a case's check_command looks for and one that does not, run
// through the real local executor (real git worktrees, real tmux, real
// processes — see e2e_real_test.go's rig, reused here exactly the way
// hooks_real_process_test.go and takeovers_test.go already do: newRealRig(t)
// then overwrite its ClaudeBin script). The point is to prove the whole
// pipeline — dispatch, per-cell worktree, auto-verify with a case's own
// check_command, grading, matrix and leaderboard — with nothing stubbed above
// the agent binary itself.

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

// evalStubAgent behaves like fakeAgent but branches on --model: "good"
// creates marker.txt in the worktree (which a case's check_command looks
// for), any other value does not. That is the "one variant's stub creates
// the file the check wants, the other doesn't" the design calls for, using
// the model field rather than a second binary — the rig only has one
// ClaudeBin, same constraint every other real-process test using it works
// under (takeovers_test.go, hooks_real_process_test.go, …).
const evalStubAgent = `#!/bin/bash
model=""
while [ $# -gt 0 ]; do
  case "$1" in
    --model) model="$2"; shift 2 ;;
    *) shift ;;
  esac
done
echo '{"type":"system","subtype":"init","session_id":"fake-eval","tools":["Bash","Edit"]}'
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"Working."}]}}'
if [ "$model" = "good" ]; then
  printf 'marker\n' > marker.txt
fi
echo '{"type":"result","subtype":"success","is_error":false,"result":"done"}'
exit 0
`

func (r *realRig) doJSON(t *testing.T, method, path string, body any) map[string]any {
	t.Helper()
	code, raw := r.do(method, path, body)
	if code < 200 || code >= 300 {
		t.Fatalf("%s %s: %d %s", method, path, code, raw)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s %s: undecodable response %s: %v", method, path, raw, err)
	}
	return out
}

func TestEvalRunRealProcessMatrixAndLeaderboard(t *testing.T) {
	r := newRealRig(t)
	if err := os.WriteFile(r.app.Cfg.ClaudeBin, []byte(evalStubAgent), 0o755); err != nil {
		t.Fatal(err)
	}

	// one in flight at a time, to prove the concurrency limit does not stall
	// the run — it must still reach every cell.
	r.doJSON(t, "PUT", "/api/settings", map[string]any{"eval_concurrency": "1"})

	suite := r.doJSON(t, "POST", "/api/evals/suites", map[string]any{
		"name": "marker suite", "project_id": r.project})
	suiteID := int64(suite["id"].(float64))
	for _, name := range []string{"case one", "case two"} {
		r.doJSON(t, "POST", fmt.Sprintf("/api/evals/suites/%d/cases", suiteID), map[string]any{
			"name": name, "prompt": "create marker.txt", "check_command": "test -f marker.txt",
			"timeout_s": 60,
		})
	}
	run := r.doJSON(t, "POST", fmt.Sprintf("/api/evals/suites/%d/runs", suiteID), map[string]any{
		"variants": []map[string]any{{"agent": "claude", "model": "good"}, {"agent": "claude", "model": "bad"}},
		"repeats":  1,
	})
	runID := int64(run["id"].(float64))

	deadline := time.Now().Add(30 * time.Second)
	var view map[string]any
	for time.Now().Before(deadline) {
		view = r.doJSON(t, "GET", fmt.Sprintf("/api/evals/runs/%d", runID), nil)
		if view["run"].(map[string]any)["status"] == "done" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if view == nil || view["run"].(map[string]any)["status"] != "done" {
		t.Fatalf("run did not finish: %v", view)
	}

	results := view["results"].([]any)
	if len(results) != 4 {
		t.Fatalf("expected 4 cells (2 cases x 2 variants), got %d: %v", len(results), results)
	}
	for _, raw := range results {
		res := raw.(map[string]any)
		variant := int(res["variant_idx"].(float64))
		status := res["status"].(string)
		if variant == 0 && status != "passed" {
			t.Errorf("the 'good' variant should pass every case: %v", res)
		}
		if variant == 1 && status != "failed" {
			t.Errorf("the 'bad' variant should fail every case: %v", res)
		}
	}

	board := view["leaderboard"].([]any)
	if len(board) != 2 {
		t.Fatalf("expected 2 leaderboard rows, got %d", len(board))
	}
	good, bad := board[0].(map[string]any), board[1].(map[string]any)
	if good["pass_rate"].(float64) != 1 {
		t.Errorf("good variant pass_rate: %v", good)
	}
	if bad["pass_rate"].(float64) != 0 {
		t.Errorf("bad variant pass_rate: %v", bad)
	}
}

// TestEvalRunRealProcessSetupCommandCreatesFixtureForCheck proves
// setup_command runs for real, on the target, in the attempt's own
// worktree, before the agent starts — not folded into the prompt as an
// instruction the agent has to remember to follow. The variant's stub agent
// is the "bad" one, which never creates marker.txt itself; the ONLY thing
// that can produce fixture.txt is the case's own setup_command running as a
// real host-side step, and the check_command (which the agent never sees or
// runs) looks for exactly that file. A pass here is only possible if the
// setup step actually executed on the target.
func TestEvalRunRealProcessSetupCommandCreatesFixtureForCheck(t *testing.T) {
	r := newRealRig(t)
	if err := os.WriteFile(r.app.Cfg.ClaudeBin, []byte(evalStubAgent), 0o755); err != nil {
		t.Fatal(err)
	}

	suite := r.doJSON(t, "POST", "/api/evals/suites", map[string]any{
		"name": "setup fixture suite", "project_id": r.project})
	suiteID := int64(suite["id"].(float64))
	r.doJSON(t, "POST", fmt.Sprintf("/api/evals/suites/%d/cases", suiteID), map[string]any{
		"name": "fixture case", "prompt": "create marker.txt",
		"setup_command": "printf fixture > fixture.txt", "check_command": "test -f fixture.txt",
		"timeout_s": 60,
	})
	run := r.doJSON(t, "POST", fmt.Sprintf("/api/evals/suites/%d/runs", suiteID), map[string]any{
		"variants": []map[string]any{{"agent": "claude", "model": "bad"}},
		"repeats":  1,
	})
	runID := int64(run["id"].(float64))

	deadline := time.Now().Add(30 * time.Second)
	var view map[string]any
	for time.Now().Before(deadline) {
		view = r.doJSON(t, "GET", fmt.Sprintf("/api/evals/runs/%d", runID), nil)
		if view["run"].(map[string]any)["status"] == "done" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if view == nil || view["run"].(map[string]any)["status"] != "done" {
		t.Fatalf("run did not finish: %v", view)
	}
	results := view["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("expected 1 cell, got %d: %v", len(results), results)
	}
	res := results[0].(map[string]any)
	if res["status"] != "passed" {
		t.Fatalf("expected the cell to pass off the setup-created fixture (agent never makes it): %v", res)
	}
}
