package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// stubHost answers ask_user and plan_review questions from a script.
type stubHost struct {
	mu      sync.Mutex
	answers []string
	asked   []tools.Question
}

func (h *stubHost) Approve(context.Context, tools.Approval) tools.Decision { return tools.Deny }

func (h *stubHost) Ask(_ context.Context, q tools.Question) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.asked = append(h.asked, q)
	if len(h.answers) == 0 {
		return "", errors.New("no answer scripted")
	}
	a := h.answers[0]
	h.answers = h.answers[1:]
	return a, nil
}

func (h *stubHost) Progress(string, string) {}

type planHarness struct {
	dir     string
	fake    *fakeLLM
	agent   *Agent
	planner *Planner
	host    *stubHost
	log     *eventLog
}

func newPlanHarness(t *testing.T, mode string, answers ...string) *planHarness {
	t.Helper()
	dir := t.TempDir()
	f := newFake(t)
	reg := tools.NewRegistry()
	reg.Register(readTool{})
	log := &eventLog{}
	planner := &Planner{Dir: dir, Sink: log.sink}
	planner.Register(reg)
	host := &stubHost{answers: answers}

	opts := baseOptions(f, reg, log, dir)
	opts.Mode = mode
	opts.Env.Host = host
	opts.System = func(m string) string {
		return BuildSystem(PromptInput{Workdir: dir, ModelRef: "fake/model-a"}, m)
	}
	a := New(opts)
	planner.Agent = a
	return &planHarness{dir: dir, fake: f, agent: a, planner: planner, host: host, log: log}
}

const planArgs = `{"title":"Add parser tests","summary":"Cover the edge cases.","steps":[{"text":"Write table tests","files":["parser_test.go"]},{"text":"Run go test ./...","files":[]}],"body":"Risks: none."}`

const revisedPlanArgs = `{"title":"Add parser tests v2","summary":"Cover more cases.","steps":[{"text":"Write table and fuzz tests","files":["parser_test.go"]}]}`

func TestPlanExecuteLiftsRestrictionsAndSavesPlan(t *testing.T) {
	h := newPlanHarness(t, "plan", "Execute")
	h.fake.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "call_p", name: "plan", args: planArgs}}}),
		turn(sseTurn{text: []string{"Plan done."}}),
	))
	stats, err := h.agent.Run(context.Background(), "add tests", nil)
	must(t, err)
	if stats.Error != "" {
		t.Fatalf("stats.Error = %q", stats.Error)
	}

	if h.agent.Mode() != "default" {
		t.Fatalf("mode after Execute = %q", h.agent.Mode())
	}
	if len(h.host.asked) != 1 || h.host.asked[0].Kind != "plan_review" ||
		strings.Join(h.host.asked[0].Options, ",") != "Execute,Revise,Cancel" {
		t.Fatalf("plan review question = %+v", h.host.asked)
	}

	reqs := h.fake.requests()
	if !strings.Contains(systemText(reqs[0]), "# Mode: plan") || strings.Contains(systemText(reqs[1]), "# Mode: plan") {
		t.Fatal("system prompt did not switch out of plan mode after Execute")
	}
	if got := toolMsg(t, reqs[1], "call_p").text(); !strings.Contains(got, "The user approved the plan") {
		t.Fatalf("plan result sent to the model = %q", got)
	}

	saved := filepath.Join(h.dir, ".scribe", "plans", "add-parser-tests.md")
	b, err := os.ReadFile(saved)
	if err != nil {
		t.Fatalf("plan not saved: %v", err)
	}
	if !strings.Contains(string(b), "# Add parser tests") || !strings.Contains(string(b), "`parser_test.go`") {
		t.Fatalf("saved plan = %s", b)
	}
	pl := h.planner.Plan()
	if pl == nil || pl.Path != saved || len(pl.Steps) != 2 || pl.Steps[0].Status != "pending" {
		t.Fatalf("planner plan = %+v", pl)
	}
	if len(h.log.of(EvPlan)) == 0 {
		t.Fatal("no plan events emitted")
	}
}

func TestPlanCancelEndsTheTurn(t *testing.T) {
	h := newPlanHarness(t, "plan", "Cancel")
	h.fake.setPick(scripted(t, turn(sseTurn{calls: []sseCall{{id: "call_p", name: "plan", args: planArgs}}})))
	_, err := h.agent.Run(context.Background(), "add tests", nil)
	must(t, err)

	if h.fake.count() != 1 {
		t.Fatalf("model called %d times after cancel, want 1", h.fake.count())
	}
	if h.agent.Mode() != "plan" {
		t.Fatalf("mode after Cancel = %q, want plan", h.agent.Mode())
	}
	if h.planner.Plan() != nil {
		t.Fatal("cancelled plan is still held")
	}
	msgs := h.agent.Messages()
	if got := msgs[len(msgs)-1].Parts[0].Result; !strings.Contains(got, "cancelled") {
		t.Fatalf("last tool result = %q", got)
	}
}

func TestPlanReviseReturnsFeedbackAndReplans(t *testing.T) {
	h := newPlanHarness(t, "plan", "Revise more tests", "Execute")
	h.fake.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "call_1", name: "plan", args: planArgs}}}),
		turn(sseTurn{calls: []sseCall{{id: "call_2", name: "plan", args: revisedPlanArgs}}}),
		turn(sseTurn{text: []string{"Implemented."}}),
	))
	_, err := h.agent.Run(context.Background(), "add tests", nil)
	must(t, err)

	reqs := h.fake.requests()
	if len(reqs) != 3 {
		t.Fatalf("requests = %d, want 3", len(reqs))
	}
	fb := toolMsg(t, reqs[1], "call_1").text()
	if !strings.Contains(fb, "wants changes") || !strings.Contains(fb, "more tests") {
		t.Fatalf("revise feedback = %q", fb)
	}
	if got := h.planner.Plan().Title; got != "Add parser tests v2" {
		t.Fatalf("plan after revision = %q", got)
	}
	if h.agent.Mode() != "default" || len(h.host.asked) != 2 {
		t.Fatalf("mode=%q questions=%d", h.agent.Mode(), len(h.host.asked))
	}
}

func TestTaskListSetAndUpdate(t *testing.T) {
	h := newPlanHarness(t, "default")
	h.fake.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "call_s", name: "task_list", args: `{"action":"set","tasks":["write parser","write tests"]}`}}}),
		turn(sseTurn{calls: []sseCall{{id: "call_u", name: "task_list", args: `{"action":"update","id":"1","status":"done"}`}}}),
		turn(sseTurn{calls: []sseCall{{id: "call_x", name: "task_list", args: `{"action":"update","id":"9","status":"done"}`}}}),
		turn(sseTurn{text: []string{"Checklist updated."}}),
	))
	_, err := h.agent.Run(context.Background(), "plan the work", nil)
	must(t, err)

	tasks := h.planner.Tasks()
	if len(tasks) != 2 || tasks[0].Status != "done" || tasks[0].Text != "write parser" || tasks[1].Status != "pending" {
		t.Fatalf("tasks = %+v", tasks)
	}
	if n := len(h.log.of(EvTasks)); n != 2 {
		t.Fatalf("EvTasks count = %d, want 2 (set, update)", n)
	}
	reqs := h.fake.requests()
	if got := toolMsg(t, reqs[1], "call_s").text(); !strings.Contains(got, "1. [pending] write parser") {
		t.Fatalf("set result = %q", got)
	}
	if got := toolMsg(t, reqs[2], "call_u").text(); !strings.Contains(got, "1. [done] write parser") {
		t.Fatalf("update result = %q", got)
	}
	if got := toolMsg(t, reqs[3], "call_x").text(); !strings.Contains(got, `error: no task "9"`) {
		t.Fatalf("bad update result = %q", got)
	}
}
