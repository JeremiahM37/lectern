package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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

// settle closes the steps a finished turn left active, so none keeps a
// spinner after the work stopped. A turn that completed cleanly finished the
// step it was on even when the model forgot to mark it done; a failed or
// stopped turn did not, so that step goes back to pending.
func (p *Planner) settle(ok bool) {
	p.mu.Lock()
	changed := false
	if p.plan != nil {
		for i := range p.plan.Steps {
			if p.plan.Steps[i].Status != "active" {
				continue
			}
			if ok {
				p.plan.Steps[i].Status = "done"
			} else {
				p.plan.Steps[i].Status = "pending"
			}
			changed = true
		}
	}
	p.mu.Unlock()
	if changed {
		p.emitPlan()
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
			"ask for revisions, or cancel. Steps must be concrete and ordered. Every step names the files it touches and the " +
			"code symbols it adds, modifies, removes or renames. Also list every file in the top-level files array with its " +
			"action (create, modify or delete), and say how the change is verified.",
		Schema: tools.Schema(map[string]any{
			"title":   tools.Str("Short title of the change"),
			"summary": tools.Str("Context: two or three sentences on what changes and why"),
			"files": tools.Arr("Every file the plan touches, with what happens to it", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":        tools.Str("File path"),
					"action":      tools.Enum("What happens to the file", "create", "modify", "delete"),
					"description": tools.Str("One line: what changes in it"),
				},
				"required": []string{"path", "action"},
			}),
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
		Title   string     `json:"title"`
		Summary string     `json:"summary"`
		Files   []PlanFile `json:"files"`
		Steps   []struct {
			Text    string       `json:"text"`
			Files   []stepFile   `json:"files"`
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
	pl := &Plan{Title: in.Title, Summary: in.Summary, Body: in.Body, Verification: in.Verification}
	pl.Files = cleanPlanFiles(in.Files)
	pl.Depth = t.depth()
	ex := planExtras{symbols: map[string][]PlanSymbol{}, verification: in.Verification}
	listed := map[string]bool{}
	for _, f := range pl.Files {
		listed[f.Path] = true
	}
	for _, s := range in.Steps {
		var paths []string
		for _, f := range s.Files {
			if f.Path == "" {
				continue
			}
			paths = append(paths, f.Path)
			// A step that says what happens to its file fills the file list
			// when the model left that list out.
			if !listed[f.Path] {
				if c := cleanPlanFiles([]PlanFile{f.PlanFile}); len(c) == 1 {
					pl.Files = append(pl.Files, c[0])
					listed[f.Path] = true
				}
			}
		}
		// A step packed with a numbered list becomes one step per item, each
		// carrying the step's files and symbols.
		for _, text := range splitStepText(s.Text) {
			id := fmt.Sprint(len(pl.Steps) + 1)
			pl.Steps = append(pl.Steps, PlanStep{ID: id, Text: text, Files: paths, Symbols: s.Symbols, Status: "pending"})
			if len(s.Symbols) > 0 {
				ex.symbols[id] = s.Symbols
			}
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
		Options:       PlanOptionsFor(pl.Depth),
		AllowFreeText: true,
	})
	if err != nil {
		return tools.Errorf("plan review was interrupted: %v", err)
	}
	lower := strings.ToLower(ans)
	switch {
	case strings.EqualFold(ans, AnswerExecute) || strings.HasPrefix(lower, "execute"):
		if t.p.Agent != nil {
			// Implementing needs edits, so plan mode ends here; say so, or the
			// header tag just vanishes and the user cannot tell why.
			if prev := t.p.Agent.Mode(); prev != "default" {
				t.p.Agent.SetMode("default")
				t.p.Agent.emit(Event{Kind: EvNotice, Text: "Plan approved: switched to Default mode to implement it."})
			}
			t.p.Agent.BeginPlanExecution()
		}
		t.p.emitOutcome(pl, "approved", "")
		return tools.Result{Output: "The user approved the plan. Restrictions are lifted: implement it now, step by step. " +
			"Call update_plan_step with status active when you start a step and done when it is finished. Verify with tests or builds at the end.",
			Summary: "approved"}
	case strings.EqualFold(ans, AnswerClear) || strings.HasPrefix(lower, "clear"):
		// The history is replaced after this turn ends (see RunClearing), so
		// the tool result below is never sent to the model.
		t.p.mu.Lock()
		t.p.cleared = &pendingClear{plan: pl, md: md}
		t.p.mu.Unlock()
		t.p.emitOutcome(pl, "approved", "")
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
		t.p.emitOutcome(pl, "cancelled", "")
		return tools.Result{Output: "The user cancelled the plan. Stop and wait for their next message.", Summary: "cancelled"}
	default:
		fb := strings.TrimSpace(strings.TrimPrefix(ans, AnswerRevise))
		if fb == "" {
			fb = "(no details given)"
		}
		t.p.emitOutcome(pl, "revision", fb)
		return tools.Result{Output: "The user wants changes to the plan before approving it. Their feedback:\n\n" + fb +
			"\n\nInvestigate as needed, then call plan again with the revised plan.", Summary: "revise"}
	}
}

// depth is the depth of a plan written now, from the context in use.
func (t planTool) depth() string {
	if t.p.Agent == nil {
		return PlanFull
	}
	used, window := t.p.Agent.ContextUsed()
	return PlanDepth(used, window)
}

// leadingStepNumberRE is the number a model puts in front of a step: "3. Add
// X", "3) Add X" or "Step 3: Add X".
var leadingStepNumberRE = regexp.MustCompile(`^\s*(?:[Ss]tep\s+)?\d{1,2}\s*[.):]\s+`)

// inlineStepMarkerRE finds a number that starts an item inside a step's text:
// "1. Add X 2. Add Y". The number must follow the start of the text or a space,
// so "v2.1" and "3.5 MB" do not count.
var inlineStepMarkerRE = regexp.MustCompile(`(^|\s)(\d{1,2})[.)]\s+`)

// splitStepText normalises the text of one plan step. A leading number is
// dropped, since the plan already numbers its steps. A text that holds a
// numbered list starting at 1 ("1. Add X 2. Add Y") becomes one text per
// item. Anything else is returned as one text, trimmed.
func splitStepText(text string) []string {
	t := strings.TrimSpace(text)
	marks := inlineStepMarkerRE.FindAllStringSubmatchIndex(t, -1)
	var starts []int // where each accepted item's text begins
	var ends []int   // where each accepted item's text ends
	next := 1
	for _, m := range marks {
		if n, err := strconv.Atoi(t[m[4]:m[5]]); err != nil || n != next {
			continue
		}
		if len(starts) > 0 {
			ends = append(ends, m[0])
		}
		starts = append(starts, m[1])
		next++
	}
	// The list must begin the text and hold at least two items.
	if len(starts) < 2 || marks[0][0] != 0 || t[marks[0][4]:marks[0][5]] != "1" {
		return []string{stripLeadingStepNumber(t)}
	}
	ends = append(ends, len(t))
	var out []string
	for i := range starts {
		if item := strings.TrimSpace(t[starts[i]:ends[i]]); item != "" {
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		return []string{stripLeadingStepNumber(t)}
	}
	return out
}

// stripLeadingStepNumber removes a leading "3." (or "3)", "Step 3:") from a
// step's text. A text that is only a number is left as it is.
func stripLeadingStepNumber(t string) string {
	if out := strings.TrimSpace(leadingStepNumberRE.ReplaceAllString(t, "")); out != "" {
		return out
	}
	return t
}

// stepFile is one entry of a step's files. The schema asks for a path, but
// models often send the top-level file shape ({path, action}) instead; both
// are accepted so a correct plan is not rejected over its shape.
type stepFile struct{ PlanFile }

func (f *stepFile) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		f.Path = strings.TrimSpace(s)
		return nil
	}
	if err := json.Unmarshal(b, &f.PlanFile); err != nil {
		return fmt.Errorf("each step file is a path string, or an object with a path")
	}
	f.Path = strings.TrimSpace(f.Path)
	return nil
}

// cleanPlanFiles keeps the files with a path and a known action. Anything
// else is a model slip and is dropped rather than shown as a wrong glyph.
func cleanPlanFiles(in []PlanFile) []PlanFile {
	var out []PlanFile
	for _, f := range in {
		f.Path = strings.TrimSpace(f.Path)
		f.Action = strings.ToLower(strings.TrimSpace(f.Action))
		switch f.Action {
		case "create", "modify", "delete":
		default:
			continue
		}
		if f.Path != "" {
			out = append(out, f)
		}
	}
	return out
}

// emitOutcome reports that the user did not approve pl. The planner keeps no
// plan afterwards; the event is a snapshot the transcript shows as rejected.
func (p *Planner) emitOutcome(pl *Plan, outcome, feedback string) {
	if p.Sink == nil {
		return
	}
	snap := *pl
	snap.Steps = append([]PlanStep(nil), pl.Steps...)
	snap.Outcome = outcome
	snap.Feedback = feedback
	p.Sink(Event{Kind: EvPlan, Plan: &snap})
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
	if len(pl.Files) > 0 {
		b.WriteString("\n## Files\n\n")
		for _, f := range pl.Files {
			fmt.Fprintf(&b, "- %s `%s`", f.Action, f.Path)
			if f.Description != "" {
				fmt.Fprintf(&b, " — %s", f.Description)
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

var (
	symbolLineRE = regexp.MustCompile("^   - (add|modify|remove|rename) `([^`]*)`(?: \\((.*)\\))?$")
	verifyLineRE = regexp.MustCompile(`^- (.+)$`)
)

// ParsePlanSteps reads the steps back out of the markdown that renderPlan
// produced, for the review panel, with each step's files and symbols. The
// section ends at the first line that is not a step or a symbol.
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
			if m := symbolLineRE.FindStringSubmatch(line); m != nil {
				last := &out[len(out)-1]
				last.Symbols = append(last.Symbols, PlanSymbol{Name: m[2], Kind: m[3], Action: m[1]})
			}
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

// ParsePlanVerification reads the "## Verification" checklist back out of
// plan markdown. It returns nil when the plan has none.
func ParsePlanVerification(md string) []string {
	var out []string
	in := false
	for _, line := range strings.Split(md, "\n") {
		t := strings.TrimSpace(line)
		if !in {
			if t == "## Verification" {
				in = true
			}
			continue
		}
		if t == "" {
			continue
		}
		m := verifyLineRE.FindStringSubmatch(t)
		if m == nil {
			break // the checklist ends at the first line that is not an item
		}
		out = append(out, m[1])
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
			"action":   tools.Enum("set replaces the list; update changes one item; clear empties it", "set", "update", "clear"),
			"tasks":    tools.Arr("For set: the task texts in order", map[string]any{"type": "string"}),
			"step_ids": tools.Arr("For set: the plan step id each task belongs to, in the same order as tasks (empty string for none)", map[string]any{"type": "string"}),
			"id":       tools.Str("For update: task id"),
			"status":   tools.Enum("For update: new status", "pending", "in_progress", "done", "cancelled"),
		}, "action"),
	}
}

func (t taskTool) Run(_ context.Context, _ *tools.Env, args json.RawMessage) tools.Result {
	var in struct {
		Action  string   `json:"action"`
		Tasks   []string `json:"tasks"`
		StepIDs []string `json:"step_ids"`
		ID      string   `json:"id"`
		Status  string   `json:"status"`
	}
	if err := tools.Decode(args, &in); err != nil {
		return tools.Errorf("%v", err)
	}
	t.p.mu.Lock()
	switch in.Action {
	case "set":
		t.p.tasks = nil
		for i, s := range in.Tasks {
			task := Task{ID: fmt.Sprint(i + 1), Text: s, Status: "pending"}
			if i < len(in.StepIDs) {
				task.StepID = strings.TrimSpace(in.StepIDs[i])
			}
			t.p.tasks = append(t.p.tasks, task)
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
