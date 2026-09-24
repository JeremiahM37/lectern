package api_test

import (
	"fmt"
	"testing"
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
