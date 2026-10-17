package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// w16UserText joins the text of every user message in a request.
func w16UserText(req recordedReq) string {
	var parts []string
	for _, m := range msgsWithRole(req, "user") {
		parts = append(parts, m.text())
	}
	return strings.Join(parts, "\n")
}

// w16AnswerHost approves every request and answers every question. The
// notification tests need a host that does not block.
type w16AnswerHost struct{}

func (w16AnswerHost) Approve(context.Context, tools.Approval) tools.Decision { return tools.AllowOnce }
func (w16AnswerHost) Ask(context.Context, tools.Question) (string, error) {
	return "main", nil
}
func (w16AnswerHost) Progress(string, string) {}

// ---- 2.2 step 12: task list reminder ---------------------------------------

// The list reaches the model as a latched reminder after the step that
// changed it, and not again while it stays the same. The system prompt never
// carries it.
func TestTaskListReminderIsLatchedAndKeepsSystemPromptStable(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(fnTool{name: "read", ro: true, run: func(context.Context, *tools.Env, json.RawMessage) tools.Result {
		return tools.Result{Output: "ok"}
	}})
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "c1", name: "task_list", args: `{"action":"set","tasks":["write parser","run tests"]}`}}}),
		turn(sseTurn{calls: []sseCall{{id: "c2", name: "read", args: `{"id":"a"}`}}}),
		turn(sseTurn{calls: []sseCall{{id: "c3", name: "read", args: `{"id":"b"}`}}}),
		turn(sseTurn{calls: []sseCall{{id: "c4", name: "task_list", args: `{"action":"update","id":"1","status":"done"}`}}}),
		turn(sseTurn{text: []string{"finished"}}),
	))
	a := New(baseOptions(f, reg, &eventLog{}, t.TempDir()))
	(&Planner{Agent: a}).Register(reg)
	if _, err := a.Run(context.Background(), "plan the work", nil); err != nil {
		t.Fatal(err)
	}

	const marker = "Task list (keep it current"
	reqs := f.requests()
	if len(reqs) != 5 {
		t.Fatalf("requests = %d, want 5", len(reqs))
	}
	want := []int{0, 1, 1, 1, 2} // reminders seen by each request
	for i, req := range reqs {
		if got := strings.Count(w16UserText(req), marker); got != want[i] {
			t.Fatalf("request %d carries %d task reminders, want %d", i, got, want[i])
		}
		if strings.Contains(systemText(req), "Task list") {
			t.Fatalf("request %d put the task list in the system prompt", i)
		}
		if systemText(req) != systemText(reqs[0]) {
			t.Fatalf("system prompt changed between requests %d and 0", i)
		}
	}
	if !strings.Contains(w16UserText(reqs[1]), "1. [pending] write parser") {
		t.Fatalf("first reminder does not list the set tasks: %s", w16UserText(reqs[1]))
	}
	if !strings.Contains(w16UserText(reqs[4]), "1. [done] write parser") {
		t.Fatalf("changed list was not re-sent: %s", w16UserText(reqs[4]))
	}
}

// An empty list is never reminded of, and clearing it means the next list is
// news again.
func TestTaskReminderClearsWithTheList(t *testing.T) {
	a := New(baseOptions(newFake(t), tools.NewRegistry(), &eventLog{}, t.TempDir()))
	a.setTaskList([]Task{{ID: "1", Text: "one", Status: "pending"}})
	if a.taskReminder() == "" {
		t.Fatal("new list not reminded of")
	}
	if a.taskReminder() != "" {
		t.Fatal("unchanged list reminded of twice")
	}
	a.setTaskList(nil)
	if a.taskReminder() != "" {
		t.Fatal("empty list reminded of")
	}
	a.setTaskList([]Task{{ID: "1", Text: "one", Status: "pending"}})
	if a.taskReminder() == "" {
		t.Fatal("list set again after clearing was not reminded of")
	}
}

// A replaced history (compaction, reset, resume) has dropped the reminder, so
// the list is sent again in the new history.
func TestTaskReminderResendsAfterHistoryReplaced(t *testing.T) {
	a := New(baseOptions(newFake(t), tools.NewRegistry(), &eventLog{}, t.TempDir()))
	a.setTaskList([]Task{{ID: "1", Text: "one", Status: "pending"}})
	a.taskReminder()
	a.SetMessages(nil)
	if a.taskReminder() == "" {
		t.Fatal("list not re-sent after the history was replaced")
	}
}

// ---- 2.2 step 2 and 5.4: malformed tool input --------------------------------

func TestTruncatedToolInputGetsTruncatedWording(t *testing.T) {
	cases := []struct {
		name, args, want string
	}{
		{"cut off mid-object", `{"id": "a.go", "content": "pack`, "truncated input"},
		{"stray brace", `{"id": "a.go"}}`, "not valid JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ran := false
			reg := tools.NewRegistry()
			reg.Register(fnTool{name: "write", run: func(context.Context, *tools.Env, json.RawMessage) tools.Result {
				ran = true
				return tools.Result{Output: "wrote"}
			}})
			f := newFake(t)
			f.setPick(scripted(t,
				turn(sseTurn{calls: []sseCall{{id: "c1", name: "write", args: tc.args}}}),
				turn(sseTurn{text: []string{"ok"}}),
			))
			a := New(baseOptions(f, reg, &eventLog{}, t.TempDir()))
			if _, err := a.Run(context.Background(), "write it", nil); err != nil {
				t.Fatal(err)
			}
			if ran {
				t.Fatal("a call with broken arguments was run")
			}
			req := f.requests()[1]
			if got := toolMsg(t, req, "c1").text(); !strings.Contains(got, tc.want) {
				t.Fatalf("tool result = %q, want it to mention %q", got, tc.want)
			}
			// The history keeps an object, so the next request stays valid.
			for _, m := range msgsWithRole(req, "assistant") {
				for _, c := range m.ToolCalls {
					if c.Function.Name == "write" && c.Function.Arguments != "{}" {
						t.Fatalf("history carries arguments %q, want {}", c.Function.Arguments)
					}
				}
			}
		})
	}
}

func TestLooksTruncated(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{`{"a":`, true},
		{`{"a":"x`, true},
		{`{"a":{"b":[1,`, true},
		{`{"a":"x\"`, true},
		{`{"a":1}`, false},
		{`{"a":1}}`, false},
		{`{"a" 1}`, false},
		{`[1,2`, false},
		{``, false},
	}
	for _, tc := range cases {
		if got := looksTruncated(tc.raw); got != tc.want {
			t.Errorf("looksTruncated(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

// ---- 2.7: steering queued during compaction ---------------------------------

// steeringCompactor queues a user message while it works, the way a user who
// types during a compaction does.
type steeringCompactor struct {
	agent *Agent
}

func (c *steeringCompactor) Compact(_ context.Context, msgs []llm.Message) ([]llm.Message, string, error) {
	c.agent.Steer("late note")
	return msgs[len(msgs)-1:], "summary", nil
}

// Compaction that runs before the first step (on a turn that starts with a
// full context) drains what was queued during it into that first request,
// rather than waiting for a later step.
func TestSteeringQueuedDuringFirstStepCompactionIsDrained(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t, turn(sseTurn{text: []string{"answer"}})))
	opts := baseOptions(f, tools.NewRegistry(), &eventLog{}, t.TempDir())
	opts.Model.Info.ContextWindow = 1000
	opts.CompactAt = 0.5
	comp := &steeringCompactor{}
	opts.Compactor = comp
	a := New(opts)
	comp.agent = a
	a.SetMessages(manyMessages(10))
	a.lastIn = 900 // the last request filled the window

	if _, err := a.Run(context.Background(), "continue", nil); err != nil {
		t.Fatal(err)
	}
	if f.count() != 1 {
		t.Fatalf("requests = %d, want 1", f.count())
	}
	got := w16UserText(f.requests()[0])
	if !strings.Contains(got, "<steering>") || !strings.Contains(got, "late note") {
		t.Fatalf("first request does not carry the steering queued during compaction: %s", got)
	}
	if len(a.Queued()) != 0 {
		t.Fatalf("queue not drained: %v", a.Queued())
	}
}

// ---- 05 §7.3: Notification on waits for the user -----------------------------

func TestApprovalAndQuestionWaitsNotify(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(fnTool{name: "guarded", run: func(ctx context.Context, env *tools.Env, _ json.RawMessage) tools.Result {
		env.Host.Approve(ctx, tools.Approval{Tool: "shell", Title: "Run shell command", Detail: "go test ./..."})
		env.Host.Ask(ctx, tools.Question{Question: "Which branch?"})
		return tools.Result{Output: "done"}
	}})
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "c1", name: "guarded", args: `{}`}}}),
		turn(sseTurn{text: []string{"done"}}),
	))
	hooks := &noticeHooks{}
	opts := baseOptions(f, reg, &eventLog{}, t.TempDir())
	opts.Hooks = hooks
	opts.Env.Host = w16AnswerHost{}
	if _, err := New(opts).Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	// The hook runs in its own goroutine, so wait for both notices.
	var got []string
	waitUntil(t, 5*time.Second, "two wait notices", func() bool {
		hooks.mu.Lock()
		defer hooks.mu.Unlock()
		got = append([]string(nil), hooks.notes...)
		return len(got) == 2
	})
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "Approval needed: Run shell command: go test ./...") {
		t.Fatalf("no approval notice in %q", got)
	}
	if !strings.Contains(joined, "Question for you: Which branch?") {
		t.Fatalf("no question notice in %q", got)
	}
}

func TestPlanReviewNoticeSaysPlanIsReady(t *testing.T) {
	if got := questionNotice(tools.Question{Kind: "plan_review", Question: "Approve?"}); got != "Plan ready for your review" {
		t.Fatalf("plan review notice = %q", got)
	}
}
