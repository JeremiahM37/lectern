package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// tlPlanWith sets a task list of the given texts on p, through the same
// task_list tool the model uses, so ids are the ones a dispatch will see.
func tlPlanWith(t *testing.T, p *Planner, texts ...string) {
	t.Helper()
	args, err := json.Marshal(map[string]any{"action": "set", "tasks": texts})
	if err != nil {
		t.Fatal(err)
	}
	if res := (taskTool{p}).Run(context.Background(), nil, args); res.IsError {
		t.Fatalf("task_list set: %s", res.Output)
	}
}

// tlStatusOf returns the status of the item with id, or "" when there is none.
func tlStatusOf(p *Planner, id string) string {
	for _, task := range p.Tasks() {
		if task.ID == id {
			return task.Status
		}
	}
	return ""
}

// A dispatch task with a taskId marks that task-list item done when the
// subagent finishes, and leaves the other items alone.
func TestDispatchTaskIdMarksPlannerItemDone(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	h.f.setPick(func(_ int, req recordedReq) reply {
		return turn(sseTurn{text: []string{"found it"}})
	})
	p := &Planner{}
	tlPlanWith(t, p, "find the parser", "check the lexer")
	h.d.TaskStatus = p.MarkTask
	res := h.run(`{"tasks":[{"task":"find the parser","role":"explore","taskId":"1"}]}`)
	if res.IsError {
		t.Fatalf("dispatch failed: %s", res.Output)
	}
	if got := tlStatusOf(p, "1"); got != "done" {
		t.Fatalf("item 1 = %q, want done", got)
	}
	if got := tlStatusOf(p, "2"); got != "pending" {
		t.Fatalf("item 2 = %q, want pending (no taskId named it)", got)
	}
}

// A subagent that fails marks its task-list item blocked, not done.
func TestFailingSubagentMarksPlannerItemBlocked(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	h.f.setPick(func(_ int, req recordedReq) reply {
		switch openingOf(req) {
		case "find the parser":
			return turn(sseTurn{text: []string{"parse.go:12"}})
		case "check the lexer":
			return providerError(400)
		}
		t.Errorf("unexpected task %q", openingOf(req))
		return providerError(500)
	})
	p := &Planner{}
	tlPlanWith(t, p, "find the parser", "check the lexer")
	h.d.TaskStatus = p.MarkTask
	h.run(`{"tasks":[` +
		`{"task":"find the parser","role":"explore","taskId":"1"},` +
		`{"task":"check the lexer","role":"explore","taskId":"2"}]}`)
	if got := tlStatusOf(p, "1"); got != "done" {
		t.Fatalf("item 1 = %q, want done", got)
	}
	if got := tlStatusOf(p, "2"); got != "blocked" {
		t.Fatalf("item 2 = %q, want blocked", got)
	}
}

// Ten explore tasks are merged down to eight. The absorbed task (the ninth,
// with taskId "9") runs inside the task it was merged into, so its item must
// still be marked, with the same outcome.
func TestMergedTaskKeepsItsTaskIds(t *testing.T) {
	texts := make([]string, 9)
	tasks := make([]string, 9)
	for i := range texts {
		texts[i] = fmt.Sprintf("scan area %d", i+1)
		tasks[i] = fmt.Sprintf(`{"task":%q,"role":"explore","taskId":"%d"}`, texts[i], i+1)
	}
	args := `{"tasks":[` + strings.Join(tasks, ",") + `]}`

	t.Run("succeeds", func(t *testing.T) {
		h := newDispatchHarness(t, plainRegistry)
		h.f.setPick(func(_ int, req recordedReq) reply {
			return turn(sseTurn{text: []string{"nothing to report"}})
		})
		p := &Planner{}
		tlPlanWith(t, p, texts...)
		h.d.TaskStatus = p.MarkTask
		res := h.run(args)
		if !strings.Contains(res.Output, "taskId=1,9") {
			t.Fatalf("merged task does not carry both ids:\n%s", res.Output)
		}
		for _, id := range []string{"1", "9"} {
			if got := tlStatusOf(p, id); got != "done" {
				t.Fatalf("item %s = %q, want done", id, got)
			}
		}
	})

	t.Run("fails", func(t *testing.T) {
		h := newDispatchHarness(t, plainRegistry)
		h.f.setPick(func(_ int, req recordedReq) reply {
			return providerError(400)
		})
		p := &Planner{}
		tlPlanWith(t, p, texts...)
		h.d.TaskStatus = p.MarkTask
		h.run(args)
		for _, id := range []string{"1", "9"} {
			if got := tlStatusOf(p, id); got != "blocked" {
				t.Fatalf("item %s = %q, want blocked", id, got)
			}
		}
	})
}

// Merging keeps a single id when only one side has one, and joins both when
// both do, without duplicates.
func TestMergeOverLimitCarriesTaskIds(t *testing.T) {
	tasks := make([]subTask, 9)
	for i := range tasks {
		tasks[i] = subTask{Task: fmt.Sprintf("area %d", i+1), Role: "explore"}
	}
	tasks[8].TaskID = "9" // the last explore task is the one merged away
	out, _, err := mergeOverLimit(tasks, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 8 {
		t.Fatalf("got %d tasks, want 8", len(out))
	}
	if out[0].TaskID != "9" {
		t.Fatalf("receiving task taskId = %q, want 9 (adopted from the merged task)", out[0].TaskID)
	}

	if got := joinTaskIDs("3", "3"); got != "3" {
		t.Fatalf("joinTaskIDs(3,3) = %q, want 3", got)
	}
	if got := joinTaskIDs("", "5"); got != "5" {
		t.Fatalf("joinTaskIDs(\"\",5) = %q, want 5", got)
	}
	if got := joinTaskIDs("2", ""); got != "2" {
		t.Fatalf("joinTaskIDs(2,\"\") = %q, want 2", got)
	}
}

// MarkTask on a list with no matching item changes nothing and emits nothing.
func TestMarkTaskIgnoresUnknownIds(t *testing.T) {
	var events []Event
	p := &Planner{Sink: func(ev Event) { events = append(events, ev) }}
	tlPlanWith(t, p, "only item")
	events = nil
	p.MarkTask("7", "done")
	if len(events) != 0 {
		t.Fatalf("an unknown id emitted %d events", len(events))
	}
	if got := tlStatusOf(p, "1"); got != "pending" {
		t.Fatalf("item 1 = %q, want pending", got)
	}
}
