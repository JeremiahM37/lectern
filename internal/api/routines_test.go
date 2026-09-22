package api_test

// A routine is the job you keep asking for, saved. The motivating one: "go
// through every open PR on librarr, sglang and gamarr, test it end to end with
// playwright, fix what is wrong, and merge once CI is green" — one button
// across three repositories, not three prompts retyped every week.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

const prReview = `Go through every open pull request. For each one: read the diff, ` +
	`run the full test suite end to end including the playwright flows, fix anything ` +
	`that is wrong by committing directly to the PR branch, wait for CI, and merge ` +
	`once it is green. Report what you merged and what you left alone.`

// twoProjects gives a routine more than one repository to run against, which is
// the whole reason it exists.
func twoProjects(h *harness, t *testing.T) []int64 {
	t.Helper()
	targets, _ := h.App.DB.Targets()
	ids := []int64{h.seededProjectID()}
	created := h.post("/api/projects", obj{
		"name": "sglang", "target_id": targets[0].ID, "repo_path": "/mock/sglang"}, 201)
	ids = append(ids, int64(created.num("id")))
	return ids
}

func TestARoutineRunsOneTaskPerProject(t *testing.T) {
	h := newHarness(t)
	projects := twoProjects(h, t)

	created := h.post("/api/routines", obj{
		"name": "PR sweep", "title": "PR sweep", "prompt": prReview,
		"project_ids": projects, "permission_mode": "acceptEdits",
	}, 201)
	id := int64(created.num("id"))
	if created.str("name") != "PR sweep" {
		t.Fatalf("%v", created)
	}

	out := h.post(fmt.Sprintf("/api/routines/%d/run", id), obj{}, 200)
	var res struct {
		Tasks  []int64  `json:"tasks"`
		Failed []string `json:"failed"`
	}
	raw, _ := json.Marshal(out)
	json.Unmarshal(raw, &res)

	if len(res.Tasks) != len(projects) {
		t.Fatalf("expected one task per project, got %d for %d projects: %v",
			len(res.Tasks), len(projects), out)
	}
	if len(res.Failed) != 0 {
		t.Errorf("unexpected failures: %v", res.Failed)
	}
	// each task carries the routine's prompt and lands on its own project
	seen := map[int64]bool{}
	for _, tid := range res.Tasks {
		task, err := h.App.DB.Task(tid)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(task.Prompt, "every open pull request") {
			t.Errorf("task %d lost the prompt: %q", tid, task.Prompt)
		}
		if task.PermissionMode != "acceptEdits" {
			t.Errorf("task %d permission mode: %q", tid, task.PermissionMode)
		}
		if !strings.HasPrefix(task.CreatedBy, "routine:") {
			t.Errorf("a routine's task should say where it came from: %q", task.CreatedBy)
		}
		if seen[task.ProjectID] {
			t.Errorf("two tasks landed on project %d", task.ProjectID)
		}
		seen[task.ProjectID] = true
	}
	// and it records that it ran
	rows := h.getList("/api/routines")
	if len(rows) != 1 || rows[0].num("last_run_at") == 0 {
		t.Errorf("the routine did not record its run: %v", rows)
	}
}

// One dead project must not stop the rest — a sweep over five repos where one
// was deleted should still run against the other four and say what it skipped.
func TestARoutineSurvivesAMissingProject(t *testing.T) {
	h := newHarness(t)
	projects := twoProjects(h, t)

	// the project list is validated on save, so a routine cannot be created
	// pointing at something that is not there
	if code, body := h.request("POST", "/api/routines", obj{
		"name": "sweep", "prompt": prReview,
		"project_ids": append(projects, int64(99999)),
	}, nil); code != 400 {
		t.Fatalf("expected a refusal, got %d %s", code, body)
	}

	// but a project deleted AFTER the routine was saved must not stop the rest
	created := h.post("/api/routines", obj{
		"name": "sweep", "prompt": prReview, "project_ids": projects,
	}, 201)
	if code, body := h.request("DELETE",
		fmt.Sprintf("/api/projects/%d?cascade=true", projects[1]), nil, nil); code != 204 {
		t.Fatalf("removing a project: %d %s", code, body)
	}
	out := h.post(fmt.Sprintf("/api/routines/%d/run", int64(created.num("id"))), obj{}, 200)
	var res struct {
		Tasks  []int64  `json:"tasks"`
		Failed []string `json:"failed"`
	}
	raw, _ := json.Marshal(out)
	json.Unmarshal(raw, &res)
	if len(res.Tasks) != 1 {
		t.Errorf("the surviving project should still have run: %v", out)
	}
	if len(res.Failed) != 1 || !strings.Contains(res.Failed[0], "gone") {
		t.Errorf("it should say what it skipped: %v", res.Failed)
	}
}

func TestARoutineRefusesAProjectThatDoesNotExist(t *testing.T) {
	h := newHarness(t)
	code, body := h.request("POST", "/api/routines", obj{
		"name": "bad", "prompt": "x", "project_ids": []int64{99999}}, nil)
	if code != 400 {
		t.Fatalf("expected a refusal, got %d %s", code, body)
	}
	if !strings.Contains(string(body), "99999") {
		t.Errorf("the error should name the project: %s", body)
	}
}

// A routine deleted after it has run must not take its tasks with it — that work
// is real and lives on the board.
func TestDeletingARoutineKeepsTheWorkItDid(t *testing.T) {
	h := newHarness(t)
	created := h.post("/api/routines", obj{
		"name": "once", "prompt": prReview, "project_ids": []int64{h.seededProjectID()},
	}, 201)
	id := int64(created.num("id"))
	out := h.post(fmt.Sprintf("/api/routines/%d/run", id), obj{}, 200)
	var res struct {
		Tasks []int64 `json:"tasks"`
	}
	raw, _ := json.Marshal(out)
	json.Unmarshal(raw, &res)
	if len(res.Tasks) == 0 {
		t.Fatal("nothing ran")
	}

	if code, body := h.request("DELETE", fmt.Sprintf("/api/routines/%d", id), nil, nil); code != 204 {
		t.Fatalf("%d %s", code, body)
	}
	if _, err := h.App.DB.Task(res.Tasks[0]); err != nil {
		t.Error("deleting the routine deleted the work it had already done")
	}
}

func TestRoutineValidation(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	for name, body := range map[string]obj{
		"no name":       {"prompt": "x", "project_ids": []int64{pid}},
		"no prompt":     {"name": "x", "project_ids": []int64{pid}},
		"no projects":   {"name": "x", "prompt": "y"},
		"bad schedule":  {"name": "x", "prompt": "y", "project_ids": []int64{pid}, "schedule": "0 9 * * *"},
		"unknown agent": {"name": "x", "prompt": "y", "project_ids": []int64{pid}, "agent": "nope"},
	} {
		if code, resp := h.request("POST", "/api/routines", body, nil); code != 400 {
			t.Errorf("%s: expected 400, got %d %s", name, code, resp)
		}
	}
	if code := h.status("POST", "/api/routines/9999/run", obj{}); code != 404 {
		t.Errorf("running an unknown routine: %d", code)
	}
}

// ---- schedules ---------------------------------------------------------

func TestASavedScheduleGetsANextRunTime(t *testing.T) {
	h := newHarness(t)
	created := h.post("/api/routines", obj{
		"name": "nightly PR sweep", "prompt": prReview,
		"project_ids": []int64{h.seededProjectID()}, "schedule": "daily at 09:00",
	}, 201)
	next := created.num("next_run_at")
	if next == 0 {
		t.Fatal("a scheduled routine needs a next run time or it never fires")
	}
	when := time.Unix(int64(next), 0)
	if when.Hour() != 9 || when.Minute() != 0 {
		t.Errorf("next run is %v, not 09:00", when)
	}
	if !when.After(time.Now()) {
		t.Errorf("next run is in the past: %v", when)
	}
}

// A routine with no schedule only runs when asked. That is the default, because
// a saved job that quietly started running itself would be a surprise.
func TestNoScheduleMeansItOnlyRunsWhenAsked(t *testing.T) {
	h := newHarness(t)
	created := h.post("/api/routines", obj{
		"name": "manual", "prompt": prReview,
		"project_ids": []int64{h.seededProjectID()},
	}, 201)
	if created.num("next_run_at") != 0 {
		t.Error("an unscheduled routine should have no next run time")
	}
	if created.str("schedule") != "" {
		t.Errorf("schedule: %q", created.str("schedule"))
	}
	// the scheduler must not pick it up
	due, err := h.App.DB.DueRoutines(store.Now() + 86400*365)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range due {
		if r.Name == "manual" {
			t.Error("an unscheduled routine was treated as due")
		}
	}
}

// The scheduler actually fires a due routine, without anyone pressing anything.
func TestTheSchedulerFiresADueRoutine(t *testing.T) {
	h := newHarness(t)
	created := h.post("/api/routines", obj{
		"name": "due now", "prompt": prReview,
		"project_ids": []int64{h.seededProjectID()}, "schedule": "every 6h",
	}, 201)
	id := int64(created.num("id"))

	// make it due
	if err := h.App.DB.Update("routines", id, map[string]any{
		"next_run_at": store.Now() - 1}); err != nil {
		t.Fatal(err)
	}
	h.waitUntil("the scheduler to fire the routine", func() bool {
		for _, task := range h.getList("/api/tasks") {
			if task.str("title") == "due now" {
				return true
			}
		}
		return false
	})

	// and it re-arms rather than firing every tick from then on
	row, err := h.App.DB.Routine(id)
	if err != nil {
		t.Fatal(err)
	}
	if row.NextRunAt == nil || *row.NextRunAt <= store.Now() {
		t.Fatalf("the routine did not re-arm; it would fire on every tick: %v", row.NextRunAt)
	}
	if row.LastRunAt == nil {
		t.Error("it did not record that it ran")
	}
}

// A schedule edited into something unreadable must stop the routine, not make it
// retry forever.
func TestAnUnreadableScheduleDisablesTheRoutine(t *testing.T) {
	h := newHarness(t)
	created := h.post("/api/routines", obj{
		"name": "broken", "prompt": prReview,
		"project_ids": []int64{h.seededProjectID()}, "schedule": "every 6h",
	}, 201)
	id := int64(created.num("id"))
	// straight into the database, the way a bad migration or hand edit would
	if err := h.App.DB.Update("routines", id, map[string]any{
		"schedule": "whenever I feel like it", "next_run_at": store.Now() - 1}); err != nil {
		t.Fatal(err)
	}
	h.waitUntil("the routine to be disabled", func() bool {
		row, err := h.App.DB.Routine(id)
		return err == nil && !row.Enabled
	})
	row, _ := h.App.DB.Routine(id)
	if row.NextRunAt != nil {
		t.Error("a disabled routine should not still be armed")
	}
}

// Turning a routine off and on again must not fire every run it missed.
func TestReenablingDoesNotFireMissedRuns(t *testing.T) {
	h := newHarness(t)
	created := h.post("/api/routines", obj{
		"name": "paused", "prompt": prReview,
		"project_ids": []int64{h.seededProjectID()}, "schedule": "every 6h",
	}, 201)
	id := int64(created.num("id"))

	h.patch(fmt.Sprintf("/api/routines/%d", id), obj{"enabled": false}, 200)
	// time passes while it is off
	h.App.DB.Update("routines", id, map[string]any{"next_run_at": store.Now() - 86400})

	back := h.patch(fmt.Sprintf("/api/routines/%d", id), obj{"enabled": true}, 200)
	if next := back.num("next_run_at"); next <= float64(store.Now()) {
		t.Errorf("re-enabling left it due in the past (%v); it would fire immediately", next)
	}
}

// Editing one field must not quietly change another. An absent `schedule` means
// "leave it alone"; only an explicitly empty one makes a routine manual.
func TestEditingARoutineDoesNotUnscheduleIt(t *testing.T) {
	h := newHarness(t)
	created := h.post("/api/routines", obj{
		"name": "nightly", "prompt": prReview,
		"project_ids": []int64{h.seededProjectID()}, "schedule": "daily at 09:00",
	}, 201)
	id := int64(created.num("id"))

	renamed := h.patch(fmt.Sprintf("/api/routines/%d", id), obj{"name": "nightly sweep"}, 200)
	if renamed.str("schedule") != "daily at 09:00" {
		t.Errorf("renaming it cleared the schedule: %q", renamed.str("schedule"))
	}
	if renamed.num("next_run_at") == 0 {
		t.Error("renaming it disarmed the routine")
	}

	// and clearing it explicitly still works
	cleared := h.patch(fmt.Sprintf("/api/routines/%d", id), obj{"schedule": ""}, 200)
	if cleared.str("schedule") != "" {
		t.Errorf("schedule: %q", cleared.str("schedule"))
	}
	if cleared.num("next_run_at") != 0 {
		t.Error("a manual routine should not stay armed")
	}
}

// ---- clearing the board ------------------------------------------------

// A board that has run for a month is mostly history, and deleting eighty cards
// one at a time is not something anyone does — so they pile up and the board
// stops being glanceable, which is the only thing it is for.
func TestClearingFinishedTasksLeavesLiveOnesAlone(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()

	done := h.run(pid, "finished", "x", nil)
	h.waitStatus(done.id(), "review")
	h.post(fmt.Sprintf("/api/tasks/%d/complete", done.id()), obj{}, 200)

	queued := h.task(pid, "not started", "x", nil)
	running := h.task(pid, "in flight", "x [mock:slow]", nil)
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", running.id()), obj{}, 200)
	h.waitStatus(running.id(), "running")

	out := h.post("/api/tasks/clear", obj{}, 200)
	if out.num("cleared") < 1 {
		t.Fatalf("nothing was cleared: %v", out)
	}
	// the finished one is gone
	if _, err := h.App.DB.Task(done.id()); err == nil {
		t.Error("the finished task is still on the board")
	}
	// and the live ones are untouched — clearing must never kill working agents
	for _, task := range []struct {
		id   int64
		what string
	}{{queued.id(), "queued"}, {running.id(), "running"}} {
		if _, err := h.App.DB.Task(task.id); err != nil {
			t.Errorf("clearing removed a %s task: %v", task.what, err)
		}
	}
}

// "Clear the failed ones" is a different request from "clear everything
// finished", and both have to be sayable.
func TestClearingCanBeNarrowedToOneStatus(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()

	ok := h.run(pid, "worked", "x", nil)
	h.waitStatus(ok.id(), "review")
	h.post(fmt.Sprintf("/api/tasks/%d/complete", ok.id()), obj{}, 200)

	bad := h.task(pid, "broke", "x [mock:fail]", nil)
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", bad.id()), obj{}, 200)
	h.waitStatus(bad.id(), "failed")

	out := h.post("/api/tasks/clear", obj{"statuses": []string{"failed"}}, 200)
	if out.num("cleared") != 1 {
		t.Fatalf("expected one failed task cleared, got %v", out)
	}
	if _, err := h.App.DB.Task(bad.id()); err == nil {
		t.Error("the failed task is still there")
	}
	if _, err := h.App.DB.Task(ok.id()); err != nil {
		t.Error("clearing failures removed a completed task too")
	}
}

// Clearing something still live would kill its agent, so it takes saying so.
func TestClearingLiveWorkNeedsSayingSo(t *testing.T) {
	h := newHarness(t)
	code, body := h.request("POST", "/api/tasks/clear", obj{"statuses": []string{"running"}}, nil)
	if code != 409 {
		t.Fatalf("expected a refusal, got %d %s", code, body)
	}
	if !strings.Contains(string(body), "force") {
		t.Errorf("the refusal should say how to proceed: %s", body)
	}
	// review is waiting on the operator, not finished
	if code, _ := h.request("POST", "/api/tasks/clear",
		obj{"statuses": []string{"review"}}, nil); code != 409 {
		t.Errorf("review should not be swept by default: %d", code)
	}
	if code, body := h.request("POST", "/api/tasks/clear",
		obj{"statuses": []string{"nonsense"}}, nil); code != 422 {
		t.Errorf("unknown status: %d %s", code, body)
	}
}

// Clearing one project's history must not touch another's.
func TestClearingCanBeScopedToAProject(t *testing.T) {
	h := newHarness(t)
	projects := twoProjects(h, t)

	var ids []int64
	for _, pid := range projects {
		task := h.run(pid, "finished", "x", nil)
		h.waitStatus(task.id(), "review")
		h.post(fmt.Sprintf("/api/tasks/%d/complete", task.id()), obj{}, 200)
		ids = append(ids, task.id())
	}

	out := h.post("/api/tasks/clear", obj{"project_id": projects[0]}, 200)
	if out.num("cleared") != 1 {
		t.Fatalf("expected to clear only the first project's task: %v", out)
	}
	if _, err := h.App.DB.Task(ids[0]); err == nil {
		t.Error("the targeted task survived")
	}
	if _, err := h.App.DB.Task(ids[1]); err != nil {
		t.Error("clearing one project removed another project's history")
	}
}

// A routine that always runs on whatever the project defaults to cannot be the
// cheap one for a sweep and the expensive one for a hard review.
func TestARoutineCarriesItsAgentAndModel(t *testing.T) {
	h := newHarness(t)
	created := h.post("/api/routines", obj{
		"name": "astra sweep", "prompt": prReview,
		"project_ids": []int64{h.seededProjectID()},
		"agent":       "codex", "model": "gpt-6-astra",
	}, 201)
	if created.str("agent") != "codex" || created.str("model") != "gpt-6-astra" {
		t.Fatalf("%v", created)
	}

	out := h.post(fmt.Sprintf("/api/routines/%d/run", int64(created.num("id"))), obj{}, 200)
	var res struct {
		Tasks []int64 `json:"tasks"`
	}
	raw, _ := json.Marshal(out)
	json.Unmarshal(raw, &res)
	if len(res.Tasks) != 1 {
		t.Fatalf("expected one task: %v", out)
	}
	task, err := h.App.DB.Task(res.Tasks[0])
	if err != nil {
		t.Fatal(err)
	}
	if task.Agent != "codex" || task.Model != "gpt-6-astra" {
		t.Errorf("the routine's agent and model did not reach its task: %q / %q",
			task.Agent, task.Model)
	}
}

// Editing a routine must be able to change them, including back to the
// project's default.
func TestARoutinesAgentAndModelAreEditable(t *testing.T) {
	h := newHarness(t)
	created := h.post("/api/routines", obj{
		"name": "switchable", "prompt": prReview,
		"project_ids": []int64{h.seededProjectID()},
		"agent":       "codex", "model": "gpt-5.6-sol",
	}, 201)
	id := int64(created.num("id"))

	moved := h.patch(fmt.Sprintf("/api/routines/%d", id),
		obj{"agent": "claude", "model": "opus"}, 200)
	if moved.str("agent") != "claude" || moved.str("model") != "opus" {
		t.Errorf("%v", moved)
	}
	// and an agent that does not exist is refused rather than saved
	if code, body := h.request("PATCH", fmt.Sprintf("/api/routines/%d", id),
		obj{"agent": "nope"}, nil); code != 400 && code != 422 {
		t.Errorf("expected a refusal for an unknown agent, got %d %s", code, body)
	}
}
