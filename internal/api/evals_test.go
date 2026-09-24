package api_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/config"
)

func TestEvalSuiteAndCaseCRUD(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	suite := h.post("/api/evals/suites", obj{"name": "my suite", "project_id": pid,
		"description": "a suite"}, 201)
	if suite.str("name") != "my suite" {
		t.Fatalf("suite: %v", suite)
	}
	c1 := h.post(fmt.Sprintf("/api/evals/suites/%d/cases", suite.id()),
		obj{"name": "case one", "prompt": "do it", "check_command": "mockverify-pass"}, 201)
	h.post(fmt.Sprintf("/api/evals/suites/%d/cases", suite.id()),
		obj{"name": "case two", "prompt": "do it too"}, 201)

	got := h.get(fmt.Sprintf("/api/evals/suites/%d", suite.id()))
	cases := got.list("cases")
	if len(cases) != 2 {
		t.Fatalf("expected 2 cases, got %v", cases)
	}
	// default timeout applies when unset
	for _, c := range cases {
		if c.str("name") == "case two" && c.num("timeout_s") != 900 {
			t.Errorf("default timeout: %v", c)
		}
	}

	h.decode("DELETE", fmt.Sprintf("/api/evals/cases/%d", c1.id()), nil, 200, nil)
	got = h.get(fmt.Sprintf("/api/evals/suites/%d", suite.id()))
	if len(got.list("cases")) != 1 {
		t.Fatalf("case delete did not take: %v", got)
	}

	list := h.getList(fmt.Sprintf("/api/evals/suites?project_id=%d", pid))
	if len(list) != 1 {
		t.Fatalf("expected the one suite scoped to the project, got %v", list)
	}

	h.decode("DELETE", fmt.Sprintf("/api/evals/suites/%d", suite.id()), nil, 200, nil)
	if code := h.status("GET", fmt.Sprintf("/api/evals/suites/%d", suite.id()), nil); code != 404 {
		t.Fatalf("suite should be gone, got %d", code)
	}
}

func TestEvalSuiteImportFromRepo(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	proj, err := h.App.DB.Project(pid)
	if err != nil {
		t.Fatal(err)
	}
	target, err := h.App.DB.Target(proj.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	ex, err := h.App.Reg.For(target)
	if err != nil {
		t.Fatal(err)
	}
	yaml := "name: imported suite\ncases:\n  - name: c1\n    prompt: do the thing\n" +
		"    check_command: mockverify-pass\n"
	if err := ex.WriteFile(t.Context(), proj.RepoPath+"/.lectern/evals/suite.yaml", []byte(yaml)); err != nil {
		t.Fatal(err)
	}
	resp := h.post("/api/evals/suites/import", obj{"project_id": pid}, 200)
	imported, ok := resp["imported"].([]any)
	if !ok || len(imported) != 1 {
		t.Fatalf("import response: %v", resp)
	}
	row := imported[0].(map[string]any)
	if row["name"] != "imported suite" || int(row["cases"].(float64)) != 1 {
		t.Fatalf("imported suite: %v", row)
	}
	suites := h.getList(fmt.Sprintf("/api/evals/suites?project_id=%d", pid))
	if len(suites) != 1 || suites[0].str("name") != "imported suite" {
		t.Fatalf("suite was not actually created: %v", suites)
	}
	cases := h.get(fmt.Sprintf("/api/evals/suites/%d", suites[0].id())).list("cases")
	if len(cases) != 1 || cases[0].str("name") != "c1" {
		t.Fatalf("cases were not imported: %v", cases)
	}

	// re-importing is idempotent: the same-named suite is replaced, not doubled
	h.post("/api/evals/suites/import", obj{"project_id": pid}, 200)
	suites = h.getList(fmt.Sprintf("/api/evals/suites?project_id=%d", pid))
	if len(suites) != 1 {
		t.Fatalf("re-import should replace, not duplicate: %v", suites)
	}
}

func TestEvalRunSchedulingRespectsConcurrencyAndRepeats(t *testing.T) {
	h := newHarness(t)
	h.decode("PUT", "/api/settings", obj{"eval_concurrency": "1"}, 200, nil)
	pid := h.seededProjectID()
	suite := h.post("/api/evals/suites", obj{"name": "sched suite", "project_id": pid}, 201)
	h.post(fmt.Sprintf("/api/evals/suites/%d/cases", suite.id()),
		obj{"name": "only case", "prompt": "do it", "check_command": "mockverify-pass"}, 201)

	run := h.post(fmt.Sprintf("/api/evals/suites/%d/runs", suite.id()), obj{
		"variants": []obj{{"model": "sonnet"}, {"model": "opus"}},
		"repeats":  2,
	}, 201)

	var view obj
	h.waitUntil("the run to finish", func() bool {
		view = h.get(fmt.Sprintf("/api/evals/runs/%d", run.id()))
		return view.sub("run").str("status") == "done"
	})
	results := view.list("results")
	if len(results) != 4 { // 1 case x 2 variants x 2 repeats
		t.Fatalf("expected 4 cells, got %d: %v", len(results), results)
	}
	for _, r := range results {
		if r.str("status") != "passed" {
			t.Errorf("cell should have passed via mockverify-pass: %v", r)
		}
	}
	board := view.list("leaderboard")
	if len(board) != 2 {
		t.Fatalf("expected 2 leaderboard rows, got %v", board)
	}
	for _, row := range board {
		if row.num("total") != 2 || row.num("passed") != 2 || row.num("pass_rate") != 1 {
			t.Errorf("leaderboard row: %v", row)
		}
	}

	// re-listing runs for the suite shows this one
	runs := h.getList(fmt.Sprintf("/api/evals/suites/%d/runs", suite.id()))
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %v", runs)
	}
}

func TestEvalRunValidatesVariantsAndRepeats(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	suite := h.post("/api/evals/suites", obj{"name": "bad suite", "project_id": pid}, 201)
	if code := h.status("POST", fmt.Sprintf("/api/evals/suites/%d/runs", suite.id()),
		obj{"variants": []obj{{"model": "sonnet"}}}); code != 409 {
		t.Fatalf("a suite with no cases should refuse a run, got %d", code)
	}
	h.post(fmt.Sprintf("/api/evals/suites/%d/cases", suite.id()),
		obj{"name": "c", "prompt": "do it"}, 201)
	if code := h.status("POST", fmt.Sprintf("/api/evals/suites/%d/runs", suite.id()),
		obj{"variants": []obj{}}); code != 422 {
		t.Fatalf("no variants should be refused, got %d", code)
	}
	if code := h.status("POST", fmt.Sprintf("/api/evals/suites/%d/runs", suite.id()),
		obj{"variants": []obj{{"agent": "not-a-real-agent"}}}); code != 422 {
		t.Fatalf("an unknown agent should be refused, got %d", code)
	}
	if code := h.status("POST", fmt.Sprintf("/api/evals/suites/%d/runs", suite.id()),
		obj{"variants": []obj{{"model": "sonnet"}}, "repeats": 999}); code != 422 {
		t.Fatalf("too many repeats should be refused, got %d", code)
	}
}

func TestEvalRunCancelStopsItAndCompareDiffsTwoRuns(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	suite := h.post("/api/evals/suites", obj{"name": "cancel suite", "project_id": pid}, 201)
	h.post(fmt.Sprintf("/api/evals/suites/%d/cases", suite.id()),
		obj{"name": "slow case", "prompt": "x [mock:slow]", "check_command": "mockverify-pass"}, 201)

	run := h.post(fmt.Sprintf("/api/evals/suites/%d/runs", suite.id()), obj{
		"variants": []obj{{"model": "sonnet"}}, "repeats": 1,
	}, 201)
	h.waitUntil("the run to start materializing", func() bool {
		v := h.get(fmt.Sprintf("/api/evals/runs/%d", run.id()))
		return len(v.list("results")) > 0
	})
	h.post(fmt.Sprintf("/api/evals/runs/%d/cancel", run.id()), obj{}, 200)
	cancelled := h.get(fmt.Sprintf("/api/evals/runs/%d", run.id()))
	if cancelled.sub("run").str("status") != "cancelled" {
		t.Fatalf("run should be cancelled: %v", cancelled.sub("run"))
	}
	for _, r := range cancelled.list("results") {
		if r.str("status") != "error" {
			t.Errorf("cancelled cell should read error, got %v", r)
		}
	}
	// cancelling twice is refused
	if code := h.status("POST", fmt.Sprintf("/api/evals/runs/%d/cancel", run.id()), obj{}); code != 409 {
		t.Fatalf("re-cancelling should be refused, got %d", code)
	}

	// a second, successful run of the same suite to compare against
	run2 := h.post(fmt.Sprintf("/api/evals/suites/%d/runs", suite.id()), obj{
		"variants": []obj{{"model": "sonnet"}}, "repeats": 1,
	}, 201)
	// swap the case's mock scenario so the second run actually passes: cancel
	// left the case's prompt as [mock:slow], which is fine — mockverify-pass
	// still grades it once it lands normally.
	h.waitUntil("the comparison run to finish", func() bool {
		v := h.get(fmt.Sprintf("/api/evals/runs/%d", run2.id()))
		return v.sub("run").str("status") == "done"
	})
	cmp := h.get(fmt.Sprintf("/api/evals/runs/%d/compare/%d", run.id(), run2.id()))
	cases := cmp.list("cases")
	if len(cases) != 1 {
		t.Fatalf("expected 1 comparable case, got %v", cases)
	}
	if cases[0].str("status") != "improvement" {
		t.Errorf("cancelled(0%%) -> passed(100%%) should read improvement: %v", cases[0])
	}
}

// TestEvalRunEnforcesTimeoutAndRecordsTimeoutStatus proves timeout_s is
// actually enforced: a cell whose attempt runs longer than its case's
// timeout_s is cancelled through the scheduler's normal cancel path and
// recorded as "timeout" — not "failed" or "error" — while the run itself
// still reaches "done" rather than hanging on the cancelled cell.
func TestEvalRunEnforcesTimeoutAndRecordsTimeoutStatus(t *testing.T) {
	// A generous per-step mock agent delay (4 steps to "result") makes the
	// attempt's natural runtime (~4s) comfortably longer than the 1s
	// timeout_s below, so the assertion is "cancelled early", not "happened
	// to still be running when it finished on its own".
	h := newHarness(t, func(c *config.Config) { c.MockAgentDelay = 1 * time.Second })
	pid := h.seededProjectID()
	suite := h.post("/api/evals/suites", obj{"name": "timeout suite", "project_id": pid}, 201)
	h.post(fmt.Sprintf("/api/evals/suites/%d/cases", suite.id()),
		obj{"name": "slow case", "prompt": "do it", "timeout_s": 1}, 201)

	run := h.post(fmt.Sprintf("/api/evals/suites/%d/runs", suite.id()), obj{
		"variants": []obj{{"model": "sonnet"}}, "repeats": 1,
	}, 201)

	var view obj
	h.waitUntil("the cell to be recorded as timeout", func() bool {
		view = h.get(fmt.Sprintf("/api/evals/runs/%d", run.id()))
		results := view.list("results")
		return len(results) == 1 && results[0].str("status") == "timeout"
	})
	res := view.list("results")[0]
	if res.num("duration_s") <= 0 || res.num("duration_s") > 3 {
		t.Errorf("expected a duration_s just over the 1s timeout, not the ~4s natural runtime: %v", res)
	}
	if !contains(res.str("check_output_tail"), "timeout") {
		t.Errorf("expected the timeout to be explained: %v", res)
	}

	// the run itself still reaches "done" — a timed-out cell must not hang it
	h.waitUntil("the run to finish", func() bool {
		view = h.get(fmt.Sprintf("/api/evals/runs/%d", run.id()))
		return view.sub("run").str("status") == "done"
	})
}

// TestEvalRunSetupCommandFailureBlocksTheAgentAndRecordsError proves
// setup_command runs for real, on the target, before the agent starts: a
// failing one must stop the cell as "error" (carrying the setup command's
// own output) and the agent must never be launched at all.
func TestEvalRunSetupCommandFailureBlocksTheAgentAndRecordsError(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	suite := h.post("/api/evals/suites", obj{"name": "bad setup suite", "project_id": pid}, 201)
	h.post(fmt.Sprintf("/api/evals/suites/%d/cases", suite.id()),
		obj{"name": "bad setup", "prompt": "do it", "check_command": "mockverify-pass",
			"setup_command": "mocksetup-fail"}, 201)

	run := h.post(fmt.Sprintf("/api/evals/suites/%d/runs", suite.id()), obj{
		"variants": []obj{{"model": "sonnet"}}, "repeats": 1,
	}, 201)

	var view obj
	h.waitUntil("the run to finish", func() bool {
		view = h.get(fmt.Sprintf("/api/evals/runs/%d", run.id()))
		return view.sub("run").str("status") == "done"
	})
	results := view.list("results")
	if len(results) != 1 {
		t.Fatalf("expected 1 cell, got %v", results)
	}
	res := results[0]
	if res.str("status") != "error" {
		t.Fatalf("a failing setup_command should read error, got %v", res)
	}
	if !contains(res.str("check_output_tail"), "setup command") {
		t.Errorf("expected the setup command's own failure to be explained: %v", res)
	}
	if h.cmdLogHas("tmux new-session") {
		t.Error("the agent must never start when its case's setup_command fails")
	}
	if !h.cmdLogHas("mocksetup-fail") {
		t.Error("the setup command should have actually been run on the target")
	}
}

// TestEvalRunSetupCommandSuccessLetsTheAgentRun is the success-path
// complement: a setup_command that succeeds lets the agent start and the
// cell grade normally.
func TestEvalRunSetupCommandSuccessLetsTheAgentRun(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	suite := h.post("/api/evals/suites", obj{"name": "good setup suite", "project_id": pid}, 201)
	h.post(fmt.Sprintf("/api/evals/suites/%d/cases", suite.id()),
		obj{"name": "good setup", "prompt": "do it", "check_command": "mockverify-pass",
			"setup_command": "mocksetup-pass"}, 201)

	run := h.post(fmt.Sprintf("/api/evals/suites/%d/runs", suite.id()), obj{
		"variants": []obj{{"model": "sonnet"}}, "repeats": 1,
	}, 201)

	var view obj
	h.waitUntil("the run to finish", func() bool {
		view = h.get(fmt.Sprintf("/api/evals/runs/%d", run.id()))
		return view.sub("run").str("status") == "done"
	})
	results := view.list("results")
	if len(results) != 1 || results[0].str("status") != "passed" {
		t.Fatalf("a passing setup_command should not block a passing cell: %v", results)
	}
	if !h.cmdLogHas("tmux new-session") {
		t.Error("the agent should have started once setup_command succeeded")
	}
}
