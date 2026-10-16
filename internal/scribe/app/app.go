// Package app assembles a Scribe session: config, model registry, tools,
// code map, language servers, memory, hooks, MCP servers, session storage
// and the agent itself. The TUI (through tui.Backend), the headless runner
// and the Lectern bridge all drive an *App.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/registry"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/memory"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/session"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/skills"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// Options configures Open.
type Options struct {
	Workdir   string
	Model     string // provider/model override
	Mode      string
	Resume    string // session id prefix to resume ("" = new session)
	NoRepoMap bool
	System    string // extra system prompt text
	Version   string
	// Host answers approvals and questions. Nil means headless rules:
	// deny risky actions unless mode is auto, first option for questions.
	Host tools.Host
	// Ephemeral sessions are not saved (headless without --save-session).
	Ephemeral bool
	MaxSteps  int
}

// App is one Scribe session.
type App struct {
	opts    Options
	Workdir string
	Cfg     config.Config

	Registry *registry.Registry
	Tools    *tools.Registry
	Agent    *agent.Agent
	Planner  *agent.Planner
	Dispatch *agent.Dispatcher

	Store  *session.Store
	Meta   session.Meta
	Checks *session.Checkpoints
	Memory *memory.Stores
	Skills *skills.Set

	ext      extensions // code map, lsp, jobs, mcp, hooks (see wire.go)
	services map[string]any
	host     tools.Host

	mu        sync.Mutex
	listeners []agent.Sink
	turn      int
	instr     []agent.Instruction
	codeMap   string
	date      string
	branch    string
	pending   strings.Builder // coalesced text deltas for the transcript
	pendKind  agent.EventKind
	pendAgent string
	closed    bool
}

// FindRoot returns the git repository root containing dir, or dir.
func FindRoot(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	for d := abs; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		p := filepath.Dir(d)
		if p == d {
			return abs
		}
		d = p
	}
}

// DefaultModel picks a model when config and flags name none: the first
// provider with a key, else a local Ollama.
func DefaultModel(reg *registry.Registry) string {
	order := []string{"anthropic", "openai", "deepseek", "openrouter", "gemini", "groq", "xai", "mistral"}
	byID := map[string]registry.ProviderInfo{}
	for _, p := range reg.List() {
		byID[p.ID] = p
	}
	for _, id := range order {
		if p, ok := byID[id]; ok && p.HasKey && p.DefaultModel != "" {
			return id + "/" + p.DefaultModel
		}
	}
	for _, p := range reg.List() {
		if p.HasKey && p.NeedsKey && p.DefaultModel != "" {
			return p.ID + "/" + p.DefaultModel
		}
	}
	if p, ok := byID["ollama"]; ok {
		return "ollama/" + p.DefaultModel
	}
	return ""
}

// Open builds a session.
func Open(ctx context.Context, o Options) (*App, error) {
	if o.Workdir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		o.Workdir = wd
	}
	root := FindRoot(o.Workdir)
	cfg, err := config.Load(root)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	a := &App{opts: o, Workdir: root, Cfg: cfg, date: time.Now().Format("2006-01-02")}
	a.Registry = registry.New(cfg)
	a.Store = session.NewStore(root)

	modelRef := firstNonEmpty(o.Model, cfg.Model)
	mode := firstNonEmpty(o.Mode, cfg.Mode, "default")

	// Session: resume or create.
	var history []llm.Message
	if o.Resume != "" {
		st, meta, err := a.findSession(o.Resume)
		if err != nil {
			return nil, err
		}
		a.Store, a.Meta = st, meta
		a.Workdir = meta.Workdir
		if a.Workdir == "" {
			a.Workdir = root
		}
		if history, err = a.Store.LoadMessages(meta.ID); err != nil {
			return nil, err
		}
		modelRef = firstNonEmpty(o.Model, meta.Model, modelRef)
		a.turn = meta.Turns
	} else {
		a.Meta = session.Meta{Workdir: a.Workdir, Mode: mode}
		if !o.Ephemeral {
			if err := a.Store.Create(&a.Meta); err != nil {
				return nil, err
			}
		}
	}
	if modelRef == "" {
		modelRef = DefaultModel(a.Registry)
	}
	if modelRef == "" {
		return nil, errors.New("no model configured: set one with --model provider/model, or add an API key with `lectern code --set-key PROVIDER KEY`")
	}
	model, err := a.resolve(modelRef)
	if err != nil {
		return nil, err
	}
	a.Meta.Model = model.Ref

	if !o.Ephemeral {
		if a.Checks, err = session.NewCheckpoints(a.Store, a.Meta.ID); err != nil {
			return nil, err
		}
	}
	if config.Bool(cfg.Memory, true) {
		if a.Memory, err = memory.Open(a.Workdir); err != nil {
			a.Memory = nil // memory is optional; never block a session on it
		}
	}
	if set, err := skills.Discover(a.Workdir); err == nil {
		a.Skills = set
	}
	a.instr = agent.LoadInstructions(a.Workdir, config.Home(), cfg.Instructions)
	a.branch = gitBranch(a.Workdir)

	a.host = o.Host
	if a.host == nil {
		a.host = headlessHost{}
	}
	a.Tools = tools.NewRegistry()
	a.services = map[string]any{}
	if err := a.wire(ctx); err != nil {
		return nil, err
	}
	a.Planner = &agent.Planner{Dir: a.Workdir, Sink: a.emit}
	a.Planner.Register(a.Tools)
	a.Dispatch = &agent.Dispatcher{New: a.newSubagent, MaxConcurrent: cfg.Router.MaxAgents, Sink: a.emit}
	a.Dispatch.Register(a.Tools)

	a.Agent = agent.New(a.agentOptions(model, mode))
	a.Planner.Agent = a.Agent
	if len(history) > 0 {
		a.Agent.SetMessages(history)
	}
	if raw, err := a.Store.LoadPlan(a.Meta.ID); err == nil && len(raw) > 0 {
		var pl agent.Plan
		if json.Unmarshal(raw, &pl) == nil {
			a.Planner.SetPlan(&pl)
		}
	}
	a.ext.sessionStart(ctx, a)
	return a, nil
}

func (a *App) findSession(prefix string) (*session.Store, session.Meta, error) {
	if m, err := a.Store.Open(prefix); err == nil {
		return a.Store, m, nil
	}
	st, m, err := session.FindGlobal(prefix)
	if err != nil {
		return nil, session.Meta{}, fmt.Errorf("no session matching %q: %w", prefix, err)
	}
	return st, m, nil
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

func gitBranch(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// resolve turns "provider/model" into an agent.Model.
func (a *App) resolve(ref string) (agent.Model, error) {
	p, id, err := a.Registry.Resolve(ref)
	if err != nil {
		return agent.Model{}, err
	}
	info := a.Registry.ModelInfo(ref)
	if info.ID == "" {
		info.ID = id
	}
	prov, _ := llm.SplitModelRef(ref)
	if prov == "" {
		prov = p.ID()
	}
	return agent.Model{Ref: prov + "/" + id, ID: id, Provider: p, Info: info}, nil
}

func (a *App) systemPrompt(sub string) func(string) string {
	return func(mode string) string {
		a.mu.Lock()
		in := agent.PromptInput{
			Workdir: a.Workdir, Date: a.date, Branch: a.branch,
			Instructions: a.instr, CodeMap: a.codeMap, Extra: a.opts.System, Subagent: sub,
		}
		a.mu.Unlock()
		if a.Agent != nil {
			in.ModelRef = a.Agent.CurrentModel().Ref
		}
		if a.Skills != nil {
			in.Skills = a.Skills.Catalog()
		}
		return agent.BuildSystem(in, mode)
	}
}

func (a *App) env() tools.Env {
	e := tools.Env{Workdir: a.Workdir, Host: a.host, Services: a.services}
	if a.Checks != nil {
		e.Files = a.Checks
	} else {
		e.Files = newMemTracker()
	}
	return e
}

func (a *App) activeTools() map[string]bool {
	if len(a.Cfg.DisabledTools) == 0 {
		return nil
	}
	off := map[string]bool{}
	for _, n := range a.Cfg.DisabledTools {
		off[n] = true
	}
	act := map[string]bool{}
	for _, n := range a.Tools.Names() {
		if !off[n] {
			act[n] = true
		}
	}
	return act
}

func (a *App) agentOptions(model agent.Model, mode string) agent.Options {
	o := agent.Options{
		Model:     model,
		Tools:     a.Tools,
		Active:    a.activeTools(),
		Env:       a.env(),
		System:    a.systemPrompt(""),
		Mode:      mode,
		MaxTokens: a.Cfg.MaxTokens,
		Reasoning: a.Cfg.Reasoning,
		Sink:      a.emit,
		MaxSteps:  a.opts.MaxSteps,
		Hooks:     a.ext.hooks,
		Headers:   map[string]string{"X-Session-Id": a.Meta.ID},
	}
	for _, ref := range a.Cfg.Router.Fallback {
		if m, err := a.resolve(ref); err == nil {
			o.Fallback = append(o.Fallback, m)
		}
	}
	if config.Bool(a.Cfg.AutoCompact, true) {
		cm := model
		if a.Cfg.Router.Compact != "" {
			if m, err := a.resolve(a.Cfg.Router.Compact); err == nil {
				cm = m
			}
		}
		o.Compactor = agent.SummaryCompactor{Model: cm}
		o.CompactAt = a.Cfg.CompactAt
	} else {
		o.CompactAt = -1
	}
	if a.Memory != nil {
		o.Recall = func(_ context.Context, prompt string) string { return a.Memory.Inject(prompt, nil) }
	}
	if !a.opts.Ephemeral {
		o.Persist = a.persist
	}
	return o
}

// newSubagent builds an explore or code subagent.
func (a *App) newSubagent(role, id string, sink agent.Sink) *agent.Agent {
	ref := a.Cfg.Router.Explore
	if role == "code" {
		ref = a.Cfg.Router.Code
	}
	model := a.Agent.CurrentModel()
	if ref != "" {
		if m, err := a.resolve(ref); err == nil {
			model = m
		}
	}
	reg := tools.NewRegistry()
	for _, n := range a.Tools.Names() {
		t, _ := a.Tools.Get(n)
		switch n {
		case "dispatch", "plan", "update_plan_step", "ask_user", "task_list", "memory":
			continue
		}
		if role == "explore" && !t.ReadOnly() {
			continue
		}
		reg.Register(t)
	}
	steps := 30
	if role == "code" {
		steps = 50
	}
	mode := "default"
	if role == "explore" {
		mode = "architect" // read-only gate as a second line of defence
	}
	return agent.New(agent.Options{
		ID:        id,
		Model:     model,
		Tools:     reg,
		Env:       a.env(),
		System:    a.systemPrompt(role),
		Mode:      mode,
		MaxTokens: a.Cfg.MaxTokens,
		Sink: func(e agent.Event) {
			e.AgentID = id
			sink(e)
			a.emit(e)
		},
		MaxSteps:  steps,
		CompactAt: -1,
		Hooks:     a.ext.hooks,
	})
}

// Subscribe adds an event listener (TUI, headless printer, Lectern bridge).
func (a *App) Subscribe(s agent.Sink) {
	a.mu.Lock()
	a.listeners = append(a.listeners, s)
	a.mu.Unlock()
}

func (a *App) emit(e agent.Event) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	a.mu.Lock()
	ls := append([]agent.Sink(nil), a.listeners...)
	a.mu.Unlock()
	for _, l := range ls {
		l(e)
	}
	a.record(e)
	if e.Kind == agent.EvPlan && e.Plan != nil && !a.opts.Ephemeral {
		if b, err := json.Marshal(e.Plan); err == nil {
			_ = a.Store.SavePlan(a.Meta.ID, b)
		}
	}
}

// record appends the event to the session's UI transcript, coalescing
// text and reasoning deltas so the file stays small.
func (a *App) record(e agent.Event) {
	if a.opts.Ephemeral || e.Kind == agent.EvToolArgs || e.Kind == agent.EvToolProgress || e.Kind == agent.EvContext {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []json.RawMessage
	if (e.Kind == agent.EvText || e.Kind == agent.EvReasoning) && (a.pendKind == "" || (a.pendKind == e.Kind && a.pendAgent == e.AgentID)) {
		a.pendKind, a.pendAgent = e.Kind, e.AgentID
		a.pending.WriteString(e.Text)
		return
	}
	if a.pendKind != "" {
		b, _ := json.Marshal(agent.Event{Kind: a.pendKind, AgentID: a.pendAgent, Text: a.pending.String(), At: e.At})
		out = append(out, b)
		a.pending.Reset()
		a.pendKind = ""
	}
	if e.Kind == agent.EvText || e.Kind == agent.EvReasoning {
		a.pendKind, a.pendAgent = e.Kind, e.AgentID
		a.pending.WriteString(e.Text)
	} else {
		b, _ := json.Marshal(e)
		out = append(out, b)
	}
	if len(out) > 0 {
		_ = a.Store.AppendUI(a.Meta.ID, out...)
	}
}

// Transcript returns the recorded events of this session.
func (a *App) Transcript() []agent.Event {
	if a.opts.Ephemeral {
		return nil
	}
	raw, err := a.Store.LoadUI(a.Meta.ID)
	if err != nil {
		return nil
	}
	out := make([]agent.Event, 0, len(raw))
	for _, r := range raw {
		var e agent.Event
		if json.Unmarshal(r, &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// persist saves message history (Options.Persist).
func (a *App) persist(msgs []llm.Message, rewritten bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	saved, _ := a.Store.LoadMessages(a.Meta.ID)
	if rewritten || len(saved) > len(msgs) {
		_ = a.Store.ReplaceMessages(a.Meta.ID, msgs)
		return
	}
	if len(msgs) > len(saved) {
		_ = a.Store.AppendMessages(a.Meta.ID, msgs[len(saved):])
	}
}

// Run executes one user turn synchronously.
func (a *App) Run(ctx context.Context, prompt string, images []llm.Part) (agent.TurnStats, error) {
	a.mu.Lock()
	a.turn++
	turn := a.turn
	a.mu.Unlock()
	if a.Checks != nil {
		a.Checks.Begin(turn, prompt)
	}
	if !a.opts.Ephemeral {
		_ = session.AppendHistory(prompt, a.Workdir)
	}
	stats, err := a.Agent.Run(ctx, prompt, images)
	if a.Checks != nil {
		_ = a.Checks.End()
	}
	a.ext.afterTurn(a, stats)
	a.saveMeta(prompt, stats)
	return stats, err
}

func (a *App) saveMeta(prompt string, stats agent.TurnStats) {
	if a.opts.Ephemeral {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Meta.Title == "" {
		a.Meta.Title = session.DeriveTitle(prompt)
	}
	a.Meta.Turns = a.turn
	a.Meta.Tokens, a.Meta.CostUSD = a.Agent.Usage()
	a.Meta.Model = a.Agent.CurrentModel().Ref
	a.Meta.Mode = a.Agent.Mode()
	a.Meta.Updated = time.Now()
	_ = a.Store.SaveMeta(&a.Meta)
}

// SetModel switches model.
func (a *App) SetModel(ref string) error {
	m, err := a.resolve(ref)
	if err != nil {
		return err
	}
	a.Agent.SetModel(m)
	a.mu.Lock()
	a.Meta.Model = m.Ref
	a.mu.Unlock()
	if !a.opts.Ephemeral {
		_ = a.Store.SaveMeta(&a.Meta)
	}
	return nil
}

// Close releases everything.
func (a *App) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	a.mu.Unlock()
	a.Agent.Abort()
	a.ext.close(a)
	if a.Memory != nil {
		_ = a.Memory.Close()
	}
	return nil
}

// SessionID returns the session id ("" for ephemeral).
func (a *App) SessionID() string { return a.Meta.ID }

// Opts returns the options the app was opened with.
func (a *App) Opts() Options { return a.opts }

// headlessHost answers prompts without a user.
type headlessHost struct{}

func (headlessHost) Approve(_ context.Context, ap tools.Approval) tools.Decision {
	if ap.Kind == "web" || ap.Kind == "mcp" {
		return tools.AllowSession
	}
	return tools.Deny
}

func (headlessHost) Ask(_ context.Context, q tools.Question) (string, error) {
	if q.Kind == "plan_review" {
		return "Execute", nil
	}
	if len(q.Options) > 0 {
		return q.Options[0], nil
	}
	return "No user is available; use your best judgement.", nil
}

func (headlessHost) Progress(string, string) {}

// memTracker is a FileTracker for ephemeral sessions.
type memTracker struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func newMemTracker() *memTracker { return &memTracker{seen: map[string]time.Time{}} }

func (m *memTracker) BeforeWrite(string) {}
func (m *memTracker) MarkRead(abs string) {
	st, err := os.Stat(abs)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err == nil {
		m.seen[abs] = st.ModTime()
	}
}
func (m *memTracker) StaleSinceRead(abs string) bool {
	m.mu.Lock()
	t, ok := m.seen[abs]
	m.mu.Unlock()
	if !ok {
		return false
	}
	st, err := os.Stat(abs)
	return err == nil && !st.ModTime().Equal(t)
}
func (m *memTracker) WasRead(abs string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.seen[abs]
	return ok
}
