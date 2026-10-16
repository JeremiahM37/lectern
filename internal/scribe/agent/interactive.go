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

// The answers a user can give to a plan review. The TUI sends these exact
// strings; Revise is followed by a space and the feedback.
const (
	AnswerExecute = "Execute"
	AnswerClear   = "Clear context and execute"
	AnswerRevise  = "Revise"
	AnswerCancel  = "Cancel"
)

// PlanOptions are the choices offered at plan review, in key order (1-4).
func PlanOptions() []string {
	return []string{AnswerExecute, AnswerClear, AnswerRevise, AnswerCancel}
}

// PlanSymbol is a code symbol a plan step adds, modifies, removes or renames.
type PlanSymbol struct {
	Name   string `json:"name"`
	Kind   string `json:"kind,omitempty"`   // func, method, type, field, ...
	Action string `json:"action,omitempty"` // add | modify | remove | rename
}

// planExtras is what the plan model carries beyond Plan: per-step symbols and
// the verification checklist. They travel in the plan markdown and the review.
type planExtras struct {
	symbols      map[string][]PlanSymbol // by step id
	verification []string
}

// pendingClear is a plan the user chose to implement with a cleared context.
type pendingClear struct {
	plan *Plan
	md   string // the plan markdown, as saved
}

// Planner owns the plan and task list of one agent and provides the plan,
// update_plan_step, ask_user and task_list tools.
type Planner struct {
	Agent *Agent // set after the agent is created
	Dir   string // project root; plans are saved in <Dir>/.scribe/plans
	Sink  Sink

	mu      sync.Mutex
	plan    *Plan
	extras  planExtras
	tasks   []Task
	cleared *pendingClear
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

// takeCleared returns the plan the user chose to implement with a cleared
// context, once. It is nil when there is none.
func (p *Planner) takeCleared() *pendingClear {
	p.mu.Lock()
	defer p.mu.Unlock()
	c := p.cleared
	p.cleared = nil
	return c
}

type planTool struct{ p *Planner }

func (planTool) ReadOnly() bool { return true }
func (planTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "plan",
		Description: "Propose an implementation plan for the user to approve. Use it in plan mode after investigating. " +
			"The user can approve (you then implement it in this same turn), approve and clear the planning context first, " +
			"ask for revisions, or cancel. Steps must be concrete and ordered; list the files and symbols each step touches " +
			"and say how the change is verified.",
		Schema: tools.Schema(map[string]any{
			"title":   tools.Str("Short title of the change"),
			"summary": tools.Str("Two or three sentences: what changes and why"),
			"steps": tools.Arr("Ordered steps", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text":  tools.Str("What this step does"),
					"files": tools.Arr("Files this step touches", map[string]any{"type": "string"}),
					"symbols": tools.Arr("Code symbols this step adds, modifies, removes or renames", map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name":   tools.Str("Symbol name"),
							"kind":   tools.Str("func, method, type, field, ..."),
							"action": tools.Enum("What happens to the symbol", "add", "modify", "remove", "rename"),
						},
						"required": []string{"name", "action"},
					}),
				},
				"required": []string{"text"},
			}),
			"verification": tools.Arr("Checks that prove the change works, in order: tests, builds, manual steps", map[string]any{"type": "string"}),
			"body":         tools.Str("Optional full plan in markdown: risks, test plan, code sketches"),
		}, "title", "steps"),
	}
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

func (t planTool) Run(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result {
	var in struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
		Steps   []struct {
			Text    string       `json:"text"`
			Files   []string     `json:"files"`
			Symbols []PlanSymbol `json:"symbols"`
		} `json:"steps"`
		Verification []string `json:"verification"`
		Body         string   `json:"body"`
	}
	if err := tools.Decode(args, &in); err != nil {
		return tools.Errorf("%v", err)
	}
	if in.Title == "" || len(in.Steps) == 0 {
		return tools.Errorf("plan needs a title and at least one step")
	}
	pl := &Plan{Title: in.Title, Summary: in.Summary, Body: in.Body}
	ex := planExtras{symbols: map[string][]PlanSymbol{}, verification: in.Verification}
	for i, s := range in.Steps {
		id := fmt.Sprint(i + 1)
		pl.Steps = append(pl.Steps, PlanStep{ID: id, Text: s.Text, Files: s.Files, Status: "pending"})
		if len(s.Symbols) > 0 {
			ex.symbols[id] = s.Symbols
		}
	}
	md := renderPlan(pl, ex)
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
	t.p.extras = ex
	t.p.mu.Unlock()
	t.p.emitPlan()

	if env.Host == nil {
		return tools.Result{Output: "Plan recorded. No reviewer is attached, so execute it now.", Summary: in.Title}
	}
	review := md
	if pl.Path != "" {
		review = "Saved to `" + pl.Path + "`\n\n" + md
	}
	ans, err := env.Host.Ask(ctx, tools.Question{
		Kind:          "plan_review",
		Question:      in.Title,
		Body:          review,
		Options:       PlanOptions(),
		AllowFreeText: true,
	})
	if err != nil {
		return tools.Errorf("plan review was interrupted: %v", err)
	}
	lower := strings.ToLower(ans)
	switch {
	case strings.EqualFold(ans, AnswerExecute) || strings.HasPrefix(lower, "execute"):
		if t.p.Agent != nil {
			t.p.Agent.SetMode("default")
		}
		return tools.Result{Output: "The user approved the plan. Restrictions are lifted: implement it now, step by step. " +
			"Call update_plan_step with status active when you start a step and done when it is finished. Verify with tests or builds at the end.",
			Summary: "approved"}
	case strings.EqualFold(ans, AnswerClear) || strings.HasPrefix(lower, "clear"):
		// The history is replaced after this turn ends (see RunClearing), so
		// the tool result below is never sent to the model.
		t.p.mu.Lock()
		t.p.cleared = &pendingClear{plan: pl, md: md}
		t.p.mu.Unlock()
		if t.p.Agent != nil {
			t.p.Agent.EndTurn()
		}
		return tools.Result{Output: "The user approved the plan and chose to clear the planning conversation. The plan is handed over to a fresh context.",
			Summary: "clear"}
	case strings.EqualFold(ans, AnswerCancel):
		if t.p.Agent != nil {
			t.p.Agent.EndTurn()
		}
		t.p.mu.Lock()
		t.p.plan = nil
		t.p.extras = planExtras{}
		t.p.mu.Unlock()
		t.p.emitPlan()
		return tools.Result{Output: "The user cancelled the plan. Stop and wait for their next message.", Summary: "cancelled"}
	default:
		fb := strings.TrimSpace(strings.TrimPrefix(ans, AnswerRevise))
		if fb == "" {
			fb = "(no details given)"
		}
		return tools.Result{Output: "The user wants changes to the plan before approving it. Their feedback:\n\n" + fb +
			"\n\nInvestigate as needed, then call plan again with the revised plan.", Summary: "revise"}
	}
}

// PlanMarkdown renders a plan as markdown, without symbols or verification.
func PlanMarkdown(pl *Plan) string { return renderPlan(pl, planExtras{}) }

// renderPlan renders a plan: title, summary, numbered steps (each with its
// files and symbols), verification, then the free body.
func renderPlan(pl *Plan, ex planExtras) string {
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
		for _, sym := range ex.symbols[s.ID] {
			fmt.Fprintf(&b, "   - %s `%s`", sym.Action, sym.Name)
			if sym.Kind != "" {
				fmt.Fprintf(&b, " (%s)", sym.Kind)
			}
			b.WriteString("\n")
		}
	}
	if len(ex.verification) > 0 {
		b.WriteString("\n## Verification\n\n")
		for _, v := range ex.verification {
			fmt.Fprintf(&b, "- %s\n", v)
		}
	}
	if pl.Body != "" {
		fmt.Fprintf(&b, "\n%s\n", pl.Body)
	}
	return b.String()
}

var (
	stepLineRE  = regexp.MustCompile(`^(\d+)\. (.*)$`)
	stepFilesRE = regexp.MustCompile("^(.*) \\(((?:`[^`]*`(?:, )?)+)\\)$")
	backtickRE  = regexp.MustCompile("`([^`]*)`")
)

// ParsePlanSteps reads the steps back out of the markdown that renderPlan
// produced, for the review panel. Symbol lines are skipped; the section ends
// at the first line that is not a step.
func ParsePlanSteps(md string) []PlanStep {
	var out []PlanStep
	in := false
	for _, line := range strings.Split(md, "\n") {
		if !in {
			if strings.TrimSpace(line) == "## Steps" {
				in = true
			}
			continue
		}
		if strings.HasPrefix(line, "   - ") {
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		m := stepLineRE.FindStringSubmatch(line)
		if m == nil {
			break
		}
		step := PlanStep{ID: m[1], Text: m[2], Status: "pending"}
		if f := stepFilesRE.FindStringSubmatch(m[2]); f != nil {
			step.Text = f[1]
			for _, p := range backtickRE.FindAllStringSubmatch(f[2], -1) {
				step.Files = append(step.Files, p[1])
			}
		}
		out = append(out, step)
	}
	return out
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
