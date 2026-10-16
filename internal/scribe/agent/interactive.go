package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// Planner owns the plan and task list of one agent and provides the plan,
// update_plan_step, ask_user and task_list tools.
type Planner struct {
	Agent *Agent // set after the agent is created
	Dir   string // project root; plans are saved in <Dir>/.scribe/plans
	Sink  Sink

	mu    sync.Mutex
	plan  *Plan
	tasks []Task
}

// Plan returns the current plan (nil when none).
func (p *Planner) Plan() *Plan {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.plan == nil {
		return nil
	}
	c := *p.plan
	c.Steps = append([]PlanStep(nil), p.plan.Steps...)
	return &c
}

// Tasks returns the task list.
func (p *Planner) Tasks() []Task {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Task(nil), p.tasks...)
}

// SetPlan restores a plan (session resume).
func (p *Planner) SetPlan(pl *Plan) { p.mu.Lock(); p.plan = pl; p.mu.Unlock() }

// Register adds the planner's tools.
func (p *Planner) Register(r *tools.Registry) {
	r.Register(planTool{p})
	r.Register(stepTool{p})
	r.Register(askTool{})
	r.Register(taskTool{p})
}

func (p *Planner) emitPlan() {
	if p.Sink != nil {
		p.Sink(Event{Kind: EvPlan, Plan: p.Plan()})
	}
}

type planTool struct{ p *Planner }

func (planTool) ReadOnly() bool { return true }
func (planTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "plan",
		Description: "Propose an implementation plan for the user to approve. Use it in plan mode after investigating. " +
			"The user can approve (you then implement it in this same turn), ask for revisions, or cancel. " +
			"Steps must be concrete and ordered; list the files each step touches.",
		Schema: tools.Schema(map[string]any{
			"title":   tools.Str("Short title of the change"),
			"summary": tools.Str("Two or three sentences: what changes and why"),
			"steps": tools.Arr("Ordered steps", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text":  tools.Str("What this step does"),
					"files": tools.Arr("Files this step touches", map[string]any{"type": "string"}),
				},
				"required": []string{"text"},
			}),
			"body": tools.Str("Optional full plan in markdown: risks, test plan, code sketches"),
		}, "title", "steps"),
	}
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

func (t planTool) Run(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result {
	var in struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
		Steps   []struct {
			Text  string   `json:"text"`
			Files []string `json:"files"`
		} `json:"steps"`
		Body string `json:"body"`
	}
	if err := tools.Decode(args, &in); err != nil {
		return tools.Errorf("%v", err)
	}
	if in.Title == "" || len(in.Steps) == 0 {
		return tools.Errorf("plan needs a title and at least one step")
	}
	pl := &Plan{Title: in.Title, Summary: in.Summary, Body: in.Body}
	for i, s := range in.Steps {
		pl.Steps = append(pl.Steps, PlanStep{ID: fmt.Sprint(i + 1), Text: s.Text, Files: s.Files, Status: "pending"})
	}
	md := PlanMarkdown(pl)
	if t.p.Dir != "" {
		dir := filepath.Join(t.p.Dir, ".scribe", "plans")
		if os.MkdirAll(dir, 0o755) == nil {
			slug := strings.Trim(slugRE.ReplaceAllString(strings.ToLower(in.Title), "-"), "-")
			if len(slug) > 50 {
				slug = slug[:50]
			}
			path := filepath.Join(dir, slug+".md")
			if os.WriteFile(path, []byte(md), 0o644) == nil {
				pl.Path = path
			}
		}
	}
	t.p.mu.Lock()
	t.p.plan = pl
	t.p.mu.Unlock()
	t.p.emitPlan()

	if env.Host == nil {
		return tools.Result{Output: "Plan recorded. No reviewer is attached, so execute it now.", Summary: in.Title}
	}
	ans, err := env.Host.Ask(ctx, tools.Question{
		Kind:          "plan_review",
		Question:      in.Title,
		Body:          md,
		Options:       []string{"Execute", "Revise", "Cancel"},
		AllowFreeText: true,
	})
	if err != nil {
		return tools.Errorf("plan review was interrupted: %v", err)
	}
	switch {
	case strings.EqualFold(ans, "Execute") || strings.HasPrefix(strings.ToLower(ans), "execute"):
		if t.p.Agent != nil {
			t.p.Agent.SetMode("default")
		}
		return tools.Result{Output: "The user approved the plan. Restrictions are lifted: implement it now, step by step. " +
			"Call update_plan_step with status active when you start a step and done when it is finished. Verify with tests or builds at the end.",
			Summary: "approved"}
	case strings.EqualFold(ans, "Cancel"):
		if t.p.Agent != nil {
			t.p.Agent.EndTurn()
		}
		t.p.mu.Lock()
		t.p.plan = nil
		t.p.mu.Unlock()
		t.p.emitPlan()
		return tools.Result{Output: "The user cancelled the plan. Stop and wait for their next message.", Summary: "cancelled"}
	default:
		fb := strings.TrimSpace(strings.TrimPrefix(ans, "Revise"))
		if fb == "" {
			fb = "(no details given)"
		}
		return tools.Result{Output: "The user wants changes to the plan before approving it. Their feedback:\n\n" + fb +
			"\n\nInvestigate as needed, then call plan again with the revised plan.", Summary: "revise"}
	}
}

// PlanMarkdown renders a plan.
func PlanMarkdown(pl *Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", pl.Title)
	if pl.Summary != "" {
		fmt.Fprintf(&b, "%s\n\n", pl.Summary)
	}
	b.WriteString("## Steps\n\n")
	for _, s := range pl.Steps {
		fmt.Fprintf(&b, "%s. %s", s.ID, s.Text)
		if len(s.Files) > 0 {
			fmt.Fprintf(&b, " (`%s`)", strings.Join(s.Files, "`, `"))
		}
		b.WriteString("\n")
	}
	if pl.Body != "" {
		fmt.Fprintf(&b, "\n%s\n", pl.Body)
	}
	return b.String()
}

type stepTool struct{ p *Planner }

func (stepTool) ReadOnly() bool { return true }
func (stepTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "update_plan_step",
		Description: "Mark a step of the approved plan active, done or skipped.",
		Schema: tools.Schema(map[string]any{
			"id":     tools.Str("Step id (its number)"),
			"status": tools.Enum("New status", "active", "done", "skipped", "pending"),
		}, "id", "status"),
	}
}

func (t stepTool) Run(_ context.Context, _ *tools.Env, args json.RawMessage) tools.Result {
	var in struct{ ID, Status string }
	if err := tools.Decode(args, &in); err != nil {
		return tools.Errorf("%v", err)
	}
	t.p.mu.Lock()
	if t.p.plan == nil {
		t.p.mu.Unlock()
		return tools.Errorf("there is no plan; call plan first")
	}
	found := false
	for i := range t.p.plan.Steps {
		if t.p.plan.Steps[i].ID == in.ID {
			t.p.plan.Steps[i].Status = in.Status
			found = true
		}
	}
	t.p.mu.Unlock()
	if !found {
		return tools.Errorf("no step %q", in.ID)
	}
	t.p.emitPlan()
	return tools.Result{Output: "ok", Summary: in.ID + " " + in.Status}
}

type askTool struct{}

func (askTool) ReadOnly() bool { return true }
func (askTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "ask_user",
		Description: "Ask the user a question when you genuinely cannot proceed without their decision. " +
			"Offer 2-4 concrete options; put your recommended option first.",
		Schema: tools.Schema(map[string]any{
			"question": tools.Str("The question"),
			"options":  tools.Arr("Choices", map[string]any{"type": "string"}),
		}, "question"),
	}
}

func (askTool) Run(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result {
	var in tools.Question
	if err := tools.Decode(args, &in); err != nil {
		return tools.Errorf("%v", err)
	}
	in.AllowFreeText = true
	if env.Host == nil {
		if len(in.Options) > 0 {
			return tools.Result{Output: "No user is available; proceed with: " + in.Options[0]}
		}
		return tools.Result{Output: "No user is available; use your best judgement and say what you assumed."}
	}
	ans, err := env.Host.Ask(ctx, in)
	if err != nil {
		return tools.Errorf("question was interrupted: %v", err)
	}
	return tools.Result{Output: "The user answered: " + ans, Summary: ans}
}

type taskTool struct{ p *Planner }

func (taskTool) ReadOnly() bool { return true }
func (taskTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "task_list",
		Description: "Keep a visible checklist for multi-step work. Replace the whole list with action=set, " +
			"or change one item's status with action=update.",
		Schema: tools.Schema(map[string]any{
			"action": tools.Enum("set replaces the list; update changes one item; clear empties it", "set", "update", "clear"),
			"tasks":  tools.Arr("For set: the task texts in order", map[string]any{"type": "string"}),
			"id":     tools.Str("For update: task id"),
			"status": tools.Enum("For update: new status", "pending", "in_progress", "done", "cancelled"),
		}, "action"),
	}
}

func (t taskTool) Run(_ context.Context, _ *tools.Env, args json.RawMessage) tools.Result {
	var in struct {
		Action string   `json:"action"`
		Tasks  []string `json:"tasks"`
		ID     string   `json:"id"`
		Status string   `json:"status"`
	}
	if err := tools.Decode(args, &in); err != nil {
		return tools.Errorf("%v", err)
	}
	t.p.mu.Lock()
	switch in.Action {
	case "set":
		t.p.tasks = nil
		for i, s := range in.Tasks {
			t.p.tasks = append(t.p.tasks, Task{ID: fmt.Sprint(i + 1), Text: s, Status: "pending"})
		}
	case "update":
		ok := false
		for i := range t.p.tasks {
			if t.p.tasks[i].ID == in.ID {
				t.p.tasks[i].Status = in.Status
				ok = true
			}
		}
		if !ok {
			t.p.mu.Unlock()
			return tools.Errorf("no task %q", in.ID)
		}
	case "clear":
		t.p.tasks = nil
	default:
		t.p.mu.Unlock()
		return tools.Errorf("unknown action %q", in.Action)
	}
	list := append([]Task(nil), t.p.tasks...)
	t.p.mu.Unlock()
	if t.p.Sink != nil {
		t.p.Sink(Event{Kind: EvTasks, Tasks: list})
	}
	var b strings.Builder
	for _, x := range list {
		fmt.Fprintf(&b, "%s. [%s] %s\n", x.ID, x.Status, x.Text)
	}
	if b.Len() == 0 {
		b.WriteString("(empty)")
	}
	return tools.Result{Output: b.String()}
}
